// dsh.go — dsh（DeepSeek Harness）接管分支（2026-10-02，服务商接管扩 dsh）。
//
// 与 CC/codex 分支的形态差异：那边是对**在位配置**的定向改值（键已存在），
// dsh 侧是**代建**——渡口路由在 dsh 里本来不存在，落点＝home 级
// `~/.dsh/cordis.patch.yml`（叠加序最高、对所有 profile 生效，含 web/desktop
// 双实例）＋ `~/.dsh/.env` 占位令牌。纪律不变：
//
//   - home patch 是 Ferryman 整文件管理域（首行接管标记）；**他人文件（无标
//     记）＝前置拒绝、全案转人工**——绝不把别人的补丁层揉进生成物；
//   - 幂等：已是目标形态零写入零备份；有标记的旧版本允许重铸（dock 地址变
//     更等），重铸同戳成组备份；
//   - 新建文件无"接管前备份"可落——Restore 对"无备份＋带标记"的文件走删除
//     还原（patch 整文件删、.env 剥接管行），回到"接管从未发生"。
//
// 线协议事实（docs/research/20261002_dsh-服务商配置与摆渡可行性调研.md＋
// .scratch/dsh-phase2-finish/spike/jiefayi-report.md；2026-10-03 用户拍板切接法乙）：
// v2 走 llm-deepseek-api-key 适配器（v1 pi-ai 路四源钉死无会话键上线，渡口
// 永远抓不到会话归属——保温/摆渡全堵死；llm-deepseek 路每请求恒带
// x-deepseek-harness-session-id 头，值＝台账现行键 session-<uuid> 零换算，
// 渡口第三回落现成接住）。请求仍落 {baseURL}/v1/messages，模型名走渡口
// 六键映射（claude-opus-5/claude-sonnet-5 与 CC 同键），切上游零写盘。
// 思考档位解锁（2026-10-04 实证改档）：enabled＋high。依据——dsh 适配器线上
// 发 thinking.type＋output_config.effort low/high/max（llm-deepseek wire-types.ts:30-31），
// 智谱 Coding Plan anthropic 端点原生收这俩字段（官方 coding-plan 文档，Claude Code
// /effort 即走它们）；GLM-5.3 系强制思考，锁 disabled 只会把 UI 钳到 off 且实际仍被
// 上游映成 low 轻思考（白烧配额）——不如放开选档。旧最小方言档（disabled＋off）系
// 保守起步，已被上述查证取代。maxTokens 钉 32768 不变（适配器缺省 256k 超上游上限，CC 同上游实测 32k 档安全）＋
// models 只写 id/contextWindow（能力标志缺省关：图片/工具增删/in-history
// system 等方言整体不上线）。两贡献者插件 disabled（dsh_plugin_packages/
// dsh_session_log 全会话明文不上线；渡口 rewrite 剥 dsh_* 顶层键兜底）。
// 实证锚点（20261003 dsh --dump-config）：适配器插件注册 id＝llm-deepseek
// （包名 dsh-llm-deepseek-api-key 是 name 不是 id）；路由 id＝deepseek-official
// （适配器 index.ts 硬编码）；disabled: true 是 dsh 原生补丁机制（base 层关
// hmr/tool-plugin-manager 同款）；.env 令牌走 credentials-local 的
// $DSH_HOME/.env 只读回落，解析路径在案。contextWindow 200000 镜像 CC 对
// 同名档的信念（claude-opus-5=200k；只影响 dsh 本地压缩规划，不上线）。
package provider

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// dsh 接管目标名（ApplyReport 稳定可读）。
const (
	targetDSHPatch = "dsh-patch"
	targetDSHEnv   = "dsh-env"
	targetDSHHooks = "dsh-hooks"
)

// dshMarker 接管标记字面量：home patch 首行注释与 .env 注释行共用。
// 判定"这份文件是不是 Ferryman 生成的"唯一依据。
const dshMarker = "ferryman-takeover"

