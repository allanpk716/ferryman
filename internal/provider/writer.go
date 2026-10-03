// Package provider 服务商接管（2026-09-30 夜链，spec v0.4.0）的外科式配置
// 写入器与体检探针。
//
// 渡口成为 CC/codex（含 orca 生态）唯一门后，由本包把三份编辑器配置一次性
// 定向写到渡口：CC 的 env.ANTHROPIC_BASE_URL、codex 两份的 base_url/
// wire_api/hooks 旗标/占位令牌。票10 起第四目标 pi 加入同一家族：~/.pi/agent
// 的 models.json（渡口供应商条目）与 settings.json（defaultProvider/
// defaultModel）两文件成对落盘。反面教材是 cc-switch 的整文件重写（曾抹掉
// 别人的行、曾把 CC 流量偷旁路）——本包的铁律恰好相反：
//
//   - 只动目标行，他人键逐字节原样；
//   - 改写后做「拔掉可变键再比对解析树」的外科性自证，自证不过即放弃写入
//     报错转人工（绝不硬改）；
//   - 幂等：重跑=校验+补缺，已正确项零写入、零备份；
//   - F7 前置：codex 认证形态须为 apikey/bearer，chatgpt-OAuth 全案拒绝。
//
// 本包不读真实用户目录（全部路径经参数传入），不做任何网络与进程操作；
// installer 面（doctor 三项体检）与 CLI 面（ferryman provider apply）经此复用。
package provider

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"

	"github.com/BurntSushi/toml"

	"ferryman/internal/config"
)

// PlaceholderToken Ferryman 占位令牌字面量（票05）：接管后 codex 配置里的
// 认证占位。cc-switch 时代的占位是 PROXY_MANAGED（凭证由它换真钥）；接管后
// 真钥注入移交渡口（剥入站占位凭证、出站换条目真钥），配置里落本包占位以标
// 明归属——字面量对渡口而言无差别（入站不鉴权、出站必替换），仅为「这份配
// 置谁在管」的人读标记与 cc-switch 再接管判别留痕。
const PlaceholderToken = "FERRYMAN_MANAGED"

// proxyManagedToken cc-switch 时代的占位字面量（只识别替换，不外泄到新写入）。
const proxyManagedToken = "PROXY_MANAGED"

// Targets 接管写入的配置目标 + 渡口地址。全部路径经调用方传入（测试用
// 临时目录，真机路径只由 CLI 装配层解析）。
type Targets struct {
	// CCSettings ~/.claude/settings.json。
	CCSettings string
	// CodexConfig ~/.codex/config.toml。
	CodexConfig string
	// OrcaCodexConfig orca 生态 CODEX_HOME 下的 config.toml
	//（%APPDATA%/orca/codex-runtime-home/home/config.toml）。
	OrcaCodexConfig string
	// PiModels ~/.pi/agent/models.json（票10 第四目标之一；与 PiSettings 须
	// 成对派生——两路径皆空＝pi 目标不入案；单空＝该目标视为缺配置跳过
	// 并回显（providerTargetsFromHome 恒成对派生，单空实际不可达）。）。
	PiModels string
	// PiSettings ~/.pi/agent/settings.json（与 PiModels 成对）。
	PiSettings string
	// PiModel pi 主模型 id（自 active 上游条目 model_map 的 pi 键派生——票09
	// config.DockUpstream.PiModel；渡口 CC 车道改写时换成上游真名）。空＝
	// models 列表派生不出，pi 两文件在场时按异形拒绝转人工。
	PiModel string
	// PiAvailability pi 可用性裁决值（票12）：调用方自 active 上游条目的
	// PiAvailability()（票09 config 单源）透传；空＝可用（零值不改既有行为面
	// ——未透传的既有调用方零变化）。PiUnsupported/PiUnavailable＝active 上游
	// 对 pi 不可用 → pi 目标跳过并如实回显、其余目标照常（票12）。注意两态
	// 分开：这里只拦「上游不可用」；pi 两文件结构异形/缺 pi 主模型键仍走计算
	// 段的异形拒绝转人工（票10 语义不变——异形是全案拒绝，不是跳过）。
	PiAvailability string
	// DockBaseURL 渡口根地址（如 http://127.0.0.1:15722）；codex 目标按 /v1
	// 后缀惯例派生（与 wire_api="responses" 兼容为准），pi/CC 目标用根地址
	//（anthropic-messages 复用 CC 车道，客户端自行追加 /v1/messages）。
	DockBaseURL string
}

func (t Targets) codexBaseURL() string {
	return strings.TrimRight(t.DockBaseURL, "/") + "/v1"
}

// 接管目标名（ApplyReport 里稳定可读）。
const (
	targetCC         = "cc"
	targetCodex      = "codex"
	targetOrca       = "orca-codex"
	targetPiModels   = "pi-models"
	targetPi         = "pi" // ~/.pi 未装时的目标级跳过行（两文件合并一行回显）
	targetPiSettings = "pi-settings"
)

// Action 逐目标动作（结构化报告用）。
type Action string

const (
	ActionUnchanged Action = "unchanged" // 已是目标形态，零写入
	ActionWritten   Action = "written"   // 定向写入完成
	ActionSkipped   Action = "skipped"   // 配置不存在等——跳过不代建
	ActionRestored  Action = "restored"  // 已从备份还原（--restore）
)

// TargetReport 逐目标结论。
type TargetReport struct {
	Name   string `json:"name"`
	Path   string `json:"path"`
	Action Action `json:"action"`
	Backup string `json:"backup,omitempty"` // 本次落下的备份路径（有则带）
	Detail string `json:"detail"`
}

// ApplyReport 一次 Apply/Restore 的逐目标报告。
type ApplyReport struct {
	Targets []TargetReport `json:"targets"`
}

