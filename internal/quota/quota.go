// Package quota — 余量查询器 ×3（票 06）：GLM（智谱）/Kimi/DeepSeek。
//
// 供应商行为全部按 cc-switch 参考实现实证移植（CLAUDE.md 铁律：供应商端点
// 不猜测——farion1231/cc-switch src-tauri/src/services/coding_plan.rs 与
// balance.rs，2026-09-25 快照）：
//   - GLM：GET {open.bigmodel.cn|api.z.ai}/api/monitor/usage/quota/limit，
//     鉴权=裸 key（Authorization 头不带 Bearer）；unit:3=5h、unit:6=周是唯一
//     分类锚（禁按 nextResetTime 排序代替——周期末尾每周窗会比 5h 窗更早
//     重置必标反，cc-switch #3036）；type 大小写不敏感兼容
//     TOKENS_LIMIT/CREDIT_LIMIT；percentage=已用%（剩余=100−p，端点无绝对
//     值）；nextResetTime 毫秒；老套餐单条降级单环；信封
//     {success,msg,data:{limits[],level?}}，业务级错误 success=false。
//   - Kimi：GET api.kimi.com/coding/v1/usages（Bearer）；顶层 usage=周窗、
//     limits[].detail=5h；额度数字/字符串两形态；口径漂移防御：limit≈100
//     整数疑百分比口径，不展示绝对数；resetTime 字符串（ISO 原样解析）或
//     秒/毫秒数字（自动判位，≤0 视为无重置）。
//   - DeepSeek：GET api.deepseek.com/user/balance（Bearer）；金额数字/字符串
//     字面量保留；total=granted+topped_up 拆分；is_available 缺省 true。
//
// 公共纪律（票面钉死）：5s 墙钟超时；错误只出类别（未配置 / 网络错误 /
// 网络错误（超时）/ 上游状态 NNN / 响应解析失败 / 上游业务错误），永不携带
// api_key 与 URL 原文（net/url.Error 的包裹文本可能带 URL，一律不透传原始
// 错误——internal/dock/balance.go 同款纪律）；解析宽容（未文档化端点）；
// 结构即白名单：只取所需字段，其余不落。
package quota

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"ferryman/internal/config"
)

// TimeoutS 查询墙钟超时（票 06 规定 5s）。
const TimeoutS = 5 * time.Second

// 错误类别文案（与 dock/balance.go 同源同款；错误串即展示行成分）。
const (
	catNetwork = "网络错误"
	catTimeout = "网络错误（超时）"
	catParse   = "响应解析失败"
	catBiz     = "上游业务错误"
)

// ErrNotConfigured 未配置哨兵：无条目/无 api_key（或 GLM 无 base_url）——
// 零 HTTP，展示层据此出「未配置」文案单源。
var ErrNotConfigured = errors.New("未配置")

// Window 一个限额窗口（剩余制）。GLM 只有百分比（无绝对值），Kimi 有
// remaining/limit 绝对值（口径可疑时 HasAbs=false 只出百分比）。
type Window struct {
	RemainingPct float64   // 剩余百分比 0..100
	Remaining    float64   // 绝对剩余（Kimi；GLM 恒 0）
	Limit        float64   // 绝对上限（Kimi；GLM 恒 0）
	HasAbs       bool      // 绝对值口径可信时才 true
	ResetsAt     time.Time // 重置时刻
	HasReset     bool      // nextResetTime/resetTime 缺席或 ≤0 时 false
}

// GLMQuota 智谱编码套餐查询结果（老套餐单条 → Week 为 nil，降级单环）。
type GLMQuota struct {
	FiveHour *Window
	Week     *Window
	Tools    *ToolQuota // MCP 工具增值服务额度（TIME_LIMIT，所有版本/档位都有）
	Plan     string     // data.level 套餐档（可空）
}

