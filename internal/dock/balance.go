// balance.go — 票07：智谱（bigmodel）余额只读查询。
//
// 语义（票面钉死）：
//   - 只读 GET：Authorization: Bearer <[dock].api_key>（与透传改写出站同一把
//     真钥，T39：钥只活本机 config.toml，不入日志/账本/错误）。
//   - 按需触发：调用方（daemon /stats 面板拉取）拉一次查一次；本文件不做任何
//     后台轮询/定时器。
//   - 错误只出类别（未配置 / 网络错误 / 网络错误（超时） / 上游状态 NNN /
//     响应解析失败）——错误串永不携带 api_key 与 URL 原文（net/url.Error 的
//     包裹文本可能带 URL，故一律不透传原始错误）。
//   - 响应只取数值/时间字段（balance/expire 类，宽松定位：data 包裹形优先、
//     顶层兜底），其余字段一律不落——BalanceInfo 只有两字段，结构即白名单。
package dock

import (
	"bytes"
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

// BalanceTimeoutS 余额查询墙钟超时（票07 规定 5s）。
const BalanceTimeoutS = 5 * time.Second

// 类别文案（错误串即展示行成分，展示层拼「余额查询失败：<类别>」）。
const (
	balanceCatNetwork = "网络错误"
	balanceCatTimeout = "网络错误（超时）"
	balanceCatParse   = "响应解析失败"
)

// ErrBalanceNotConfigured 未配置哨兵：无 [dock] 节或 api_key 为空（展示层
// 据此出「未配置」文案单源；该路径零 HTTP）。
var ErrBalanceNotConfigured = errors.New("未配置")

// BalanceInfo 余额查询结果——只取数值/时间字段（结构即白名单，票07；
// 字面量原样保留，如 "310.00" 不塌缩成 310）。
type BalanceInfo struct {
	Balance string // 余额数值字面量
	Expire  string // 过期时间字段字面量（可选，缺失为空）
}

// FetchBalance 生产入口：5s 墙钟超时。
func FetchBalance(dk *config.DockCfg) (BalanceInfo, error) {
	return FetchBalanceWithClient(&http.Client{Timeout: BalanceTimeoutS}, dk)
}

// FetchBalanceWithClient client 注入内核（测试注入短超时/httptest）。
func FetchBalanceWithClient(c *http.Client, dk *config.DockCfg) (BalanceInfo, error) {
	if dk == nil || strings.TrimSpace(dk.APIKey) == "" {
		return BalanceInfo{}, ErrBalanceNotConfigured // 未配置不发请求
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet,
		balanceEndpoint(dk), nil)
	if err != nil { // balance_url 配坏：不出 URL 原文（可能内嵌敏感串）
		return BalanceInfo{}, errors.New(balanceCatNetwork)
	}
	req.Header.Set("Authorization", "Bearer "+dk.APIKey) // 真钥只进出站头
	req.Header.Set("Accept", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			return BalanceInfo{}, errors.New(balanceCatTimeout)
		}
		return BalanceInfo{}, errors.New(balanceCatNetwork) // 不包裹 err 原文（防 URL 泄漏）
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return BalanceInfo{}, fmt.Errorf("上游状态 %d", resp.StatusCode)
	}
	// 防御性 1 MiB 上限：余额响应不该大，超大截断按解析失败处理即可。
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return BalanceInfo{}, errors.New(balanceCatNetwork)
	}
	return parseBalance(body)
}

// balanceEndpoint 端点解析单源：balance_url 覆写（空白视同未配），否则内置
// 默认（程序化构造 DockCfg 不经 TOML 解析层时的防御回落）。
func balanceEndpoint(dk *config.DockCfg) string {
	if u := strings.TrimSpace(dk.BalanceURL); u != "" {
		return u
	}
	return config.DefaultDockBalanceURL
}

// parseBalance 防御性解析：data 包裹形优先、顶层兜底，只取 balance/expire
// 两键（确切响应字段以运行时实测为准，票07 授权宽松定位）。
func parseBalance(body []byte) (BalanceInfo, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber() // 数值按字面量保留（"310.00" 不丢形）
	var root map[string]any
	if err := dec.Decode(&root); err != nil {
		return BalanceInfo{}, errors.New(balanceCatParse)
	}
	b, ok := pickBalanceField(root, "balance")
	if !ok {
		return BalanceInfo{}, errors.New(balanceCatParse)
	}
	info := BalanceInfo{Balance: balanceLiteral(b)}
	if info.Balance == "" { // 缺失/对象/数组/布尔/null 一律按解析失败
		return BalanceInfo{}, errors.New(balanceCatParse)
	}
	if e, ok := pickBalanceField(root, "expire"); ok {
		info.Expire = balanceLiteral(e)
	}
	return info, nil
}

// pickBalanceField 宽松定位：data 包裹形优先，顶层兜底。
func pickBalanceField(root map[string]any, key string) (any, bool) {
	if d, ok := root["data"].(map[string]any); ok {
		if v, ok := d[key]; ok {
			return v, true
		}
	}
	v, ok := root[key]
	return v, ok
}

// balanceLiteral JSON 值 → 字面量串：数值（json.Number）原样、字符串去空白；
// 其余类型返回空串（调用方按解析失败处理）。
func balanceLiteral(v any) string {
	switch x := v.(type) {
	case json.Number:
		return x.String()
	case string:
		return strings.TrimSpace(x)
	}
	return ""
}
