package daemon

// query_widget.go — 票 08：GET /widget/summary 实现（注册于 queryapi.go 的
// queryEndpoints 分派表；鉴权/127.0.0.1 绑定复用既有面）。
//
// 契约 v0（spec「契约 v0」节 + widget/ui/data.js JSDoc 同源）：
//   {version:1, generated_at, upstreams:[{id,kind,label,plan?,metrics[],error?}],
//    handoff:{id,kind,label,metrics[]}}；
//   metric = {key, remaining_pct|text, value?, abs?, breakdown?, available?,
//             source: fetched|estimated, as_of, resets_at?}。
//   本端点在 v0 枚举之上追加 handoffs_month/handoffs_week 两键（spec「只增不
//   改义」的 sanctioned 演进路径；widget 侧同 commit 消费——摆渡执行器
//   provider 无价格表时 spend_* 不可算不造数，计数是唯一诚实可显示）。
//
// 数据面 = 票 06 查询器（远端，缓存 10 分钟默认——各家限流差异的可配化留
// 后续）+ 台账聚合（秒级现算，票 07 月度口径与 /report 同源：usage 科目四列
// 纯加总）。单上游时代归属口径见 widgetLedgerAggregate 头注。
//
// 缓存语义：成功 10 分钟；失败负缓存 1 分钟（防 widget 30s 轮询钉着坏上游
// 每 5s 墙钟打满）。失败类目照 quota 错误串原样透传（类别单源，不含钥/URL）。
//
// 红线（queryapi.go 顶部块全文适用）：无消息内容（台账 title 等永不进响应）、
// 无凭据字段、纯只读（远端 GET + 账本 Read 只读遍历）。

import (
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"ferryman/internal/accounts"
	"ferryman/internal/clock"
	"ferryman/internal/config"
	"ferryman/internal/quota"
)

// widget 缓存常量（票 08：远端余量 5–15 分钟，默认 10）。
const (
	widgetQuotaTTL = 10 * time.Minute
	widgetErrTTL   = time.Minute
)

// widgetKindOf 供应商类别 → 契约 kind/展示名（单源；handle 与 fetch 共用，
// 绝不两处映射）。
var widgetKindOf = map[string][2]string{
	"glm":      {"coding_plan", "GLM"},
	"kimi":     {"coding_plan", "Kimi"},
	"deepseek": {"paygo", "DeepSeek"},
}

// widgetHTTPClient 远端取数 client 缝（生产 5s 墙钟超时；测试注入改写传输
// 做 httptest 零外呼——quota 包同款手法）。
var widgetHTTPClient = &http.Client{Timeout: quota.TimeoutS}

// widgetRemote 一个上游的远端查询缓存条目（metrics 为远端部分；台账
// estimated 指标每次现算不入缓存）。
type widgetRemote struct {
	id       string
	kind     string // coding_plan|paygo
	label    string
	plan     string
	note     string // 版本语义注记（如智谱 V1 无周/月窗）；空=无注记
	metrics  []map[string]any
	errCat   string  // 非空=该上游整体查询失败（error.category）
	expireAt float64 // 缓存截止（clock 秒——冻结时钟下测试确定性）
}

// widgetQuotaCache 远端结果缓存（daemon 单例进程内；票 08 缓存验收的载体）。
var widgetQuotaCache = struct {
	sync.Mutex
	m map[string]*widgetRemote
}{m: map[string]*widgetRemote{}}

// widgetCacheFresh 缓存命中读取（过期/缺失返回 nil）。
func widgetCacheFresh(id string) *widgetRemote {
	widgetQuotaCache.Lock()
	defer widgetQuotaCache.Unlock()
	e, ok := widgetQuotaCache.m[id]
	if !ok || clock.Now() >= e.expireAt {
		return nil
	}
	return e
}

