package daemon

// query_stats.go — 票04：GET /stats/summary 与 GET /stats/usage 实现（注册于
// queryapi.go 的 queryEndpoints 分派表；鉴权/127.0.0.1 绑定/预检 CORS 面复用
// 既有 httpapi 面——预检在 makeHandler OPTIONS 集中答 204，实际响应的 ACAO
// 回声在此按 query_widget.go 同款补齐，Tauri 壳 webview 方可读取）。
//
// 冻结 schema：days/kpi 字段名与 widget/ui/mock/stats/gen_snapshot.py（票01
// 快照脚本，票05 页面直接消费）逐字一致——本文件头部不另立 schema 副本，
// 消费面对照该脚本的 schema 段；差异仅一处如实声明：kpi.cost.value 恒 null
// （usage 行 token 级成本在 internal/report 无导出面，本票涉及路径锁
// internal/daemon/——不造第二份公式，票面「金额面由前端用可算性结构处理，
// 端点不硬造」；mock 快照里的 value 是评审通道的 Python 侧产物）。
//
// 公式单源红线：节省额只调 report.SavingsV1——KPI 全时段对全量 entries 一次
// 调用（与 /report 同法），逐日按日分桶后每桶各调一次（票面口径），净额取
// SavingsV1 内部 Round(...,4) 原值，不再包一层公式；分桶净额之和与全时段净额
// 可能有 4 位小数舍入差（首查项①，如实接受）。
//
// 红线（queryapi.go 顶部块全文适用）：响应永不包含消息内容（usage 行的
// title 等绝不回吐）、无凭据字段、纯只读（账本 Read 锁外只读遍历，
// query_report.go 同纪律）；服务端聚合，绝不整段下发流水（明细端点分页
// ≤500 行/次）。

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"ferryman/internal/accounts"
	"ferryman/internal/clock"
	"ferryman/internal/mathx"
	"ferryman/internal/prices"
	"ferryman/internal/report"
)

// statsKinds 五类事件（gen_snapshot.py KINDS 同员同序——冻结 schema）。
var statsKinds = []string{"block", "bypass", "inject", "handoff", "window"}

// statsUsageLimitMax / Def 明细分页上限与缺省（票面：≤500 行/次、缺省 50）。
const (
	statsUsageLimitMax = 500
	statsUsageLimitDef = 50
)

// statsEchoACAO 实际响应的 CORS 回声（query_widget.go handleWidgetSummary
// 同款）：白名单源（Tauri 壳 webview）才回声，无 Origin（curl/壳外）不设头。
func statsEchoACAO(w http.ResponseWriter, r *http.Request) {
	if origin := r.Header.Get("Origin"); widgetAllowedOrigins[origin] {
		w.Header().Set("Access-Control-Allow-Origin", origin)
	}
}

// statsWindow since/until 查询窗：Read 过滤秒值（0=不过滤）＋序列/明细窗的
// 日界字符串（YYYY-MM-DD；空=缺省）。
type statsWindow struct {
	since, until       float64
	sinceDay, untilDay string
}

// parseStatsWindow 解析 since/until（YYYY-MM-DD，本地时区）。until 含当日
// 全天（report.parseDate endOfDay 同口径：+86399.99）；格式坏 → 400 文案。
func parseStatsWindow(since, until string) (statsWindow, error) {
	var win statsWindow
	if since != "" {
		t, err := time.ParseInLocation("2006-01-02", since, time.Local)
		if err != nil {
			return win, fmt.Errorf("since/until 必须为 YYYY-MM-DD，得到 since=%q", since)
		}
		win.since, win.sinceDay = float64(t.Unix()), since
	}
	if until != "" {
		t, err := time.ParseInLocation("2006-01-02", until, time.Local)
		if err != nil {
			return win, fmt.Errorf("since/until 必须为 YYYY-MM-DD，得到 until=%q", until)
		}
		win.until, win.untilDay = float64(t.Unix())+86399.99, until
	}
	return win, nil
}

