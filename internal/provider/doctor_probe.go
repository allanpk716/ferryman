// doctor_probe.go — 票05 doctor 三项体检（只读探针；票11 增 pi 生效链第四项）：
//
//	① CheckCCPointsDock      CC 指向渡口；
//	② CheckCodexPointsDock   codex 两份指向渡口且 wire_api=responses 且
//	                         [features] hooks 旗标在位；
//	③ CheckOrcaCodexHealth   orca codex 健康（配置存在、指向渡口、认证形态
//	                         合法）；
//	④ CheckPiPointsDock      pi 生效链（票11）：defaultProvider 解析到渡口条
//	                         目 ∧ defaultModel ∈ 该条目 models ∧ api=
//	                         anthropic-messages ∧ baseUrl=渡口根地址。
//
// 判定与写入器（writer.go）同一套解析函数与目标地址派生（单源）——doctor 与
// apply 绝不出现两套判据（CONTEXT.md 同一检查只许一份实现）。installer 面
// （doctor.go）只做 Verdict → CheckResult 换装，不重复判定。
package provider

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/BurntSushi/toml"
)

// Verdict 体检结论（installer 面换装成 CheckResult：NotChecked→not_checked、
// OK→pass、!OK→fail，Detail 原样透传）。
type Verdict struct {
	OK bool
	// NotChecked 检查目标不在（票11：~/.pi 未装）——如实标注不伪造，不产红。
	NotChecked bool
	Detail     string
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
			return Verdict{Detail: fmt.Sprintf("%s 不存在——CC 未接管（跑 ferryman provider apply）", settingsPath)}
		}
		return Verdict{Detail: fmt.Sprintf("%s 读取失败: %v", settingsPath, err)}
	}
	var root map[string]any
	if err := json.Unmarshal(raw, &root); err != nil {
		return Verdict{Detail: fmt.Sprintf("settings.json 解析失败（转人工）: %v", err)}
	}
	env, _ := root["env"].(map[string]any)
	cur, _ := env["ANTHROPIC_BASE_URL"].(string)
	if cur == dockBaseURL {
		return Verdict{OK: true, Detail: fmt.Sprintf("CC env.ANTHROPIC_BASE_URL → 渡口（%s）", dockBaseURL)}
	}
	if cur == "" {
		return Verdict{Detail: "settings.json env.ANTHROPIC_BASE_URL 缺失——CC 未接管" +
			"（跑 ferryman provider apply）"}
	}
	return Verdict{Detail: fmt.Sprintf("settings.json env.ANTHROPIC_BASE_URL=%s 未指向渡口"+
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
		return Verdict{Detail: "codex 接管形态有问题: " + strings.Join(probs, "；") +
			"——跑 ferryman provider apply 修复"}
	}
	return Verdict{OK: true, Detail: fmt.Sprintf("codex 两份 config 指向渡口（%s）且 wire_api=responses、"+
		"hooks 旗标在位", dockCodexURL)}
}

// ---- ③ orca codex 健康 ----

// CheckOrcaCodexHealth orca 生态 codex 健康体检：配置存在、指向渡口、认证
// 形态合法（apikey/bearer；chatgpt-OAuth/不明 → 失败转人工，F7——体检只报
// 不改，硬改是写入器被 F7 拒绝的同一红线）。
func CheckOrcaCodexHealth(orcaCfgPath, dockCodexURL string) Verdict {
	if probs := codexBaseProblems(orcaCfgPath, "orca-codex", dockCodexURL); len(probs) > 0 {
		return Verdict{Detail: "orca codex 不健康: " + strings.Join(probs, "；")}
	}
	form := CodexAuthForm(orcaCfgPath)
	if form != AuthAPIKey && form != AuthBearer {
		return Verdict{Detail: fmt.Sprintf("orca codex 认证形态为 %q——chatgpt-OAuth/不明"+
			"形态不硬改，转人工（F7）", string(form))}
	}
	return Verdict{OK: true, Detail: fmt.Sprintf("orca codex 健康（config 在位、指向渡口、认证形态 %s）",
		string(form))}
}

// ---- ④ pi 生效链（票11） ----