// handleWidgetSummary GET /widget/summary：契约 v0 装配。
func handleWidgetSummary(d *Daemon, w http.ResponseWriter, r *http.Request) {
	now := time.Unix(int64(clock.Now()), 0)
	asOf := now.Format("15:04")

	// 上游表（确定性输出：名字排序）
	var dockUp map[string]config.DockUpstream
	if d.Cfg != nil && d.Cfg.Dock != nil {
		dockUp = d.Cfg.Dock.Upstreams
	}
	names := make([]string, 0, len(dockUp))
	for n := range dockUp {
		names = append(names, n)
	}
	sort.Strings(names)

	// 过期条目并行补拉（互不拖累：单上游最坏 5s，不叠串行）
	var pending []string
	for _, n := range names {
		if quota.DetectKind(dockUp[n].BaseURL) != "" && widgetCacheFresh(n) == nil {
			pending = append(pending, n)
		}
	}
	if len(pending) > 0 {
		var wg sync.WaitGroup
		for _, n := range pending {
			wg.Add(1)
			go func(name string, u config.DockUpstream) {
				defer wg.Done()
				widgetFetchRemote(name, &u)
			}(n, dockUp[n])
		}
		wg.Wait()
	}

	// 台账聚合（秒级现算；usage 四列月度 + handoff 计数月/周）
	var agg *widgetLedgerAgg
	if d.Accounts != nil {
		agg = widgetLedgerAggregate(d.Accounts, dockUp, now)
	}

	ups := make([]map[string]any, 0, len(names))
	for _, n := range names {
		provider := quota.DetectKind(dockUp[n].BaseURL)
		kl, ok := widgetKindOf[provider]
		if !ok {
			continue // 未识别供应商：契约只认三家，不进列表
		}
		e := widgetCacheFresh(n)
		if e == nil { // 防御路径：并行补拉后理论不可达（错误也进缓存）
			e = widgetErrRemote(quota.ErrNotConfigured, kl[0], kl[1])
		}
		entry := map[string]any{"id": n, "kind": e.kind, "label": e.label}
		if e.plan != "" {
			entry["plan"] = e.plan
		}
		if e.note != "" {
			entry["note"] = e.note
		}
		if e.errCat != "" {
			entry["error"] = map[string]any{"category": e.errCat}
		}
		// metrics 拷贝装配（缓存条目不可变——绝不原地 append 改缓存背板）
		metrics := make([]map[string]any, 0, len(e.metrics)+1)
		metrics = append(metrics, e.metrics...)
		if e.kind == "coding_plan" && agg != nil {
			tok := agg.monthTokens[n]
			metrics = append(metrics, map[string]any{
				"key": "month_tokens", "text": widgetTokText(tok), "value": tok,
				"source": "estimated", "as_of": asOf,
			})
		}
		entry["metrics"] = metrics
		ups = append(ups, entry)
	}

	handoff := map[string]any{"id": "handoff", "kind": "handoff", "label": "摆渡",
		"metrics": []map[string]any{}}
	if agg != nil {
		handoff["metrics"] = []map[string]any{
			{"key": "handoffs_month", "text": fmt.Sprintf("月 %d 次", agg.handoffsM),
				"value": agg.handoffsM, "source": "estimated", "as_of": asOf},
			{"key": "handoffs_week", "text": fmt.Sprintf("周 %d 次", agg.handoffsW),
				"value": agg.handoffsW, "source": "estimated", "as_of": asOf},
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"version":      1,
		"generated_at": now.Format(time.RFC3339),
		"upstreams":    ups,
		"handoff":      handoff,
	})
}

// widgetFetchRemote 缓存检查 + 补拉 + 回填（成功/失败分档 TTL）。
func widgetFetchRemote(name string, u *config.DockUpstream) *widgetRemote {
	if e := widgetCacheFresh(name); e != nil {
		return e
	}
	e := widgetFetchUncached(u)
	e.id = name
	ttl := widgetQuotaTTL
	if e.errCat != "" {
		ttl = widgetErrTTL
	}
	e.expireAt = clock.Now() + ttl.Seconds()
	widgetQuotaCache.Lock()
	widgetQuotaCache.m[name] = e
	widgetQuotaCache.Unlock()
	return e
}