// Apply 外科式接管写入（票05，spec Implementation Decisions 6；票10 增 pi
// 第四目标）：
//
//  1. F7 前置：两份 codex 配置须为 apikey/bearer 认证形态，chatgpt-OAuth/
//     不明形态 → 全案拒绝（零备份零写盘），报错转人工；
//  2. 纯计算逐文件的定向改写结果（不落盘），全部已是目标形态 → 幂等短路
//     （零备份零写入）；
//  3. 有任一写入时，先对在位配置逐一落同戳备份（orca 份/pi 份缺失则该份无
//     备份，如实跳过），再写改动文件；pi 两文件成对落盘，任一写败按同戳
//     备份恢复两份（F10），绝不留半写形态。
func Apply(t Targets) (ApplyReport, error) {
	var rep ApplyReport
	if strings.TrimSpace(t.DockBaseURL) == "" {
		return rep, errors.New("provider: DockBaseURL 未设置——拒绝盲写")
	}
	type plan struct {
		name, path string
		required   bool // required=false 的目标（orca 份）缺失时跳过不代建
	}
	plans := []plan{
		{targetCC, t.CCSettings, true},
		{targetCodex, t.CodexConfig, true},
		{targetOrca, t.OrcaCodexConfig, false},
	}
	// ① F7 认证前置校验（在任何备份/写盘之前）。
	for _, p := range plans {
		if p.name == targetCC {
			continue // CC 侧无 codex 认证形态问题
		}
		if _, err := os.Stat(p.path); errors.Is(err, os.ErrNotExist) {
			continue // 缺失文件由步骤②按 required/skip 处理
		}
		if form := CodexAuthForm(p.path); form != AuthAPIKey && form != AuthBearer {
			return rep, fmt.Errorf("provider: %s 认证形态为 %q——chatgpt-OAuth/不明形态"+
				"不硬改，转人工（F7）；处置: codex 内改用 apikey 登录或手填 auth.json "+
				"OPENAI_API_KEY 后重跑 apply", p.path, string(form))
		}
	}
	// ② 纯计算改写结果（不落盘）。
	var newContent []string // 与 plans 对齐；未在位目标为 ""
	changed := make([]bool, len(plans))
	exists := make([]bool, len(plans))
	for i, p := range plans {
		raw, err := os.ReadFile(p.path)
		if errors.Is(err, os.ErrNotExist) {
			if p.required {
				return rep, fmt.Errorf("provider: %s 不存在——接管前置缺失，转人工", p.path)
			}
			newContent = append(newContent, "")
			rep.Targets = append(rep.Targets, TargetReport{Name: p.name, Path: p.path,
				Action: ActionSkipped, Detail: "配置不存在——跳过（不代建）"})
			continue
		}
		if err != nil {
			return rep, fmt.Errorf("provider: %s 读取失败: %w", p.path, err)
		}
		exists[i] = true
		var nxt string
		var ch bool
		if p.name == targetCC {
			nxt, ch, err = applyCCSettings(string(raw), t.DockBaseURL)
		} else {
			nxt, ch, err = applyCodexConfig(string(raw), t.codexBaseURL())
		}
		if err != nil {
			return rep, fmt.Errorf("provider: %s: %w", p.path, err)
		}
		newContent = append(newContent, nxt)
		changed[i] = ch
	}
	// ②-b pi 目标计算（票10，spec Implementation Decisions 3）：两文件成对——
	// 任一异形（结构不合/模型名无法对齐/pi 主模型位缺/单边缺失）→ 整体拒绝
	// 转人工（全案零备份零写盘，绝不半写）；两文件皆不在位（~/.pi 未装）→
	// 该目标跳过不代建、如实回显、不算失败。前置（票12）：active 上游对 pi
	// 不可用（显式否决/方言非 anthropic，票09 PiAvailability 单源）→ pi 目标
	// 跳过不写入、如实回显、不算失败，其余目标照常——同样发生在任何读取之前。
	piInPlay := t.PiModels != "" && t.PiSettings != ""
	piComputed := false
	var piModelsNew, piSettingsNew string
	var piModelsCh, piSettingsCh bool
	if piInPlay {
		if why := piSkipReason(t.PiAvailability); why != "" {
			rep.Targets = append(rep.Targets, TargetReport{Name: targetPi,
				Path: t.PiModels, Action: ActionSkipped, Detail: why})
		} else {
			mRaw, mErr := os.ReadFile(t.PiModels)
			sRaw, sErr := os.ReadFile(t.PiSettings)
			switch {
			case errors.Is(mErr, os.ErrNotExist) && errors.Is(sErr, os.ErrNotExist):
				rep.Targets = append(rep.Targets, TargetReport{Name: targetPi,
					Path: t.PiModels, Action: ActionSkipped,
					Detail: "~/.pi 未装（models.json 与 settings.json 皆不在位）——跳过（不代建、不算失败）"})
			case errors.Is(mErr, os.ErrNotExist) || errors.Is(sErr, os.ErrNotExist):
				return rep, errors.New("provider: pi 两文件须成对在位——单边缺失＝异形，" +
					"整体拒绝转人工（绝不半写；手工补齐或删除另一份后重跑）")
			case mErr != nil:
				return rep, fmt.Errorf("provider: %s 读取失败: %w", t.PiModels, mErr)
			case sErr != nil:
				return rep, fmt.Errorf("provider: %s 读取失败: %w", t.PiSettings, sErr)
			default:
				var err error
				piModelsNew, piModelsCh, err = applyPiModels(string(mRaw), t.DockBaseURL, t.PiModel)
				if err != nil {
					return rep, fmt.Errorf("provider: %s: %w", t.PiModels, err)
				}
				piSettingsNew, piSettingsCh, err = applyPiSettings(string(sRaw), piProviderName, t.PiModel)
				if err != nil {
					return rep, fmt.Errorf("provider: %s: %w", t.PiSettings, err)
				}
				// 成对自证（票10：「模型名无法对齐」异形）：settings 的 defaultModel
				// 必须落在 models 新形态 ferryman 条目的 models 列表内。两值同源于
				// PiModel，构造上恒真——此处是防回归硬闸（改派生链路先红在这里）。
				if err := piPairAligned(piModelsNew, piSettingsNew); err != nil {
					return rep, fmt.Errorf("provider: pi 两文件模型名无法对齐，转人工: %w", err)
				}
				piComputed = true
			}
		}
	}
	// ③ 幂等短路：零改动 → 零备份零写盘。
	any := false
	for _, c := range changed {
		if c {
			any = true
			break
		}
	}
	if !any && !piModelsCh && !piSettingsCh {
		for i, p := range plans {
			if !exists[i] {
				continue
			}
			rep.Targets = append(rep.Targets, TargetReport{Name: p.name, Path: p.path,
				Action: ActionUnchanged, Detail: "已是目标形态（零写入）"})
		}
		appendPiRows(&rep, t, piComputed, false, [2]string{}, piModelsCh, piSettingsCh)
		return rep, nil
	}
	// ④ 备份：在位配置逐一落同戳备份（成组，同一时间戳前缀；orca 份/pi 份缺
	// 失则该组缺成员，Restore 如实跳过）。
	stamp := stampNow()
	backupOf := make([]string, len(plans))
	for i, p := range plans {
		if !exists[i] {
			continue
		}
		b := filepath.Join(filepath.Dir(p.path), filepath.Base(p.path)+backupMarker+stamp)
		if err := copyFile(p.path, b); err != nil {
			return rep, fmt.Errorf("provider: 备份 %s 失败: %w", p.path, err)
		}
		backupOf[i] = b
	}
	var piBackup [2]string
	if piComputed {
		for i, p := range []string{t.PiModels, t.PiSettings} {
			b := filepath.Join(filepath.Dir(p), filepath.Base(p)+backupMarker+stamp)
			if err := copyFile(p, b); err != nil {
				return rep, fmt.Errorf("provider: 备份 %s 失败: %w", p, err)
			}
			piBackup[i] = b
		}
	}
	// ⑤ 写盘：只写改动文件（权限位沿用原文件）。
	for i, p := range plans {
		if !changed[i] {
			continue
		}
		perm := os.FileMode(0o644)
		if info, err := os.Stat(p.path); err == nil {
			perm = info.Mode().Perm()
		}
		if err := os.WriteFile(p.path, []byte(newContent[i]), perm); err != nil {
			return rep, fmt.Errorf("provider: 写入 %s 失败: %w", p.path, err)
		}
	}
	// ⑤-b pi 对成对落盘（票10 F10）：任一文件写败 → 按同戳备份恢复两份（回到
	// 接管前基线），绝不留半写形态；恢复亦败 → 如实报错并提示幂等重跑收敛。
	if piComputed && (piModelsCh || piSettingsCh) {
		permOf := func(p string) os.FileMode {
			perm := os.FileMode(0o644)
			if info, err := os.Stat(p); err == nil {
				perm = info.Mode().Perm()
			}
			return perm
		}
		piPairFail := func(failed string, werr error) error {
			for i, p := range []string{t.PiModels, t.PiSettings} {
				if err := copyFile(piBackup[i], p); err != nil {
					return fmt.Errorf("provider: pi 对写败(%s: %v)且按同戳备份恢复 %s 亦失败: %w"+
						"——手工按备份恢复或修复后幂等重跑收敛，转人工", failed, werr, p, err)
				}
			}
			appendPiRows(&rep, t, piComputed, true, piBackup, true, true)
			return fmt.Errorf("provider: pi 两文件成对落盘失败(%s): %w——已按同戳备份恢复两份"+
				"（与接管前基线一致；修复写盘障碍后幂等重跑收敛），转人工", failed, werr)
		}
		if piModelsCh {
			if err := osWriteFile(t.PiModels, []byte(piModelsNew), permOf(t.PiModels)); err != nil {
				return rep, piPairFail(t.PiModels, err)
			}
		}
		if piSettingsCh {
			if err := osWriteFile(t.PiSettings, []byte(piSettingsNew), permOf(t.PiSettings)); err != nil {
				return rep, piPairFail(t.PiSettings, err)
			}
		}
	}
	for i, p := range plans {
		if !exists[i] {
			continue // skipped 行已在步骤②追加
		}
		act := ActionUnchanged
		detail := "已是目标形态（零写入）"
		if changed[i] {
			act = ActionWritten
			detail = "已定向写入"
		}
		rep.Targets = append(rep.Targets, TargetReport{Name: p.name, Path: p.path,
			Action: act, Backup: backupOf[i], Detail: detail})
	}
	appendPiRows(&rep, t, piComputed, false, piBackup, piModelsCh, piSettingsCh)
	return rep, nil
}

