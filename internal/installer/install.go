// Package installer 钩子安装器 + doctor 体检（规格 ferryman/install.py + doctor.py
// 1:1，票20）。
//
// 地雷背景（DESIGN §3，2026-09-17 T21 三轮实测定案）：CC Switch 切换供应商 =
// 把 ~/.cc-switch/cc-switch.db 中该供应商的 settings_config 快照**逐字写入**
// settings.json——不在快照里的键（hooks）每次切换 / Live 模式重写都会被抹。
// 故 install-cc 在检测到 cc-switch.db 时自动把钩子注进全部 claude 供应商快照
// （InjectCCSwitch：幂等、保留既有条目、改库前备份）。
//
// 幂等：先移除 command 含 "ferryman" 的旧条目再追加。
//
// Go 新形态（spec 决策 C9/C12 + 评审附录#1/#14）：
//   - EnsureLauncher 产出 `start "" /min "<exe>" serve` 点火脚本——不再指向
//     venv python；窗口隐藏归启动方（console 子系统 exe + start /min）；
//   - 事件子集安装：CC 与 Codex 两侧一致（events 空 = 全集）；切换日装
//     SessionStart+SubagentStart+SubagentStop 三类——UserPromptSubmit 闸门
//     按用户指令暂不装（防子代理久跑场景主会话输入被吞），doctor 对闸门
//     缺位提示不失败；
//   - 心跳真实发送（HttpBeatSender）不实装（Q14 未授权）——doctor 输出
//     功能退化声明（信息行，不判 FAIL）。
package installer

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// LauncherName 点火脚本文件名（Python LAUNCHER_NAME）。
const LauncherName = "start-daemon.cmd"

// FerryEvents 四事件（doctor 覆盖口径同此序；Python FERRY_EVENTS）。
var FerryEvents = []string{"UserPromptSubmit", "SessionStart", "SubagentStart", "SubagentStop"}

// GateEvent 闸门事件（C12：缺位=提示不失败——用户指令未装）。
const GateEvent = "UserPromptSubmit"

// CCSwitchDBPath <home>/.cc-switch/cc-switch.db（路径经 HOME 注入，测试可指临时目录）。
func CCSwitchDBPath(home string) string {
	return filepath.Join(home, ".cc-switch", "cc-switch.db")
}

// CodexHooksPath <home>/.codex/hooks.json。
func CodexHooksPath(home string) string {
	return filepath.Join(home, ".codex", "hooks.json")
}

// CodexConfigPath <home>/.codex/config.toml。
func CodexConfigPath(home string) string {
	return filepath.Join(home, ".codex", "config.toml")
}

// repoRoot 仓库根缝（Python Path(__file__).parent.parent 的 Go 形）：默认 exe
// 所在目录（build.ps1 把 exe 出到仓库根，切换后 exe 与 hooks/ 同根）；
// 测试可整体替换。
var repoRoot = func() string {
	exe, err := os.Executable()
	if err != nil {
		return "."
	}
	return filepath.Dir(exe)
}

// exePath 点火脚本目标 = 当前 exe（合并 exe 自带 serve 子命令；C9 新形态）。
func exePath() string {
	exe, err := os.Executable()
	if err != nil {
		return "ferryman.exe"
	}
	return exe
}

// homeDir Path.home() 同位（失败回落空串——各默认路径退化为相对形，行为面
// 与 Python 的 userhome 缺失场景一致地不可用）。
func homeDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return home
}

// EnsureLauncher 生成守护进程点火脚本 <dataDir>/start-daemon.cmd（钩子自举用，
// DESIGN §3）。Go 新形态：start "" /min "<exe>" serve——不做开机自启，任意
// agent 的钩子 POST 前探测 :7311，不在则经本脚本隐藏/最小化窗口拉起 exe；
// 输出重定向到 serve.{out,err}.log（进程独立于钩子存活的关键）。
func EnsureLauncher(dataDir, exePath string) string {
	_ = os.MkdirAll(dataDir, 0o755) // mkdir(parents=True, exist_ok=True)
	// CRLF 行尾（Python win 分支逐字）；cmd 内容三行：@echo off + rem 注释 + start 行
	body := "@echo off\r\n" +
		"rem Ferryman 守护进程点火脚本（install-cc 自动生成，勿手改）\r\n" +
		fmt.Sprintf("start \"\" /min \"%s\" serve >> \"%s\" 2>> \"%s\"\r\n",
			exePath,
			filepath.Join(dataDir, "serve.out.log"),
			filepath.Join(dataDir, "serve.err.log"))
	launcher := filepath.Join(dataDir, LauncherName)
	if err := os.WriteFile(launcher, []byte(body), 0o644); err != nil {
		fmt.Println(err)
	}
	fmt.Printf("[ensure] 点火脚本就绪: %s（钩子自举 = agent 启动会话即拉起 daemon）\n", launcher)
	return launcher
}