// ToolQuota MCP 工具增值服务额度（按调用次数；2026-09-25 用户口径+真机实证：
// usage=总额度、currentValue=已用、remaining=剩余、percentage=已用%、
// usageDetails=分工具计数、nextResetTime=重置）。结构即白名单。
type ToolQuota struct {
	Remaining    float64 // 剩余次数
	Total        float64 // 总额度
	RemainingPct float64 // 剩余 %（绝对数口径现算；绝对数缺失回落 100−percentage）
	Details      []ToolUsage
	ResetsAt     time.Time
	HasReset     bool
}

// ToolUsage 分工具计数（usageDetails[].modelCode/usage）。
type ToolUsage struct {
	Name string
	Used float64
}

// KimiQuota Kimi Coding Plan 查询结果。
type KimiQuota struct {
	FiveHour *Window
	Week     *Window
}

// DSBalance DeepSeek 按量余额查询结果（金额字面量原样保留，如 "110.00"）。
type DSBalance struct {
	Total     string
	Granted   string
	ToppedUp  string
	Available bool // is_available 缺省 true
}

// DetectKind 按 base_url 判定供应商类别（cc-switch detect_provider 同款
// 小写子串判定）："glm"|"kimi"|"deepseek"|""（未识别——widget 契约只认三家，
// 未识别上游不进 /widget/summary）。
func DetectKind(baseURL string) string {
	u := strings.ToLower(baseURL)
	switch {
	case strings.Contains(u, "bigmodel.cn"), strings.Contains(u, "z.ai"):
		return "glm"
	case strings.Contains(u, "api.kimi.com"):
		return "kimi"
	case strings.Contains(u, "api.deepseek.com"):
		return "deepseek"
	}
	return ""
}

// httpGetJSON 共享取数内核：GET + 鉴权头 + 类别化错误 + 1 MiB 防御上限。
// auth 值原样进 Authorization 头（GLM=裸 key；Kimi/DS="Bearer "+key——拼装
// 在各家 Fetch 里，本函数不猜前缀）。
func httpGetJSON(c *http.Client, url, auth string) ([]byte, error) {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	if err != nil { // URL 配坏：不出原文（可能内嵌敏感串）
		return nil, errors.New(catNetwork)
	}
	req.Header.Set("Authorization", auth)
	req.Header.Set("Accept", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			return nil, errors.New(catTimeout)
		}
		return nil, errors.New(catNetwork) // 不包裹 err 原文（防 URL 泄漏）
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("上游状态 %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, errors.New(catNetwork)
	}
	return body, nil
}

// decodeObject 宽容 JSON 对象解码（UseNumber：数值字面量保留）。
func decodeObject(body []byte) (map[string]any, error) {
	dec := json.NewDecoder(strings.NewReader(string(body)))
	dec.UseNumber()
	var root map[string]any
	if err := dec.Decode(&root); err != nil {
		return nil, errors.New(catParse)
	}
	return root, nil
}

// asF64 数值/字符串两形态通吃（Kimi 额度、DS 金额都可能发字符串）。
func asF64(v any) (float64, bool) {
	switch x := v.(type) {
	case json.Number:
		f, err := x.Float64()
		return f, err == nil
	case float64:
		return x, true
	case string:
		var f float64
		if _, err := fmt.Sscanf(strings.TrimSpace(x), "%g", &f); err == nil {
			return f, true
		}
	}
	return 0, false
}

// asLiteral JSON 值 → 字面量串：数值（json.Number）原样、字符串去空白；
// 其余类型返回空串（调用方按解析失败/缺失处理）。
func asLiteral(v any) string {
	switch x := v.(type) {
	case json.Number:
		return x.String()
	case string:
		return strings.TrimSpace(x)
	}
	return ""
}

// keyedEntry 未配置判定共用：条目缺失 / key 空 / （GLM）base_url 空。
func keyedEntry(u *config.DockUpstream, needBase bool) error {
	if u == nil {
		return ErrNotConfigured
	}
	if strings.TrimSpace(u.APIKey) == "" {
		return ErrNotConfigured
	}
	if needBase && strings.TrimSpace(u.BaseURL) == "" {
		return ErrNotConfigured
	}
	return nil
}