// piSkipReason active 上游对 pi 的不可用跳过报因（票12）：不可用 → 回显行明细
//（「pi 目标跳过: <原因>」形态，不算失败）；可用或调用方未透传（空值）→ 空串
// ＝照常计算。判定值与 config.PiAvailability 单源同字面量。
func piSkipReason(avail string) string {
	switch avail {
	case config.PiUnsupported:
		return fmt.Sprintf("pi 目标跳过: active 上游对 pi 显式否决（pi = %q）——不写 pi 两文件，其余目标照常",
			config.PiUnsupported)
	case config.PiUnavailable:
		return "pi 目标跳过: active 上游为 openai_responses 方言，pi 无入站车道——不写 pi 两文件，其余目标照常"
	}
	return ""
}

// appendPiRows pi 两文件的报告行（成功路径 unchanged/written；restoredOnly 时
// 报 F10 恢复行——成对写败后已按同戳备份还原）。pi 目标不在案（piComputed
// false）时零行——~/.pi 未装的跳过行已在计算段追加。
func appendPiRows(rep *ApplyReport, t Targets, piComputed, restoredOnly bool,
	piBackup [2]string, piModelsCh, piSettingsCh bool) {
	if !piComputed {
		return
	}
	rows := [2]struct {
		name, path string
		ch         bool
	}{
		{targetPiModels, t.PiModels, piModelsCh},
		{targetPiSettings, t.PiSettings, piSettingsCh},
	}
	for i, r := range rows {
		if restoredOnly {
			rep.Targets = append(rep.Targets, TargetReport{Name: r.name, Path: r.path,
				Action: ActionRestored, Backup: piBackup[i],
				Detail: "成对落盘失败——已按同戳备份恢复（与接管前基线一致；幂等重跑可收敛）"})
			continue
		}
		act, detail := ActionUnchanged, "已是目标形态（零写入）"
		if r.ch {
			act, detail = ActionWritten, "已定向写入"
		}
		rep.Targets = append(rep.Targets, TargetReport{Name: r.name, Path: r.path,
			Action: act, Backup: piBackup[i], Detail: detail})
	}
}

// ---- CC：settings.json 外科改写 ----

// ccBaseURLValueRe env.ANTHROPIC_BASE_URL 的字符串值（首个字面量；替换后由
// 解析树比对自证外科性——命中错位会在验证步失败）。
var ccBaseURLValueRe = regexp.MustCompile(`("ANTHROPIC_BASE_URL"\s*:\s*")[^"]*(")`)

