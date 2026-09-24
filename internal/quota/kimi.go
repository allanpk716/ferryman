// kimi.go — Kimi Coding Plan 用量查询（票 06）。
//
// GET https://api.kimi.com/coding/v1/usages（Bearer；端点固定，不随 base_url
// 派生——cc-switch query_kimi 逐字先例）。响应顶层 usage=周窗、
// limits[].detail=5h 窗；limit/remaining 数字/字符串两形态；resetTime
// 字符串（ISO 或数字串，票 01）或秒/毫秒数字（自动判位，≤0 视为无重置）。
//
// 口径漂移防御（票面钉死）：limit≈100 整数疑百分比口径 → 不展示绝对数
// （HasAbs=false），remaining/limit 归一化照出。
package quota

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"ferryman/internal/config"
)

// kimiUsageURL Kimi 用量端点（固定）。
const kimiUsageURL = "https://api.kimi.com/coding/v1/usages"

// FetchKimiWithClient client 注入内核。
func FetchKimiWithClient(c *http.Client, u *config.DockUpstream) (*KimiQuota, error) {
	if err := keyedEntry(u, false); err != nil {
		return nil, err
	}
	body, err := httpGetJSON(c, kimiUsageURL, "Bearer "+u.APIKey)
	if err != nil {
		return nil, err
	}
	return parseKimi(body)
}

// FetchKimi 生产入口：5s 墙钟超时。
func FetchKimi(u *config.DockUpstream) (*KimiQuota, error) {
	return FetchKimiWithClient(&http.Client{Timeout: TimeoutS}, u)
}

// parseKimi 响应体解析（纯函数）。
func parseKimi(body []byte) (*KimiQuota, error) {
	root, err := decodeObject(body)
	if err != nil {
		return nil, err
	}
	q := &KimiQuota{}
	// 5h 窗：limits[].detail（多条时取首条——cc-switch 对每条各出一档，
	// 实测仅一条；widget 契约 5h 单环）
	if limits, _ := root["limits"].([]any); limits != nil {
		for _, it := range limits {
			m, _ := it.(map[string]any)
			if m == nil {
				continue
			}
			d, _ := m["detail"].(map[string]any)
			if d == nil {
				continue
			}
			if w := kimiWindow(d); w != nil {
				q.FiveHour = w
				break
			}
		}
	}
	// 周窗：顶层 usage
	if usage, _ := root["usage"].(map[string]any); usage != nil {
		q.Week = kimiWindow(usage)
	}
	if q.FiveHour == nil && q.Week == nil {
		return nil, errParse() // 两窗全缺：形状不符
	}
	return q, nil
}

// kimiWindow {limit, remaining, resetTime} → 剩余制窗口。limit/remaining
// 缺失或 limit≤0 → nil（不造数——按无该窗处理）。
func kimiWindow(m map[string]any) *Window {
	limit, okL := asF64(m["limit"])
	remaining, okR := asF64(m["remaining"])
	if !okL || !okR || limit <= 0 {
		return nil
	}
	w := &Window{
		RemainingPct: remaining / limit * 100,
		Remaining:    remaining,
		Limit:        limit,
		HasAbs:       !suspiciousPercentScale(limit),
	}
	if t, ok := resetTimeOf(m["resetTime"]); ok {
		w.ResetsAt, w.HasReset = t, true
	}
	return w
}

// suspiciousPercentScale 口径漂移防御：limit≈100 整数疑百分比口径
// （绝对数在百分比口径下毫元意义，不展示）。
func suspiciousPercentScale(limit float64) bool {
	return limit > 99.5 && limit < 100.5
}

// resetTimeOf resetTime 多格式兼容（cc-switch extract_reset_time 同款）：
// 字符串先剪首尾空白，按常见 ISO 形态解析（缺时区按本地），ISO 全败后按
// 纯数字解析（票 01：数字串形态）；数字（含数字串）秒/毫秒自动判位，≤0 无。
func resetTimeOf(v any) (time.Time, bool) {
	switch x := v.(type) {
	case string:
		s := strings.TrimSpace(x)
		if s == "" {
			return time.Time{}, false
		}
		for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05",
			"2006-01-02 15:04:05", "2006-01-02"} {
			if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
				return t, true
			}
		}
		// 数字串（"1761412800" 秒/毫秒）——ISO 全败后按数字判位（阈值与数字支同款）。
		if n, err := strconv.ParseInt(s, 10, 64); err == nil && n > 0 {
			if n < 1_000_000_000_000 {
				n *= 1000
			}
			return time.UnixMilli(n), true
		}
		return time.Time{}, false
	default:
		n, ok := asInt64(v)
		if !ok || n <= 0 {
			return time.Time{}, false
		}
		if n < 1_000_000_000_000 { // 秒级 < 1e12，毫秒 ≥ 1e12
			n *= 1000
		}
		return time.UnixMilli(n), true
	}
}
