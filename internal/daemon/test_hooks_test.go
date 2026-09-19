package daemon

// test_hooks_test.go — 票21：tests/test_hooks.py 16 例 1:1 移植。真跑
// powershell 子进程执行 worktree 根下 hooks/*.ps1（-NoProfile
// -ExecutionPolicy Bypass），stdin 喂 UTF-8 JSON，断言 exit code 与 stdout。
// 守护用 integHarness（integration_test.go 的守望+工人+HTTP 全装配）监听
// 临时端口；钩子经 FERRYMAN_PORT / FERRYMAN_TOKEN_FILE 环境变量指向它——
// hooks/*.ps1 均读这两个变量（默认 7311 ~/ferryman/daemon.token），测试
// 从不落写死端口，无 7311 冲突面。仅 Windows 执行，其余 t.Skip。
//
// 语义以 tests/test_hooks.py 实测为准：fail-open（守护关/401/超时→exit 0
// 且零输出）；block 分支 exit 0 + stdout 纯 JSON（decision/reason/
// suppressOriginalPrompt=true，不带 hookSpecificOutput——block 走 JSON 决策
// 通道而非 exit 2 码）。
//
// 16 例对照（Python → Go）：
//   test_t16_gate_block_json_contract                 → TestT16GateBlockJSONContract
//   test_t16_gate_failopen_on_401                     → TestT16GateFailopenOn401
//   test_t16_gate_failopen_daemon_down                → TestT16GateFailopenDaemonDown
//   test_t16_gate_env_disable_short_circuits          → TestT16GateEnvDisableShortCircuits
//   test_t17_restore_skips_resume                     → TestT17RestoreSkipsResume
//   test_t17_restore_injects_on_clear                 → TestT17RestoreInjectsOnClear
//   test_t17_restore_failopen_daemon_down             → TestT17RestoreFailopenDaemonDown
//   test_t32_subagent_hook_roundtrip                  → TestT32SubagentHookRoundtrip
//   test_t32_subagent_hook_failopen_daemon_down       → TestT32SubagentHookFailopenDaemonDown
//   test_t32_subagent_hook_env_disable_short_circuits → TestT32SubagentHookEnvDisableShortCircuits
//   test_t23_codex_gate_hook_blocks_idle              → TestT23CodexGateHookBlocksIdle
//   test_t23_codex_gate_hook_failopen_daemon_down     → TestT23CodexGateHookFailopenDaemonDown
//   test_t23_codex_restore_hook_filters_source        → TestT23CodexRestoreHookFiltersSource
//   test_t23_codex_restore_hook_failopen_daemon_down  → TestT23CodexRestoreHookFailopenDaemonDown
//   test_t37_codex_subagent_hook_roundtrip            → TestT37CodexSubagentHookRoundtrip
//   test_t37_codex_subagent_hook_ignores_other_events → TestT37CodexSubagentHookIgnoresOtherEvents

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"ferryman/internal/config"
	"ferryman/internal/store"
)

// ---- 测试基建（tests/test_hooks.py 顶部工具的 Go 形） ----

// hooksRoot 定位 worktree 根下的 hooks/（go test 的工作目录 = 包源码目录，
// internal/daemon → ../../hooks）；非 Windows 或目录缺失即跳过。
func hooksRoot(t *testing.T) string {
	t.Helper()
	if runtime.GOOS != "windows" {
		t.Skip("hooks PS1 端到端仅 Windows（powershell 子进程）")
	}
	dir, err := filepath.Abs(filepath.Join("..", "..", "hooks"))
	if err != nil {
		t.Skipf("hooks 目录定位失败: %v", err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Skipf("hooks 目录不存在（需在仓库 worktree 内运行）: %s", dir)
	}
	return dir
}

// hookEnv PowerShell 子进程环境：os.Environ() 基底滤掉 FERRYMAN_*（开发机
// shell 的真 Ferryman 配置不得串场——FERRYMAN_DISABLE=1 会让 fail-open 用例
// 走错路径还装作绿）与 USERPROFILE（重定向到空临时目录：ferryman-ensure.ps1
// 在守护不在场时找不到 ~/ferryman/start-daemon.cmd 即放弃——测试期间绝不
// 拉起真机守护；gate/restore-codex 的 hook-debug ON 标记也不会命中真机
// 路径），再叠用例覆盖（FERRYMAN_PORT/TOKEN_FILE/DISABLE…）。
func hookEnv(t *testing.T, overrides map[string]string) []string {
	t.Helper()
	profile := filepath.Join(t.TempDir(), "psprofile")
	if err := os.MkdirAll(profile, 0o755); err != nil {
		t.Fatal(err)
	}
	var env []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if k == "USERPROFILE" || strings.HasPrefix(strings.ToUpper(k), "FERRYMAN_") {
			continue
		}
		env = append(env, kv)
	}
	env = append(env, "USERPROFILE="+profile)
	for k, v := range overrides {
		env = append(env, k+"="+v)
	}
	return env
}