// applyCCSettings CC 外科写入：env.ANTHROPIC_BASE_URL → 渡口，其余一字不动。
// 只支持已有键的定向改值（键缺失/env 块缺失 = 异形，报错转人工——写入器不
// 代建 JSON 结构，代建正是整文件重写的滑坡起点）。
func applyCCSettings(raw, dockBaseURL string) (string, bool, error) {
	var root map[string]any
	if err := json.Unmarshal([]byte(raw), &root); err != nil {
		return "", false, fmt.Errorf("settings.json 解析失败（转人工）: %w", err)
	}
	env, _ := root["env"].(map[string]any)
	cur, has := "", false
	if env != nil {
		cur, has = env["ANTHROPIC_BASE_URL"].(string)
	}
	if has && cur == dockBaseURL {
		return raw, false, nil // 已指向渡口：零写入
	}
	if !has {
		return "", false, errors.New("settings.json env.ANTHROPIC_BASE_URL 键缺失或非字符串" +
			"——外科写入只做已有键的定向改值，转人工")
	}
	loc := ccBaseURLValueRe.FindStringSubmatchIndex(raw)
	if loc == nil {
		return "", false, errors.New("settings.json 未定位到 ANTHROPIC_BASE_URL 字面量" +
			"（转义异形）——转人工")
	}
	// 只替换该键的值段（组1收尾到组2开头），其余字节一律不动。
	out := raw[:loc[3]] + dockBaseURL + raw[loc[4]:]
	// 验证一：新值确实到位（env 下那把键）——键值对在文件里出现多次、首个
	// 字面量不是 env 下那个等异形，都会在此拦下。
	var oldDoc, newDoc map[string]any
	if err := json.Unmarshal([]byte(out), &newDoc); err != nil {
		return "", false, fmt.Errorf("改写后 settings.json 解析失败（放弃写入，转人工）: %w", err)
	}
	e2, ok := newDoc["env"].(map[string]any)
	if !ok || e2["ANTHROPIC_BASE_URL"] != dockBaseURL {
		return "", false, errors.New("改写后 env.ANTHROPIC_BASE_URL 未指向渡口" +
			"（放弃写入，转人工）")
	}
	// 验证二：外科性自证——拔掉目标键后两棵解析树必须逐值相同。
	if err := json.Unmarshal([]byte(raw), &oldDoc); err != nil {
		return "", false, err // 前面已解析过，防御分支
	}
	if e1, ok := oldDoc["env"].(map[string]any); ok {
		delete(e1, "ANTHROPIC_BASE_URL")
	}
	delete(e2, "ANTHROPIC_BASE_URL")
	if !reflect.DeepEqual(oldDoc, newDoc) {
		return "", false, errors.New("外科性校验失败（改写波及他人键；放弃写入，转人工）")
	}
	return out, true, nil
}

// ---- codex：config.toml 外科改写 ----

var (
	tomlBaseURLRe = regexp.MustCompile(`^(\s*base_url\s*=\s*")[^"]*(")`)
	tomlWireAPIRe = regexp.MustCompile(`^(\s*wire_api\s*=\s*")[^"]*(")`)
	tomlBearerRe  = regexp.MustCompile(`^(\s*experimental_bearer_token\s*=\s*")[^"]*(")`)
)

// tomlLineValue 取 `key = "value"` 行的值（无该键行 = 未命中）。
func tomlLineValue(line, key string) (string, bool) {
	m := regexp.MustCompile(`^\s*` + key + `\s*=\s*"([^"]*)"`).FindStringSubmatch(line)
	if m == nil {
		return "", false
	}
	return m[1], true
}

func leadingWS(line string) string {
	return line[:len(line)-len(strings.TrimLeft(line, " \t"))]
}

// applyCodexConfig codex 外科写入（对普通份与 orca 镜像份同一实现）：
// base_url → 渡口（保 /v1 惯例）、wire_api 保持 responses（异值改写、缺失在
// base_url 行后补插）、experimental_bearer_token 的 PROXY_MANAGED 占位换
// Ferryman 占位（他值不碰）、[features] hooks = true 保住（缺失补插/无节追
// 加）；其余节（mcp_servers/hooks.state 等）一字不动。改写后做解析树外科性
// 自证（拔掉四把可变键后 DeepEqual）。
func applyCodexConfig(raw, dockCodexURL string) (string, bool, error) {
	var doc map[string]any
	if err := toml.Unmarshal([]byte(raw), &doc); err != nil {
		return "", false, fmt.Errorf("config.toml 解析失败（转人工）: %w", err)
	}
	active, _ := doc["model_provider"].(string)
	if strings.TrimSpace(active) == "" {
		return "", false, errors.New("config.toml 缺 model_provider——接管目标不明，转人工")
	}
	mps, _ := doc["model_providers"].(map[string]any)
	tbl, _ := mps[active].(map[string]any)
	if tbl == nil {
		return "", false, fmt.Errorf("config.toml 缺 [model_providers.%s] 表，转人工", active)
	}
	lines := strings.Split(raw, "\n")
	header := "[model_providers." + active + "]"
	hi := -1
	for i, ln := range lines {
		if strings.TrimSpace(ln) == header {
			hi = i
			break
		}
	}
	if hi < 0 {
		// 引号键/内联表等异形不做文本手术——宁可拒绝不可改坏。
		return "", false, fmt.Errorf("config.toml 未定位到节头 %s（引号键/内联表等"+
			"异形不支持），转人工", header)
	}
	// 节界：下一个表头为止。多行数组续行形如 "[" 开头会提前截断节界——后果
	// 是目标行找不到而报错转人工，不会改错行（失败方向安全）。
	secEnd := func() int {
		for j := hi + 1; j < len(lines); j++ {
			t := strings.TrimSpace(lines[j])
			if strings.HasPrefix(t, "[") && strings.HasSuffix(t, "]") {
				return j
			}
		}
		return len(lines)
	}
	// 1) base_url：必在（缺行 = 转人工，写入器不代建表条目）。
	end := secEnd()
	baseIdx := -1
	for i := hi + 1; i < end; i++ {
		if _, ok := tomlLineValue(lines[i], "base_url"); ok {
			baseIdx = i
			break
		}
	}
	if baseIdx < 0 {
		return "", false, fmt.Errorf("[model_providers.%s] 缺 base_url 行，转人工", active)
	}
	if cur, _ := tomlLineValue(lines[baseIdx], "base_url"); cur != dockCodexURL {
		nl := tomlBaseURLRe.ReplaceAllString(lines[baseIdx], "${1}"+dockCodexURL+"${2}")
		if nl == lines[baseIdx] {
			return "", false, fmt.Errorf("base_url 行形态不支持（单引号串/异形），转人工: %q",
				lines[baseIdx])
		}
		lines[baseIdx] = nl
	}
	// 2) wire_api：在位保持 responses；异值定向改写；缺失补插在 base_url 行后。
	end = secEnd()
	wireIdx := -1
	for i := hi + 1; i < end; i++ {
		if v, ok := tomlLineValue(lines[i], "wire_api"); ok {
			if v != "responses" {
				nl := tomlWireAPIRe.ReplaceAllString(lines[i], "${1}responses${2}")
				if nl == lines[i] {
					return "", false, fmt.Errorf("wire_api 行形态不支持，转人工: %q", lines[i])
				}
				lines[i] = nl
			}
			wireIdx = i
			break
		}
	}
	if wireIdx < 0 {
		ins := leadingWS(lines[baseIdx]) + `wire_api = "responses"`
		lines = append(lines[:baseIdx+1], append([]string{ins}, lines[baseIdx+1:]...)...)
	}
	// 3) 占位令牌：PROXY_MANAGED → Ferryman 占位（他值——真钥/他占位——不碰；
	// 渡口入站不鉴权，真钥流到入站口也会被剥）。
	end = secEnd()
	for i := hi + 1; i < end; i++ {
		if v, ok := tomlLineValue(lines[i], "experimental_bearer_token"); ok {
			if v == proxyManagedToken {
				lines[i] = tomlBearerRe.ReplaceAllString(lines[i],
					"${1}"+PlaceholderToken+"${2}")
			}
			break
		}
	}
	out := strings.Join(lines, "\n")
	// 4) [features] hooks = true 保住（缺失补插；无节按既有 install 惯例末尾
	// 追加）。
	if !hasHooksFlagText(out) {
		fi := -1
		for i, ln := range lines {
			if strings.TrimSpace(ln) == "[features]" {
				fi = i
				break
			}
		}
		if fi >= 0 {
			ins := leadingWS(lines[fi]) + "hooks = true"
			lines = append(lines[:fi+1], append([]string{ins}, lines[fi+1:]...)...)
			out = strings.Join(lines, "\n")
		} else {
			out = strings.TrimRight(out, "\n") + "\n\n[features]\nhooks = true\n"
		}
	}
	if out == raw {
		return raw, false, nil
	}
	// 5) 验证：值到位 + 外科性自证（拔掉四把可变键后解析树 DeepEqual）。
	var doc2 map[string]any
	if err := toml.Unmarshal([]byte(out), &doc2); err != nil {
		return "", false, fmt.Errorf("改写后 config.toml 解析失败（放弃写入，转人工）: %w", err)
	}
	mps2, _ := doc2["model_providers"].(map[string]any)
	tbl2, _ := mps2[active].(map[string]any)
	if tbl2 == nil || tbl2["base_url"] != dockCodexURL {
		return "", false, errors.New("改写后 base_url 未指向渡口（放弃写入，转人工）")
	}
	if tbl2["wire_api"] != "responses" {
		return "", false, errors.New("改写后 wire_api ≠ responses（放弃写入，转人工）")
	}
	feat2, _ := doc2["features"].(map[string]any)
	if feat2 == nil || feat2["hooks"] != true {
		return "", false, errors.New("改写后 [features] hooks 旗标不在位（放弃写入，转人工）")
	}
	stripMutableKeys(doc, active)
	stripMutableKeys(doc2, active)
	if !reflect.DeepEqual(doc, doc2) {
		return "", false, errors.New("外科性校验失败（改写波及他人键；放弃写入，转人工）")
	}
	return out, true, nil
}

