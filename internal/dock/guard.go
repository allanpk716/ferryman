// guard.go — 票06：双改写守卫与改写准入（spec F10/S3）。
//
// 为什么在构造期一次判死（不逐请求重判）：[dock] 节重启生效（无热加载），
// upstream 与 model_map 在 daemon 生命周期内不变——准入状态是常量，逐请求
// 重判只会白烧 CPU 并引入半途切换的不一致。拒绝＝退回纯透传（绝不半改写），
// 与票05 泳道决策对齐：改写模式必须显式配 default，否则宁可全透传。
//
// 判定单源：doctor（installer）与 daemon 构造期都读本文件的
// ResolveRewrite/DoubleRewriteRisk，绝不出现两套判据。
package dock

import (
	"fmt"
	"net/url"
	"strings"

	"ferryman/internal/config"
)

// localRelayPorts 已知本地中转端口：15721=cc-switch（默认上游）、15722=渡口
// 自身、15723=验证转发器。上游指回这三者之一＝改写链路成环（cc-switch 已
// 改一遍、渡口再改一遍＝双重改写）。
var localRelayPorts = map[string]bool{"15721": true, "15722": true, "15723": true}

// isLoopbackHost 回环主机归一判定：localhost / ::1 / 127.0.0.0/8（含 IPv4
// 映射形 ::ffff:127.x）一律视为本机。
func isLoopbackHost(hostname string) bool {
	h := strings.ToLower(strings.TrimSpace(hostname))
	if h == "localhost" || h == "::1" || strings.HasPrefix(h, "127.") {
		return true
	}
	return strings.HasPrefix(h, "::ffff:127.")
}

// DoubleRewriteRisk upstream 地址在回环主机＋已知本地中转端口上＝双重改写
// 风险（doctor 与构造期守卫共用此单源）。地址解析失败＝不判风险（构造期
// New 另有合法性校验兜底，两处职责不同）。
func DoubleRewriteRisk(upstreamBaseURL string) bool {
	u, err := url.Parse(upstreamBaseURL)
	if err != nil || u.Host == "" {
		return false
	}
	return isLoopbackHost(u.Hostname()) && localRelayPorts[u.Port()]
}

// ResolveRewrite 改写准入单源：
//   - RewriteEnabled=false ＝ 未请求改写（ok=false、reason 空——不算告警）；
//   - 请求了但 model_map 缺 default 键/值为空、或上游指回本地中转端口
//     ＝ 拒绝（ok=false＋reason 为告警文案，调用方负责打日志/体检展示）；
//   - 其余 ＝ 放行（ok=true）并给出换算后的 RewriteConfig：default 键摘出、
//     别名表净化、text_only 克隆防外部改动。
func ResolveRewrite(d *config.DockCfg) (RewriteConfig, bool, string) {
	if !d.RewriteEnabled {
		return RewriteConfig{}, false, ""
	}
	if d.ModelMap["default"] == "" {
		return RewriteConfig{}, false,
			"[dock] rewrite_enabled=true 但 model_map 缺 default 键或值为空——" +
				"改写模式必须显式配 default（空配置未知名原样透传），退回纯透传"
	}
	if DoubleRewriteRisk(d.UpstreamBaseURL) {
		return RewriteConfig{}, false, fmt.Sprintf(
			"[dock] rewrite_enabled=true 但上游 %s 指向本地中转端口（15721/15722/15723）"+
				"——防双重改写，退回纯透传", d.UpstreamBaseURL)
	}
	rw := RewriteConfig{
		ModelMap: make(map[string]string, len(d.ModelMap)),
		Default:  d.ModelMap["default"],
		TextOnly: append([]string(nil), d.TextOnly...),
	}
	for k, v := range d.ModelMap {
		if k == "default" {
			continue
		}
		rw.ModelMap[k] = v
	}
	return rw, true, ""
}
