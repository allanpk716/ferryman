// install_test.go — 票20：install 移植验收（规格 tests/test_install.py 1:1）
// + ensure_launcher 语义覆盖（票15 缓交回填：脚本内容/exe/日志重定向/幂等）。
//
// 密闭性：settings/hooks/config/dataDir 全部指临时目录，绝不碰本机真实配置。

package installer

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// captureStdout fmt 打印面捕获（Python capsys 同位；守护包同款实现）。
func captureStdout(t *testing.T) (read func() string) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	done := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
		_ = r.Close()
	}()
	return func() string {
		_ = w.Close()
		os.Stdout = old
		return <-done
	}
}

// writeJSONFile 以 ensure_ascii=False 语义落盘 JSON。
func writeJSONFile(t *testing.T, path string, v any) string {
	t.Helper()
	if err := os.WriteFile(path, dumpJSON(v), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// orcaSettings 有 Orca 既有条目的 settings.json（test_install.py _orca_settings 同形）。
func orcaSettings(t *testing.T, dir string) string {
	t.Helper()
	p := filepath.Join(dir, "settings.json")
	writeJSONFile(t, p, map[string]any{"hooks": map[string]any{
		"UserPromptSubmit": []any{map[string]any{"hooks": []any{
			map[string]any{"type": "command", "command": "orca-hook.cmd", "timeout": 10},
		}}},
	}})
	return p
}

// parseFile 读回 JSON（断言前的一致性辅助）。
func parseFile(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("%s 解析失败: %v", path, err)
	}
	return v
}

func hooksOf(t *testing.T, data map[string]any) map[string]any {
	t.Helper()
	hooks, _ := data["hooks"].(map[string]any)
	if hooks == nil {
		t.Fatal("hooks 缺失")
	}
	return hooks
}

// ferryCount 事件数组里条目 JSON 含 "ferryman" 的个数。
func ferryCount(t *testing.T, hooks map[string]any, evt string) int {
	t.Helper()
	n := 0
	for _, e := range asList(hooks[evt]) {
		if strings.Contains(marshalCompact(e), "ferryman") {
			n++
		}
	}
	return n
}

// compactOf marshalCompact 包一层（断言里反复用）。
func compactOf(t *testing.T, v any) string {
	t.Helper()
	return marshalCompact(v)
}

// ---- test_install_appends_keeps_orca_and_is_idempotent ----

func TestInstallAppendsKeepsOrcaAndIsIdempotent(t *testing.T) {
	tmp := t.TempDir()
	p := orcaSettings(t, tmp)
	// 密闭性：显式指向不存在的 cc-switch.db，绝不碰本机真实 CC Switch 库
	noDB := filepath.Join(tmp, "no-ccswitch.db")
	repo := filepath.Join(tmp, "repo") // 钩子路径嵌入用（本测不查存在性）
	dataDir := filepath.Join(tmp, "data")

	read := captureStdout(t)
	InstallCC(p, noDB, dataDir, repo, nil)
	read()

	data := parseFile(t, p)
	hooks := hooksOf(t, data)

	ups := asList(hooks["UserPromptSubmit"])
	anyOrca := false
	for _, e := range ups {
		if strings.Contains(compactOf(t, e), "orca-hook.cmd") {
			anyOrca = true
		}
	}
	if !anyOrca {
		t.Fatal("Orca 既有条目被删")
	}
	if n := ferryCount(t, hooks, "UserPromptSubmit"); n != 1 {
		t.Fatalf("UPS ferryman 条目数 = %d, want 1", n)
	}
	ferryUPS := ""
	for _, e := range ups {
		blob := compactOf(t, e)
		if strings.Contains(blob, "ferryman") {
			ferryUPS = blob
		}
	}
	if !strings.Contains(ferryUPS, "ferryman-gate.ps1") {
		t.Fatalf("UPS ferryman 条目不含 ferryman-gate.ps1: %s", ferryUPS)
	}
	entry := firstMap(t, asList(hooks["UserPromptSubmit"]), "ferryman")
	timeout, _ := firstMap(t, asList(entry["hooks"]), "")["timeout"].(float64)
	if timeout != 3 {
		t.Fatalf("UPS timeout = %v, want 3", timeout)
	}

	ss := asList(hooks["SessionStart"])
	if len(ss) == 0 {
		t.Fatal("SessionStart 缺失")
	}
	if m, _ := ss[0].(map[string]any)["matcher"].(string); m != "clear|startup" {
		t.Fatalf("SessionStart matcher = %v, want clear|startup", m)
	}

	// T32：子代理生命周期钩子（SubagentStart/Stop → daemon 计数）
	for _, evt := range []string{"SubagentStart", "SubagentStop"} {
		if n := ferryCount(t, hooks, evt); n != 1 {
			t.Fatalf("%s ferryman 条目数 = %d, want 1", evt, n)
		}
		blob := compactOf(t, firstMap(t, asList(hooks[evt]), "ferryman"))
		if !strings.Contains(blob, "ferryman-subagent.ps1") {
			t.Fatalf("%s 不含 ferryman-subagent.ps1: %s", evt, blob)
		}
	}

	read = captureStdout(t)
	InstallCC(p, noDB, dataDir, repo, nil) // 幂等：ferryman 条目不重复
	read()
	data2 := parseFile(t, p)
	ups2 := asList(hooksOf(t, data2)["UserPromptSubmit"])
	if n := ferryCount(t, hooksOf(t, data2), "UserPromptSubmit"); n != 1 {
		t.Fatalf("幂等后 UPS ferryman 条目数 = %d, want 1", n)
	}
	anyOrca = false
	for _, e := range ups2 {
		if strings.Contains(compactOf(t, e), "orca-hook.cmd") {
			anyOrca = true
		}
	}
	if !anyOrca {
		t.Fatal("幂等后 Orca 仍在——被误删")
	}

	// 备份存在（同秒内两次安装会覆盖同名备份，故只断言 ≥1）
	baks, err := filepath.Glob(filepath.Join(tmp, "settings.json.bak-ferryman-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(baks) < 1 {
		t.Fatal("备份未生成")
	}
}

// firstMap 列表里第一个满足谓词（blob 含 needle；needle 空 = 首个）的 map。
func firstMap(t *testing.T, list []any, needle string) map[string]any {
	t.Helper()
	for _, e := range list {
		m, ok := e.(map[string]any)
		if !ok {
			continue
		}
		if needle == "" || strings.Contains(compactOf(t, m), needle) {
			return m
		}
	}
	t.Fatalf("未找到含 %q 的条目", needle)
	return nil
}

// ---- T36 · install-codex：hooks.json 注入 + 功能旗标 ----

func TestInstallCodexMergesAndEnablesFeature(t *testing.T) {
	tmp := t.TempDir()
	hooks := filepath.Join(tmp, "hooks.json")
	writeJSONFile(t, hooks, map[string]any{"hooks": map[string]any{
		"Stop": []any{map[string]any{"hooks": []any{
			map[string]any{"type": "command", "command": "orca.cmd", "timeout": 10},
		}}},
	}})
	cfg := filepath.Join(tmp, "config.toml")
	if err := os.WriteFile(cfg, []byte("[features]\ngoals = true\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	read := captureStdout(t)
	n := InstallCodex(hooks, cfg, tmp, nil)
	read()
	if n != 4 { // gate + restore + subagent×2
		t.Fatalf("InstallCodex 返回 %d, want 4", n)
	}

	data := parseFile(t, hooks)
	dHooks := hooksOf(t, data)
	if n := ferryCount(t, dHooks, "UserPromptSubmit"); n != 1 {
		t.Fatalf("UPS ferryman 条目数 = %d, want 1", n)
	}
	if blob := compactOf(t, firstMap(t, asList(dHooks["UserPromptSubmit"]), "ferryman")); !strings.Contains(blob, "ferryman-gate-codex.ps1") {
		t.Fatalf("UPS 不含 ferryman-gate-codex.ps1: %s", blob)
	}
	anyOrca := false
	for _, e := range asList(dHooks["Stop"]) {
		if strings.Contains(compactOf(t, e), "orca.cmd") {
			anyOrca = true
		}
	}
	if !anyOrca { // Orca 保留
		t.Fatal("Stop 的 Orca 条目被删")
	}
	ss := firstMap(t, asList(dHooks["SessionStart"]), "ferryman")
	if tv, _ := firstMap(t, asList(ss["hooks"]), "")["timeout"].(float64); tv != 10 { // 自举等待预算
		t.Fatalf("SessionStart timeout = %v, want 10", tv)
	}
	// 票22 骑手 M1：Python install.py 的 Codex 侧 SessionStart 无 matcher 键
	//（matcher 是 CC 侧 resume/compact 过滤专属），多写已删——断言防复发
	if _, has := ss["matcher"]; has {
		t.Fatalf("Codex SessionStart 不应带 matcher（Python 无此键）: %s", compactOf(t, ss))
	}
	// 子代理生命周期（subagent 钩子 session_id = 父会话 id，官方文档）
	for _, evt := range []string{"SubagentStart", "SubagentStop"} {
		sub := firstMap(t, asList(dHooks[evt]), "ferryman")
		if !strings.Contains(compactOf(t, sub), "ferryman-subagent-codex.ps1") {
			t.Fatalf("%s 不含 ferryman-subagent-codex.ps1", evt)
		}
	}
	// 无控制字符（\a→BEL / \f→FF 事故的回归防线）
	raw, err := os.ReadFile(hooks)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.ContainsRune(raw, 0x07) || bytes.ContainsRune(raw, 0x0c) {
		t.Fatal("hooks.json 含控制字符（\\a / \\f 事故回归）")
	}

	toml, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(toml), "hooks = true") { // 钩子默认关，必须开旗标
		t.Fatal("config.toml 未开 hooks = true")
	}
}

func TestInstallCodexIdempotentAndCreatesMissing(t *testing.T) {
	tmp := t.TempDir()
	hooks := filepath.Join(tmp, "hooks.json") // 不存在 → 创建
	cfg := filepath.Join(tmp, "config.toml")  // 无 [features] 段 → 追加
	read := captureStdout(t)
	InstallCodex(hooks, cfg, tmp, nil)
	InstallCodex(hooks, cfg, tmp, nil) // 幂等
	read()
	data := parseFile(t, hooks)
	if n := ferryCount(t, hooksOf(t, data), "UserPromptSubmit"); n != 1 {
		t.Fatalf("幂等后 UPS ferryman 条目数 = %d, want 1", n)
	}
	toml, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(toml), "hooks = true"); n != 1 {
		t.Fatalf("hooks = true 出现 %d 次, want 1", n)
	}
}

// ---- 防线：既有 hooks 值非对象 → 响亮拒绝（Python AttributeError 崩的同位；
// 静默覆盖 = 数据丢失） ----

func TestInstallCCRejectsNonObjectHooks(t *testing.T) {
	tmp := t.TempDir()
	p := filepath.Join(tmp, "settings.json")
	if err := os.WriteFile(p, []byte(`{"hooks": "not-an-object"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	read := captureStdout(t)
	code := InstallCC(p, filepath.Join(tmp, "no.db"), filepath.Join(tmp, "data"), tmp, nil)
	out := read()
	if code != 1 {
		t.Fatalf("非对象 hooks 应拒绝（退出 1）, got %d", code)
	}
	if !strings.Contains(out, "拒绝改写") {
		t.Fatalf("缺拒绝文案: %s", out)
	}
}

func TestInstallCodexRejectsNonObjectHooks(t *testing.T) {
	tmp := t.TempDir()
	hooks := filepath.Join(tmp, "hooks.json")
	if err := os.WriteFile(hooks, []byte(`{"hooks": []}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(tmp, "config.toml")
	read := captureStdout(t)
	code := InstallCodex(hooks, cfg, tmp, nil)
	read()
	if code != 1 {
		t.Fatalf("非对象 hooks 应拒绝（退出 1）, got %d", code)
	}
}

// ---- 子集安装（C12：切换日三类；评审附录#1 两侧同款） ----

func TestInstallSubsetOnlyWritesRequestedEvents(t *testing.T) {
	tmp := t.TempDir()
	subset := []string{"SessionStart", "SubagentStart", "SubagentStop"}

	// CC 侧：装完 settings.json 的 ferryman 钩子只含三事件（闸门 UserPromptSubmit
	// 不装——Orca 等既有条目原样保留）
	p := orcaSettings(t, tmp)
	read := captureStdout(t)
	InstallCC(p, filepath.Join(tmp, "no.db"), filepath.Join(tmp, "data"), tmp, subset)
	read()
	hooks := hooksOf(t, parseFile(t, p))
	if n := ferryCount(t, hooks, "UserPromptSubmit"); n != 0 {
		t.Fatal("子集安装不应写入 UserPromptSubmit 的 ferryman 条目")
	}
	for _, evt := range subset {
		if n := ferryCount(t, hooks, evt); n != 1 {
			t.Fatalf("子集缺事件 %s（条目数 %d）", evt, n)
		}
	}

	// Codex 侧：同款子集（评审附录#1）——hooks.json 全新建，键只有三事件
	codexHooks := filepath.Join(tmp, "hooks.json")
	codexCfg := filepath.Join(tmp, "config.toml")
	read = captureStdout(t)
	InstallCodex(codexHooks, codexCfg, tmp, subset)
	read()
	cData := hooksOf(t, parseFile(t, codexHooks))
	if _, ok := cData["UserPromptSubmit"]; ok {
		t.Fatal("Codex 子集安装不应写入 UserPromptSubmit")
	}
	for _, evt := range subset {
		if n := ferryCount(t, cData, evt); n != 1 {
			t.Fatalf("Codex 子集缺事件 %s", evt)
		}
	}

	// FerryHookEntries 直查：空 = 全集
	if got := len(FerryHookEntries(tmp, nil)); got != 4 {
		t.Fatalf("全集事件数 = %d, want 4", got)
	}
	if got := FerryHookEntries(tmp, subset); len(got) != 3 {
		t.Fatalf("子集事件数 = %d, want 3", len(got))
	}
}

// ---- ensure_launcher 语义覆盖（票15 缓交回填，Go 新形态） ----

func TestEnsureLauncherContent(t *testing.T) {
	tmp := t.TempDir()
	dataDir := filepath.Join(tmp, "data")
	exe := filepath.Join(tmp, "ferryman.exe")
	if err := os.WriteFile(exe, []byte("MZ"), 0o644); err != nil {
		t.Fatal(err)
	}

	read := captureStdout(t)
	got, err := EnsureLauncher(dataDir, exe)
	if err != nil {
		t.Fatalf("EnsureLauncher 不应失败: %v", err)
	}
	read()

	if want := filepath.Join(dataDir, LauncherName); got != want {
		t.Fatalf("launcher 路径 = %s, want %s", got, want)
	}
	raw, err := os.ReadFile(got)
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	// 裸形态启动行（票22 骑手1/M6）：`"<exe>" serve >> out 2>> err`——
	// 无 start/min；重定向绑定守护进程，日志真落盘
	if strings.Contains(body, "start ") {
		t.Fatalf("点火脚本不应再有 start /min（裸形态）: %s", body)
	}
	wantStart := fmt.Sprintf(`"%s" serve >> "%s" 2>> "%s"`, exe,
		filepath.Join(dataDir, "serve.out.log"),
		filepath.Join(dataDir, "serve.err.log"))
	if !strings.Contains(body, wantStart) {
		t.Fatalf("裸形态启动行缺失:\nwant: %s\ngot:  %s", wantStart, body)
	}
	for _, logName := range []string{"serve.out.log", "serve.err.log"} {
		if !strings.Contains(body, filepath.Join(dataDir, logName)) {
			t.Fatalf("日志重定向缺失: %s", logName)
		}
	}
	// rem 注释改 ASCII 英文（终局评审 B，对齐 rollback 模板铁律）+ 全文 ASCII
	// 不变量：点火脚本会在非 UTF-8 代码页控制台下被重解码，任何非 ASCII 字节
	// （含占位路径里的）都可能破坏 rem 记号/行结构——ASCII 对任意代码页无条件安全
	if !strings.Contains(body, "rem Ferryman daemon launcher (auto-generated by install-cc, do not edit)") {
		t.Fatal("rem 注释行缺失或非 ASCII 形")
	}
	for i, b := range raw {
		if b >= 0x80 {
			t.Fatalf("点火脚本含非 ASCII 字节 @%d: %q", i, raw[max(0, i-10):min(len(raw), i+10)])
		}
	}
	// CRLF 行尾（win 分支逐字）：每个 \n 都有前置 \r
	if !strings.Contains(body, "\r\n") {
		t.Fatal("点火脚本非 CRLF 行尾")
	}
	if n := strings.Count(body, "\n"); n != strings.Count(body, "\r\n") || n == 0 {
		t.Fatalf("行尾混入裸 LF（\\n=%d, \\r\\n=%d）", n, strings.Count(body, "\r\n"))
	}
	if !strings.HasPrefix(body, "@echo off\r\n") {
		t.Fatal("首行须为 @echo off")
	}
}

// ---- 票22 骑手 M3：写盘失败响亮返回 error，不报"就绪" ----

func TestEnsureLauncherWriteFailureLoud(t *testing.T) {
	tmp := t.TempDir()
	// 用文件占住 dataDir 名字 → MkdirAll 必败
	blocker := filepath.Join(tmp, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	read := captureStdout(t)
	got, err := EnsureLauncher(blocker, filepath.Join(tmp, "ferryman.exe"))
	out := read()
	if err == nil {
		t.Fatalf("写盘失败应响亮返回 error, got 路径 %q", got)
	}
	if strings.Contains(out, "就绪") {
		t.Fatalf("失败时不得报『就绪』: %s", out)
	}
}

func TestInstallCCLauncherFailureAborts(t *testing.T) {
	// 点火脚本写不出 → InstallCC 响亮失败（退出 1），不继续装钩子
	tmp := t.TempDir()
	blocker := filepath.Join(tmp, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(tmp, "settings.json")
	if err := os.WriteFile(p, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	read := captureStdout(t)
	code := InstallCC(p, filepath.Join(tmp, "no.db"), blocker, tmp, nil)
	out := read()
	if code != 1 {
		t.Fatalf("点火脚本失败应退出 1, got %d", code)
	}
	if strings.Contains(out, "已追加") {
		t.Fatal("点火脚本失败后不应继续装钩子")
	}
}

func TestEnsureLauncherIdempotent(t *testing.T) {
	tmp := t.TempDir()
	dataDir := filepath.Join(tmp, "data")
	exe := filepath.Join(tmp, "ferryman.exe")
	if err := os.WriteFile(exe, []byte("MZ"), 0o644); err != nil {
		t.Fatal(err)
	}
	read := captureStdout(t)
	if _, err := EnsureLauncher(dataDir, exe); err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(filepath.Join(dataDir, LauncherName))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = EnsureLauncher(dataDir, exe) // 重跑：内容不变（幂等）
	read()
	second, err := os.ReadFile(filepath.Join(dataDir, LauncherName))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("重跑 EnsureLauncher 内容变了（幂等破坏）")
	}
}