// DSHTokenEnv 渡口路由的占位令牌环境变量名（.env 落值 PlaceholderToken；
// 渡口入站不鉴权，非空即可过 dsh 的 MISSING_CREDENTIAL 前置）。
const DSHTokenEnv = "FERRYMAN_DOCK_TOKEN"

// dshHomePatchName / dshEnvName dsh 家目录下的两个接管文件名。
const (
	dshHomePatchName = "cordis.patch.yml"
	dshEnvName       = ".env"
)

// dshFilePlan dsh 侧单个文件的写入计划（纯计算产物，不落盘）。
type dshFilePlan struct {
	target  string // 目标名（targetDSHPatch / targetDSHEnv）
	path    string
	exists  bool   // 在位（改写场景；false＋changed＝新建，无备份）
	content string // 目标内容（changed=true 时有效）
	changed bool
	skipped bool // true＝本文件不参与（报告行携带 detail）
	detail  string
}

// dshPaths 两个接管文件的路径（Targets.DSHHome 派生）。
func dshPaths(dshHome string) (patch, env string) {
	return filepath.Join(dshHome, dshHomePatchName), filepath.Join(dshHome, dshEnvName)
}

// planDSH 纯计算 dsh 两文件的写入计划。返回顺序恒为 [patch, env]：
//   - DSHHome 目录不存在 → 两行 skipped（"dsh 未安装——跳过（不代建）"）；
//   - home patch 在位且无接管标记 → error（他人文件，全案拒绝转人工）；
//   - 其余按"已是目标/重铸/新建/补行"各就各位。
func planDSH(dshHome, dockBaseURL string) ([]dshFilePlan, error) {
	patchPath, envPath := dshPaths(dshHome)
	if fi, err := os.Stat(dshHome); err != nil || !fi.IsDir() {
		return []dshFilePlan{
			{target: targetDSHPatch, path: patchPath, skipped: true, detail: "dsh 未安装——跳过（不代建）"},
			{target: targetDSHEnv, path: envPath, skipped: true, detail: "dsh 未安装——跳过（不代建）"},
		}, nil
	}
	// —— home patch ——
	wantPatch := dshHomePatchYAML(dockBaseURL)
	raw, err := os.ReadFile(patchPath)
	switch {
	case errors.Is(err, os.ErrNotExist):
		patch := dshFilePlan{target: targetDSHPatch, path: patchPath,
			content: wantPatch, changed: true,
			detail: "新建（home 级补丁层——接管前不存在，无备份）"}
		return append([]dshFilePlan{patch}, planDSHEnv(envPath)...), nil
	case err != nil:
		return nil, fmt.Errorf("provider: %s 读取失败: %w", patchPath, err)
	}
	if !hasDSHMarker(string(raw)) {
		return nil, fmt.Errorf("provider: %s 是他人文件（无 %s 标记）——外科纪律不整文件"+
			"重写别人的补丁层，全案转人工；处置: 手工把 deepseek-official 渡口路由"+
			"并入该文件（形状＝ferryman provider apply 的 dshHomePatchYAML v2，"+
			"见 .scratch/dsh-phase2-finish/spike/jiefayi-report.md §2.3）后重跑 apply",
			patchPath, dshMarker)
	}
	patch := dshFilePlan{target: targetDSHPatch, path: patchPath, exists: true}
	if string(raw) == wantPatch {
		patch.detail = "已是目标形态（零写入）"
	} else {
		patch.content, patch.changed = wantPatch, true
		patch.detail = "带接管标记的旧版本——整体重铸（dock 地址等参数变更）"
	}
	return append([]dshFilePlan{patch}, planDSHEnv(envPath)...), nil
}