// CheckPiPointsDock pi 生效链体检（票11，spec Implementation Decisions 4）。
// 绿＝生效链全中：settings.json 的 defaultProvider 解析到 providers 条目 ∧
// defaultModel ∈ 该条目 models ∧ 该条目 api=anthropic-messages ∧ 该条目
// baseUrl=渡口根地址。判定与写入器（writer.go）同一套（单源，不造第二套判
// 据）：models.json 走 applyPiModels 同款 parseJDoc 树与结构门，条目判定素材
// 直用 piAPIAnthropicMessages 常量与 hasModelID/isDeadCCSwitchBaseURL；
// settings.json 键读取与 applyPiSettings 同口径（defaultProvider/defaultModel
// 字符串键，缺失＝异形）。
//
// ~/.pi 未装（两文件皆不在位，与写入器 piInPlay 同口径）→ NotChecked 不产
// 红；单边缺失＝异形（写入器成对纪律同款）→ fail 转人工。残留旧 15721 条目
// 但生效链正确 → 绿，Detail 附一行警告（非生效残留不阻断，F9——清理归
// apply，体检只提示；与写入器清理测试分开钉）。
func CheckPiPointsDock(piModelsPath, piSettingsPath, dockBaseURL string) Verdict {
	mRaw, mErr := os.ReadFile(piModelsPath)
	sRaw, sErr := os.ReadFile(piSettingsPath)
	switch {
	case errors.Is(mErr, os.ErrNotExist) && errors.Is(sErr, os.ErrNotExist):
		return Verdict{NotChecked: true, Detail: "~/.pi 未装（models.json 与 settings.json " +
			"皆不在位）——pi 未接入，不算失败"}
	case errors.Is(mErr, os.ErrNotExist) || errors.Is(sErr, os.ErrNotExist):
		return Verdict{Detail: "pi 两文件须成对在位——单边缺失＝异形（与写入器成对纪律" +
			"同源），转人工（手工补齐或删除另一份后跑 ferryman provider apply）"}
	case mErr != nil:
		return Verdict{Detail: fmt.Sprintf("%s 读取失败: %v", piModelsPath, mErr)}
	case sErr != nil:
		return Verdict{Detail: fmt.Sprintf("%s 读取失败: %v", piSettingsPath, sErr)}
	}
	// settings 侧：与 applyPiSettings 同一套键读取。
	var sroot map[string]any
	if err := json.Unmarshal(sRaw, &sroot); err != nil {
		return Verdict{Detail: fmt.Sprintf("pi settings.json 解析失败（转人工）: %v", err)}
	}
	dp, okP := sroot["defaultProvider"].(string)
	dm, okM := sroot["defaultModel"].(string)
	if !okP || !okM {
		return Verdict{Detail: "pi settings.json 缺 defaultProvider/defaultModel 键或非字符串" +
			"——生效链断（转人工）"}
	}
	// models 侧：与 applyPiModels 同一套 parseJDoc 树与结构门。
	mdoc, err := parseJDoc(string(mRaw))
	if err != nil {
		return Verdict{Detail: fmt.Sprintf("pi models.json 解析失败（转人工）: %v", err)}
	}
	if !mdoc.isObj {
		return Verdict{Detail: "pi models.json 根非对象——结构不合，转人工"}
	}
	provs, ok := mdoc.fields["providers"]
	if !ok || !provs.isObj {
		return Verdict{Detail: "pi models.json 缺 providers 对象——结构不合，转人工"}
	}
	var probs []string
	entry := provs.fields[dp]
	if entry == nil || !entry.isObj {
		probs = append(probs, fmt.Sprintf("defaultProvider=%q 解析不到 providers 条目", dp))
	} else {
		if u, _ := entry.fields["baseUrl"].leafString(); u != dockBaseURL {
			probs = append(probs, fmt.Sprintf("defaultProvider=%s 的条目 baseUrl=%s 未指向渡口（%s）",
				dp, u, dockBaseURL))
		}
		if a, _ := entry.fields["api"].leafString(); a != piAPIAnthropicMessages {
			probs = append(probs, fmt.Sprintf("defaultProvider=%s 的条目 api=%q 非 anthropic-messages",
				dp, a))
		}
		if ms := entry.fields["models"]; ms == nil || !ms.isArr || !hasModelID(ms.items, dm) {
			probs = append(probs, fmt.Sprintf("defaultModel=%q 不在条目 %s models 内", dm, dp))
		}
	}
	if len(probs) > 0 {
		return Verdict{Detail: "pi 生效链有问题: " + strings.Join(probs, "；") +
			"——跑 ferryman provider apply 修复"}
	}
	// 绿。残留旧 15721 条目（cc-switch 旧址死条目，isDeadCCSwitchBaseURL 同一
	// 判据）→ 附一行警告：非生效残留不阻断（F9），清理归 apply。
	var dead []string
	for _, k := range provs.keys {
		e := provs.fields[k]
		if e == nil || !e.isObj {
			continue
		}
		if u, _ := e.fields["baseUrl"].leafString(); isDeadCCSwitchBaseURL(u) {
			dead = append(dead, k)
		}
	}
	detail := fmt.Sprintf("pi 生效链走渡口（%s）：defaultProvider=%s、defaultModel=%s、api=%s",
		dockBaseURL, dp, dm, piAPIAnthropicMessages)
	if len(dead) > 0 {
		detail += fmt.Sprintf("；警告: 残留旧 15721 条目 %s（非生效残留不阻断，F9——跑 "+
			"ferryman provider apply 可清理）", strings.Join(dead, "/"))
	}
	return Verdict{OK: true, Detail: detail}
}
