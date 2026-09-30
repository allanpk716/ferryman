// doctor_probe.go — 票05 doctor 三项体检（只读探针）：
//
//	① CheckCCPointsDock      CC 指向渡口；
//	② CheckCodexPointsDock   codex 两份指向渡口且 wire_api=responses 且
//	                         [features] hooks 旗标在位；
//	③ CheckOrcaCodexHealth   orca codex 健康（配置存在、指向渡口、认证形态
//	                         合法）。
//
// 判定与写入器（writer.go）同一套解析函数与目标地址派生（单源）——doctor 与
// apply 绝不出现两套判据（CONTEXT.md 同一检查只许一份实现）。installer 面
// （doctor.go）只做 Verdict → CheckResult 换装，不重复判定。
package provider

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/BurntSushi/toml"
)

// Verdict 体检结论（installer 面换装成 CheckResult：OK→pass、!OK→fail，
// Detail 原样透传）。
type Verdict struct {
	OK     bool
	Detail string
}

// DockURLFromListen 渡口 CC 目标地址：[dock].listen（host:port）→ http 根。
func DockURLFromListen(listen string) string {
	return "http://" + listen
}

// DockCodexURLFromListen 渡口 codex 目标地址：保持 /v1 后缀惯例（与
// wire_api="responses" 兼容为准；与 writer 的 codexBaseURL 同一派生规则）。
func DockCodexURLFromListen(listen string) string {
	return "http://" + listen + "/v1"
}

// ---- ① CC 指向渡口 ----

// CheckCCPointsDock CC 体检：env.ANTHROPIC_BASE_URL 是否指向渡口。首周内不
// 指向多视作 cc-switch 会话自动同步复开（F9 归因指引），文案点名归因与修法。
func CheckCCPointsDock(settingsPath, dockBaseURL string) Verdict {
	raw, err := os.ReadFile(settingsPath)
	if err != nil {
		if os.IsNotExist(err) {
			return Verdict{false, fmt.Sprintf("%s 不存在——CC 未接管（跑 ferryman provider apply）", settingsPath)}
		}
		return Verdict{false, fmt.Sprintf("%s 读取失败: %v", settingsPath, err)}
	}
	var root map[string]any
	if err := json.Unmarshal(raw, &root); err != nil {
		return Verdict{false, fmt.Sprintf("settings.json 解析失败（转人工）: %v", err)}
	}
	env, _ := root["env"].(map[string]any)
	cur, _ := env["ANTHROPIC_BASE_URL"].(string)
	if cur == dockBaseURL {
		return Verdict{true, fmt.Sprintf("CC env.ANTHROPIC_BASE_URL → 渡口（%s）", dockBaseURL)}
	}
	if cur == "" {
		return Verdict{false, "settings.json env.ANTHROPIC_BASE_URL 缺失——CC 未接管" +
			"（跑 ferryman provider apply）"}
	}
	return Verdict{false, fmt.Sprintf("settings.json env.ANTHROPIC_BASE_URL=%s 未指向渡口"+
		"（%s）——疑似被外部工具改写（首周多视作 cc-switch 会话自动同步复开，F9）"+
		"——跑 ferryman provider apply 修复", cur, dockBaseURL)}
}

// ---- codex config 解析小件（与 writer 同一套） ----

// codexActiveProvider 解析 active provider 表（缺 model_provider/表 → nil）。
func codexActiveProvider(cfgPath string) (map[string]any, error) {
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		return nil, err
	}
	var doc map[string]any
	if err := toml.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	active, _ := doc["model_provider"].(string)
	mps, _ := doc["model_providers"].(map[string]any)
	tbl, _ := mps[active].(map[string]any)
	return tbl, nil
}

