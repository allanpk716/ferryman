// guard.go — 票06：双改写守卫与改写准入（spec F10/S3）。
//
// 为什么在构造期一次判死（不逐请求重判）：渡口上游配置重启生效（无热加载），
// base_url 与 model_map 在 daemon 生命周期内不变——准入状态是常量，逐请求
// 重判只会白烧 CPU 并引入半途切换的不一致。拒绝＝退回纯透传（绝不半改写），
// 与票05 泳道决策对齐：改写模式必须显式配 default，否则宁可全透传。
//
// 判定单源：doctor（installer）与 daemon 构造期都读本文件的
// ResolveRewrite/DoubleRewriteRisk，绝不出现两套判据；本地中转地址判定单源
// 在 config.IsLocalRelayAddr（票01 迁入——配置层校验豁免与守卫共用同一谓词）。
package dock

import (
	"fmt"

	"ferryman/internal/config"
)

// DoubleRewriteRisk upstream 地址在回环主机＋已知本地中转端口上＝双重改写
// 风险（doctor 与构造期守卫共用此单源；判定本体＝config.IsLocalRelayAddr）。
// 地址解析失败＝不判风险（构造期 New 另有合法性校验兜底，两处职责不同）。
func DoubleRewriteRisk(upstreamBaseURL string) bool {
	return config.IsLocalRelayAddr(upstreamBaseURL)
}

// ResolveRewrite 改写准入（旧 [dock] 单值面：doctor 沿用；显式开关形）：
//   - RewriteEnabled=false ＝ 未请求改写（ok=false、reason 空——不算告警）；
//   - 请求了但 model_map 缺 default 键/值为空、或上游指回本地中转端口
//     ＝ 拒绝（ok=false＋reason 为告警文案，调用方负责打日志/体检展示）；
//   - 其余 ＝ 放行（ok=true）并给出换算后的 RewriteConfig：default 键摘出、
//     别名表净化、text_only 克隆防外部改动。
func ResolveRewrite(d *config.DockCfg) (RewriteConfig, bool, string) {
	return resolveRewrite(d.RewriteEnabled, d.UpstreamBaseURL, d.ModelMap, d.TextOnly)
}

// resolveRewrite 改写准入内核（票01：装配点隐含 true 与 ResolveRewrite 显式
// 开关共用；判定顺序不变）。
func resolveRewrite(enabled bool, upstreamBaseURL string,
	modelMap map[string]string, textOnly []string) (RewriteConfig, bool, string) {
	if !enabled {
		return RewriteConfig{}, false, ""
	}
	if modelMap["default"] == "" {
		return RewriteConfig{}, false,
			"改写模式要求 model_map 含非空 default 键（当前缺失/为空）——" +
				"空配置未知名原样透传，退回纯透传"
	}
	if DoubleRewriteRisk(upstreamBaseURL) {
		return RewriteConfig{}, false, fmt.Sprintf(
			"上游 %s 指向本地中转端口（15721/15722/15723）——"+
				"守卫强制透传（防双重改写）", upstreamBaseURL)
	}
	rw := RewriteConfig{
		ModelMap: make(map[string]string, len(modelMap)),
		Default:  modelMap["default"],
		TextOnly: append([]string(nil), textOnly...),
	}
	for k, v := range modelMap {
		if k == "default" {
			continue
		}
		rw.ModelMap[k] = v
	}
	return rw, true, ""
}