// hookSpec 单事件条目形（script/timeout 逐字；matcher 仅 SessionStart 带）。
type hookSpec struct {
	script  string
	timeout int
	matcher string
}

// ccSpecs CC 侧四段钩子（install.py _ferry_hook_entries 逐字）：
// SessionStart 仅 clear|startup 注入（resume/compact 不注入，DESIGN §5）；
// 超时 10s：钩子内含 daemon 自举（最坏 ~2.5s 等就绪）+ POST。
// T32：子代理生命周期（CC ≥2.1.273；旧版本不触发事件 → T31 悬空检测兜底）。
var ccSpecs = map[string]hookSpec{
	"UserPromptSubmit": {script: "ferryman-gate.ps1", timeout: 3},
	"SessionStart":     {script: "ferryman-restore.ps1", timeout: 10, matcher: "clear|startup"},
	"SubagentStart":    {script: "ferryman-subagent.ps1", timeout: 3},
	"SubagentStop":     {script: "ferryman-subagent.ps1", timeout: 3},
}

// codexSpecs Codex 侧四段钩子（install.py install_codex 逐字；超时 10s 含
// daemon 自举等待预算同 CC restore；子代理钩子 session_id = 父会话 id → 纯计数）。
var codexSpecs = map[string]hookSpec{
	"UserPromptSubmit": {script: "ferryman-gate-codex.ps1", timeout: 3},
	"SessionStart":     {script: "ferryman-restore-codex.ps1", timeout: 10, matcher: "clear|startup"},
	"SubagentStart":    {script: "ferryman-subagent-codex.ps1", timeout: 3},
	"SubagentStop":     {script: "ferryman-subagent-codex.ps1", timeout: 3},
}

// FerryHookEntries 四段钩子条目（settings.json 与 CC Switch 快照共用同一结构；
// install.py _ferry_hook_entries 逐字）。events 子集筛选：空 = 全集（C12 /
// 评审附录#1）；输出按 FerryEvents 规范序（Go map 无序，落盘/打印序由
// orderedEvents 兜住）。
func FerryHookEntries(repo string, events []string) map[string]any {
	return buildEntries(repo, events, ccSpecs)
}

// buildEntries specs → 条目 map（PS 命令行/timeout 逐字）。
func buildEntries(repo string, events []string, specs map[string]hookSpec) map[string]any {
	ps := "powershell -NoProfile -ExecutionPolicy Bypass -File"
	entry := func(evt string) map[string]any {
		sp := specs[evt]
		cmd := map[string]any{
			"type":    "command",
			"command": fmt.Sprintf(`%s "%s"`, ps, filepath.Join(repo, "hooks", sp.script)),
			"timeout": sp.timeout,
		}
		inner := map[string]any{"hooks": []any{cmd}}
		if sp.matcher != "" {
			inner["matcher"] = sp.matcher
		}
		return inner
	}
	out := map[string]any{}
	for _, evt := range normalizeEvents(events) {
		out[evt] = []any{entry(evt)}
	}
	return out
}

// normalizeEvents 空 = 全集；否则按 FerryEvents 规范序过滤（未知事件名忽略）。
func normalizeEvents(events []string) []string {
	if len(events) == 0 {
		return slices.Clone(FerryEvents)
	}
	var out []string
	for _, evt := range FerryEvents {
		if slices.Contains(events, evt) {
			out = append(out, evt)
		}
	}
	return out
}

// orderedEvents entries 的规范序键（Python dict 插入序的 Go 形）。
func orderedEvents(entries map[string]any) []string {
	return normalizeEvents(keysOf(entries))
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// dumpJSON json.dumps(v, ensure_ascii=False, indent=2) 的 Go 形：非 ASCII 直出、
// 不转义 HTML、两空格缩进、无尾随换行（对齐 Python write_text 落盘字节面）。
func dumpJSON(v any) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
}

// marshalCompact json.dumps(v, ensure_ascii=False) 的 Go 形（幂等剔除判定用）。
func marshalCompact(v any) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
	return string(bytes.TrimSuffix(buf.Bytes(), []byte("\n")))
}