// statsEconBook 经济价格表选取（query_report.go handleReport 87-99 行同口径的
// 输入装配，非公式副本）：provider 命中取之，否则仅一本取唯一本，再否则 nil
// （不可算，不造数）。
func statsEconBook(d *Daemon, books map[string]prices.PriceBook) *prices.PriceBook {
	econKey := ""
	if d.Cfg != nil {
		econKey = d.Cfg.EconKey() // econ_provider 优先（独立票），缺省回落 provider
	}
	if econKey != "" {
		if b, ok := books[econKey]; ok {
			return &b
		}
	}
	if len(books) == 1 {
		for _, b := range books {
			return &b
		}
	}
	return nil
}

// statsDayBucket 逐日桶：usage 四列＋请求数、五类事件计数、五类行原样留存
// （供每桶调 SavingsV1 单源复算）。
type statsDayBucket struct {
	input, cacheRead, cacheCreation, output, requests int
	events                                            map[string]int
	entries                                           []map[string]any
}

// handleStatsSummary GET /stats/summary?since=&until=（均可选，缺省=全时段）：
// KPI（四列＋requests＋命中率两原始数＋可算性）＋逐日序列（自账本最早日至
// 今天；since 给定则自 since 日；until 给定则止于 until 日），日界=本地时区
// 自然日，连续无空洞（空日零值占位）。
func handleStatsSummary(d *Daemon, w http.ResponseWriter, r *http.Request) {
	statsEchoACAO(w, r)
	q := r.URL.Query()
	win, err := parseStatsWindow(qsOr(q, "since", ""), qsOr(q, "until", ""))
	if err != nil {
		badRequest(w, err)
		return
	}
	var entries []map[string]any
	if d.Accounts != nil {
		entries = d.Accounts.Read(accounts.ReadOpts{Since: win.since, Until: win.until})
	}
	books := queryReportPrices()
	econBook := statsEconBook(d, books)
	computable, note := savingsComputability(econBook)
	unit := "智谱积分" // gen_snapshot 同款缺省（无经济表时的展示单位）
	if econBook != nil {
		unit = econBook.Unit
	}

	// 一遍过：KPI 四列累计＋逐日分桶（本地自然日）。归日 memo：账本行近似
	// 时序（月文件序+追加序），同日连续行免重复 Format（19.5 万行 → 数十次）。
	buckets := map[string]*statsDayBucket{}
	var in, cr, cc, outN, reqs int
	firstDay := ""
	var lastT time.Time
	lastDay, haveLast := "", false
	for _, e := range entries {
		t := time.Unix(int64(acctNum(e, "ts")), 0).In(time.Local)
		day := lastDay
		if !haveLast || t.Year() != lastT.Year() || t.YearDay() != lastT.YearDay() {
			day = t.Format("2006-01-02")
			lastT, lastDay, haveLast = t, day, true
		}
		if firstDay == "" || day < firstDay {
			firstDay = day
		}
		b := buckets[day]
		if b == nil {
			b = &statsDayBucket{events: map[string]int{}}
			buckets[day] = b
		}
		switch k := strVal(e, "kind"); k {
		case "usage":
			vi, vc, vcc, vo := int(acctNum(e, "input_tokens")),
				int(acctNum(e, "cache_read_tokens")),
				int(acctNum(e, "cache_creation_tokens")),
				int(acctNum(e, "output_tokens"))
			in, cr, cc, outN, reqs = in+vi, cr+vc, cc+vcc, outN+vo, reqs+1
			b.input, b.cacheRead, b.cacheCreation, b.output, b.requests =
				b.input+vi, b.cacheRead+vc, b.cacheCreation+vcc, b.output+vo, b.requests+1
		case "block", "bypass", "inject", "handoff", "window":
			b.events[k]++
			b.entries = append(b.entries, e)
		}
	}

	// KPI 成效账：对全量 entries 一次 SavingsV1（与 /report 同法）。
	full := report.SavingsV1(entries, books, econBook)
	tot, _ := full["totals"].(map[string]any)
	var gross, inject, net any
	hc := 0.0
	counts := map[string]any{}
	for _, k := range statsKinds {
		counts[k] = 0
	}
	if tot != nil {
		hc, _ = tot["handoff_cost"].(float64)
		counts["block"], counts["bypass"] = tot["blocks"], tot["bypass"]
		counts["inject"], counts["handoff"] = tot["injects"], tot["handoffs"]
		counts["window"] = tot["windows"]
		if computable { // 不可算：金额面 null（冻结 schema；计数/交接照发）
			g, _ := tot["gross"].(float64)
			ic, _ := tot["inject_cost"].(float64)
			gross = mathx.Round(g, 6)
			inject = mathx.Round(ic, 6)
			net = tot["net"] // SavingsV1 内部已 Round(...,4)
		}
	}

	hit, base := cr, cr+in
	var rate any // base=0 → null（gen_snapshot 同款）
	if base > 0 {
		rate = float64(hit) / float64(base)
	}
	kpi := map[string]any{
		"requests": reqs,
		"tokens": map[string]any{"input": in, "cache_read": cr,
			"cache_creation": cc, "output": outN},
		"cache_hit": map[string]any{"hit": hit, "base": base, "rate": rate},
		"cost": map[string]any{"computable": computable, "note": note,
			"unit": unit, "value": nil},
		"savings": map[string]any{
			"computable": computable, "note": note, "unit": unit,
			"gross": gross, "inject_cost": inject,
			"handoff_cost": mathx.Round(hc, 6),
			"net":          net, "counts": counts, "unpriced": full["unpriced"],
		},
	}

	// 逐日序列：起点=since 日或账本最早日（皆无则今天），终点=until 日或今天。
	now := time.Unix(int64(clock.Now()), 0).In(time.Local)
	today := now.Format("2006-01-02")
	start, end := today, today
	if firstDay != "" && firstDay < start {
		start = firstDay
	}
	if win.sinceDay != "" {
		start = win.sinceDay
	}
	if win.untilDay != "" {
		end = win.untilDay
	}
	days := make([]map[string]any, 0, len(buckets)+1)
	if start <= end { // YYYY-MM-DD 字典序=时间序；起点晚于终点 → 空序列
		cur, _ := time.ParseInLocation("2006-01-02", start, time.Local)
		for {
			key := cur.Format("2006-01-02")
			days = append(days, statsDayObj(key, buckets[key], computable, books, econBook))
			if key == end {
				break
			}
			cur = cur.AddDate(0, 0, 1)
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"generated_at": now.Format(time.RFC3339),
		"kpi":          kpi,
		"days":         days,
	})
}