// runPS _run_ps 的 Go 形：powershell 子进程跑 hooks/<script>，stdin 喂 UTF-8
// JSON（ensure_ascii=False 同形：Encoder 关 HTML 转义），返回 (exit code,
// stdout)。60s 超时（Python timeout=30 上取整——PS 冷启动 + ensure 自举 +
// REST 2s 的余量）；非 ExitError（启动失败/超时）按用例失败处理。stderr 仅
// 掺进失败信息（Python capture 但不断言，同形）。
func runPS(t *testing.T, script string, stdin map[string]any, env map[string]string) (int, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "powershell", "-NoProfile", "-ExecutionPolicy", "Bypass",
		"-File", filepath.Join(hooksRoot(t), script))
	cmd.Env = hookEnv(t, env)
	if stdin != nil {
		var in bytes.Buffer
		enc := json.NewEncoder(&in)
		enc.SetEscapeHTML(false)
		if err := enc.Encode(stdin); err != nil {
			t.Fatal(err)
		}
		cmd.Stdin = &in
	}
	var out, errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	err := cmd.Run()
	if err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("powershell 运行失败: %v\nstderr: %s", err, errOut.String())
		}
		return ee.ExitCode(), out.String()
	}
	return 0, out.String()
}

// liveEnv _gate_env 的 Go 形：指向 integHarness 的临时守护。
func liveEnv(t *testing.T, h *integHarness) map[string]string {
	t.Helper()
	return map[string]string{
		"FERRYMAN_PORT":       strconv.Itoa(h.port),
		"FERRYMAN_TOKEN_FILE": filepath.Join(h.tmp, "data", "daemon.token"),
	}
}

// downEnv 守护不在场用例的环境（free_port + 不存在的 token 文件，1:1）。
func downEnv(t *testing.T) map[string]string {
	t.Helper()
	return map[string]string{
		"FERRYMAN_PORT":       strconv.Itoa(freePort(t)),
		"FERRYMAN_TOKEN_FILE": "C:/nonexistent.token",
	}
}

// assertSilentOK fail-open 断言（Python `r.returncode == 0 and
// not r.stdout.strip()`）：exit 0 且 stdout 零输出。
func assertSilentOK(t *testing.T, phase string, code int, stdout string) {
	t.Helper()
	if code != 0 {
		t.Fatalf("%s: exit=%d, want 0（fail-open 放行）", phase, code)
	}
	if strings.TrimSpace(stdout) != "" {
		t.Fatalf("%s: stdout 非空: %q（fail-open 零输出）", phase, stdout)
	}
}

// gateBlockPoll Python 两例 block 用例的同构轮询：未到拦截阈值（block_s=2s）
// 或交接未就绪时守护先放行（钩子空 stdout），每秒重试至 15s 死线；出输出
// 即停（Python 同款）。
func gateBlockPoll(t *testing.T, script string, body map[string]any, env map[string]string) (int, string) {
	t.Helper()
	var code int
	var out string
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		code, out = runPS(t, script, body, env)
		if strings.TrimSpace(out) != "" {
			return code, out
		}
		time.Sleep(1 * time.Second) // 未到拦截阈值先放行(空输出)
	}
	t.Fatalf("%s 15s 内未产出 block JSON（拦截阈值或交接未就绪；末次 exit=%d stdout=%q）",
		script, code, out)
	return code, out
}

// parseHookJSON stdout 必须是纯 JSON（Python json.loads(out) 同位）。
func parseHookJSON(t *testing.T, out string) map[string]any {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("stdout 必须是纯 JSON: %v (%q)", err, out)
	}
	return payload
}