// widgetFetchUncached 按 base_url 类别分发到对应查询器并装配远端 metric 行
// （as_of=取数时刻——缓存期内如实停留在取数时刻）。
func widgetFetchUncached(u *config.DockUpstream) *widgetRemote {
	provider := quota.DetectKind(u.BaseURL)
	kl, ok := widgetKindOf[provider]
	if !ok {
		return widgetErrRemote(quota.ErrNotConfigured, "", "")
	}
	asOf := time.Unix(int64(clock.Now()), 0).Format("15:04")
	switch provider {
	case "glm":
		q, err := quota.FetchGLMWithClient(widgetHTTPClient, u)
		if err != nil {
			return widgetErrRemote(err, kl[0], kl[1])
		}
		e := &widgetRemote{kind: kl[0], label: kl[1], plan: q.Plan,
			metrics: widgetWindowMetrics(q.FiveHour, q.Week, asOf)}
		// MCP 工具增值服务额度（TIME_LIMIT unit:5 按次数，所有版本/档位都有
		// ——2026-09-25 用户口径）：环+绝对数+分工具明细。
		if q.Tools != nil {
			tq := map[string]any{"key": "tools_quota",
				"remaining_pct": q.Tools.RemainingPct, "value": q.Tools.Remaining,
				"source": "fetched", "as_of": asOf}
			tq["abs"] = fmt.Sprintf("%.0f / %.0f", q.Tools.Remaining, q.Tools.Total)
			if q.Tools.HasReset {
				tq["resets_at"] = q.Tools.ResetsAt.Format(time.RFC3339)
			}
			if len(q.Tools.Details) > 0 {
				ds := make([]map[string]any, 0, len(q.Tools.Details))
				for _, d := range q.Tools.Details {
					ds = append(ds, map[string]any{"name": d.Name, "used": d.Used})
				}
				tq["details"] = ds
			}
			e.metrics = append(e.metrics, tq)
		}
		// 套餐版本语义注记（2026-09-25 用户口径）：智谱 Coding Plan V1 无周
		// 限制与月度限制（V2+ 才有）——周窗缺席按响应如实判定，注记让「为何
		// 没有周环」在详情卡可读；月度用量无论版本都是台账估算值（带估标）。
		if q.Week == nil {
			e.note = "V1 套餐语义：无周/月配额窗（仅 5h 窗；周环缺席是套餐本身无此限制）"
		}
		return e
	case "kimi":
		q, err := quota.FetchKimiWithClient(widgetHTTPClient, u)
		if err != nil {
			return widgetErrRemote(err, kl[0], kl[1])
		}
		return &widgetRemote{kind: kl[0], label: kl[1],
			metrics: widgetWindowMetrics(q.FiveHour, q.Week, asOf)}
	default: // deepseek
		b, err := quota.FetchDeepSeekWithClient(widgetHTTPClient, u)
		if err != nil {
			return widgetErrRemote(err, kl[0], kl[1])
		}
		m := map[string]any{"key": "balance_cny", "text": "¥" + b.Total,
			"available": b.Available, "source": "fetched", "as_of": asOf}
		if b.Granted != "" || b.ToppedUp != "" {
			m["breakdown"] = map[string]any{"granted": b.Granted, "topped_up": b.ToppedUp}
		}
		return &widgetRemote{kind: kl[0], label: kl[1], metrics: []map[string]any{m}}
	}
}

// widgetErrRemote 错误条目（ErrNotConfigured→「未配置」；其余类别照错误串
// 原样——类别单源，不含钥/URL）。
func widgetErrRemote(err error, kind, label string) *widgetRemote {
	cat := err.Error()
	if errors.Is(err, quota.ErrNotConfigured) {
		cat = "未配置"
	}
	return &widgetRemote{kind: kind, label: label, errCat: cat}
}

// widgetWindowMetrics 编码套餐两窗 metric 行（缺席窗不造行；Kimi 可信口径
// 附绝对数；GLM 剩余=100−已用%）。
func widgetWindowMetrics(fiveHour, week *quota.Window, asOf string) []map[string]any {
	var out []map[string]any
	for _, kv := range []struct {
		key string
		wd  *quota.Window
	}{
		{"window_5h", fiveHour},
		{"week", week},
	} {
		if kv.wd == nil {
			continue
		}
		m := map[string]any{"key": kv.key, "remaining_pct": kv.wd.RemainingPct,
			"source": "fetched", "as_of": asOf}
		if kv.wd.HasReset {
			m["resets_at"] = kv.wd.ResetsAt.Format(time.RFC3339)
		}
		if kv.wd.HasAbs {
			m["abs"] = fmt.Sprintf("%.0f / %.0f", kv.wd.Remaining, kv.wd.Limit)
		}
		out = append(out, m)
	}
	return out
}