// codexBaseProblems 存在性 + 可解析 + active 表 base_url 指向（体检②③共用）。
func codexBaseProblems(cfgPath, label, dockCodexURL string) []string {
	tbl, err := codexActiveProvider(cfgPath)
	if err != nil {
		if os.IsNotExist(err) {
			return []string{fmt.Sprintf("%s config.toml 不存在（%s）", label, cfgPath)}
		}
		return []string{fmt.Sprintf("%s config.toml 解析失败（转人工）: %v", label, err)}
	}
	if tbl == nil {
		return []string{fmt.Sprintf("%s 缺 model_provider 或 [model_providers.*] 表（转人工）", label)}
	}
	if cur, _ := tbl["base_url"].(string); cur != dockCodexURL {
		return []string{fmt.Sprintf("%s base_url=%s 未指向渡口（%s）", label, cur, dockCodexURL)}
	}
	return nil
}

// codexProtoProblems 线协议与钩子旗标：wire_api=responses + [features] hooks
// = true（体检②专属；③只管健康面，不管协议面）。
func codexProtoProblems(cfgPath, label string) []string {
	var probs []string
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		return nil // 存在性问题已由 codexBaseProblems 报过，不重复
	}
	var doc map[string]any
	if toml.Unmarshal(raw, &doc) != nil {
		return nil // 同上：解析性问题已由 codexBaseProblems 报过
	}
	active, _ := doc["model_provider"].(string)
	mps, _ := doc["model_providers"].(map[string]any)
	tbl, _ := mps[active].(map[string]any)
	wire, _ := tbl["wire_api"].(string)
	if wire != "responses" {
		probs = append(probs, fmt.Sprintf("%s wire_api 缺失或非 responses（现值 %q）", label, wire))
	}
	if !hasHooksFlagText(string(raw)) {
		probs = append(probs, fmt.Sprintf("%s [features] hooks = true 未开（Codex 钩子默认关闭）", label))
	}
	return probs
}

// ---- ② codex 两份指向渡口 + wire_api + hooks 旗标 ----

// CheckCodexPointsDock codex 接管形态体检：普通份与 orca 镜像份都须指向渡口、
// wire_api=responses、hooks 旗标在位。逐份列问题（一次体检报全，不挤牙膏）。
func CheckCodexPointsDock(cfgPath, orcaCfgPath, dockCodexURL string) Verdict {
	probs := append([]string{},
		codexBaseProblems(cfgPath, "codex", dockCodexURL)...)
	probs = append(probs, codexProtoProblems(cfgPath, "codex")...)
	probs = append(probs, codexBaseProblems(orcaCfgPath, "orca-codex", dockCodexURL)...)
	probs = append(probs, codexProtoProblems(orcaCfgPath, "orca-codex")...)
	if len(probs) > 0 {
		return Verdict{false, "codex 接管形态有问题: " + strings.Join(probs, "；") +
			"——跑 ferryman provider apply 修复"}
	}
	return Verdict{true, fmt.Sprintf("codex 两份 config 指向渡口（%s）且 wire_api=responses、"+
		"hooks 旗标在位", dockCodexURL)}
}

// ---- ③ orca codex 健康 ----

// CheckOrcaCodexHealth orca 生态 codex 健康体检：配置存在、指向渡口、认证
// 形态合法（apikey/bearer；chatgpt-OAuth/不明 → 失败转人工，F7——体检只报
// 不改，硬改是写入器被 F7 拒绝的同一红线）。
func CheckOrcaCodexHealth(orcaCfgPath, dockCodexURL string) Verdict {
	if probs := codexBaseProblems(orcaCfgPath, "orca-codex", dockCodexURL); len(probs) > 0 {
		return Verdict{false, "orca codex 不健康: " + strings.Join(probs, "；")}
	}
	form := CodexAuthForm(orcaCfgPath)
	if form != AuthAPIKey && form != AuthBearer {
		return Verdict{false, fmt.Sprintf("orca codex 认证形态为 %q——chatgpt-OAuth/不明"+
			"形态不硬改，转人工（F7）", string(form))}
	}
	return Verdict{true, fmt.Sprintf("orca codex 健康（config 在位、指向渡口、认证形态 %s）",
		string(form))}
}