// stripMutableKeys 拔掉写入器有权改动的键（外科性比对的基准面）。旧文档没有
// 的键（补插场景）在新文档里删除后即对齐；[features] 整节是写入器可追加的
// （hooks 旗标补缺），拔空后连表一起拔——否则「原文档无此表 vs 新文档空表」
// 会误报外科性失败。
func stripMutableKeys(doc map[string]any, active string) {
	if mps, ok := doc["model_providers"].(map[string]any); ok {
		if tbl, ok := mps[active].(map[string]any); ok {
			delete(tbl, "base_url")
			delete(tbl, "wire_api")
			delete(tbl, "experimental_bearer_token")
		}
	}
	if feat, ok := doc["features"].(map[string]any); ok {
		delete(feat, "hooks")
		if len(feat) == 0 {
			delete(doc, "features")
		}
	}
}

// hasHooksFlagText 任一行 strip 后去空格以 "hooks=true" 开头（与 installer
// 包 InstallCodex 的 tomlHasHooksFlag 同语义同字面量——本包不 import installer
// （installer 依赖本包，反向会成环），两包测试各自钉住该行为，漂移双双报红）。
func hasHooksFlagText(tomlText string) bool {
	for _, line := range strings.Split(tomlText, "\n") {
		if strings.HasPrefix(strings.ReplaceAll(strings.TrimSpace(line), " ", ""), "hooks=true") {
			return true
		}
	}
	return false
}

// ---- pi：models.json / settings.json 成对外科改写（票10） ----

// piProviderName pi 侧渡口条目名（providers 键与条目 name 字段同用）。spec
// 决定 3：清掉 15721 死条目后落一条干净条目，条目名固定 "ferryman"——机器可
// 识别的归属标记（与 codex 侧占位令牌同一用意，cc-switch 再接管可判别）。
const piProviderName = "ferryman"

// piAPIAnthropicMessages pi 的 anthropic-messages 线协议标识（复用渡口 CC
// 车道，spec 决定 2/D4：不建 chat completions 入站）。
const piAPIAnthropicMessages = "anthropic-messages"

// piDeadRelayPort cc-switch 旧址端口（死条目清理靶）。判定故意收窄到 15721
// 单口、不借用 config.IsLocalRelayAddr——那会连带渡口自身 15722（本写入器的
// 目标地址，清了就自噬）与验证转发器 15723（不归本写入器管）；两包间也不引
// 入新的依赖方向。
const piDeadRelayPort = "15721"

// osWriteFile pi 对落盘缝（票10 F10 单测注桩：模拟第二文件写败以钉恢复路
// 径）。只用于 pi 两文件——其余目标写盘、备份与恢复仍走 os.WriteFile/copyFile
// 实路径，桩的影响面被钉死在 pi 对。
var osWriteFile = os.WriteFile