// statsDayObj 单日对象（冻结 schema：date/四列短名/requests/events 五类/
// savings{computable,gross,inject_cost,handoff_cost,net}/neg）。逐日节省=该桶
// 五类行调一次 report.SavingsV1（单源复用，不抄公式）；空桶零值不调。
func statsDayObj(key string, b *statsDayBucket, computable bool,
	books map[string]prices.PriceBook, econBook *prices.PriceBook) map[string]any {
	var in, cr, cc, outN, reqs int
	events := map[string]any{}
	for _, k := range statsKinds {
		events[k] = 0
	}
	if b != nil {
		in, cr, cc, outN, reqs = b.input, b.cacheRead, b.cacheCreation, b.output, b.requests
		for _, k := range statsKinds {
			events[k] = b.events[k]
		}
	}
	var gross, inject, net any
	hc := 0.0
	n := 0.0
	if b != nil && len(b.entries) > 0 {
		tot, _ := report.SavingsV1(b.entries, books, econBook)["totals"].(map[string]any)
		if tot != nil {
			hc, _ = tot["handoff_cost"].(float64)
			if computable {
				g, _ := tot["gross"].(float64)
				ic, _ := tot["inject_cost"].(float64)
				n, _ = tot["net"].(float64)
				gross, inject = mathx.Round(g, 6), mathx.Round(ic, 6)
				net = n
			}
		}
	} else if computable { // 空桶零金额（可算态数值 0，不可算态保持 null）
		gross, inject, net = 0.0, 0.0, 0.0
	}
	return map[string]any{
		"date":           key,
		"input":          in,
		"cache_read":     cr,
		"cache_creation": cc,
		"output":         outN,
		"requests":       reqs,
		"events":         events,
		"savings": map[string]any{"computable": computable, "gross": gross,
			"inject_cost": inject, "handoff_cost": mathx.Round(hc, 6), "net": net},
		"neg": net != nil && net.(float64) < 0,
	}
}