// writeHookRollout tests/test_hooks.py::_write_rollout 1:1：合成 rollout
// <dir>/2026/09/16/rollout-2026-09-16T12-00-00-<uuid>.jsonl（session_meta +
// token_count 轮次，input 2000 ≥ 测试阈值 1000；mtime=now 供守望判定）。
func writeHookRollout(t *testing.T, codexDir, uuid, cwd string) string {
	t.Helper()
	d := filepath.Join(codexDir, "2026", "09", "16")
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	f := filepath.Join(d, "rollout-2026-09-16T12-00-00-"+uuid+".jsonl")
	j := func(v any) string {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	lines := []string{
		j(map[string]any{"type": "session_meta", "timestamp": "2026-09-16T12:00:00.000Z",
			"payload": map[string]any{"session_id": uuid, "cwd": cwd,
				"cli_version": "0.153.0"}}),
		j(map[string]any{"type": "event_msg", "timestamp": "2026-09-16T12:00:01.000Z",
			"payload": map[string]any{"type": "token_count",
				"info": map[string]any{"last_token_usage": map[string]any{
					"input_tokens": 2000, "cached_input_tokens": 100,
					"cached_omitted_tokens": 0, "output_tokens": 5}}}}),
	}
	if err := os.WriteFile(f, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	now := time.Now() // os.utime(f, None) → mtime=now
	if err := os.Chtimes(f, now, now); err != nil {
		t.Fatal(err)
	}
	return f
}

// ---- T16 gate 钩子 ----

func TestT16GateBlockJSONContract(t *testing.T) {
	h := newIntegHarness(t, succFerry)
	proj := filepath.Join(h.tmp, "proj")
	sid := "hook-0001"
	f := writeIntegSession(t, h.projects, sid, proj, 2000)
	waitForCond(t, 15*time.Second, func() bool {
		return h.handoffInStore("cc", proj, func(e *store.Entry) bool { return e.SessionID == sid })
	})
	body := map[string]any{"session_id": sid, "transcript_path": f, "cwd": proj,
		"prompt": "被拦的原话", "source": "user"}
	code, out := gateBlockPoll(t, "ferryman-gate.ps1", body, liveEnv(t, h))
	if code != 0 { // block 走 stdout JSON 通道，exit 仍 0（test_hooks.py 实测）
		t.Fatalf("exit=%d, want 0", code)
	}
	payload := parseHookJSON(t, out)
	if payload["decision"] != "block" {
		t.Fatalf("decision = %v, want block", payload["decision"])
	}
	if payload["suppressOriginalPrompt"] != true {
		t.Fatalf("suppressOriginalPrompt = %v, want true", payload["suppressOriginalPrompt"])
	}
	if rsn, _ := payload["reason"].(string); !strings.Contains(rsn, "交接") {
		t.Fatalf("reason 缺「交接」: %q", rsn)
	}
	if _, has := payload["hookSpecificOutput"]; has { // block 不走注入通道
		t.Fatalf("block 响应不应有 hookSpecificOutput: %v", payload)
	}
}

func TestT16GateFailopenOn401(t *testing.T) {
	h := newIntegHarness(t, succFerry)
	proj := filepath.Join(h.tmp, "proj")
	sid := "hook-0002"
	f := writeIntegSession(t, h.projects, sid, proj, 2000)
	badToken := filepath.Join(h.tmp, "bad.token")
	if err := os.WriteFile(badToken, []byte("wrong-token"), 0o644); err != nil {
		t.Fatal(err)
	}
	body := map[string]any{"session_id": sid, "transcript_path": f, "cwd": proj,
		"prompt": "x", "source": "user"}
	env := liveEnv(t, h)
	env["FERRYMAN_TOKEN_FILE"] = badToken
	code, out := runPS(t, "ferryman-gate.ps1", body, env)
	assertSilentOK(t, "401", code, out) // 401 → 放行且零输出
}

func TestT16GateFailopenDaemonDown(t *testing.T) {
	body := map[string]any{"session_id": "s", "transcript_path": "C:/x.jsonl",
		"cwd": "C:/x", "prompt": "hi"}
	code, out := runPS(t, "ferryman-gate.ps1", body, downEnv(t))
	assertSilentOK(t, "守护关", code, out) // 连接拒绝/文件缺失 → 放行
}

func TestT16GateEnvDisableShortCircuits(t *testing.T) {
	env := downEnv(t)
	env["FERRYMAN_DISABLE"] = "1"
	t0 := time.Now()
	code, out := runPS(t, "ferryman-gate.ps1",
		map[string]any{"session_id": "s", "prompt": "x"}, env)
	assertSilentOK(t, "DISABLE", code, out)
	if el := time.Since(t0); el >= 5*time.Second { // 首行短路，不碰网络
		t.Fatalf("DISABLE 短路耗时 %v, want < 5s", el)
	}
}

// ---- T17 restore 钩子 ----

func TestT17RestoreSkipsResume(t *testing.T) {
	h := newIntegHarness(t, succFerry)
	proj := filepath.Join(h.tmp, "proj")
	writeIntegSession(t, h.projects, "hook-0003", proj, 2000)
	code, out := runPS(t, "ferryman-restore.ps1",
		map[string]any{"session_id": "s", "cwd": proj, "source": "resume"},
		liveEnv(t, h))
	assertSilentOK(t, "resume", code, out) // resume/compact 一律不注入
}

func TestT17RestoreInjectsOnClear(t *testing.T) {
	h := newIntegHarness(t, succFerry)
	proj := filepath.Join(h.tmp, "proj")
	sid := "hook-0004"
	writeIntegSession(t, h.projects, sid, proj, 2000)
	waitForCond(t, 15*time.Second, func() bool {
		return h.handoffInStore("cc", proj, func(e *store.Entry) bool { return e.SessionID == sid })
	})
	code, out := runPS(t, "ferryman-restore.ps1",
		map[string]any{"session_id": "fresh-session", "cwd": proj, "source": "clear"},
		liveEnv(t, h))
	if code != 0 {
		t.Fatalf("exit=%d, want 0", code)
	}
	if !strings.Contains(out, "Ferryman 交接") || !strings.Contains(out, "不可信") {
		t.Fatalf("stdout 缺注入层/不可信声明: %.200q", out)
	}
}

func TestT17RestoreFailopenDaemonDown(t *testing.T) {
	code, out := runPS(t, "ferryman-restore.ps1",
		map[string]any{"session_id": "s", "cwd": "C:/x", "source": "clear"}, downEnv(t))
	assertSilentOK(t, "守护关", code, out)
}

// ---- T32 subagent 钩子 ----

func TestT32SubagentHookRoundtrip(t *testing.T) {
	h := newIntegHarness(t, succFerry)
	env := liveEnv(t, h)
	code, out := runPS(t, "ferryman-subagent.ps1",
		map[string]any{"session_id": "hook-sub1", "agent_id": "a123",
			"agent_type": "general-purpose", "hook_event_name": "SubagentStart"}, env)
	assertSilentOK(t, "start", code, out) // fire-and-forget：无输出
	if !h.led.SubagentActive("cc", "hook-sub1") {
		t.Fatal("start 后台账 subagent_active = false, want true")
	}
	code, out = runPS(t, "ferryman-subagent.ps1",
		map[string]any{"session_id": "hook-sub1", "agent_id": "a123",
			"hook_event_name": "SubagentStop"}, env)
	if code != 0 {
		t.Fatalf("stop exit=%d, want 0", code)
	}
	if h.led.SubagentActive("cc", "hook-sub1") {
		t.Fatal("stop 后台账 subagent_active = true, want false")
	}
}

func TestT32SubagentHookFailopenDaemonDown(t *testing.T) {
	code, out := runPS(t, "ferryman-subagent.ps1",
		map[string]any{"session_id": "s", "hook_event_name": "SubagentStart"}, downEnv(t))
	assertSilentOK(t, "守护关", code, out) // daemon 死 → 静默放行
}

func TestT32SubagentHookEnvDisableShortCircuits(t *testing.T) {
	env := downEnv(t)
	env["FERRYMAN_DISABLE"] = "1"
	t0 := time.Now()
	code, out := runPS(t, "ferryman-subagent.ps1",
		map[string]any{"session_id": "s", "hook_event_name": "SubagentStart"}, env)
	assertSilentOK(t, "DISABLE", code, out)
	if el := time.Since(t0); el >= 5*time.Second { // 首行短路，不碰网络
		t.Fatalf("DISABLE 短路耗时 %v, want < 5s", el)
	}
}

// ---- T23 Codex 钩子（准备阶段：脚本契约真跑验证） ----

func TestT23CodexGateHookBlocksIdle(t *testing.T) {
	// 票21 Minor1（票22 落地）：GateCodex 不再装配后活写（守护 goroutine 并发
	// 读 cfg，活写是数据竞争）——经构造前配置钩子注入
	h := newIntegHarness(t, succFerry, func(c *config.Config) { c.GateCodex = "enforce" })
	proj := filepath.Join(h.tmp, "proj")
	f := writeHookRollout(t, filepath.Join(h.tmp, "no-codex"), "codexgat01", proj)
	// 防御式字段：rollout_path / transcript_path 二选一（真实 schema 晨间信任后校准）
	body := map[string]any{"session_id": "codexgat01", "rollout_path": f,
		"cwd": proj, "prompt": "hi"}
	code, out := gateBlockPoll(t, "ferryman-gate-codex.ps1", body, liveEnv(t, h))
	if code != 0 {
		t.Fatalf("exit=%d, want 0", code)
	}
	payload := parseHookJSON(t, out) // stdout 必须是合法 block JSON
	if payload["decision"] != "block" {
		t.Fatalf("decision = %v, want block", payload["decision"])
	}
	if payload["suppressOriginalPrompt"] != true {
		t.Fatalf("suppressOriginalPrompt = %v, want true", payload["suppressOriginalPrompt"])
	}
	if rsn, _ := payload["reason"].(string); rsn == "" {
		t.Fatal("reason 为空")
	}
}

func TestT23CodexGateHookFailopenDaemonDown(t *testing.T) {
	code, out := runPS(t, "ferryman-gate-codex.ps1",
		map[string]any{"session_id": "s", "rollout_path": "C:/x.jsonl", "prompt": "hi"},
		downEnv(t))
	assertSilentOK(t, "守护关", code, out) // daemon 死 → 放行零输出
}

func TestT23CodexRestoreHookFiltersSource(t *testing.T) {
	h := newIntegHarness(t, succFerry)
	code, out := runPS(t, "ferryman-restore-codex.ps1",
		map[string]any{"session_id": "s", "cwd": "C:/x", "source": "resume"},
		liveEnv(t, h))
	assertSilentOK(t, "resume", code, out) // resume/compact 不注入
}

func TestT23CodexRestoreHookFailopenDaemonDown(t *testing.T) {
	code, out := runPS(t, "ferryman-restore-codex.ps1",
		map[string]any{"session_id": "s", "cwd": "C:/x", "source": "clear"}, downEnv(t))
	assertSilentOK(t, "守护关", code, out)
}

// ---- T37 Codex 子代理生命周期钩子 ----

func TestT37CodexSubagentHookRoundtrip(t *testing.T) {
	h := newIntegHarness(t, succFerry)
	env := liveEnv(t, h)
	code, _ := runPS(t, "ferryman-subagent-codex.ps1",
		map[string]any{"session_id": "cxsub1", "hook_event_name": "SubagentStart"}, env)
	if code != 0 {
		t.Fatalf("start exit=%d, want 0", code)
	}
	if st := h.get("/stats"); st["subagents_active"] != float64(1) {
		t.Fatalf("subagents_active = %v, want 1", st["subagents_active"])
	}
	code, _ = runPS(t, "ferryman-subagent-codex.ps1",
		map[string]any{"session_id": "cxsub1", "hook_event_name": "SubagentStop"}, env)
	if code != 0 {
		t.Fatalf("stop exit=%d, want 0", code)
	}
	st := h.get("/stats")
	if st["subagents_active"] != float64(0) {
		t.Fatalf("subagents_active = %v, want 0", st["subagents_active"])
	}
	if st["subagent_events_total"].(float64) < 2 {
		t.Fatalf("subagent_events_total = %v, want >= 2", st["subagent_events_total"])
	}
}

func TestT37CodexSubagentHookIgnoresOtherEvents(t *testing.T) {
	h := newIntegHarness(t, succFerry)
	code, out := runPS(t, "ferryman-subagent-codex.ps1",
		map[string]any{"session_id": "cxsub2", "hook_event_name": "UserPromptSubmit"},
		liveEnv(t, h))
	assertSilentOK(t, "未知事件", code, out) // 与本钩子无关：静默退出
}
