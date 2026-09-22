// doctor_test.go — 票20：doctor 体检验收（规格 tests/test_doctor.py 1:1）
// + C12 闸门缺位提示不失败用例 + 附录#14 HttpBeatSender 声明输出用例
// + runDoctor 聚合（退出码/结论行）用例。

package installer

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"ferryman/internal/config"
	"ferryman/internal/ferry"
)

// writeSettings {"hooks": hooks} 落盘（test_doctor.py _write_settings 同形）。
func writeSettings(t *testing.T, p string, hooks map[string]any) {
	t.Helper()
	writeJSONFile(t, p, map[string]any{"hooks": hooks})
}

// makeHookRepo 造带钩子脚本的临时仓库根（路径存在性检查的对象；BOM 在位——
// runDoctor 的 CheckHookScripts 全查 7 件）。
func makeHookRepo(t *testing.T, dir string) string {
	t.Helper()
	hooksDir := filepath.Join(dir, "hooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, n := range doctorScriptNames() {
		if err := os.WriteFile(filepath.Join(hooksDir, n), []byte("\xef\xbb\xbf# ok\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// ---- test_cc_hooks_all_present ----

func TestCCHooksAllPresent(t *testing.T) {
	tmp := t.TempDir()
	repo := makeHookRepo(t, filepath.Join(tmp, "repo"))
	p := filepath.Join(tmp, "settings.json")
	if err := os.WriteFile(p, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	read := captureStdout(t)
	InstallCC(p, filepath.Join(tmp, "no.db"), filepath.Join(tmp, "data"), repo, nil)
	read()
	c := CheckCCHooks(p)
	if !c.OK {
		t.Fatalf("应通过: %s", c.Msg)
	}
}

func TestCCHooksMissingReported(t *testing.T) {
	tmp := t.TempDir()
	p := filepath.Join(tmp, "settings.json")
	writeSettings(t, p, map[string]any{}) // 被 CC Switch 抹掉的样子
	c := CheckCCHooks(p)
	if c.OK {
		t.Fatalf("应失败: %s", c.Msg)
	}
	if !strings.Contains(c.Msg, "UserPromptSubmit") {
		t.Fatalf("缺事件点名: %s", c.Msg)
	}
}

// ---- C12：闸门事件缺位 = 提示不失败（仅缺闸门时） ----

func TestCCHooksGateMissingHintNotFail(t *testing.T) {
	tmp := t.TempDir()
	repo := makeHookRepo(t, filepath.Join(tmp, "repo"))
	p := filepath.Join(tmp, "settings.json")
	subset := []string{"SessionStart", "SubagentStart", "SubagentStop"}
	read := captureStdout(t)
	InstallCC(p, filepath.Join(tmp, "no.db"), filepath.Join(tmp, "data"), repo, subset)
	read()
	c := CheckCCHooks(p)
	if !c.OK {
		t.Fatalf("闸门缺位不应判 FAIL（C12）: %s", c.Msg)
	}
	if !strings.Contains(c.Msg, "UserPromptSubmit") || !strings.Contains(c.Msg, "未安装") {
		t.Fatalf("提示应注明闸门钩子按用户指令未安装: %s", c.Msg)
	}
}

func TestCCHooksGatePlusOtherMissingStillFails(t *testing.T) {
	// 闸门缺位 + 其余事件也缺 → 照旧失败（C12 只豁免"仅缺闸门"）
	tmp := t.TempDir()
	p := filepath.Join(tmp, "settings.json")
	writeSettings(t, p, map[string]any{"UserPromptSubmit": []any{map[string]any{
		"hooks": []any{map[string]any{"type": "command", "command": "ferryman-x.ps1", "timeout": 3}}}}})
	c := CheckCCHooks(p)
	if c.OK {
		t.Fatalf("缺三事件应失败: %s", c.Msg)
	}
	if !strings.Contains(c.Msg, "SessionStart") {
		t.Fatalf("缺事件应点名: %s", c.Msg)
	}
}

func TestCCHooksMissingScriptPathFails(t *testing.T) {
	// 路径不存在照旧失败（闸门豁免不覆盖路径检查）
	tmp := t.TempDir()
	p := filepath.Join(tmp, "settings.json")
	writeSettings(t, p, map[string]any{
		"SessionStart": []any{map[string]any{"matcher": "clear|startup", "hooks": []any{
			map[string]any{"type": "command",
				"command": `powershell -NoProfile -ExecutionPolicy Bypass -File "Z:/nope/ferryman-restore.ps1"`,
				"timeout": 10}}}},
		"UserPromptSubmit": []any{map[string]any{"hooks": []any{
			map[string]any{"type": "command",
				"command": `powershell -NoProfile -ExecutionPolicy Bypass -File "Z:/nope/ferryman-gate.ps1"`,
				"timeout": 3}}}},
		"SubagentStart": []any{map[string]any{"hooks": []any{
			map[string]any{"type": "command", "command": "ferryman-ok.ps1", "timeout": 3}}}},
		"SubagentStop": []any{map[string]any{"hooks": []any{
			map[string]any{"type": "command", "command": "ferryman-ok.ps1", "timeout": 3}}}},
	})
	c := CheckCCHooks(p)
	if c.OK {
		t.Fatalf("路径不存在应失败: %s", c.Msg)
	}
	if !strings.Contains(c.Msg, "钩子脚本路径不存在") {
		t.Fatalf("文案不符: %s", c.Msg)
	}
}

func TestCCHooksSessionStartTimeoutFails(t *testing.T) {
	tmp := t.TempDir()
	repo := makeHookRepo(t, filepath.Join(tmp, "repo"))
	p := filepath.Join(tmp, "settings.json")
	subset := []string{"SessionStart", "SubagentStart", "SubagentStop"}
	read := captureStdout(t)
	InstallCC(p, filepath.Join(tmp, "no.db"), filepath.Join(tmp, "data"), repo, subset)
	read()
	// 篡改 SessionStart timeout → 3（<10 应失败，C12 不豁免）
	data := parseFile(t, p)
	hooks := hooksOf(t, data)
	ss := firstMap(t, asList(hooks["SessionStart"]), "ferryman")
	hook := firstMap(t, asList(ss["hooks"]), "")
	hook["timeout"] = 3
	writeJSONFile(t, p, data)
	c := CheckCCHooks(p)
	if c.OK {
		t.Fatalf("SessionStart 超时 <10s 应失败: %s", c.Msg)
	}
	if !strings.Contains(c.Msg, "SessionStart 超时 <10s") {
		t.Fatalf("文案不符: %s", c.Msg)
	}
}

// ---- test_hook_scripts_bom_and_control_chars ----

func TestHookScriptsBomAndControlChars(t *testing.T) {
	tmp := t.TempDir()
	good := filepath.Join(tmp, "good.ps1")
	if err := os.WriteFile(good, []byte("\xef\xbb\xbf# ok\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	nobom := filepath.Join(tmp, "nobom.ps1")
	if err := os.WriteFile(nobom, []byte("# bad\n"), 0o644); err != nil { // 无 BOM（PS5.1 中文注释地雷）
		t.Fatal(err)
	}
	ctrl := filepath.Join(tmp, "ctrl.ps1")
	if err := os.WriteFile(ctrl, []byte("\xef\xbb\xbf$a = 'x\x0cy'\n"), 0o644); err != nil { // FF 控制字符（事故回归）
		t.Fatal(err)
	}
	checks := CheckHookScripts([]string{good, nobom, ctrl})
	if !checks[0].OK {
		t.Fatalf("good 应通过: %s", checks[0].Msg)
	}
	if checks[1].OK {
		t.Fatalf("nobom 应失败: %s", checks[1].Msg)
	}
	if !strings.Contains(checks[1].Msg, "BOM") {
		t.Fatalf("文案缺 BOM: %s", checks[1].Msg)
	}
	if checks[2].OK {
		t.Fatalf("ctrl 应失败: %s", checks[2].Msg)
	}
	if !strings.Contains(checks[2].Msg, "控制字符") {
		t.Fatalf("文案缺 控制字符: %s", checks[2].Msg)
	}
}

// ---- test_ccswitch_snapshot_coverage ----

func TestCCSwitchSnapshotCoverage(t *testing.T) {
	tmp := t.TempDir()
	db := filepath.Join(tmp, "cc-switch.db")
	makeDB(t, db, [][3]string{
		{"claude", "P1", "{}"}, // 无钩子 → 缺
		{"claude", "P2", mustJSON(t, map[string]any{"hooks": map[string]any{
			"UserPromptSubmit": []any{map[string]any{"hooks": []any{map[string]any{"command": "ferryman.ps1"}}}},
			"SessionStart":     []any{map[string]any{"hooks": []any{map[string]any{"command": "ferryman.ps1"}}}},
			"SubagentStart":    []any{map[string]any{"hooks": []any{map[string]any{"command": "ferryman.ps1"}}}},
			"SubagentStop":     []any{map[string]any{"hooks": []any{map[string]any{"command": "ferryman.ps1"}}}},
		}})},
	})
	c := CheckCCSwitch(db)
	if c.OK {
		t.Fatalf("应失败: %s", c.Msg)
	}
	if !strings.Contains(c.Msg, "P1") || strings.Contains(c.Msg, "P2") { // 点名缺钩子的供应商
		t.Fatalf("点名不符: %s", c.Msg)
	}
}

// ---- test_codex_hooks_and_flag ----

func TestCodexHooksAndFlag(t *testing.T) {
	tmp := t.TempDir()
	hooks := filepath.Join(tmp, "hooks.json")
	all4 := map[string]any{}
	for _, evt := range FerryEvents {
		all4[evt] = []any{map[string]any{"hooks": []any{map[string]any{"command": "ferryman.ps1"}}}}
	}
	writeJSONFile(t, hooks, map[string]any{"hooks": all4})
	cfg := filepath.Join(tmp, "config.toml")
	if err := os.WriteFile(cfg, []byte("[features]\nhooks = true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := CheckCodex(hooks, cfg)
	if !c.OK {
		t.Fatalf("应通过: %s", c.Msg)
	}
	if err := os.WriteFile(cfg, []byte("[features]\n"), 0o644); err != nil { // 旗标关
		t.Fatal(err)
	}
	c = CheckCodex(hooks, cfg)
	if c.OK {
		t.Fatalf("旗标关应失败: %s", c.Msg)
	}
	if !strings.Contains(c.Msg, "hooks = true") {
		t.Fatalf("文案缺 hooks = true: %s", c.Msg)
	}
}

// ---- test_daemon_probe ----

func TestDaemonProbe(t *testing.T) {
	tmp := t.TempDir()
	c := CheckDaemon(func() map[string]any { return nil }, filepath.Join(tmp, "no.pid"))
	if c.OK || !strings.Contains(c.Msg, "未运行") {
		t.Fatalf("daemon 死应失败: %+v", c)
	}
	c = CheckDaemon(func() map[string]any { return map[string]any{"health_alert": false} },
		filepath.Join(tmp, "no.pid"))
	if !c.OK || !strings.Contains(c.Msg, "ok") {
		t.Fatalf("daemon 活应通过: %+v", c)
	}
}

// ---- test_ferry_provider_check ----

func TestFerryProviderCheck(t *testing.T) {
	c := CheckFerryProvider("", map[string]ferry.Provider{}) // 未配置
	if c.OK || !strings.Contains(c.Msg, "未配置") {
		t.Fatalf("未配置应失败: %+v", c)
	}
	c = CheckFerryProvider("mine", map[string]ferry.Provider{}) // 名字无定义
	if c.OK || !strings.Contains(c.Msg, "未在") {
		t.Fatalf("无定义应失败: %+v", c)
	}
	c = CheckFerryProvider("mine", map[string]ferry.Provider{
		"mine": {Name: "mine", BaseURL: "http://x", Model: "m"}})
	if !c.OK {
		t.Fatalf("在位应通过: %s", c.Msg)
	}
}

// ---- test_launcher（+ Go 新形态：exe 有效性检查） ----

func TestLauncher(t *testing.T) {
	tmp := t.TempDir()
	c := CheckLauncher(filepath.Join(tmp, "no.cmd"))
	if c.OK {
		t.Fatalf("脚本不在应失败: %s", c.Msg)
	}
	// ensure_launcher 产物：exe 存在 → 通过
	exe := filepath.Join(tmp, "ferryman.exe")
	if err := os.WriteFile(exe, []byte("MZ"), 0o644); err != nil {
		t.Fatal(err)
	}
	dataDir := filepath.Join(tmp, "data")
	read := captureStdout(t)
	if _, err := EnsureLauncher(dataDir, exe); err != nil {
		t.Fatal(err)
	}
	read()
	c = CheckLauncher(filepath.Join(dataDir, LauncherName))
	if !c.OK {
		t.Fatalf("应通过: %s", c.Msg)
	}
	// Go 新形态：启动行 exe 路径不存在 → 失败（票15 覆盖：exe 存在性检查）
	missingData := filepath.Join(tmp, "data2")
	read = captureStdout(t)
	if _, err := EnsureLauncher(missingData, filepath.Join(tmp, "gone.exe")); err != nil {
		t.Fatal(err)
	}
	read()
	c = CheckLauncher(filepath.Join(missingData, LauncherName))
	if c.OK {
		t.Fatalf("exe 失效应失败: %s", c.Msg)
	}
	if !strings.Contains(c.Msg, "失效") {
		t.Fatalf("文案缺 失效: %s", c.Msg)
	}
}

// ---- 附录#14：HttpBeatSender 功能退化声明（信息行不判 FAIL） ----

func TestHttpBeatNoticeContent(t *testing.T) {
	if !strings.Contains(HttpBeatNotice, "心跳真实发送未实装") ||
		!strings.Contains(HttpBeatNotice, "Q14") ||
		!strings.Contains(HttpBeatNotice, "observe") {
		t.Fatalf("声明文案不符: %s", HttpBeatNotice)
	}
}

// ---- 票22 骑手 M2：CheckCCSwitch / CheckCodex 闸门事件豁免 ----

// subsetSnapshot 只带三硬性事件的供应商快照（C12 切换日安装面）。
func subsetSnapshot(t *testing.T) string {
	t.Helper()
	return mustJSON(t, map[string]any{"hooks": map[string]any{
		"SessionStart":  []any{map[string]any{"hooks": []any{map[string]any{"command": "ferryman.ps1"}}}},
		"SubagentStart": []any{map[string]any{"hooks": []any{map[string]any{"command": "ferryman.ps1"}}}},
		"SubagentStop":  []any{map[string]any{"hooks": []any{map[string]any{"command": "ferryman.ps1"}}}},
	}})
}

func TestCCSwitchGateMissingHintNotFail(t *testing.T) {
	tmp := t.TempDir()
	db := filepath.Join(tmp, "cc-switch.db")
	makeDB(t, db, [][3]string{
		{"claude", "子集快照", subsetSnapshot(t)},
		{"claude", "全集快照", mustJSON(t, map[string]any{"hooks": map[string]any{
			"UserPromptSubmit": []any{map[string]any{"hooks": []any{map[string]any{"command": "ferryman.ps1"}}}},
			"SessionStart":     []any{map[string]any{"hooks": []any{map[string]any{"command": "ferryman.ps1"}}}},
			"SubagentStart":    []any{map[string]any{"hooks": []any{map[string]any{"command": "ferryman.ps1"}}}},
			"SubagentStop":     []any{map[string]any{"hooks": []any{map[string]any{"command": "ferryman.ps1"}}}},
		}})},
	})
	c := CheckCCSwitch(db)
	if !c.OK {
		t.Fatalf("仅缺闸门应提示不失败（C12）: %s", c.Msg)
	}
	if !strings.Contains(c.Msg, "UserPromptSubmit") || !strings.Contains(c.Msg, "未安装") {
		t.Fatalf("提示应注明闸门钩子按用户指令未安装: %s", c.Msg)
	}
	// 硬性事件缺失照旧失败（闸门豁免不覆盖三硬性事件）
	db2 := filepath.Join(tmp, "cc2.db")
	makeDB(t, db2, [][3]string{{"claude", "缺硬事件", mustJSON(t, map[string]any{"hooks": map[string]any{
		"UserPromptSubmit": []any{map[string]any{"hooks": []any{map[string]any{"command": "ferryman.ps1"}}}},
	}})}})
	c = CheckCCSwitch(db2)
	if c.OK || !strings.Contains(c.Msg, "缺硬事件") {
		t.Fatalf("缺硬性事件应失败并点名: %s", c.Msg)
	}
}

func TestCodexGateMissingHintNotFail(t *testing.T) {
	tmp := t.TempDir()
	hooks := filepath.Join(tmp, "hooks.json")
	writeJSONFile(t, hooks, map[string]any{"hooks": map[string]any{
		"SessionStart":  []any{map[string]any{"hooks": []any{map[string]any{"command": "ferryman.ps1"}}}},
		"SubagentStart": []any{map[string]any{"hooks": []any{map[string]any{"command": "ferryman.ps1"}}}},
		"SubagentStop":  []any{map[string]any{"hooks": []any{map[string]any{"command": "ferryman.ps1"}}}},
	}})
	cfg := filepath.Join(tmp, "config.toml")
	if err := os.WriteFile(cfg, []byte("[features]\nhooks = true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := CheckCodex(hooks, cfg)
	if !c.OK {
		t.Fatalf("仅缺闸门应提示不失败（C12）: %s", c.Msg)
	}
	if !strings.Contains(c.Msg, "UserPromptSubmit") || !strings.Contains(c.Msg, "未安装") {
		t.Fatalf("提示应注明闸门钩子按用户指令未安装: %s", c.Msg)
	}
	// 硬性事件缺失照旧失败
	writeJSONFile(t, hooks, map[string]any{"hooks": map[string]any{
		"UserPromptSubmit": []any{map[string]any{"hooks": []any{map[string]any{"command": "ferryman.ps1"}}}},
	}})
	c = CheckCodex(hooks, cfg)
	if c.OK || !strings.Contains(c.Msg, "SessionStart") {
		t.Fatalf("缺硬性事件应失败并点名: %s", c.Msg)
	}
}

// ---- 票22 骑手 M5：CheckCodex 读失败与解析失败分开 ----

func TestCodexReadFailureSeparatedFromParse(t *testing.T) {
	tmp := t.TempDir()
	hooks := filepath.Join(tmp, "hooks.json")
	if err := os.WriteFile(hooks, []byte("not json {"), 0o644); err != nil { // 坏 JSON
		t.Fatal(err)
	}
	cfg := filepath.Join(tmp, "config.toml")
	if err := os.WriteFile(cfg, []byte("[features]\nhooks = true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := CheckCodex(hooks, cfg)
	if c.OK || !strings.Contains(c.Msg, "hooks.json 解析失败") {
		t.Fatalf("坏 JSON 应报解析失败: %s", c.Msg)
	}
	if strings.Contains(c.Msg, "读取失败") {
		t.Fatalf("解析失败不得混报读取失败: %s", c.Msg)
	}
	// 读失败分支：路径在（目录）但读不出（Windows 拒读目录）——独立于解析失败
	asDir := filepath.Join(tmp, "hooks-as-dir")
	if err := os.MkdirAll(asDir, 0o755); err != nil {
		t.Fatal(err)
	}
	c = CheckCodex(asDir, cfg)
	if c.OK {
		t.Fatalf("读失败应失败: %s", c.Msg)
	}
	if !strings.Contains(c.Msg, "读取失败") || strings.Contains(c.Msg, "解析失败") {
		t.Fatalf("读失败应独立成支: %s", c.Msg)
	}
}

// ---- runDoctor 聚合：闸门缺位提示不失败 + 声明行 + 退出码 ----

// greenDoctorDeps 全绿环境（闸门缺位除外）：临时 HOME/仓库/codex 文件，
// 配置与探针注入。票22 骑手 M2：CC/Codex 两侧 + CC Switch 快照全部按 C12
// 子集安装——doctor 必须全绿（提示不判失败）。
func greenDoctorDeps(t *testing.T, probe func() map[string]any) (doctorDeps, string) {
	t.Helper()
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")
	repo := makeHookRepo(t, filepath.Join(tmp, "repo"))
	if err := os.MkdirAll(filepath.Join(home, "ferryman"), 0o755); err != nil {
		t.Fatal(err)
	}
	// CC 子集安装（闸门缺位）；.claude 目录先建（Python write_text 同样要求父目录在）
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	dataDir := filepath.Join(home, "ferryman")
	subset := []string{"SessionStart", "SubagentStart", "SubagentStop"}
	read := captureStdout(t)
	InstallCC(filepath.Join(home, ".claude", "settings.json"),
		filepath.Join(tmp, "no.db"), dataDir, repo, subset)
	// Codex 同款子集（骑手 M2：CheckCodex 闸门豁免）
	InstallCodex(CodexHooksPath(home), CodexConfigPath(home), repo, subset)
	// CC Switch 库在位且快照为子集（骑手 M2：CheckCCSwitch 闸门豁免）
	db := filepath.Join(tmp, "cc-switch.db")
	makeDB(t, db, [][3]string{{"claude", "P1", subsetSnapshot(t)}})
	// 票06：MCP 注册在位（用户级 .claude.json 装态——mcp_registration 全绿前提）
	InstallMCP(filepath.Join(home, ".claude.json"), filepath.Join(repo, "ferryman.exe"), false)
	read()
	cfg := config.Default()
	cfg.FerryProvider = "glm"
	return doctorDeps{
		Home:        home,
		Repo:        repo,
		CCSwitchDB:  db,
		CodexHooks:  CodexHooksPath(home),
		CodexConfig: CodexConfigPath(home),
		LoadCfg:     func() (*config.Config, error) { return cfg, nil },
		LoadProviders: func() (map[string]ferry.Provider, error) {
			return map[string]ferry.Provider{"glm": {Name: "glm", BaseURL: "http://x", Model: "m"}}, nil
		},
		Probe: probe,
		// 票02：常驻保障两查注入绿色（在位）
		Autostart:    func() (autostartStatus, error) { return autostartInstalled, nil },
		WatchdogTask: func() (TaskStatus, error) { return TaskStatus{Exists: true, NextRun: "2026/9/19 21:00:00"}, nil },
		Out:          nil, // 调用方填
	}, dataDir
}

func TestRunDoctorGateHintNotFailAndNotice(t *testing.T) {
	deps, _ := greenDoctorDeps(t, func() map[string]any {
		return map[string]any{"health_alert": false}
	})
	var out strings.Builder
	deps.Out = &out
	code := runDoctor(deps)
	got := out.String()
	if code != 0 {
		t.Fatalf("仅闸门缺位应退出 0:\n%s", got)
	}
	if !strings.Contains(got, "UserPromptSubmit") || !strings.Contains(got, "未安装") {
		t.Fatalf("缺闸门提示:\n%s", got)
	}
	if !strings.Contains(got, HttpBeatNotice) {
		t.Fatalf("缺 HttpBeatSender 声明行:\n%s", got)
	}
	if !strings.Contains(got, "通过") || strings.Contains(got, "——有问题见上") {
		t.Fatalf("结论行不符:\n%s", got)
	}
	// 提示行按 [OK] 打印（提示不失败的输出面）
	if !strings.Contains(got, "[OK]   settings.json 缺闸门钩子 UserPromptSubmit") {
		t.Fatalf("闸门提示应以 [OK] 打印:\n%s", got)
	}
}

func TestRunDoctorFailsWhenDaemonDown(t *testing.T) {
	deps, _ := greenDoctorDeps(t, func() map[string]any { return nil })
	var out strings.Builder
	deps.Out = &out
	code := runDoctor(deps)
	got := out.String()
	if code != 1 {
		t.Fatalf("daemon 死应退出 1:\n%s", got)
	}
	if !strings.Contains(got, "daemon 未运行") || !strings.Contains(got, "——有问题见上") {
		t.Fatalf("结论/文案不符:\n%s", got)
	}
}

// 结论行计数 sanity：全绿时 N/N 通过（N = 检查项数，不含声明行）。
func TestRunDoctorConclusionCount(t *testing.T) {
	deps, _ := greenDoctorDeps(t, func() map[string]any {
		return map[string]any{"health_alert": false}
	})
	var out strings.Builder
	deps.Out = &out
	_ = runDoctor(deps)
	got := out.String()
	// 票02 起：+2 = Run 键自启 + 看门计划任务两查；票06 起：+1 = MCP 注册在位；
	// 升级链票06 起：+1 = 升级事务残留检查
	want := fmt.Sprintf("体检结论: %d/%d 通过", 1+1+1+1+len(doctorScriptNames())+1+1+2+1+1,
		1+1+1+1+len(doctorScriptNames())+1+1+2+1+1)
	if !strings.Contains(got, want) {
		t.Fatalf("结论计数不符:\nwant: %s\ngot:\n%s", want, got)
	}
	// 票02：两查绿色行可见
	if !strings.Contains(got, "Run 键自启在位") || !strings.Contains(got, "看门计划任务在位") {
		t.Fatalf("缺常驻保障两查绿色行:\n%s", got)
	}
}

// ---- 票02（规格 §A）：doctor 版本行（结论清单收尾段） ----

// TestRunDoctorVersionLine 版本行两态：dev（含空串=未装配）显「非 release
// 构建」提示；注入值（ldflags 注入的 release 值经 cmd/ferryman 装配传入）
// 原样显摆、不带提示。版本行是信息行——不计检查项、不影响退出码。
func TestRunDoctorVersionLine(t *testing.T) {
	deps, _ := greenDoctorDeps(t, func() map[string]any { return map[string]any{"health_alert": false} })
	var out strings.Builder
	deps.Out = &out
	deps.Version = "dev"
	if code := runDoctor(deps); code != 0 {
		t.Fatalf("全绿夹具应退出 0:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "版本: dev") ||
		!strings.Contains(out.String(), "非 release 构建") {
		t.Fatalf("dev 构建版本行应显 dev 与非 release 提示:\n%s", out.String())
	}
	// 注入 release 值：原样显摆、不带非 release 提示
	var out2 strings.Builder
	deps.Out = &out2
	deps.Version = "v0.1.0-3-gabcdef"
	_ = runDoctor(deps)
	got2 := out2.String()
	if !strings.Contains(got2, "版本: v0.1.0-3-gabcdef") {
		t.Fatalf("注入版本应原样显摆:\n%s", got2)
	}
	if strings.Contains(got2, "非 release 构建") {
		t.Fatalf("release 值不应带非 release 提示:\n%s", got2)
	}
}

// ---- 票02：Run 键自启 + 看门计划任务两项检查 ----

// 三态 × 判定（可注入面，fake 返回三态——票面验收④）。
func TestCheckAutostartStates(t *testing.T) {
	table := []struct {
		name    string
		st      autostartStatus
		err     error
		wantOK  bool
		wantSub string
	}{
		{"installed", autostartInstalled, nil, true, "在位"},
		{"missing", autostartMissing, nil, false, "缺失"},
		{"mismatch", autostartMismatch, nil, false, "不符"},
		{"readerr", 0, errors.New("boom"), false, "读取失败"},
	}
	for _, tc := range table {
		c := CheckAutostart(func() (autostartStatus, error) { return tc.st, tc.err })
		if c.OK != tc.wantOK || !strings.Contains(c.Msg, tc.wantSub) {
			t.Fatalf("%s: got %+v, want ok=%v msg含%q", tc.name, c, tc.wantOK, tc.wantSub)
		}
	}
}

// 在位（含/缺下次运行）/缺失/查询失败 四面。
func TestCheckWatchdogTaskStates(t *testing.T) {
	c := CheckWatchdogTask(func() (TaskStatus, error) {
		return TaskStatus{Exists: true, NextRun: "2026/9/19 21:00:00"}, nil
	})
	if !c.OK || !strings.Contains(c.Msg, "2026/9/19 21:00:00") {
		t.Fatalf("在位应通过并带下次运行: %+v", c)
	}
	c = CheckWatchdogTask(func() (TaskStatus, error) { return TaskStatus{Exists: true}, nil })
	if !c.OK {
		t.Fatalf("在位但解析不出下次运行仍应通过: %+v", c)
	}
	c = CheckWatchdogTask(func() (TaskStatus, error) { return TaskStatus{}, nil })
	if c.OK || !strings.Contains(c.Msg, "缺失") {
		t.Fatalf("缺失应失败并点名: %+v", c)
	}
	c = CheckWatchdogTask(func() (TaskStatus, error) { return TaskStatus{}, errors.New("schtasks broken") })
	if c.OK || !strings.Contains(c.Msg, "查询失败") {
		t.Fatalf("查询失败应失败: %+v", c)
	}
}

// runDoctor 聚合：两查缺失 → 两行可见 + 退出 1。
func TestRunDoctorAutostartWatchdogFailVisible(t *testing.T) {
	deps, _ := greenDoctorDeps(t, func() map[string]any { return map[string]any{"health_alert": false} })
	deps.Autostart = func() (autostartStatus, error) { return autostartMissing, nil }
	deps.WatchdogTask = func() (TaskStatus, error) { return TaskStatus{}, nil }
	var out strings.Builder
	deps.Out = &out
	if code := runDoctor(deps); code != 1 {
		t.Fatalf("两查缺失应退出 1, got %d:\n%s", code, out.String())
	}
	got := out.String()
	if !strings.Contains(got, "Run 键自启缺失") || !strings.Contains(got, "看门计划任务缺失") {
		t.Fatalf("缺两查失败行:\n%s", got)
	}
}

// ---- 票06(规格 §C 第9条崩溃恢复,D9/F4 配套):doctor「升级事务残留」检查 ----

// TestCheckUpdateResiduesNone 无残留 = 常规通过项(不噪声)。
func TestCheckUpdateResiduesNone(t *testing.T) {
	tmp := t.TempDir()
	dataDir := filepath.Join(tmp, "data")
	exeDir := filepath.Join(tmp, "exe")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(exeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	c := CheckUpdateResidues(dataDir, exeDir)
	if !c.OK {
		t.Fatalf("无残留应通过: %s", c.Msg)
	}
}

// TestCheckUpdateResiduesJournal journal 在册 = 失败 + 点名残留物 + 一行处置
// 建议(运行 ferryman update 自动恢复/清理)。
func TestCheckUpdateResiduesJournal(t *testing.T) {
	tmp := t.TempDir()
	dataDir := filepath.Join(tmp, "data")
	exeDir := filepath.Join(tmp, "exe")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(exeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "update-journal.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := CheckUpdateResidues(dataDir, exeDir)
	if c.OK {
		t.Fatalf("journal 残留应失败: %s", c.Msg)
	}
	if !strings.Contains(c.Msg, "update-journal.json") {
		t.Fatalf("应点名残留物: %s", c.Msg)
	}
	if !strings.Contains(c.Msg, "ferryman update") {
		t.Fatalf("应含一行处置建议(ferryman update 自动恢复/清理): %s", c.Msg)
	}
}

// TestCheckUpdateResiduesHalfWritten 半写临时 update-journal.json.tmp 同判残留
// (update.saveJournal 的临时文件+rename 中断形态)。
func TestCheckUpdateResiduesHalfWritten(t *testing.T) {
	tmp := t.TempDir()
	dataDir := filepath.Join(tmp, "data")
	exeDir := filepath.Join(tmp, "exe")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(exeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "update-journal.json.tmp"), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := CheckUpdateResidues(dataDir, exeDir)
	if c.OK || !strings.Contains(c.Msg, "update-journal.json.tmp") {
		t.Fatalf("半写临时应判残留并点名: %+v", c)
	}
}

// TestCheckUpdateResiduesSwapDomain exe 旁换装残留 = update.cleanSwapResidues
// 清扫域(.new/.new.part/.swap-tmp*)逐一对出;.old-* 备份与正式 exe 不在残留
// 域(备份是 D9 回滚保障,不得误报);处置建议含极端缺位(swap-tmp 改回)提示。
func TestCheckUpdateResiduesSwapDomain(t *testing.T) {
	tmp := t.TempDir()
	dataDir := filepath.Join(tmp, "data")
	exeDir := filepath.Join(tmp, "exe")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(exeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	residues := []string{"ferryman.exe.new", "ferryman.exe.new.part", "ferryman.exe.swap-tmp", "ferryman.exe.swap-tmp-9"}
	for _, n := range residues {
		if err := os.WriteFile(filepath.Join(exeDir, n), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// 诱饵:正式 exe 与备份不在清扫域
	for _, n := range []string{"ferryman.exe", "ferryman.exe.old-v0.1.0"} {
		if err := os.WriteFile(filepath.Join(exeDir, n), []byte("MZ"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	c := CheckUpdateResidues(dataDir, exeDir)
	if c.OK {
		t.Fatalf("换装残留应失败: %s", c.Msg)
	}
	for _, n := range residues {
		if !strings.Contains(c.Msg, n) {
			t.Fatalf("应点名 %s: %s", n, c.Msg)
		}
	}
	if strings.Contains(c.Msg, ".old-") {
		t.Fatalf("备份不得误报为残留: %s", c.Msg)
	}
	if !strings.Contains(c.Msg, "swap-tmp") || !strings.Contains(c.Msg, "改回") {
		t.Fatalf("应含极端缺位改回提示: %s", c.Msg)
	}
}

// TestDoctorResultsUpdateResidueWiring 装配缝:全绿夹具末位 = update_residues
// 且 pass;数据目录出现 journal 残留 → 该项 fail 行可见 + 退出码 1。
func TestDoctorResultsUpdateResidueWiring(t *testing.T) {
	deps, dataDir := greenDoctorDeps(t, func() map[string]any { return map[string]any{"health_alert": false} })
	res := doctorResults(deps)
	if last := res[len(res)-1]; last.Name != "update_residues" || last.Status != StatusPass {
		t.Fatalf("末位应为 update_residues 且全绿夹具下 pass: %+v", last)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "update-journal.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	deps.Out = &out
	if code := runDoctor(deps); code != 1 {
		t.Fatalf("残留应退出 1, got %d:\n%s", code, out.String())
	}
	if !strings.Contains(out.String(), "发现升级事务残留") || !strings.Contains(out.String(), "update-journal.json") {
		t.Fatalf("残留结论行不可见:\n%s", out.String())
	}
}

// ---- 票04：CheckDockRewrite 新语义（改写隐含开启；复用 dock.ResolveRewrite
// 显式开关内核，绝不读废弃 rewrite_enabled） ----

// TestCheckDockRewriteImplicitOn 票01 评审确认缺陷的修复钉子：迁移后的新配置
// rewrite_enabled 恒缺省——旧实现据此误报"渡口纯透传"；新实现按 ActiveUpstream
// 条目交守卫裁决。
func TestCheckDockRewriteImplicitOn(t *testing.T) {
	// 新表形态：active=智谱（非本地、default 在）、rewrite_enabled 保持 false
	d := &config.DockCfg{
		Listen: "127.0.0.1:15722", Active: "智谱",
		Upstreams: map[string]config.DockUpstream{
			"智谱": {BaseURL: "https://open.bigmodel.cn/api/anthropic",
				ModelMap: map[string]string{"default": "glm-5.3"}},
		},
	}
	c := CheckDockRewrite(d)
	if !c.OK || !strings.Contains(c.Msg, "改写模式在位") {
		t.Fatalf("新表非本地上游应判改写在位（不得读废弃 rewrite_enabled）: %+v", c)
	}
	// 迁移回退条目：本地中转地址＝守卫强制透传——设计内，不判失败
	mig := &config.DockCfg{
		Listen: "127.0.0.1:15722", Active: "cc-switch",
		Upstreams: map[string]config.DockUpstream{
			"cc-switch": {BaseURL: "http://127.0.0.1:15721"},
		},
	}
	c = CheckDockRewrite(mig)
	if !c.OK || !strings.Contains(c.Msg, "透传") || !strings.Contains(c.Msg, "设计内") {
		t.Fatalf("本地回退条目应判设计内透传: %+v", c)
	}
	// nil dock＝渡口未配置
	c = CheckDockRewrite(nil)
	if !c.OK || !strings.Contains(c.Msg, "渡口未配置") {
		t.Fatalf("nil dock 应通过零行为: %+v", c)
	}
	// 表形态非本地缺 default → FAIL 点名
	bad := &config.DockCfg{
		Active: "x",
		Upstreams: map[string]config.DockUpstream{
			"x": {BaseURL: "https://x.example", ModelMap: map[string]string{}},
		},
	}
	c = CheckDockRewrite(bad)
	if c.OK || !strings.Contains(c.Msg, "default") {
		t.Fatalf("非本地缺 default 应 FAIL: %+v", c)
	}
	// 悬空 active → FAIL
	dangling := &config.DockCfg{Active: "ghost",
		Upstreams: map[string]config.DockUpstream{
			"real": {BaseURL: "https://r.example", ModelMap: map[string]string{"default": "m"}}}}
	c = CheckDockRewrite(dangling)
	if c.OK {
		t.Fatalf("悬空 active 应 FAIL: %+v", c)
	}
}

// ---- 票04：渡口上游检查组（CheckDockUpstreams） ----

// migratedDock 全量迁移产物形态（票01）：cc-switch 回退条目＋智谱/kimi/deepseek
// 三条未激活预置（key 留空），active=cc-switch（本地回退通道）。
func migratedDock() *config.DockCfg {
	return &config.DockCfg{
		Listen: "127.0.0.1:15722", Active: "cc-switch",
		Upstreams: map[string]config.DockUpstream{
			"cc-switch": {BaseURL: "http://127.0.0.1:15721"},
			"智谱": {BaseURL: "https://open.bigmodel.cn/api/anthropic",
				ModelMap: map[string]string{"default": "glm-5.3"}},
			"kimi": {BaseURL: "https://api.kimi.com/coding/",
				ModelMap: map[string]string{"default": "kimi-for-coding"}},
			"deepseek": {BaseURL: "https://api.deepseek.com/anthropic",
				ModelMap: map[string]string{"default": "deepseek-flash"}},
		},
	}
}

// TestCheckDockUpstreamsMigratedHealthy 迁移形态健康面：主判通过；三条未激活
// 预置逐条缺钥提示（pass 不判失败）；本地 cc-switch 条目豁免缺钥提示；active
// 非 deepseek 无边界提示。
func TestCheckDockUpstreamsMigratedHealthy(t *testing.T) {
	res := CheckDockUpstreams(migratedDock())
	byName := map[string]CheckResult{}
	for _, r := range res {
		byName[r.Name] = r
	}
	main, ok := byName["dock_upstream"]
	if !ok || main.Status != StatusPass {
		t.Fatalf("迁移形态应主判通过: %+v", res)
	}
	for _, n := range []string{"dock_upstream_key:智谱", "dock_upstream_key:kimi", "dock_upstream_key:deepseek"} {
		r, ok := byName[n]
		if !ok || r.Status != StatusPass || !strings.Contains(r.Detail, "未配置（手编 config 填 api_key）") {
			t.Fatalf("缺钥提示缺位或形态不对: %s → %+v", n, r)
		}
	}
	if _, ok := byName["dock_upstream_key:cc-switch"]; ok {
		t.Fatalf("本地中转条目不出站鉴权，不应出缺钥提示: %+v", res)
	}
	if _, ok := byName["dock_deepseek_boundary"]; ok {
		t.Fatalf("active 非 deepseek 不应出边界提示: %+v", res)
	}
}

// TestCheckDockUpstreamsMissingDefaultFails 非本地条目缺 default＝FAIL 点名；
// 本地条目（cc-switch 无 model_map）豁免——同 validateDockUpstreams 判据域。
func TestCheckDockUpstreamsMissingDefaultFails(t *testing.T) {
	d := migratedDock()
	x := d.Upstreams["kimi"]
	x.ModelMap = map[string]string{}
	d.Upstreams["kimi"] = x
	res := CheckDockUpstreams(d)
	if res[0].Name != "dock_upstream" || res[0].Status != StatusFail || !strings.Contains(res[0].Detail, "kimi") {
		t.Fatalf("非本地缺 default 应 FAIL 并点名: %+v", res[0])
	}
}

// TestCheckDockUpstreamsDanglingActiveFails active 悬空＝FAIL（可用条目列全）。
func TestCheckDockUpstreamsDanglingActiveFails(t *testing.T) {
	d := migratedDock()
	d.Active = "ghost"
	res := CheckDockUpstreams(d)
	if res[0].Status != StatusFail || !strings.Contains(res[0].Detail, "ghost") || !strings.Contains(res[0].Detail, "kimi") {
		t.Fatalf("悬空 active 应 FAIL 并列可用条目: %+v", res[0])
	}
}

// TestCheckDockUpstreamsDeepSeekBoundary active=deepseek 时输出官方边界（清单
// 逐 token 齐全）；手改名条目按端点命中同样出提示。
func TestCheckDockUpstreamsDeepSeekBoundary(t *testing.T) {
	d := migratedDock()
	d.Active = "deepseek"
	res := CheckDockUpstreams(d)
	var boundary *CheckResult
	for i := range res {
		if res[i].Name == "dock_deepseek_boundary" {
			boundary = &res[i]
		}
	}
	if boundary == nil || boundary.Status != StatusPass {
		t.Fatalf("active=deepseek 应出边界提示: %+v", res)
	}
	for _, tok := range []string{"document", "search_result", "redacted_thinking",
		"mcp_tool_use", "mcp_tool_result", "is_error", "disable_parallel_tool_use",
		"budget_tokens", "透传", "upstream use"} {
		if !strings.Contains(boundary.Detail, tok) {
			t.Fatalf("边界提示缺 %q: %s", tok, boundary.Detail)
		}
	}
	// 手改名条目按端点识别
	d2 := &config.DockCfg{Active: "ds", Upstreams: map[string]config.DockUpstream{
		"ds": {BaseURL: "https://api.deepseek.com/anthropic",
			ModelMap: map[string]string{"default": "deepseek-flash"}}}}
	res2 := CheckDockUpstreams(d2)
	found := false
	for _, r := range res2 {
		if r.Name == "dock_deepseek_boundary" {
			found = true
		}
	}
	if !found {
		t.Fatalf("端点命中也应出边界提示: %+v", res2)
	}
}

// TestCheckDockUpstreamsLegacyForm 旧单值形态（未迁移/迁移失败回退）＝合法回退
// 态：单项提示不判失败，含迁移反复失败异形排查面；deepseek 端点照出边界。
func TestCheckDockUpstreamsLegacyForm(t *testing.T) {
	legacy := &config.DockCfg{Listen: "127.0.0.1:15722",
		UpstreamBaseURL: "http://127.0.0.1:15721"} // 旧单值、无表、无 model_map
	res := CheckDockUpstreams(legacy)
	if len(res) != 1 || res[0].Name != "dock_upstream" || res[0].Status != StatusPass {
		t.Fatalf("旧单值形态应单项提示不判失败: %+v", res)
	}
	for _, tok := range []string{"未迁移", "异形"} {
		if !strings.Contains(res[0].Detail, tok) {
			t.Fatalf("迁移提示缺 %q: %s", tok, res[0].Detail)
		}
	}
	// 旧单值＋deepseek 端点 → 边界提示照出
	dsLegacy := &config.DockCfg{UpstreamBaseURL: "https://api.deepseek.com/anthropic"}
	res = CheckDockUpstreams(dsLegacy)
	if len(res) != 2 || res[1].Name != "dock_deepseek_boundary" {
		t.Fatalf("旧单值 deepseek 端点应出边界提示: %+v", res)
	}
}

// TestCheckDockUpstreamsMigrationRewriteHint 迁移条目非本地上游＝改写已隐含
// 开启的行为变化提示（评审低危备注）；本地回退条目（常态）不出提示。
func TestCheckDockUpstreamsMigrationRewriteHint(t *testing.T) {
	d := migratedDock()
	cc := d.Upstreams["cc-switch"]
	cc.BaseURL = "https://open.bigmodel.cn/api/anthropic" // 迁移条目非本地（旧直连形态）
	cc.ModelMap = map[string]string{"default": "glm-5.3"}
	d.Upstreams["cc-switch"] = cc
	res := CheckDockUpstreams(d)
	var hint *CheckResult
	for i := range res {
		if res[i].Name == "dock_upstream_rewrite_hint" {
			hint = &res[i]
		}
	}
	if hint == nil || hint.Status != StatusPass || !strings.Contains(hint.Detail, "隐含开启") {
		t.Fatalf("迁移条目非本地应出行为变化提示: %+v", res)
	}
	for _, r := range CheckDockUpstreams(migratedDock()) {
		if r.Name == "dock_upstream_rewrite_hint" {
			t.Fatalf("本地回退条目不应出行为变化提示: %+v", res)
		}
	}
}

// ---- 票04：doctor 装配缝——渡口上游检查组接线与人面输出 ----

// TestDoctorResultsDockGroupWiring cfg.Dock 在位时：dock_rewrite 后紧跟
// dock_upstream（组内主判在前），缺钥提示以 pass 语义进入结构化出口。
func TestDoctorResultsDockGroupWiring(t *testing.T) {
	deps, _ := greenDoctorDeps(t, func() map[string]any { return map[string]any{"health_alert": false} })
	cfg, _ := deps.LoadCfg()
	cfg.Dock = migratedDock()
	res := doctorResults(deps)
	names := make([]string, len(res))
	for i, r := range res {
		names[i] = r.Name
	}
	idx := -1
	for i, n := range names {
		if n == "dock_rewrite" {
			idx = i
		}
	}
	if idx < 0 || idx+1 >= len(names) || names[idx+1] != "dock_upstream" {
		t.Fatalf("dock 检查组接线不符（dock_rewrite 后应紧跟 dock_upstream）: %v", names)
	}
	for _, r := range res {
		if strings.HasPrefix(r.Name, "dock_upstream_key:") && r.Status != StatusPass {
			t.Fatalf("缺钥提示应 pass: %+v", r)
		}
	}
}

// TestRunDoctorDockHintsDoNotFail 人面/退出码：迁移形态（含未激活预置缺钥提示）
// 全绿退出 0、健康行与提示行以 [OK] 可见；缺 default 判 [FAIL] 并退出 1。
func TestRunDoctorDockHintsDoNotFail(t *testing.T) {
	deps, _ := greenDoctorDeps(t, func() map[string]any { return map[string]any{"health_alert": false} })
	cfg, _ := deps.LoadCfg()
	cfg.Dock = migratedDock()
	var out strings.Builder
	deps.Out = &out
	if code := runDoctor(deps); code != 0 {
		t.Fatalf("迁移形态（含未激活预置缺钥提示）应退出 0:\n%s", out.String())
	}
	got := out.String()
	if !strings.Contains(got, "[OK]   渡口上游 active=cc-switch 在位") {
		t.Fatalf("缺渡口上游健康行:\n%s", got)
	}
	if !strings.Contains(got, "未配置（手编 config 填 api_key）") {
		t.Fatalf("缺预置缺钥提示行:\n%s", got)
	}
	// 缺 default 判 FAIL：可见 + 退出 1
	bad := migratedDock()
	x := bad.Upstreams["deepseek"]
	x.ModelMap = map[string]string{}
	bad.Upstreams["deepseek"] = x
	cfg.Dock = bad
	var out2 strings.Builder
	deps.Out = &out2
	if code := runDoctor(deps); code != 1 {
		t.Fatalf("非本地缺 default 应退出 1:\n%s", out2.String())
	}
	if !strings.Contains(out2.String(), "[FAIL] 渡口上游配置有问题") {
		t.Fatalf("缺 default 失败行不可见:\n%s", out2.String())
	}
}

// ---- 票05：结构化出口（doctorResults / DoctorStructured / realStatsProbe 端口） ----

// notProdPort 结构化出口测试的端口验收钉子（生产端口全集：渡口双轨/上游/
// 守护/面板——测试一律临时口）。
func notProdPort(t *testing.T, port int) {
	t.Helper()
	for _, p := range []int{15721, 15722, 15724, 7311, 15900} {
		if port == p {
			t.Fatalf("测试撞生产端口 %d——换口", port)
		}
	}
}

// freeListenPort 绑 0 取空闲口即关（竞窗接受，测试夹具同 mcp 包惯例）。
func freeListenPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	notProdPort(t, port)
	return port
}

// statsTestServer 临时 /stats 端点（恒回 {}）；返回端口（非生产端口已断言）。
func statsTestServer(t *testing.T) int {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, "{}")
	}))
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatal(err)
	}
	notProdPort(t, port)
	return port
}

// TestDoctorResultsThreeFieldsAndOrder 结构化出口三要素：名称稳定、状态合法、
// 说明非空；项目顺序 = CLI 打印序（夹具 cfg.Dock 缺 → 无 dock_rewrite 项）。
func TestDoctorResultsThreeFieldsAndOrder(t *testing.T) {
	deps, _ := greenDoctorDeps(t, func() map[string]any { return map[string]any{"health_alert": false} })
	got := doctorResults(deps)
	if len(got) == 0 {
		t.Fatal("结构化结果为空")
	}
	legal := map[CheckStatus]bool{StatusPass: true, StatusFail: true, StatusNotChecked: true}
	want := []string{
		"cc_hooks", "launcher", "ccswitch_snapshots", "ferry_provider",
		"hook_script:ferryman-gate.ps1", "hook_script:ferryman-restore.ps1",
		"hook_script:ferryman-subagent.ps1", "hook_script:ferryman-ensure.ps1",
		"hook_script:ferryman-gate-codex.ps1", "hook_script:ferryman-restore-codex.ps1",
		"hook_script:ferryman-subagent-codex.ps1",
		"codex_hooks", "daemon_liveness", "autostart", "watchdog_task",
		"mcp_registration", // 票06：追加在末位（既有项顺序零漂移）
		"update_residues",  // 升级事务残留（规格 §C 第9条）：续接末位追加
	}
	if len(got) != len(want) {
		t.Fatalf("项数 = %d, want %d: %+v", len(got), len(want), got)
	}
	for i, r := range got {
		if r.Name != want[i] {
			t.Fatalf("第 %d 项名称 = %q, want %q（顺序=CLI 打印序）", i, r.Name, want[i])
		}
		if r.Detail == "" || !legal[r.Status] {
			t.Fatalf("三要素不齐: %+v", r)
		}
		if r.Status != StatusPass {
			t.Fatalf("全绿夹具应全 pass: %+v", r)
		}
	}
}

// TestDoctorResultsMatchCLILines 人面零漂移钉子：CLI 打印行与结构化结果一一
// 对应——tag 与 Status 同相（fail→[FAIL]、其余→[OK]），退出码随 fail 数。
func TestDoctorResultsMatchCLILines(t *testing.T) {
	deps, _ := greenDoctorDeps(t, func() map[string]any { return nil }) // daemon 死 → 必有 fail
	var out strings.Builder
	deps.Out = &out
	code := runDoctor(deps)
	got := doctorResults(deps) // 同 deps 再算一遍：CLI 面与结构化出口同源
	fails := 0
	for _, r := range got {
		wantTag := "[OK]   "
		if r.Status == StatusFail {
			wantTag = "[FAIL] "
			fails++
		}
		if !strings.Contains(out.String(), wantTag+r.Detail) {
			t.Fatalf("CLI 行与结构化结果不对应: %s%q\n输出:\n%s", wantTag, r.Detail, out.String())
		}
	}
	if fails == 0 {
		t.Fatal("夹具 daemon 死应至少一项 fail")
	}
	if code != 1 {
		t.Fatalf("有 fail 应退出 1, got %d", code)
	}
}

// TestRealStatsProbeUsesConfigPort 票05：daemon 活性目标经 config 解析的端口
// （7311 硬编码成历史）——活口真探通、死口真失败。
func TestRealStatsProbeUsesConfigPort(t *testing.T) {
	tmp := t.TempDir()
	dataDir := filepath.Join(tmp, "data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "daemon.token"), []byte("tok"), 0o600); err != nil {
		t.Fatal(err)
	}
	port := statsTestServer(t)
	if got := realStatsProbe(dataDir, port)(); got == nil {
		t.Fatalf("临时 daemon（端口 %d）应探活成功", port)
	}
	deadPort := freeListenPort(t)
	if got := realStatsProbe(dataDir, deadPort)(); got != nil {
		t.Fatalf("无人听的口 %d 应探活失败, got %v", deadPort, got)
	}
}

// TestDoctorStructuredTempTargets agent 面真实装配（DoctorStructured）：临时
// HOME/repo/config——用户目录相关检查面向临时目标（不读真实用户目录）；
// residency=false 时常驻保障两查显式 not_checked（零子进程/零注册表读）；
// daemon 活性目标经 cfg 解析——临时 daemon 在线 pass、离线 fail 且完整
// 结构化结果照常返回（不缩水、不是错误）。
func TestDoctorStructuredTempTargets(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")
	repo := filepath.Join(tmp, "repo") // 空 repo：脚本缺失 → 逐件 fail（确定性）
	dataDir := filepath.Join(home, "ferryman")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "daemon.token"), []byte("tok"), 0o600); err != nil {
		t.Fatal(err)
	}
	// providers 面向临时 config（cfgPath 参数）——绝不读真实 ~/ferryman/config.toml。
	cfgPath := filepath.Join(tmp, "config.toml")
	if err := os.WriteFile(cfgPath, []byte("[ferry]\nprovider = 'glm'\n"+
		"[providers.glm]\nbase_url = 'http://x'\nmodel = 'm'\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Server.DataDir = dataDir
	cfg.Server.Port = statsTestServer(t) // 在线：临时 /stats
	cfg.FerryProvider = "glm"

	res := DoctorStructured(home, repo, cfg, cfgPath, false)
	byName := map[string]CheckResult{}
	for _, r := range res {
		byName[r.Name] = r
	}
	if r := byName["daemon_liveness"]; r.Status != StatusPass {
		t.Fatalf("临时 daemon 在线（%d）活性应 pass: %+v", cfg.Server.Port, r)
	}
	if r := byName["ferry_provider"]; r.Status != StatusPass {
		t.Fatalf("providers 面向临时 config 应 pass: %+v", r)
	}
	if r := byName["ccswitch_snapshots"]; r.Status != StatusPass {
		t.Fatalf("未装 CC Switch 应跳过通过: %+v", r)
	}
	for _, n := range []string{"autostart", "watchdog_task"} {
		if r := byName[n]; r.Status != StatusNotChecked {
			t.Fatalf("%s 应 not_checked（residency=false 如实标注）: %+v", n, r)
		}
	}

	// 离线：换无人听的口——活性 fail，完整结构化结果照常（项数不缩水）。
	cfg.Server.Port = freeListenPort(t)
	res2 := DoctorStructured(home, repo, cfg, cfgPath, false)
	if len(res2) == 0 || len(res2) != len(res) {
		t.Fatalf("离线应返回完整结构化结果（%d 项）, got %d", len(res), len(res2))
	}
	byName2 := map[string]CheckResult{}
	for _, r := range res2 {
		byName2[r.Name] = r
	}
	if r := byName2["daemon_liveness"]; r.Status != StatusFail {
		t.Fatalf("daemon 离线活性应 fail: %+v", r)
	}
}

// TestDoctorSameModelHeatTTLMissing 终局修复(防静默死档,终局评审普通建议):
// same_model 开而 [heartbeat].ttl_s 未设(=0)→ ferry.PredictHot 恒判冷,同模型
// 档永不触发——fail 行点名修法;已设 → pass;same_model 关 → 零新增行。
func TestDoctorSameModelHeatTTLMissing(t *testing.T) {
	mk := func(enabled bool, ttls float64) doctorDeps {
		cfg := config.Default()
		cfg.FerryProvider = "glm"
		cfg.SameModel.Enabled = enabled
		cfg.SameModel.Upstreams = []string{"glm"}
		cfg.Heartbeat.TTLS = ttls
		return doctorDeps{
			LoadCfg: func() (*config.Config, error) { return cfg, nil },
			LoadProviders: func() (map[string]ferry.Provider, error) {
				return map[string]ferry.Provider{"glm": {Name: "glm", BaseURL: "http://x", Model: "m"}}, nil
			},
			Probe: func() map[string]any { return map[string]any{"health_alert": false} },
		}
	}
	find := func(rs []CheckResult) *CheckResult {
		for i := range rs {
			if rs[i].Name == "same_model_heat_ttl" {
				return &rs[i]
			}
		}
		return nil
	}
	// 未设:fail,点名"恒冷"与填 ttl_s 修法。
	if r := find(doctorResults(mk(true, 0))); r == nil || r.Status != StatusFail ||
		!strings.Contains(r.Detail, "恒冷") || !strings.Contains(r.Detail, "ttl_s") {
		t.Fatalf("ttl_s 未设应 fail 并点名修法: %+v", r)
	}
	// 已设:pass。
	if r := find(doctorResults(mk(true, 600))); r == nil || r.Status != StatusPass {
		t.Fatalf("ttl_s 已设应 pass: %+v", r)
	}
	// same_model 关:零新增行(存量用户 doctor 输出零漂移)。
	if r := find(doctorResults(mk(false, 0))); r != nil {
		t.Fatalf("same_model 关闭不应出判热 TTL 行: %+v", r)
	}
}