// backupFile shutil.copy2 同位：内容拷贝 + 尽力保留权限位。
func backupFile(src, backup string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	perm := os.FileMode(0o644)
	if info, err := os.Stat(src); err == nil {
		perm = info.Mode().Perm()
	}
	return os.WriteFile(backup, data, perm)
}

// stamp time.strftime('%Y%m%d_%H%M%S') 同位（本地时间）。
func stamp() string { return time.Now().Format("20060102_150405") }

// mergeHooks 幂等替换（install.py merge 逐字）：剔除条目 JSON 含 "ferryman"
// 的旧条目后追加最新；既有键值非列表时原样跳过（Python isinstance 守卫）。
func mergeHooks(hooks map[string]any, event string, newEntries []any) {
	existing, isList := hooks[event].([]any)
	if hooks[event] != nil && !isList {
		return
	}
	var kept []any
	for _, e := range existing {
		if !strings.Contains(marshalCompact(e), "ferryman") {
			kept = append(kept, e)
		}
	}
	hooks[event] = append(kept, newEntries...)
}

// ensureHooksMap data["hooks"] 取形（缺键建空表；根为 null 或既有值非对象
// → nil，由调用方响亮拒绝——Python 对其 .setdefault 会 AttributeError 崩）。
func ensureHooksMap(data map[string]any) map[string]any {
	if data == nil {
		return nil
	}
	if hooks, ok := data["hooks"].(map[string]any); ok {
		return hooks
	}
	if data["hooks"] == nil {
		hooks := map[string]any{}
		data["hooks"] = hooks
		return hooks
	}
	return nil
}

// InstallCC 把 Ferryman 钩子追加进 settings.json（不动既有条目；install.py
// install_cc 1:1）：点火脚本就绪 → 备份 → 幂等替换 → 写回（indent 2/非 ASCII
// 直出）→ CC Switch 地雷自动注入（检测不到则打印手动方案）。events 子集安装
// （C12）；repo 空 = exe 所在目录。返回 0。
func InstallCC(settingsPath, dbPath, dataDir, repo string, events []string) int {
	home := homeDir()
	if settingsPath == "" {
		settingsPath = filepath.Join(home, ".claude", "settings.json")
	}
	if dataDir == "" {
		dataDir = filepath.Join(home, "ferryman")
	}
	if dbPath == "" {
		dbPath = CCSwitchDBPath(home)
	}
	if repo == "" {
		repo = repoRoot()
	}
	EnsureLauncher(dataDir, exePath()) // 钩子自举点火脚本（唯一化前提）
	if _, err := os.Stat(settingsPath); errors.Is(err, os.ErrNotExist) {
		_ = os.WriteFile(settingsPath, []byte("{}"), 0o644)
	}
	backup := filepath.Join(filepath.Dir(settingsPath),
		"settings.json.bak-ferryman-"+stamp())
	if err := backupFile(settingsPath, backup); err != nil {
		fmt.Println(err)
		return 1
	}

	rawData, err := os.ReadFile(settingsPath)
	if err != nil {
		fmt.Println(err)
		return 1
	}
	var data map[string]any
	if err := json.Unmarshal(rawData, &data); err != nil {
		// Python json.loads 对坏文件直接 traceback（响亮失败）；Go 同为响亮：
		// 打印后拒绝写回（静默覆盖 = 数据丢失，更糟）。
		fmt.Println(err)
		return 1
	}
	hooks := ensureHooksMap(data)
	if hooks == nil {
		// 既有 "hooks" 值非对象：Python 对其 .setdefault 会 AttributeError 响亮崩，
		// Go 同为响亮——拒绝写回（静默覆盖 = 数据丢失，更糟）。
		fmt.Println("settings.json 的 hooks 不是对象，拒绝改写")
		return 1
	}
	entries := FerryHookEntries(repo, events)
	for _, evt := range orderedEvents(entries) {
		mergeHooks(hooks, evt, entries[evt].([]any))
	}
	if err := os.WriteFile(settingsPath, dumpJSON(data), 0o644); err != nil {
		fmt.Println(err)
		return 1
	}
	fmt.Printf("已追加 Ferryman 钩子到 %s（备份: %s）\n", settingsPath, filepath.Base(backup))
	fmt.Println()

	// CC Switch 地雷（DESIGN §3）：检测到 → 钩子注进全部 claude 供应商快照
	n := InjectCCSwitch(dbPath, events)
	if n == 0 {
		fmt.Println("[!] CC Switch 地雷（DESIGN §3）：切换供应商会全量覆盖 settings.json。")
		fmt.Println("   未检测到 cc-switch.db——若日后安装 CC Switch，重跑本命令或 install-ccswitch。")
		fmt.Println("   手动方案（四段钩子同步进各供应商模板）：")
		for _, evt := range orderedEvents(entries) {
			fmt.Printf("# %s\n", evt)
			fmt.Println(string(dumpJSON(entries[evt])))
		}
	}
	return 0
}