// applyPiModels pi models.json 外科改写：providers 表内 ① 清理 baseUrl 指向
// 15721（cc-switch 旧址）的死条目——其 models 里与 pi 主模型同 id 的条目先
// 收割留用（保留用户调过的 input/maxTokens 真值，剥 compat 兼容键）；② upsert
// 一条干净 ferryman 条目：api=anthropic-messages、baseUrl=渡口根地址、apiKey=
// 占位令牌、models 至少含 pi 主模型 id（主模型位空＝异形拒绝）。
//
// 实现取「有序解析树改写 + 按原缩进风格整档重排」——JSON 无行式结构可做
// 逐字节手术，且 cc-switch 参考实现对同一文件即整档 pretty 重写（serde_json
// preserve_order）；他人条目的键序与键值经有序树保真，外科性由「旧树拔掉
// ferryman 条目与死条目后与新树 DeepEqual」自证（同 codex 的拔可变键纪律）。
// 已是目标形态（树形同）→ 原字节一律不动（幂等零写入）。
func applyPiModels(raw, dockBaseURL, piModel string) (string, bool, error) {
	if strings.TrimSpace(piModel) == "" {
		return "", false, errors.New("active 上游 model_map 未派生出 pi 主模型（缺 pi 键）" +
			"——models 列表为空＝异形拒绝，转人工（补 model_map pi 键后重跑）")
	}
	root, err := parseJDoc(raw)
	if err != nil {
		return "", false, fmt.Errorf("models.json 解析失败（转人工）: %w", err)
	}
	if !root.isObj {
		return "", false, errors.New("models.json 根非对象——结构不合，转人工")
	}
	provs, ok := root.fields["providers"]
	if !ok || !provs.isObj {
		return "", false, errors.New("models.json 缺 providers 对象——结构不合，转人工")
	}
	layout := detectJLayout(raw)
	render := func() string {
		s := marshalJDoc(root, layout)
		if layout.trailNL {
			s += layout.eol
		}
		return s
	}
	before := render() // 改写前快照（幂等判定基准：树形已同＝零写入）
	// ① 死条目清理（先收割同 id 模型对象，再删条目——收割面从宽：非对象项/
	// 无字符串 id 的垃圾随死条目一起清走，不阻拦清理本身）。
	harvest := map[string]*jnode{}
	var dead []string
	for _, k := range provs.keys {
		e := provs.fields[k]
		if e == nil || !e.isObj {
			return "", false, fmt.Errorf("providers.%s 非对象——结构不合，转人工", k)
		}
		if u, _ := e.fields["baseUrl"].leafString(); isDeadCCSwitchBaseURL(u) {
			for _, m := range modelItems(e) {
				if id, ok := m.fields["id"].leafString(); ok && harvest[id] == nil {
					harvest[id] = stripJCompat(m)
				}
			}
			dead = append(dead, k)
		}
	}
	for _, k := range dead {
		provs.deleteKey(k)
	}
	// ② ferryman 条目 upsert：自有条目的 models 严校验（宁拒不改）；pi 主模型
	// 缺位时收割死条目真值，再无则保守合成。
	models := []*jnode{}
	if old := provs.fields[piProviderName]; old != nil {
		if !old.isObj {
			return "", false, errors.New("providers.ferryman 非对象——结构不合，转人工")
		}
		if ms, ok := old.fields["models"]; ok {
			if !ms.isArr {
				return "", false, errors.New("providers.ferryman.models 非数组——结构不合，转人工")
			}
			models = append(models, ms.items...)
		}
	}
	for _, m := range models {
		if !m.isObj {
			return "", false, errors.New("providers.ferryman.models 含非对象项——结构不合，转人工")
		}
		if _, ok := m.fields["id"].leafString(); !ok {
			return "", false, errors.New("providers.ferryman.models 项缺字符串 id——结构不合，转人工")
		}
	}
	if !hasModelID(models, piModel) {
		if h := harvest[piModel]; h != nil {
			models = append(models, h)
		} else {
			models = append(models, synthPiModel(piModel))
		}
	}
	entry := &jnode{isObj: true, fields: map[string]*jnode{}}
	for _, kv := range []struct {
		k string
		v *jnode
	}{
		{"name", jLeaf(piProviderName)},
		{"baseUrl", jLeaf(dockBaseURL)},
		{"api", jLeaf(piAPIAnthropicMessages)},
		{"apiKey", jLeaf(PlaceholderToken)},
		{"models", &jnode{isArr: true, items: models}},
	} {
		entry.set(kv.k, kv.v)
	}
	provs.set(piProviderName, entry)
	after := render()
	if after == before {
		return raw, false, nil // 已是目标形态：树形同＝原字节原样（幂等零写入）
	}
	// 验证一（值到位）：ferryman 条目四要素 + models 含 pi 主模型 id。
	var oldDoc, newDoc map[string]any
	if err := json.Unmarshal([]byte(raw), &oldDoc); err != nil {
		return "", false, err // 前面已解析过，防御分支
	}
	if err := json.Unmarshal([]byte(after), &newDoc); err != nil {
		return "", false, fmt.Errorf("改写后 models.json 解析失败（放弃写入，转人工）: %w", err)
	}
	newProvs, _ := newDoc["providers"].(map[string]any)
	fm, _ := newProvs[piProviderName].(map[string]any)
	if fm == nil || fm["baseUrl"] != dockBaseURL || fm["api"] != piAPIAnthropicMessages ||
		fm["apiKey"] != PlaceholderToken || fm["name"] != piProviderName {
		return "", false, errors.New("改写后 ferryman 条目未到位（放弃写入，转人工）")
	}
	found := false
	if ms, _ := fm["models"].([]any); ms != nil {
		for _, mi := range ms {
			if m, _ := mi.(map[string]any); m["id"] == piModel {
				found = true
			}
		}
	}
	if !found {
		return "", false, errors.New("改写后 models 列表缺 pi 主模型（放弃写入，转人工）")
	}
	// 验证二（外科性自证）：旧树拔 ferryman+死条目 vs 新树拔 ferryman，DeepEqual
	// ——改写只允许发生在主权域（ferryman 条目与死条目清理）内。
	stripPiMutable(oldDoc, dead)
	stripPiMutable(newDoc, nil)
	if !reflect.DeepEqual(oldDoc, newDoc) {
		return "", false, errors.New("外科性校验失败（改写波及他人键；放弃写入，转人工）")
	}
	return after, true, nil
}

// applyPiSettings pi settings.json 外科改写：defaultProvider → ferryman、
// defaultModel → pi 主模型，其余键（hooks/lastChangelogVersion 等）一字不动。
// 与 applyCCSettings 同款纪律：正则只换值段、只做已有键的定向改值（键缺失/
// 非字符串＝异形转人工，不代建），改后拔两键 DeepEqual 自证。
func applyPiSettings(raw, providerName, piModel string) (string, bool, error) {
	var root map[string]any
	if err := json.Unmarshal([]byte(raw), &root); err != nil {
		return "", false, fmt.Errorf("settings.json 解析失败（转人工）: %w", err)
	}
	curP, okP := root["defaultProvider"].(string)
	curM, okM := root["defaultModel"].(string)
	if !okP || !okM {
		return "", false, errors.New("settings.json 缺 defaultProvider/defaultModel 键或非字符串" +
			"——外科写入只做已有键的定向改值，转人工")
	}
	if curP == providerName && curM == piModel {
		return raw, false, nil // 两键已就位：零写入
	}
	out := raw
	if curP != providerName {
		loc := piDefaultProviderRe.FindStringSubmatchIndex(out)
		if loc == nil {
			return "", false, errors.New("settings.json 未定位到 defaultProvider 字面量" +
				"（转义异形）——转人工")
		}
		out = out[:loc[3]] + providerName + out[loc[4]:]
	}
	if curM != piModel {
		loc := piDefaultModelRe.FindStringSubmatchIndex(out)
		if loc == nil {
			return "", false, errors.New("settings.json 未定位到 defaultModel 字面量" +
				"（转义异形）——转人工")
		}
		out = out[:loc[3]] + piModel + out[loc[4]:]
	}
	// 验证一：两值到位（键值对在文件里出现多次、首个字面量不是目标键等异形
	// 在此拦下）。
	var newDoc map[string]any
	if err := json.Unmarshal([]byte(out), &newDoc); err != nil {
		return "", false, fmt.Errorf("改写后 settings.json 解析失败（放弃写入，转人工）: %w", err)
	}
	if newDoc["defaultProvider"] != providerName || newDoc["defaultModel"] != piModel {
		return "", false, errors.New("改写后 defaultProvider/defaultModel 未到位（放弃写入，转人工）")
	}
	// 验证二：外科性自证——拔掉两把可变键后两棵解析树逐值相同。
	delete(root, "defaultProvider")
	delete(root, "defaultModel")
	delete(newDoc, "defaultProvider")
	delete(newDoc, "defaultModel")
	if !reflect.DeepEqual(root, newDoc) {
		return "", false, errors.New("外科性校验失败（改写波及他人键；放弃写入，转人工）")
	}
	return out, true, nil
}