// handleStatsUsage GET /stats/usage?since=&until=&project=&model=&limit=&offset=：
// 逐请求 usage 明细分页。过滤：时间范围/项目走 accounts.ReadOpts（盘上），
// model 内存过滤（包含匹配、大小写不敏感）；分页 limit 缺省 50、上限 500
// （超出钳制），offset 缺省 0；时间倒序（gen_snapshot details 同序，稳定排序
// 保同刻行盘序）。行字段=ts_iso/project/session_id/model/四列 token（账本
// 原字段名）——不含 title、不含 cost 金额（票面：金额面由前端用可算性结构
// 处理，端点不硬造）。
func handleStatsUsage(d *Daemon, w http.ResponseWriter, r *http.Request) {
	statsEchoACAO(w, r)
	q := r.URL.Query()
	win, err := parseStatsWindow(qsOr(q, "since", ""), qsOr(q, "until", ""))
	if err != nil {
		badRequest(w, err)
		return
	}
	limit := statsUsageLimitDef
	if s := qsOr(q, "limit", ""); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 {
			badRequest(w, fmt.Errorf("limit 必须为 ≥1 的整数（上限 %d），得到 %q",
				statsUsageLimitMax, s))
			return
		}
		if n > statsUsageLimitMax {
			n = statsUsageLimitMax
		}
		limit = n
	}
	offset := 0
	if s := qsOr(q, "offset", ""); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 0 {
			badRequest(w, fmt.Errorf("offset 必须为 ≥0 的整数，得到 %q", s))
			return
		}
		offset = n
	}

	var entries []map[string]any
	if d.Accounts != nil {
		// kind 过滤下推（缓存后六维过滤在内存条目上扫，少一遍科目判）：
		// 本端点只消费 usage 行。
		entries = d.Accounts.Read(accounts.ReadOpts{
			Since: win.since, Until: win.until, Project: qsOr(q, "project", ""),
			Kind: "usage"})
	}
	modelQ := strings.ToLower(qsOr(q, "model", ""))
	rows := make([]map[string]any, 0, 64)
	for _, e := range entries {
		if modelQ != "" && !strings.Contains(strings.ToLower(strVal(e, "model")), modelQ) {
			continue
		}
		rows = append(rows, e)
	}
	sort.SliceStable(rows, func(i, j int) bool { // 时间倒序
		return acctNum(rows[i], "ts") > acctNum(rows[j], "ts")
	})
	total := len(rows)
	lo, hi := offset, offset+limit
	if lo > total {
		lo = total
	}
	if hi > total {
		hi = total
	}
	page := rows[lo:hi]
	out := make([]map[string]any, 0, len(page))
	for _, e := range page {
		out = append(out, map[string]any{
			"ts_iso":                strVal(e, "ts_iso"),
			"project":               strVal(e, "project"),
			"session_id":            strVal(e, "session_id"),
			"model":                 strVal(e, "model"),
			"input_tokens":          int(acctNum(e, "input_tokens")),
			"cache_read_tokens":     int(acctNum(e, "cache_read_tokens")),
			"cache_creation_tokens": int(acctNum(e, "cache_creation_tokens")),
			"output_tokens":         int(acctNum(e, "output_tokens")),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"total": total, "limit": limit, "offset": offset, "rows": out,
	})
}
