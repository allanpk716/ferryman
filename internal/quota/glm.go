// glm.go — 智谱（bigmodel/z.ai）编码套餐配额查询（票 06）。
//
// 端点路由（cc-switch zhipu_quota_base 同款）：base_url 含 bigmodel.cn →
// https://open.bigmodel.cn，否则 → https://api.z.ai；路径固定
// /api/monitor/usage/quota/limit。鉴权=裸 key（Authorization 不带 Bearer——
// cc-switch query_zhipu 逐字先例）。信封 {success?, msg?, data:{limits[],
// level?}}；业务级错误 success=false + msg（类别=上游业务错误，msg 只出
// 上游原文不含钥/URL）。
package quota

import (
	"net/http"
	"sort"
	"strings"
	"time"

	"ferryman/internal/config"
)

// glmQuotaPath 配额端点路径（bigmodel.cn 与 z.ai 共用同一后端，字段一致）。
const glmQuotaPath = "/api/monitor/usage/quota/limit"

// glmBase 端点主机单源：bigmodel.cn → 国内站；其余（z.ai 域）→ 国际站。
func glmBase(baseURL string) string {
	if strings.Contains(strings.ToLower(baseURL), "bigmodel.cn") {
		return "https://open.bigmodel.cn"
	}
	return "https://api.z.ai"
}

// FetchGLMWithClient client 注入内核（测试注入 httptest/短超时）。
func FetchGLMWithClient(c *http.Client, u *config.DockUpstream) (*GLMQuota, error) {
	if err := keyedEntry(u, true); err != nil {
		return nil, err
	}
	body, err := httpGetJSON(c, glmBase(u.BaseURL)+glmQuotaPath, u.APIKey) // 裸 key
	if err != nil {
		return nil, err
	}
	return parseGLM(body)
}

// FetchGLM 生产入口：5s 墙钟超时。
func FetchGLM(u *config.DockUpstream) (*GLMQuota, error) {
	return FetchGLMWithClient(&http.Client{Timeout: TimeoutS}, u)
}

// glmEntry 一条 limit 记录的中间形（分类前）。
type glmEntry struct {
	resetMs *int64
	usedPct float64
}

// parseGLM 响应体解析（纯函数，测试直喂夹具）。
//
// 套餐版本差异（2026-09-25 用户口径：智谱 Coding Plan 分 V1/V2/V3，V1 无周
// 限制与月度限制，后续版本才有）：
//   - V1：仅 TOKENS_LIMIT(unit:3) 一条 → FiveHour 有、Week=nil（端点侧据此
//     带「V1 语义」注记）；真机 max 档实测同形。
//   - V2+：unit:3 + unit:6 双窗。月度窗的 unit 码无实证——**未知 unit 一律
//     不做启发式兜底**（防把月窗错标进周槽），等实证再接。
//   - 老形态（条目全无 unit 字段，cc-switch 2026-02-12 前订阅实证）：保留
//     兜底启发式——无 nextResetTime 的优先归 5h（5h 桶在 0% 等状态可能没有
//     reset），其余按 reset 升序填仍空缺的槽。
//
// unit 锚纪律（#3036）：禁按 nextResetTime 排序代替——周期末尾每周窗会比
// 5h 窗更早重置必标反。
func parseGLM(body []byte) (*GLMQuota, error) {
	root, err := decodeObject(body)
	if err != nil {
		return nil, err
	}
	if b, ok := root["success"].(bool); ok && !b {
		msg := asLiteral(root["msg"])
		if msg == "" {
			msg = "未知错误"
		}
		return nil, &bizError{msg}
	}
	data, _ := root["data"].(map[string]any)
	if data == nil {
		return nil, errParse()
	}
	q := &GLMQuota{Plan: asLiteral(data["level"])}

	var fiveHour, weekly *glmEntry
	var unclassified []glmEntry
	sawUnit := false // 任一条目带显式 unit → 严格 unit 锚模式（不做启发式兜底）
	if limits, _ := data["limits"].([]any); limits != nil {
		for _, it := range limits {
			m, _ := it.(map[string]any)
			if m == nil {
				continue
			}
			// type 锚：TOKENS_LIMIT/CREDIT_LIMIT 是配额窗（大小写不敏感——上游
			// 改名 TOKENS_LIMIT→CREDIT_LIMIT 已实测发生，两态都认）；TIME_LIMIT
			// 是 MCP 工具增值服务额度（unit:5，按次数，所有版本/档位都有——
			// 2026-09-25 用户口径），单走 tools 槽。
			t := strings.ToLower(asLiteral(m["type"]))
			switch t {
			case "tokens_limit", "credit_limit":
			case "time_limit":
				if q.Tools == nil {
					if u, has := asInt64(m["unit"]); has && u == 5 {
						q.Tools = parseGLMTools(m)
					}
				}
				continue
			default:
				continue
			}
			p, _ := asF64(m["percentage"]) // 已用%；缺失按 0（cc-switch 同款）
			e := glmEntry{usedPct: p}
			if n, ok := asInt64(m["nextResetTime"]); ok {
				e.resetMs = &n
			}
			unit, hasUnit := asInt64(m["unit"])
			if hasUnit {
				sawUnit = true
			}
			switch unit {
			case 3:
				if hasUnit && fiveHour == nil {
					fiveHour = &e
					continue
				}
			case 6:
				if hasUnit && weekly == nil {
					weekly = &e
					continue
				}
			}
			unclassified = append(unclassified, e)
		}
	}
	// 老形态（全无 unit）才走启发式兜底；unit 在场时未知 unit 条目宁可
	// 不展示也不错标（V2+ 月窗防错标进周槽）。
	if !sawUnit {
		sort.SliceStable(unclassified, func(i, j int) bool {
			a, b := unclassified[i], unclassified[j]
			switch {
			case a.resetMs == nil && b.resetMs != nil:
				return true
			case a.resetMs != nil && b.resetMs == nil:
				return false
			case a.resetMs == nil:
				return false
			}
			return *a.resetMs < *b.resetMs
		})
		for _, e := range unclassified {
			if fiveHour == nil {
				c := e
				fiveHour = &c
			} else if weekly == nil {
				c := e
				weekly = &c
			}
			// 智谱当前最多两条 TOKENS_LIMIT，多余的忽略
		}
	}

	if fiveHour != nil {
		q.FiveHour = glmWindow(*fiveHour)
	}
	if weekly != nil {
		q.Week = glmWindow(*weekly)
	}
	if q.FiveHour == nil && q.Week == nil {
		return nil, errParse() // limits 无一条可认条目：形状不符
	}
	return q, nil
}

