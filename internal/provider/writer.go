// Package provider 服务商接管（2026-09-30 夜链，spec v0.4.0）的外科式配置
// 写入器与体检探针。
//
// 渡口成为 CC/codex（含 orca 生态）唯一门后，由本包把三份编辑器配置一次性
// 定向写到渡口：CC 的 env.ANTHROPIC_BASE_URL、codex 两份的 base_url/
// wire_api/hooks 旗标/占位令牌。反面教材是 cc-switch 的整文件重写（曾抹掉
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
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"

	"github.com/BurntSushi/toml"
)

// PlaceholderToken Ferryman 占位令牌字面量（票05）：接管后 codex 配置里的
// 认证占位。cc-switch 时代的占位是 PROXY_MANAGED（凭证由它换真钥）；接管后
// 真钥注入移交渡口（剥入站占位凭证、出站换条目真钥），配置里落本包占位以标
// 明归属——字面量对渡口而言无差别（入站不鉴权、出站必替换），仅为「这份配
// 置谁在管」的人读标记与 cc-switch 再接管判别留痕。
const PlaceholderToken = "FERRYMAN_MANAGED"

// proxyManagedToken cc-switch 时代的占位字面量（只识别替换，不外泄到新写入）。
const proxyManagedToken = "PROXY_MANAGED"

// Targets 接管写入的三份配置目标 + 渡口地址。全部路径经调用方传入（测试用
// 临时目录，真机路径只由 CLI 装配层解析）。
type Targets struct {
	// CCSettings ~/.claude/settings.json。
	CCSettings string
	// CodexConfig ~/.codex/config.toml。
	CodexConfig string
	// OrcaCodexConfig orca 生态 CODEX_HOME 下的 config.toml
	//（%APPDATA%/orca/codex-runtime-home/home/config.toml）。
	OrcaCodexConfig string
	// DSHHome dsh 家目录（~/.dsh）；空＝dsh 分支整体不参与（旧调用零变化）。
	// 非空但目录不在位＝两行 skip（不代建）；home patch 在位且无接管标记＝
	// 他人文件，全案拒绝转人工（见 planDSH）。
	DSHHome string
	// DockBaseURL 渡口根地址（如 http://127.0.0.1:15722）；codex 目标按 /v1
	// 后缀惯例派生（与 wire_api="responses" 兼容为准），dsh 路由原样用作
	// baseURL（pi-ai 自行追加 /v1/messages）。
	DockBaseURL string
}

func (t Targets) codexBaseURL() string {
	return strings.TrimRight(t.DockBaseURL, "/") + "/v1"
}

// 接管目标名（ApplyReport 里稳定可读）。
const (
	targetCC    = "cc"
	targetCodex = "codex"
	targetOrca  = "orca-codex"
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

// Apply 外科式接管写入（票05，spec Implementation Decisions 6）：
//
//  1. F7 前置：两份 codex 配置须为 apikey/bearer 认证形态，chatgpt-OAuth/
//     不明形态 → 全案拒绝（零备份零写盘），报错转人工；
//  2. 纯计算逐文件的定向改写结果（不落盘），全部已是目标形态 → 幂等短路
//     （零备份零写入）；
//  3. 有任一写入时，先对三份在位配置逐一落同戳备份（orca 份缺失则该份无备
//     份，如实跳过），再写改动文件。
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
	// ② 纯计算改写结果（不落盘）。dsh 分支同段规划（读文件+出计划，不写）：
	// 他人文件（无标记）在此全案拒绝——任何备份/写盘都还没发生。
	var dshPlans []dshFilePlan
	if t.DSHHome != "" {
		var dshErr error
		dshPlans, dshErr = planDSH(t.DSHHome, t.DockBaseURL)
		if dshErr != nil {
			return rep, dshErr
		}
	}
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
	// ③ 幂等短路：零改动 → 零备份零写盘。
	any := false
	for _, c := range changed {
		if c {
			any = true
			break
		}
	}
	if !any {
		for _, p := range dshPlans {
			if p.skipped {
				rep.Targets = append(rep.Targets, TargetReport{Name: p.target, Path: p.path,
					Action: ActionSkipped, Detail: p.detail})
				continue
			}
			act := ActionUnchanged
			if p.changed {
				act = ActionWritten // 理论不达（any=false），防御保真
			}
			rep.Targets = append(rep.Targets, TargetReport{Name: p.target, Path: p.path,
				Action: act, Detail: p.detail})
		}
		for i, p := range plans {
			if !exists[i] {
				continue
			}
			rep.Targets = append(rep.Targets, TargetReport{Name: p.name, Path: p.path,
				Action: ActionUnchanged, Detail: "已是目标形态（零写入）"})
		}
		return rep, nil
	}
	// ④ 备份：在位配置逐一落同戳备份（成组，同时间戳前缀；缺失份如实跳过）。
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
	dshBackupOf := make([]string, len(dshPlans))
	for i, p := range dshPlans {
		if !p.exists {
			continue // 新建文件无"接管前"可备（Restore 走删除还原）
		}
		b := filepath.Join(filepath.Dir(p.path), filepath.Base(p.path)+backupMarker+stamp)
		if err := copyFile(p.path, b); err != nil {
			return rep, fmt.Errorf("provider: 备份 %s 失败: %w", p.path, err)
		}
		dshBackupOf[i] = b
	}
	// ⑤ 写盘：只写改动文件（权限位沿用原文件；新建文件 0644）。
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
	for _, p := range dshPlans {
		if p.skipped || !p.changed {
			continue
		}
		perm := os.FileMode(0o644)
		if info, err := os.Stat(p.path); err == nil {
			perm = info.Mode().Perm()
		}
		if err := os.WriteFile(p.path, []byte(p.content), perm); err != nil {
			return rep, fmt.Errorf("provider: 写入 %s 失败: %w", p.path, err)
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
	for i, p := range dshPlans {
		if p.skipped {
			rep.Targets = append(rep.Targets, TargetReport{Name: p.target, Path: p.path,
				Action: ActionSkipped, Detail: p.detail})
			continue
		}
		act := ActionUnchanged
		if p.changed {
			act = ActionWritten
		}
		rep.Targets = append(rep.Targets, TargetReport{Name: p.target, Path: p.path,
			Action: act, Backup: dshBackupOf[i], Detail: p.detail})
	}
	return rep, nil
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