var (
	piDefaultProviderRe = regexp.MustCompile(`("defaultProvider"\s*:\s*")[^"]*(")`)
	piDefaultModelRe    = regexp.MustCompile(`("defaultModel"\s*:\s*")[^"]*(")`)
)

// piPairAligned 成对自证：settings 的 defaultModel 必须能在 models 新形态
// ferryman 条目的 models 列表里找到同 id 项（票10「模型名无法对齐」异形）。
func piPairAligned(modelsNew, settingsNew string) error {
	var s map[string]any
	if err := json.Unmarshal([]byte(settingsNew), &s); err != nil {
		return err
	}
	dm, _ := s["defaultModel"].(string)
	var m struct {
		Providers map[string]struct {
			Models []struct {
				ID string `json:"id"`
			} `json:"models"`
		} `json:"providers"`
	}
	if err := json.Unmarshal([]byte(modelsNew), &m); err != nil {
		return err
	}
	for _, mo := range m.Providers[piProviderName].Models {
		if mo.ID == dm {
			return nil
		}
	}
	return fmt.Errorf("defaultModel %q 不在 ferryman 条目 models 内", dm)
}

// isDeadCCSwitchBaseURL 条目 baseUrl 是否指向 cc-switch 旧址（回环主机 +
// 15721 端口；尾斜杠/带路径均认——host:port 判定不受 path 影响）。
func isDeadCCSwitchBaseURL(u string) bool {
	p, err := url.Parse(u)
	if err != nil || p.Host == "" {
		return false
	}
	h := strings.ToLower(p.Hostname())
	loop := h == "localhost" || h == "::1" ||
		strings.HasPrefix(h, "127.") || strings.HasPrefix(h, "::ffff:127.")
	return loop && p.Port() == piDeadRelayPort
}

// stripPiMutable 拔掉 pi models.json 写入器主权域的键（外科性比对基准面）：
// ferryman 条目整条（upsert 目标）+ 死条目（清理目标）。拔掉后两树 DeepEqual
// ＝ 改动只发生在主权域内、他人条目与顶层他键原样。
func stripPiMutable(doc map[string]any, dead []string) {
	pr, ok := doc["providers"].(map[string]any)
	if !ok {
		return
	}
	delete(pr, piProviderName)
	for _, k := range dead {
		delete(pr, k)
	}
}

// modelItems 条目 models 数组里的对象项（非对象项跳过——只用于死条目收割面）。
func modelItems(e *jnode) []*jnode {
	ms, ok := e.fields["models"]
	if !ok || !ms.isArr {
		return nil
	}
	var out []*jnode
	for _, it := range ms.items {
		if it != nil && it.isObj {
			out = append(out, it)
		}
	}
	return out
}

// hasModelID 列表内是否有 id 等值的模型项（调用方已严校验形态）。
func hasModelID(items []*jnode, id string) bool {
	for _, m := range items {
		if s, _ := m.fields["id"].leafString(); s == id {
			return true
		}
	}
	return false
}

// stripJCompat 模型对象剥除 compat 键的拷贝（渡口条目不带 compat——兼容改写
// 是渡口车道自己的职责，残留旧 compat 反而会双重整形）。
func stripJCompat(m *jnode) *jnode {
	out := &jnode{isObj: true, fields: map[string]*jnode{}}
	for _, k := range m.keys {
		if k == "compat" {
			continue
		}
		out.set(k, m.fields[k])
	}
	return out
}

// synthPiModel 无处收割时的保守合成项（票面形态：id/name/reasoning/input/
// maxTokens，不带 compat）。reasoning=false 对齐渡口 CC 车道对 GLM 关 thinking
// 的改写惯例；input 只标 text（图片能力按上游实情手工放开更稳，误开反而坏
// 请求）；maxTokens 取保守缺省——doctor（票11）只验 id 在列表内，用户可调。
func synthPiModel(id string) *jnode {
	m := &jnode{isObj: true, fields: map[string]*jnode{}}
	for _, kv := range []struct {
		k string
		v *jnode
	}{
		{"id", jLeaf(id)},
		{"name", jLeaf(id)},
		{"reasoning", jLeaf(false)},
		{"input", &jnode{isArr: true, items: []*jnode{jLeaf("text")}}},
		{"maxTokens", jLeaf(json.Number("32000"))},
	} {
		m.set(kv.k, kv.v)
	}
	return m
}

// ---- 有序 JSON 树（只服务 pi models.json；CC settings 走正则、codex 走行式） ----

// jnode 有序 JSON 节点：object 键序保真（Go map 无序——整档重排时他人条目的
// 键序不得被字典序重排）、数组保序、叶子保 json.Number 字面（数字形态不被
// 浮点归一，重排不产生语义噪声）。
type jnode struct {
	isObj  bool
	isArr  bool
	keys   []string
	fields map[string]*jnode
	items  []*jnode
	leaf   any // string / json.Number / bool / nil
}

func jLeaf(v any) *jnode { return &jnode{leaf: v} }

func (n *jnode) set(k string, v *jnode) {
	if _, ok := n.fields[k]; !ok {
		n.keys = append(n.keys, k)
	}
	n.fields[k] = v // 重复键后者胜（与 json.Unmarshal 同语义），键序取首现
}

func (n *jnode) deleteKey(k string) {
	if _, ok := n.fields[k]; !ok {
		return
	}
	delete(n.fields, k)
	for i, kk := range n.keys {
		if kk == k {
			n.keys = append(n.keys[:i], n.keys[i+1:]...)
			return
		}
	}
}