// planDSHEnv .env 的写入计划：缺令牌行＝补行/新建；已有＝零写入。
func planDSHEnv(envPath string) []dshFilePlan {
	env := dshFilePlan{target: targetDSHEnv, path: envPath}
	raw, err := os.ReadFile(envPath)
	switch {
	case errors.Is(err, os.ErrNotExist):
		env.content, env.changed = dshEnvNewContent(), true
		env.detail = "新建（占位令牌——渡口入站不鉴权，非空即可）"
		return []dshFilePlan{env}
	case err != nil:
		// 读不了＝不敢动（.env 常载真钥）：如实报错全案转人工。
		env.skipped, env.detail = true, fmt.Sprintf("读取失败: %v（转人工）", err)
		return []dshFilePlan{env}
	}
	if dshEnvHasToken(string(raw)) {
		env.exists, env.detail = true, "已是目标形态（零写入）"
		return []dshFilePlan{env}
	}
	env.exists = true
	env.content = dshEnvAppendContent(string(raw))
	env.changed = true
	env.detail = "补占位令牌行（其余行原样保留）"
	return []dshFilePlan{env}
}

// dshHomePatchYAML home 级 cordis.patch.yml 的确定性全量内容（v2＝接法乙，
// 2026-10-03 拍板；v1 pi-ai 形态已被本版取代——带标记旧版由 apply 整体重铸）。
// 改这里＝改接管形态：幂等比对、带标记重铸、测试钉字面量三处都会盯着。
func dshHomePatchYAML(dockBaseURL string) string {
	return strings.Join([]string{
		"# ferryman-takeover —— 本文件由 Ferryman provider apply 生成并整体管理。",
		"# 作用：dsh 的模型流量经 deepseek-official 路由（llm-deepseek 适配器）走本机",
		"# 渡口，对所有 profile 生效（叠加序里 home 层最后、最高）。接法乙 v2：每请求",
		"# 恒带 x-deepseek-harness-session-id 会话键，渡口接住后捕获/保温/摆渡/闸门",
		"# 全套同权。thinking enabled＋effort high＝思考档解锁＋maxTokens 钉值；两个",
		"# dsh_* 贡献者插件已 disabled（会话明文不上线，渡口另兜底剥除）。切供应商＝",
		"# ferryman provider switch <名>（翻渡口 [dock].active，本文件不动）；",
		"# 还原＝ferryman provider apply --restore。手改本文件会被下次 apply 重铸。",
		"- id: llm-deepseek",
		"  name: '@deepseek-ai/dsh-llm-deepseek-api-key'",
		"  config:",
		"    baseURL: " + dockBaseURL,
		"    apiKeyEnv: " + DSHTokenEnv,
		"    thinking: enabled",
		"    reasoningEffort: high",
		"    maxTokens: 32768",
		"    models:",
		"      - id: claude-opus-5",
		"        contextWindow: 200000",
		"      - id: claude-sonnet-5",
		"        contextWindow: 200000",
		"- id: plugin-package-inventory-deepseek",
		"  name: '@deepseek-ai/dsh-plugin-package-inventory-deepseek'",
		"  disabled: true",
		"- id: session-log-deepseek",
		"  name: '@deepseek-ai/dsh-session-log-deepseek'",
		"  disabled: true",
		"- id: agent-default-model",
		"  name: '@deepseek-ai/dsh-agent-default-model'",
		"  config:",
		"    provider: deepseek-official",
		"    model: claude-opus-5",
	}, "\n") + "\n"
}

// dshEnvNewContent 全新 .env 内容（接管前不存在时）。
func dshEnvNewContent() string {
	return dshEnvMarkerLine() + DSHTokenEnv + "=" + PlaceholderToken + "\n"
}

// dshEnvMarkerLine .env 里的接管标记注释行（Restore 剥离的锚点）。
func dshEnvMarkerLine() string {
	return "# " + dshMarker + " —— 以下令牌行由 Ferryman provider apply 管理（--restore 剥离）\n"
}

// dshEnvAppendContent 在既有 .env 尾部并入标记行＋令牌行（其余字节不动；
// 缺尾换行先补）。调用前提：内容里尚无令牌行。
func dshEnvAppendContent(raw string) string {
	if raw != "" && !strings.HasSuffix(raw, "\n") {
		raw += "\n"
	}
	return raw + "\n" + dshEnvMarkerLine() + DSHTokenEnv + "=" + PlaceholderToken + "\n"
}