// widgetTokText 月 token 文本（demo 契约同款「月 3.2M tok」形态；G/M/k 一位
// 小数、尾 0 剥离——重缓存月账可到 1e9 量级，无 G 档会出「9223.4M」怪相）。
func widgetTokText(n float64) string {
	v, unit := n, ""
	switch {
	case n >= 1e9:
		v, unit = n/1e9, "G"
	case n >= 1e6:
		v, unit = n/1e6, "M"
	case n >= 1e3:
		v, unit = n/1e3, "k"
	default:
		return fmt.Sprintf("月 %.0f tok", n)
	}
	s := strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.1f", v), "0"), ".")
	return fmt.Sprintf("月 %s%s tok", s, unit)
}

// ---- 台账聚合（票 07 子集：usage 四列月度按上游 + handoff 计数月/周） ----

// widgetLedgerAgg 月/周两桶聚合结果。
type widgetLedgerAgg struct {
	monthTokens map[string]float64 // 上游名 → 本自然月 usage 四列合计（估算）
	handoffsM   int
	handoffsW   int
}

// widgetLedgerAggregate 台账现算（只读）。
//
// 归属口径（单上游时代，诚实披露）：usage 行只有 model 名（无 provider 字段）
// ——按各上游 model_map 值域小写匹配归属；无任何映射命中且仅配置了一个上游
// 时全归该上游（cc-switch 中转时代的行 model 名是 claude-*，同样流向该上游
// 账户）。多上游时代需按 dock 科目 model_out 细分，届时重构此处。
// handoff spend_*：摆渡执行器 provider 无价格表（price_ver=null）→ 不可算
// 不造数（票 07 剩余项，见晨报）。
func widgetLedgerAggregate(acc *accounts.Accounts, ups map[string]config.DockUpstream, now time.Time) *widgetLedgerAgg {
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.Local)
	weekStart := widgetWeekStart(now)
	since := float64(monthStart.Unix())
	if float64(weekStart.Unix()) < since {
		since = float64(weekStart.Unix()) // 周一可能落上月（如 09-01 周三 → 08-31 周一）
	}
	until := float64(monthStart.AddDate(0, 1, 0).Unix()) - 0.001
	entries := acc.Read(accounts.ReadOpts{Since: since, Until: until})

	owner := map[string]string{}
	var single string
	for n, u := range ups {
		for _, v := range u.ModelMap {
			if v != "" {
				owner[strings.ToLower(v)] = n
			}
		}
	}
	if len(ups) == 1 {
		for n := range ups {
			single = n
		}
	}

	agg := &widgetLedgerAgg{monthTokens: map[string]float64{}}
	for _, e := range entries {
		k, _ := e["kind"].(string)
		ts, _ := e["ts"].(float64)
		switch k {
		case "usage":
			if ts < float64(monthStart.Unix()) {
				continue // 读窗因周界前移而宽出的部分，月桶不收
			}
			name := owner[strings.ToLower(strVal(e, "model"))]
			if name == "" {
				name = single
			}
			if name == "" {
				continue // 归属不明（多上游且无映射命中）：宁缺勿猜
			}
			agg.monthTokens[name] += acctNum(e, "input_tokens") +
				acctNum(e, "cache_read_tokens") +
				acctNum(e, "cache_creation_tokens") +
				acctNum(e, "output_tokens")
		case "handoff":
			if ts >= float64(monthStart.Unix()) {
				agg.handoffsM++
			}
			if ts >= float64(weekStart.Unix()) {
				agg.handoffsW++
			}
		}
	}
	return agg
}

// widgetWeekStart 本地自然周起点（周一 00:00）。
func widgetWeekStart(t time.Time) time.Time {
	d := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.Local)
	wd := (int(d.Weekday()) + 6) % 7 // Monday=0 … Sunday=6
	return d.AddDate(0, 0, -wd)
}

// strVal 台账行取值小助手（数值面复用同包 acctNum，不另立第二份）。
func strVal(e map[string]any, k string) string {
	s, _ := e[k].(string)
	return s
}