// glmWindow 已用%→剩余制窗口（端点无绝对值；毫秒→time）。
func glmWindow(e glmEntry) *Window {
	w := &Window{RemainingPct: 100 - e.usedPct}
	if e.resetMs != nil && *e.resetMs > 0 {
		w.ResetsAt = time.UnixMilli(*e.resetMs)
		w.HasReset = true
	}
	return w
}

// parseGLMTools TIME_LIMIT(unit:5) 条目 → 工具额度（字段语义真机实证：
// usage=总额度/currentValue=已用/remaining=剩余/percentage=已用%/usageDetails=
// 分工具计数；数字字符串两形态通吃；总额度≤0 或两绝对数全缺 → nil 不造数）。
func parseGLMTools(m map[string]any) *ToolQuota {
	total, hasTotal := asF64(m["usage"])
	remaining, hasRemaining := asF64(m["remaining"])
	if !hasTotal || total <= 0 {
		return nil
	}
	q := &ToolQuota{Total: total}
	if hasRemaining {
		q.Remaining = remaining
		q.RemainingPct = remaining / total * 100
	} else if p, ok := asF64(m["percentage"]); ok {
		q.RemainingPct = 100 - p
	} else {
		return nil
	}
	if n, ok := asInt64(m["nextResetTime"]); ok && n > 0 {
		q.ResetsAt = time.UnixMilli(n)
		q.HasReset = true
	}
	if ds, _ := m["usageDetails"].([]any); ds != nil {
		for _, d := range ds {
			dm, _ := d.(map[string]any)
			if dm == nil {
				continue
			}
			name := asLiteral(dm["modelCode"])
			used, ok := asF64(dm["usage"])
			if name == "" || !ok {
				continue
			}
			q.Details = append(q.Details, ToolUsage{Name: name, Used: used})
		}
	}
	return q
}

// bizError 业务级错误（success=false）：类别+上游 msg 原文（不含钥/URL）。
type bizError struct{ msg string }

func (b *bizError) Error() string { return catBiz + "：" + b.msg }

// errParse 解析失败哨兵构造（类别串单源）。
func errParse() error { return &parseError{} }

type parseError struct{}

func (*parseError) Error() string { return catParse }

// asInt64 数值/字符串两形态 int64（nextResetTime 毫秒、unit 锚）。
func asInt64(v any) (int64, bool) {
	f, ok := asF64(v)
	return int64(f), ok
}