func (n *jnode) leafString() (string, bool) {
	if n == nil || n.isObj || n.isArr {
		return "", false
	}
	s, ok := n.leaf.(string)
	return s, ok
}

// parseJDoc 解析整档为 jnode 树（键序保真）。尾随多余内容（两个 JSON 文档等
// 异形）按 json.Unmarshal 同口径拒绝。
func parseJDoc(raw string) (*jnode, error) {
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.UseNumber()
	n, err := readJNode(dec)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("文档尾随多余内容——结构不合")
	}
	return n, nil
}

func readJNode(dec *json.Decoder) (*jnode, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if d, ok := tok.(json.Delim); ok {
		switch d {
		case '{':
			n := &jnode{isObj: true, fields: map[string]*jnode{}}
			for dec.More() {
				kt, err := dec.Token()
				if err != nil {
					return nil, err
				}
				key, ok := kt.(string)
				if !ok {
					return nil, errors.New("对象键非字符串") // 不达：More() 后必为键 Token
				}
				v, err := readJNode(dec)
				if err != nil {
					return nil, err
				}
				n.set(key, v)
			}
			if _, err := dec.Token(); err != nil { // 消费 '}'
				return nil, err
			}
			return n, nil
		case '[':
			n := &jnode{isArr: true}
			for dec.More() {
				v, err := readJNode(dec)
				if err != nil {
					return nil, err
				}
				n.items = append(n.items, v)
			}
			if _, err := dec.Token(); err != nil { // 消费 ']'
				return nil, err
			}
			return n, nil
		default:
			return nil, fmt.Errorf("异常分隔符 %v", d) // ']' '}' 不会出现在值位
		}
	}
	return &jnode{leaf: tok}, nil
}

// jLayout 整档重排的排版参数（自原文检测——「JSON 写入保持原文件缩进风格」：
// 缩进单位、换行风格、尾换行；原文无缩进（单行档）按两空格缺省）。
type jLayout struct {
	indent  string
	eol     string
	trailNL bool
}

func detectJLayout(raw string) jLayout {
	lay := jLayout{indent: "  ", eol: "\n"}
	if strings.Contains(raw, "\r\n") {
		lay.eol = "\r\n"
	}
	lay.trailNL = strings.HasSuffix(raw, "\n")
	best := ""
	for _, ln := range strings.Split(raw, "\n") {
		t := strings.TrimRight(ln, "\r")
		if strings.TrimSpace(t) == "" {
			continue
		}
		ws := leadingWS(t)
		if ws == "" {
			continue
		}
		if best == "" || len(ws) < len(best) {
			best = ws // 最小非零缩进＝一层单位（2/4 空格或 tab）
		}
	}
	if best != "" {
		lay.indent = best
	}
	return lay
}

// marshalJDoc 树 → 文本（缩进风格照 layout；空对象/数组收成 {}/[]）。叶子经
// Encoder 关 HTML 转义编码——URL 里的 & < > 不被 & 化成噪声。
func marshalJDoc(n *jnode, lay jLayout) string {
	var sb strings.Builder
	writeJNode(&sb, n, lay, "")
	return sb.String()
}

func writeJNode(sb *strings.Builder, n *jnode, lay jLayout, pad string) {
	switch {
	case n == nil:
		sb.WriteString("null")
	case n.isObj:
		if len(n.keys) == 0 {
			sb.WriteString("{}")
			return
		}
		sb.WriteByte('{')
		for i, k := range n.keys {
			if i > 0 {
				sb.WriteByte(',')
			}
			sb.WriteString(lay.eol + pad + lay.indent + marshalJLeaf(k) + ": ")
			writeJNode(sb, n.fields[k], lay, pad+lay.indent)
		}
		sb.WriteString(lay.eol + pad + "}")
	case n.isArr:
		if len(n.items) == 0 {
			sb.WriteString("[]")
			return
		}
		sb.WriteByte('[')
		for i, it := range n.items {
			if i > 0 {
				sb.WriteByte(',')
			}
			sb.WriteString(lay.eol + pad + lay.indent)
			writeJNode(sb, it, lay, pad+lay.indent)
		}
		sb.WriteString(lay.eol + pad + "]")
	default:
		sb.WriteString(marshalJLeaf(n.leaf))
	}
}

// marshalJLeaf 叶子字面（Encoder 追加的尾换行剥掉）。
func marshalJLeaf(v any) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return "null" // 不达：叶子皆 Decoder Token 可编码值
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// ---- F7：codex 认证形态判定 ----

// AuthForm codex 认证形态（F7 前置校验的判定对象）。
type AuthForm string

const (
	// AuthAPIKey auth.json 的 OPENAI_API_KEY 形态。
	AuthAPIKey AuthForm = "apikey"
	// AuthBearer config.toml 的 experimental_bearer_token 形态（含占位令牌——
	// 占位也是 bearer 配置，合法）。
	AuthBearer AuthForm = "bearer"
	// AuthOAuth auth.json 的 tokens 形态（chatgpt 登录态）——拒绝硬改。
	AuthOAuth AuthForm = "chatgpt-oauth"
	// AuthUnknown 两处皆无/读不了——形态不明，同照拒绝。
	AuthUnknown AuthForm = "unknown"
)

// CodexAuthForm 判一份 codex 配置（同目录 auth.json）的认证形态。判定序：
// config.toml 任一 provider 表带非空 experimental_bearer_token → bearer
// （config 层令牌是 codex 实际生效凭证）；否则 auth.json OPENAI_API_KEY 非空
// → apikey；否则 auth.json 带 tokens → chatgpt-OAuth；再否则 unknown。
func CodexAuthForm(cfgPath string) AuthForm {
	if raw, err := os.ReadFile(cfgPath); err == nil {
		var doc map[string]any
		if toml.Unmarshal(raw, &doc) == nil {
			if mps, ok := doc["model_providers"].(map[string]any); ok {
				for _, v := range mps {
					if tbl, ok := v.(map[string]any); ok {
						if s, _ := tbl["experimental_bearer_token"].(string); strings.TrimSpace(s) != "" {
							return AuthBearer
						}
					}
				}
			}
		}
	}
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(cfgPath), "auth.json"))
	if err != nil {
		return AuthUnknown
	}
	var auth map[string]any
	if json.Unmarshal(raw, &auth) != nil {
		return AuthUnknown
	}
	if k, _ := auth["OPENAI_API_KEY"].(string); strings.TrimSpace(k) != "" {
		return AuthAPIKey
	}
	if _, ok := auth["tokens"]; ok {
		return AuthOAuth
	}
	return AuthUnknown
}
