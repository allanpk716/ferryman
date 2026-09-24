// deepseek.go — DeepSeek 按量余额查询（票 06）。
//
// GET https://api.deepseek.com/user/balance（Bearer；端点固定）。响应
// {balance_infos: [{currency, total_balance, granted_balance,
// topped_up_balance}], is_available}；金额数字/字符串字面量保留（"110.00"
// 不塌缩成 110）；total=granted+topped_up 拆分透传（验证在消费侧）；
// is_available 缺省 true（cc-switch query_deepseek 逐字先例）。
package quota

import (
	"net/http"

	"ferryman/internal/config"
)

// dsBalanceURL DeepSeek 余额端点（固定）。
const dsBalanceURL = "https://api.deepseek.com/user/balance"

// FetchDeepSeekWithClient client 注入内核。
func FetchDeepSeekWithClient(c *http.Client, u *config.DockUpstream) (*DSBalance, error) {
	if err := keyedEntry(u, false); err != nil {
		return nil, err
	}
	body, err := httpGetJSON(c, dsBalanceURL, "Bearer "+u.APIKey)
	if err != nil {
		return nil, err
	}
	return parseDeepSeek(body)
}

// FetchDeepSeek 生产入口：5s 墙钟超时。
func FetchDeepSeek(u *config.DockUpstream) (*DSBalance, error) {
	return FetchDeepSeekWithClient(&http.Client{Timeout: TimeoutS}, u)
}

// parseDeepSeek 响应体解析（纯函数）。首条 balance_infos 为准；金额缺失
// → 解析失败（不造数）；空数组 → 解析失败。
func parseDeepSeek(body []byte) (*DSBalance, error) {
	root, err := decodeObject(body)
	if err != nil {
		return nil, err
	}
	infos, _ := root["balance_infos"].([]any)
	if len(infos) == 0 {
		return nil, errParse()
	}
	m, _ := infos[0].(map[string]any)
	if m == nil {
		return nil, errParse()
	}
	total := asLiteral(m["total_balance"])
	if total == "" {
		return nil, errParse()
	}
	b := &DSBalance{
		Total:     total,
		Granted:   asLiteral(m["granted_balance"]),
		ToppedUp:  asLiteral(m["topped_up_balance"]),
		Available: true,
	}
	if av, ok := root["is_available"].(bool); ok {
		b.Available = av
	}
	return b, nil
}
