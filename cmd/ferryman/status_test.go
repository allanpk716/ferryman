package main

// status_test.go —— 票03：status/stop 的单测。
//
// 纪律（票面副作用声明）：绝不触碰真实守护——不发真请求到 15700/15722。
// 请求形态用 httptest 桩钉（daemonStats/daemonSessionsSummary/daemonShutdownPost
// 直打假端点，断言方法/路径/Bearer）；决策树用注桩 deps（零网络）。
// 帮助契约经 run() 分发走真路径（票01 同款：-h/--help 退 0 打印 usage，
// 用法错退 2），只用 -h/错误形态，绝不裸跑 status/stop（会触真配置真端口）。

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ferryman/internal/config"
	"ferryman/internal/installer"
)

// ---- 帮助契约（票01：-h/--help 退 0 打印 usage；用法错退 2） ----

func TestStatusStopHelpContract(t *testing.T) {
	helps := []struct {
		name string
		args []string
		want string
	}{
		{"status -h", []string{"status", "-h"}, statusUsage},
		{"status --help", []string{"status", "--help"}, statusUsage},
		{"stop -h", []string{"stop", "-h"}, stopUsage},
		{"stop --help", []string{"stop", "--help"}, stopUsage},
	}
	for _, tc := range helps {
		var code int
		stdout, _ := captureStd(t, func() { code = run(tc.args) })
		if code != 0 {
			t.Fatalf("%s: 退出码 = %d, want 0", tc.name, code)
		}
		if stdout != tc.want {
			t.Fatalf("%s: 帮助输出应恰为 usage 常量:\ngot  %q\nwant %q", tc.name, stdout, tc.want)
		}
	}

	errs := []struct {
		name string
		args []string
	}{
		{"status 多参", []string{"status", "extra"}},
		{"status 未知旗标", []string{"status", "--nope"}},
		{"stop 位置参数", []string{"stop", "5"}},
		{"stop 负预算", []string{"stop", "--wait=-5"}},
		{"stop 未知旗标", []string{"stop", "--nope"}},
	}
	for _, tc := range errs {
		var code int
		_, stderr := captureStd(t, func() { code = run(tc.args) })
		if code != 2 {
			t.Fatalf("%s: 退出码 = %d, want 2（用法错）", tc.name, code)
		}
		if !strings.Contains(stderr, "用法:") {
			t.Fatalf("%s: 用法错应打印 usage 到 stderr, got %q", tc.name, stderr)
		}
	}
}

// TestTopLevelUsageMentionsStatusStop 顶层 usage 收录两命令（票面：进顶层帮助）。
func TestTopLevelUsageMentionsStatusStop(t *testing.T) {
	for _, want := range []string{"ferryman status", "ferryman stop"} {
		if !strings.Contains(usage, want) {
			t.Errorf("顶层 usage 缺 %q", want)
		}
	}
}

// ---- status：三面渲染与决策（deps 全注桩，零网络） ----