// dshEnvHasToken .env 是否已含令牌行（行首精确键名，# 注释行不算）。
func dshEnvHasToken(raw string) bool {
	for _, ln := range strings.Split(raw, "\n") {
		if strings.HasPrefix(ln, DSHTokenEnv+"=") {
			return true
		}
	}
	return false
}

// dshEnvStripToken 从 .env 剥掉接管内容（标记注释行＋令牌行＋我们并入了的
// 前置空行），其余行逐字节保留；剥完为空 → 返回 ""（调用方删文件）。
func dshEnvStripToken(raw string) string {
	var keep []string
	lines := strings.Split(raw, "\n")
	for i := 0; i < len(lines); i++ {
		ln := lines[i]
		if strings.HasPrefix(ln, "# "+dshMarker) {
			// 我们并入时在标记行前垫了一个空行——一并剥掉（仅当它是我们垫的：
			// 上一保留行非空且原本紧贴标记行，无从分辨时多留一个空行无害）。
			if len(keep) > 0 && keep[len(keep)-1] == "" {
				keep = keep[:len(keep)-1]
			}
			continue
		}
		if strings.HasPrefix(ln, DSHTokenEnv+"=") {
			continue
		}
		keep = append(keep, ln)
	}
	out := strings.Join(keep, "\n")
	// 尾部空行收敛成一个换行（剥行后的残渣不留给下次 append 判空）。
	return strings.TrimRight(out, "\n") + "\n"
}

// hasDSHMarker 文件首段（前 4 行内）含接管标记＝Ferryman 生成的管理域文件。
// 只认首段不认全文：他人文件正文里引用了 ferryman 文档链接不构成"我们的文件"。
func hasDSHMarker(raw string) bool {
	for i, ln := range strings.Split(raw, "\n") {
		if i >= 4 {
			return false
		}
		if strings.Contains(ln, dshMarker) {
			return true
		}
	}
	return false
}

// ---- dsh-hooks：CC 钩子桥配置单发（票01，P2-3 桥仓库侧，2026-10-03）----

// dshHooksJSONName dsh 钩子桥配置文件名（落 ~/ferryman/dsh-hooks/——Ferryman
// 自家目录；绝不写 ~/.dsh/ 任何文件，D12 红线）。
const dshHooksJSONName = "hooks.json"

// dshHooksDoc hooks.json 文档形。JSON 不能带 # 注释，接管标记以顶层键承载：
// 桥解析器只遍历 CLAUDE_EVENTS 事件键（调研克隆 config.ts:86），多余键被忽略，
// 标记键因此无害且落在首行（hasDSHMarker 的前 4 行判定窗口内）。字段序即
// marshal 序——标记键必须居首。
type dshHooksDoc struct {
	FerrymanTakeover bool           `json:"ferryman-takeover"`
	UserPromptSubmit []dshHookGroup `json:"UserPromptSubmit"`
}

// dshHookGroup 单匹配组（桥 MatcherGroup 形：调研克隆 hook-protocol/src/
// types.ts:68-71）。UserPromptSubmit 无 matcher 载体——桥对该事件丢弃 matcher
// （config.ts:109-111），生成物不写该键。
type dshHookGroup struct {
	Hooks []dshHookEntry `json:"hooks"`
}

// dshHookEntry 单 command 钩子（types.ts:56-61；config.ts:103-106：type 必须
// command——其余类型被桥 skip 不跑；timeout 单位秒）。
type dshHookEntry struct {
	Type    string `json:"type"`
	Command string `json:"command"`
	Timeout int    `json:"timeout"`
}

