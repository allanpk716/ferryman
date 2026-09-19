// doctor_test.go — 票20：doctor 体检验收（规格 tests/test_doctor.py 1:1）
// + C12 闸门缺位提示不失败用例 + 附录#14 HttpBeatSender 声明输出用例
// + runDoctor 聚合（退出码/结论行）用例。

package installer

import (
	"fmt"
	"os"
	"path/filepath"
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
		Out:   nil, // 调用方填
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
	want := fmt.Sprintf("体检结论: %d/%d 通过", 1+1+1+1+len(doctorScriptNames())+1+1,
		1+1+1+1+len(doctorScriptNames())+1+1)
	if !strings.Contains(got, want) {
		t.Fatalf("结论计数不符:\nwant: %s\ngot:\n%s", want, got)
	}
}