func TestStatusRunOnline(t *testing.T) {
	deps := &statusDeps{
		Port: 15700, Token: "secret-tk", DockAddr: "127.0.0.1:15722",
		Stats: func(int, string) (map[string]any, error) {
			return map[string]any{"version": "v9.9.9"}, nil
		},
		Sessions: func(int, string) (int, int, error) { return 3, 1, nil },
		Dial:     func(string, time.Duration) error { return nil },
	}
	var buf bytes.Buffer
	if code := statusRun(&buf, deps); code != 0 {
		t.Fatalf("status 退出码 = %d, want 0\n%s", code, buf.String())
	}
	out := buf.String()
	for _, want := range []string{"v9.9.9", "在线", "在册会话 3", "闲置", "127.0.0.1:15722"} {
		if !strings.Contains(out, want) {
			t.Errorf("输出缺 %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "secret-tk") {
		t.Errorf("输出泄漏 token:\n%s", out)
	}
}

// TestStatusRunOfflineAllFacesHonest 守护不在线：三面如实标注，仍 exit 0；
// 且不再打 /sessions（省一次注定失败的超时）。
func TestStatusRunOfflineAllFacesHonest(t *testing.T) {
	sessionsCalled := 0
	deps := &statusDeps{
		Port: 15700, Token: "tk", DockAddr: "127.0.0.1:15722",
		Stats: func(int, string) (map[string]any, error) {
			return nil, errDaemonOffline
		},
		Sessions: func(int, string) (int, int, error) { sessionsCalled++; return 0, 0, nil },
		Dial:     func(string, time.Duration) error { return errors.New("refused") },
	}
	var buf bytes.Buffer
	if code := statusRun(&buf, deps); code != 0 {
		t.Fatalf("不在线也是状态查询，退出码应 0, got %d\n%s", code, buf.String())
	}
	out := buf.String()
	if !strings.Contains(out, "守护: 不在线") {
		t.Errorf("守护面应如实标不在线:\n%s", out)
	}
	if !strings.Contains(out, "渡口: 不在线") {
		t.Errorf("渡口面应如实标不在线:\n%s", out)
	}
	if !strings.Contains(out, "台账") {
		t.Errorf("台账面也应有一行交代:\n%s", out)
	}
	if sessionsCalled != 0 {
		t.Errorf("守护不在线时不应再打 /sessions（调了 %d 次）", sessionsCalled)
	}
}

// TestStatusRunAuthFailed 401（token 错位）：在线但鉴权失败——如实报因，仍 exit 0。
func TestStatusRunAuthFailed(t *testing.T) {
	sessionsCalled := 0
	deps := &statusDeps{
		Port: 15700, Token: "tk", DockAddr: "",
		Stats: func(int, string) (map[string]any, error) { return nil, errAuth },
		Sessions: func(int, string) (int, int, error) { sessionsCalled++; return 0, 0, nil },
		Dial:     func(string, time.Duration) error { return nil },
	}
	var buf bytes.Buffer
	if code := statusRun(&buf, deps); code != 0 {
		t.Fatalf("退出码 = %d, want 0\n%s", code, buf.String())
	}
	if out := buf.String(); !strings.Contains(out, "鉴权失败") {
		t.Errorf("应如实报鉴权失败:\n%s", out)
	}
	if sessionsCalled != 0 {
		t.Errorf("鉴权失败时 /sessions 必败，不应再打（调了 %d 次）", sessionsCalled)
	}
}

// TestStatusRunDockDisabled [dock] 节缺失 = 渡口未启用（F11 opt-in）——标「未启用」。
func TestStatusRunDockDisabled(t *testing.T) {
	deps := &statusDeps{
		Port: 15700, Token: "tk", DockAddr: "",
		Stats:    func(int, string) (map[string]any, error) { return map[string]any{"version": "dev"}, nil },
		Sessions: func(int, string) (int, int, error) { return 0, 0, nil },
		Dial: func(string, time.Duration) error {
			t.Fatal("渡口未启用不应探测")
			return nil
		},
	}
	var buf bytes.Buffer
	if code := statusRun(&buf, deps); code != 0 {
		t.Fatalf("退出码 = %d, want 0\n%s", code, buf.String())
	}
	if out := buf.String(); !strings.Contains(out, "未启用") {
		t.Errorf("渡口未启用应如实标注:\n%s", out)
	}
}

// ---- 请求形态（httptest 桩直打假端点） ----

func TestDaemonStatsRequestShape(t *testing.T) {
	var gotMethod, gotURI, gotAuth string
	mux := http.NewServeMux()
	mux.HandleFunc("/stats", func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotURI, gotAuth = r.Method, r.RequestURI, r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"version":"v1.2.3","gate_calls_total":7}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	st, err := daemonStats(portOfURL(t, srv.URL), "tk")
	if err != nil {
		t.Fatalf("daemonStats: %v", err)
	}
	if gotMethod != http.MethodGet || gotURI != "/stats" || gotAuth != "Bearer tk" {
		t.Errorf("请求形态不符: method=%q uri=%q auth=%q", gotMethod, gotURI, gotAuth)
	}
	if st["version"] != "v1.2.3" {
		t.Errorf("版本解析不符: %v", st["version"])
	}
}

func TestDaemonStatsOfflineAndAuth(t *testing.T) {
	if _, err := daemonStats(deadPort(t), "tk"); !errors.Is(err, errDaemonOffline) {
		t.Errorf("死口应报守护不在线: %v", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/stats", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	if _, err := daemonStats(portOfURL(t, srv.URL), "WRONG"); !errors.Is(err, errAuth) {
		t.Errorf("401 应报鉴权失败: %v", err)
	}
}

func TestDaemonSessionsSummaryShape(t *testing.T) {
	var gotURI, gotAuth string
	mux := http.NewServeMux()
	mux.HandleFunc("/sessions", func(w http.ResponseWriter, r *http.Request) {
		gotURI, gotAuth = r.RequestURI, r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"sessions":[` +
			`{"session_id":"a","stale":true},{"session_id":"b","stale":false},` +
			`{"session_id":"c","stale":true}]}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	reg, idle, err := daemonSessionsSummary(portOfURL(t, srv.URL), "tk")
	if err != nil {
		t.Fatalf("daemonSessionsSummary: %v", err)
	}
	if !strings.HasPrefix(gotURI, "/sessions?") || gotAuth != "Bearer tk" {
		t.Errorf("请求形态不符: uri=%q auth=%q", gotURI, gotAuth)
	}
	if !strings.Contains(gotURI, "limit=") {
		t.Errorf("应带 limit 参数（缺省 50 会截断计数）: %q", gotURI)
	}
	if reg != 3 || idle != 2 {
		t.Errorf("在册/闲置计数不符: got (%d,%d), want (3,2)", reg, idle)
	}
}

func TestDaemonShutdownRequestShape(t *testing.T) {
	var gotMethod, gotURI, gotAuth string
	var gotBody []byte
	mux := http.NewServeMux()
	mux.HandleFunc("/shutdown", func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotURI, gotAuth = r.Method, r.RequestURI, r.Header.Get("Authorization")
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	if err := daemonShutdownPost(portOfURL(t, srv.URL), "tk"); err != nil {
		t.Fatalf("daemonShutdownPost: %v", err)
	}
	if gotMethod != http.MethodPost || gotURI != "/shutdown" || gotAuth != "Bearer tk" {
		t.Errorf("请求形态不符: method=%q uri=%q auth=%q", gotMethod, gotURI, gotAuth)
	}
	if len(gotBody) != 0 {
		t.Errorf("shutdown 不应携带请求体: %q", gotBody)
	}

	// 401 → errAuth；死口 → errDaemonOffline
	mux401 := http.NewServeMux()
	mux401.HandleFunc("/shutdown", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})
	srv401 := httptest.NewServer(mux401)
	defer srv401.Close()
	if err := daemonShutdownPost(portOfURL(t, srv401.URL), "WRONG"); !errors.Is(err, errAuth) {
		t.Errorf("401 应报鉴权失败: %v", err)
	}
	if err := daemonShutdownPost(deadPort(t), "tk"); !errors.Is(err, errDaemonOffline) {
		t.Errorf("死口应报守护不在线: %v", err)
	}
}

// ---- stop：决策树（deps 全注桩，零网络零进程） ----

func TestStopRunGracefulSuccess(t *testing.T) {
	var calls []string
	deps := &stopDeps{
		Port: 15700, Token: "tk", DataDir: t.TempDir(),
		Shutdown: func(int, string) error { calls = append(calls, "shutdown"); return nil },
		WaitFree: func(addr string, _ time.Duration) bool {
			calls = append(calls, "free:"+addr)
			return true
		},
		WaitExit: func(pid int, _ time.Duration) bool {
			calls = append(calls, fmt.Sprintf("exit:%d", pid))
			return true
		},
		ReadPID: func() (int, error) { return 4242, nil },
		Dial:    func(string, time.Duration) error { return nil },
	}
	var buf bytes.Buffer
	if code := stopRun(&buf, 5*time.Second, deps); code != 0 {
		t.Fatalf("优雅停退出码 = %d, want 0\n%s", code, buf.String())
	}
	want := []string{"shutdown", "free:127.0.0.1:15700", "exit:4242"}
	if len(calls) != len(want) {
		t.Fatalf("调用序 = %v, want %v", calls, want)
	}
	for i := range want {
		if calls[i] != want[i] {
			t.Fatalf("调用序 = %v, want %v（先 /shutdown 再等端口再等进程）", calls, want)
		}
	}
	if !strings.Contains(buf.String(), "已停") {
		t.Errorf("成功输出应交代已停:\n%s", buf.String())
	}
}

// TestStopRunTimeoutPortNotFree 端口在预算内未释放：exit 1、如实报告、
// 不进进程等待（更无 kill 路径）。
func TestStopRunTimeoutPortNotFree(t *testing.T) {
	exitCalled := false
	deps := &stopDeps{
		Port: 15700, Token: "tk", DataDir: t.TempDir(),
		Shutdown: func(int, string) error { return nil },
		WaitFree: func(string, time.Duration) bool { return false },
		WaitExit: func(int, time.Duration) bool { exitCalled = true; return true },
		ReadPID:  func() (int, error) { return 4242, nil },
		Dial:     func(string, time.Duration) error { return nil },
	}
	var buf bytes.Buffer
	if code := stopRun(&buf, time.Second, deps); code != 1 {
		t.Fatalf("超时退出码 = %d, want 1\n%s", code, buf.String())
	}
	if exitCalled {
		t.Error("端口未释放不应进进程等待")
	}
	if out := buf.String(); !strings.Contains(out, "超时") || !strings.Contains(out, "未硬杀") {
		t.Errorf("超时应如实报告且声明未硬杀:\n%s", out)
	}
}

// TestStopRunTimeoutProcessNotExit 端口已释但进程未退：exit 1 并点出 PID。
func TestStopRunTimeoutProcessNotExit(t *testing.T) {
	deps := &stopDeps{
		Port: 15700, Token: "tk", DataDir: t.TempDir(),
		Shutdown: func(int, string) error { return nil },
		WaitFree: func(string, time.Duration) bool { return true },
		WaitExit: func(int, time.Duration) bool { return false },
		ReadPID:  func() (int, error) { return 4242, nil },
		Dial:     func(string, time.Duration) error { return nil },
	}
	var buf bytes.Buffer
	if code := stopRun(&buf, time.Second, deps); code != 1 {
		t.Fatalf("超时退出码 = %d, want 1\n%s", code, buf.String())
	}
	if out := buf.String(); !strings.Contains(out, "4242") {
		t.Errorf("超时应点出未退的 PID:\n%s", out)
	}
}

// TestStopRunOfflineNoOp 守护本就不在线（无监听）：无需停止，exit 0。
func TestStopRunOfflineNoOp(t *testing.T) {
	deps := &stopDeps{
		Port: 15700, Token: "tk", DataDir: t.TempDir(),
		Shutdown: func(int, string) error { return errDaemonOffline },
		WaitFree: func(string, time.Duration) bool {
			t.Fatal("未发停机指令不应等待")
			return false
		},
		WaitExit: func(int, time.Duration) bool { return true },
		ReadPID:  func() (int, error) { return 0, errDaemonPIDUnknown },
		Dial:     func(string, time.Duration) error { return errors.New("refused") },
	}
	var buf bytes.Buffer
	if code := stopRun(&buf, time.Second, deps); code != 0 {
		t.Fatalf("本就不在线应 exit 0, got %d\n%s", code, buf.String())
	}
	if out := buf.String(); !strings.Contains(out, "无需停止") {
		t.Errorf("应交代无需停止:\n%s", out)
	}
}

// TestStopRunOfflinePortHeldByOther 拨不通但端口有监听（非 Ferryman 进程）：
// 不处置、绝不硬杀，exit 1。
func TestStopRunOfflinePortHeldByOther(t *testing.T) {
	deps := &stopDeps{
		Port: 15700, Token: "tk", DataDir: t.TempDir(),
		Shutdown: func(int, string) error { return errDaemonOffline },
		WaitFree: func(string, time.Duration) bool { return false },
		WaitExit: func(int, time.Duration) bool { return true },
		ReadPID:  func() (int, error) { return 0, errDaemonPIDUnknown },
		Dial:     func(string, time.Duration) error { return nil }, // 有监听
	}
	var buf bytes.Buffer
	if code := stopRun(&buf, time.Second, deps); code != 1 {
		t.Fatalf("端口被占且非守护应 exit 1, got %d\n%s", code, buf.String())
	}
	if out := buf.String(); !strings.Contains(out, "不处置") {
		t.Errorf("应声明不处置（绝不硬杀）:\n%s", out)
	}
}

// TestStopRunAuthRejected 401：停机被拒，守护未停，exit 1。
func TestStopRunAuthRejected(t *testing.T) {
	deps := &stopDeps{
		Port: 15700, Token: "WRONG", DataDir: t.TempDir(),
		Shutdown: func(int, string) error { return errAuth },
		WaitFree: func(string, time.Duration) bool { return true },
		WaitExit: func(int, time.Duration) bool { return true },
		ReadPID:  func() (int, error) { return 0, errDaemonPIDUnknown },
		Dial:     func(string, time.Duration) error { return nil },
	}
	var buf bytes.Buffer
	if code := stopRun(&buf, time.Second, deps); code != 1 {
		t.Fatalf("鉴权被拒退出码 = %d, want 1\n%s", code, buf.String())
	}
	if out := buf.String(); !strings.Contains(out, "鉴权失败") {
		t.Errorf("应如实报鉴权失败:\n%s", out)
	}
}

// ---- 装配与等待原语 ----

// TestRealStatusDepsDerivation 真装配派生面：port 缺省/覆盖、dock 地址派生、
// token 读盘（provider switch realProviderSwitchDeps 同纪律）；token 已在场时
// 装配零落盘（status 只读）。
func TestRealStatusDepsDerivation(t *testing.T) {
	dir := t.TempDir()
	tokenFile := filepath.Join(dir, "daemon.token")
	if err := os.WriteFile(tokenFile, []byte("tok-abc\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	before := dirSnapshot(t, dir)

	cfg := &config.Config{Server: config.ServerCfg{Port: 12345, DataDir: dir}}
	deps := realStatusDeps(cfg)
	if deps.Port != 12345 {
		t.Errorf("Port 应取 cfg.Server.Port, got %d", deps.Port)
	}
	if deps.Token != "tok-abc" {
		t.Errorf("Token 应读 daemon.token（strip）, got %q", deps.Token)
	}
	if deps.DockAddr != "" {
		t.Errorf("无 [dock] 节应标未启用（空地址）, got %q", deps.DockAddr)
	}

	cfg.Dock = &config.DockCfg{Listen: "127.0.0.1:15999"}
	if deps = realStatusDeps(cfg); deps.DockAddr != "127.0.0.1:15999" {
		t.Errorf("DockAddr 应从 Dock.Listen 派生, got %q", deps.DockAddr)
	}

	// Port 0 → 缺省 15700；listen 空 → 缺省 15722
	cfg.Server.Port = 0
	cfg.Dock.Listen = ""
	deps = realStatusDeps(cfg)
	if deps.Port != installer.DefaultDaemonPort {
		t.Errorf("Port 0 应回落缺省 %d, got %d", installer.DefaultDaemonPort, deps.Port)
	}
	if deps.DockAddr != fmt.Sprintf("127.0.0.1:%d", installer.DefaultDockPort) {
		t.Errorf("listen 空应回落缺省渡口口, got %q", deps.DockAddr)
	}

	// nil cfg（配置不可读）：缺省口+缺省渡口探测口，不读不建 token
	if deps := realStatusDeps(nil); deps.Port != installer.DefaultDaemonPort || deps.Token != "" {
		t.Errorf("nil cfg 应缺省口且无 token, got port=%d token=%q", deps.Port, deps.Token)
	}

	// 装配不落盘：token 已在场 → 目录快照不变
	if after := dirSnapshot(t, dir); !sameSnapshot(before, after) {
		t.Errorf("realStatusDeps 改动了文件: %v → %v", before, after)
	}
}

func TestWaitPortFreeAddr(t *testing.T) {
	srv := httptest.NewServer(http.NewServeMux())
	addr := srv.Listener.Addr().String()
	if waitPortFreeAddr(addr, 300*time.Millisecond) {
		t.Fatal("监听中应等满预算返回 false")
	}
	srv.Close()
	if !waitPortFreeAddr(addr, 2*time.Second) {
		t.Fatal("关闭后应立即判空 true")
	}
}

func TestWaitPIDExit(t *testing.T) {
	alive := func(bool2 bool) func(int) bool {
		return func(int) bool { return bool2 }
	}
	if !waitPIDExit(0, time.Second, alive(true)) {
		t.Fatal("pid≤0（daemon.pid 缺失）应放行")
	}
	if !waitPIDExit(5, time.Second, alive(false)) {
		t.Fatal("进程已死应立即 true")
	}
	if waitPIDExit(5, 250*time.Millisecond, alive(true)) {
		t.Fatal("预算内未退应 false")
	}
}

// ---- 小帮手 ----

// deadPort 拿一个确定无监听的口（测试里模拟「守护不在线」）。
func deadPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return port
}

// dirSnapshot 目录内容快照（文件名→内容；子目录只记名）——钉「装配零落盘」。
func dirSnapshot(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	des, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, de := range des {
		if de.IsDir() {
			out[de.Name()+"/"] = ""
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, de.Name()))
		if err != nil {
			t.Fatal(err)
		}
		out[de.Name()] = string(b)
	}
	return out
}

func sameSnapshot(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}