// planDSHHooks hooks.json 的写入计划（纯计算，不落盘）。dsh 家族第三件：
//   - FerrymanHooksDir 空＝拒绝盲写（钩子命令无从派生）；
//   - dsh 家目录不在位＝skip 不代建（桥配置对未装 dsh 的机器无意义）；
//   - 在位无标记＝他人文件，全案拒绝转人工（与 patch/.env 同纪律）；
//   - 其余按 新建/重铸/已就位 各就各位。
func planDSHHooks(dshHome, hooksPath, hooksDir string) (dshFilePlan, error) {
	if strings.TrimSpace(hooksDir) == "" {
		return dshFilePlan{}, errors.New("provider: DSHHooksJSON 已设置但 FerrymanHooksDir 为空" +
			"——钩子命令无从派生，拒绝盲写")
	}
	if fi, err := os.Stat(dshHome); err != nil || !fi.IsDir() {
		return dshFilePlan{target: targetDSHHooks, path: hooksPath, skipped: true,
			detail: "dsh 未安装——跳过（不代建）"}, nil
	}
	want := dshHooksJSONContent(hooksDir)
	raw, err := os.ReadFile(hooksPath)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return dshFilePlan{target: targetDSHHooks, path: hooksPath,
			content: want, changed: true,
			detail: "新建（dsh CC 钩子桥配置——接管前不存在，无备份）"}, nil
	case err != nil:
		return dshFilePlan{}, fmt.Errorf("provider: %s 读取失败: %w", hooksPath, err)
	}
	if !hasDSHMarker(string(raw)) {
		return dshFilePlan{}, fmt.Errorf("provider: %s 是他人文件（无 %s 标记）——不整文件"+
			"覆盖他人的钩子配置，全案转人工；处置: 确认该文件用途后手工并入或挪走，再重跑 apply",
			hooksPath, dshMarker)
	}
	if string(raw) == want {
		return dshFilePlan{target: targetDSHHooks, path: hooksPath, exists: true,
			detail: "已是目标形态（零写入）"}, nil
	}
	return dshFilePlan{target: targetDSHHooks, path: hooksPath, exists: true,
		content: want, changed: true,
		detail: "带接管标记的旧版本——整体重铸（脚本路径等参数变更）"}, nil
}

// dshHooksJSONContent hooks.json 的确定性全量内容：UserPromptSubmit 单组单
// command 钩子，指向 dsh 专用闸门变体脚本（ferryman-gate-dsh.ps1——终局修复1：
// 桥 base() 恒传空 transcript_path（hooks-claude-code/src/index.ts:331-333），
// 基脚本的 agent='cc'+/gate 对 dsh 会话必 miss 台账＝永 no-ledger 放行；变体钉
// agent='dsh' 问 /dsh/gate 才能按 (dsh, session_id) 键命中，block→deny 语义与
// CC 同链），只指路径不复制脚本本体。命令行形与 installer.buildEntries 的 CC 侧
// 同字面量（powershell -NoProfile -ExecutionPolicy Bypass -File "<脚本>"）；
// timeout 3s 与 installer ccSpecs UserPromptSubmit 同值。改这里＝改桥配置形态：
// 幂等比对、带标记重铸、测试钉字面量三处都会盯着。
func dshHooksJSONContent(hooksDir string) string {
	doc := dshHooksDoc{
		FerrymanTakeover: true,
		UserPromptSubmit: []dshHookGroup{{Hooks: []dshHookEntry{{
			Type: "command",
			// 命令值＝手工套引号（%q 是 Go 转义，会把路径反斜杠翻倍；JSON
			// 转义归 marshal 管，钩子命令值里是单反斜杠原路径）。
			Command: fmt.Sprintf("powershell -NoProfile -ExecutionPolicy Bypass -File \"%s\"",
				filepath.Join(hooksDir, "ferryman-gate-dsh.ps1")),
			Timeout: 3,
		}}}},
	}
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		// 全为可序列化字面量，不可达；防御保真不吞错。
		panic(fmt.Sprintf("provider: dshHooksJSONContent 序列化失败: %v", err))
	}
	return string(b) + "\n"
}