// InstallCodex 把 Ferryman 四段钩子注进 hooks.json 并开 [features] hooks =
// true（install.py install_codex 1:1；events 子集同款——评审附录#1）。
//
// 2026-09-17 实测定案（T23）：
//   - Codex 钩子**默认关闭**，config.toml 必须开 `hooks = true`——不开则
//     hooks.json 被静默忽略（此前一直没生效的根因之一）；
//   - 必须用 JSON 序列化写盘：手工写入把路径里的 \a / \f 写成 BEL/FF 控制字符，
//     钩子指向不存在路径且 fail-open 静默（已修过的真实事故，测试含控制字符回归）；
//   - 响应契约与 CC 认同构但 additionalContext 是顶层字段（非 hookSpecificOutput 包装）。
//
// 返回注入的事件数（全集 4）。
func InstallCodex(hooksPath, configPath, repo string, events []string) int {
	home := homeDir()
	if hooksPath == "" {
		hooksPath = CodexHooksPath(home)
	}
	if configPath == "" {
		configPath = CodexConfigPath(home)
	}
	if repo == "" {
		repo = repoRoot()
	}
	entries := buildEntries(repo, events, codexSpecs)

	var data map[string]any
	if _, err := os.Stat(hooksPath); err == nil {
		backup := filepath.Join(filepath.Dir(hooksPath),
			"hooks.json.bak-ferryman-"+stamp())
		if err := backupFile(hooksPath, backup); err != nil {
			fmt.Println(err)
			return 1
		}
		rawData, err := os.ReadFile(hooksPath)
		if err != nil {
			fmt.Println(err)
			return 1
		}
		if err := json.Unmarshal(rawData, &data); err != nil { // Python 坏 JSON = traceback 同响亮
			fmt.Println(err)
			return 1
		}
	} else {
		data = map[string]any{}
	}
	hooks := ensureHooksMap(data)
	if hooks == nil {
		fmt.Println("hooks.json 的 hooks 不是对象，拒绝改写")
		return 1
	}
	for _, evt := range orderedEvents(entries) {
		mergeHooks(hooks, evt, entries[evt].([]any))
	}
	if err := os.MkdirAll(filepath.Dir(hooksPath), 0o755); err != nil {
		fmt.Println(err)
		return 1
	}
	if err := os.WriteFile(hooksPath, dumpJSON(data), 0o644); err != nil {
		fmt.Println(err)
		return 1
	}

	// 功能旗标：[features] hooks = true（幂等；无 [features] 段则追加）
	tomlRaw, err := os.ReadFile(configPath)
	tomlText := ""
	if err == nil {
		tomlText = string(tomlRaw)
	}
	if !tomlHasHooksFlag(tomlText) {
		if strings.Contains(tomlText, "[features]") {
			tomlText = strings.Replace(tomlText, "[features]", "[features]\nhooks = true", 1)
		} else {
			tomlText = strings.TrimRight(tomlText, "\n") + "\n\n[features]\nhooks = true\n"
		}
		if err == nil { // config 存在才备份（Python config_path.exists() 守卫）
			_ = backupFile(configPath, filepath.Join(filepath.Dir(configPath),
				filepath.Base(configPath)+".bak-ferryman-"+stamp()))
		}
		if err := os.WriteFile(configPath, []byte(tomlText), 0o644); err != nil {
			fmt.Println(err)
			return 1
		}
	}

	fmt.Printf("已注入 Codex 钩子到 %s（Orca 等既有条目保留；注意 hooks.json "+
		"变更后需在 TUI /hooks 重新信任）\n", hooksPath)
	fmt.Printf("功能旗标 [features] hooks = true 已确保开启（%s）\n", configPath)
	return len(entries)
}

// tomlHasHooksFlag 任一行 strip 后去空格以 "hooks=true" 开头（Python 判定逐字）。
func tomlHasHooksFlag(tomlText string) bool {
	for _, line := range strings.Split(tomlText, "\n") {
		if strings.HasPrefix(strings.ReplaceAll(strings.TrimSpace(line), " ", ""), "hooks=true") {
			return true
		}
	}
	return false
}
