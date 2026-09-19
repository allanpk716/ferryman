// watchdog_test.go — 票02：看门三分支判定 + schtasks 命令构造验收。
//
// HTTP 分支用真 httptest/真 listener（真 401/5xx 响应、真 connection refused、
// 真超时），拉起动作用接口注入 fake 断言被调（票面验收②）；
// schtasks 只断言构造参数（逐字，含 /MO 5 与无窗口标志），真实执行全走
// runner 注入——绝不真建/删计划任务（本票副作用声明）。

package installer

import (
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"
)

// fakeLauncher 拉起动作替身：调用计数 + 可注入失败。
type fakeLauncher struct {
	calls int
	err   error
}

func (l *fakeLauncher) launch() error {
	l.calls++
	return l.err
}

// silentLogf 静音日志（判定分支的输出面不在本票断言范围）。
func silentLogf(string, ...any) {}

// wdDeps 便捷装配：真探针 + 注入拉起。
func wdDeps(port int, timeout time.Duration, l *fakeLauncher) WatchdogDeps {
	return WatchdogDeps{Port: port, Timeout: timeout, Probe: probeHTTP, Launch: l.launch, Logf: silentLogf}
}

// 分支①：有响应（任何状态码，含 401/5xx）→ 退出 0 且不拉起。
func TestWatchdogRespondsNoLaunch(t *testing.T) {
	for _, code := range []int{http.StatusOK, http.StatusUnauthorized, http.StatusInternalServerError} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(code)
		}))
		port := srv.Listener.Addr().(*net.TCPAddr).Port
		l := &fakeLauncher{}
		got := runWatchdog(wdDeps(port, 2*time.Second, l))
		srv.Close()
		if got != 0 {
			t.Fatalf("状态 %d: 应退出 0, got %d", code, got)
		}
		if l.calls != 0 {
			t.Fatalf("状态 %d: 不应拉起, calls=%d", code, l.calls)
		}
	}
}

// 分支②：无监听（真 connection refused）→ 恰好拉起一次，退出 0。
func TestWatchdogRefusedLaunchesOnce(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close() // 制造真·拒绝
	l := &fakeLauncher{}
	if got := runWatchdog(wdDeps(port, 2*time.Second, l)); got != 0 {
		t.Fatalf("拉起成功应退出 0, got %d", got)
	}
	if l.calls != 1 {
		t.Fatalf("应恰好拉起一次, calls=%d", l.calls)
	}
}

// 分支②变体：拉起失败 → 退出 1（响亮，绝不假装看门成功）。
func TestWatchdogRefusedLaunchFailureExits1(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	l := &fakeLauncher{err: errors.New("powershell 不在")}
	if got := runWatchdog(wdDeps(port, 2*time.Second, l)); got != 1 {
		t.Fatalf("拉起失败应退出 1, got %d", got)
	}
	if l.calls != 1 {
		t.Fatalf("失败也应恰好尝试一次, calls=%d", l.calls)
	}
}

// 分支③：端口被占但非 daemon → 只告警退出 0，绝不拉起（单实例，不双拉）。
// ③a 黑洞监听（accept 后不回话 → 探针超时）；③b 立即断连（EOF）。
func TestWatchdogOccupiedNoLaunch(t *testing.T) {
	// ③a：黑洞——listen 后故意不 Accept，连接挂在内核 backlog，请求永远无响应
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port
	l := &fakeLauncher{}
	if got := runWatchdog(wdDeps(port, 200*time.Millisecond, l)); got != 0 {
		t.Fatalf("占用超时应退出 0（只告警）, got %d", got)
	}
	if l.calls != 0 {
		t.Fatalf("占用超时绝不拉起, calls=%d", l.calls)
	}

	// ③b：占用但立即断连（EOF ≠ 拒绝 → 同归"无 daemon 响应"）
	ln2, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln2.Close()
	quit := make(chan struct{})
	defer close(quit)
	go func() {
		for {
			c, err := ln2.Accept()
			if err != nil {
				return
			}
			_ = c.Close() // 收下就掐断：HTTP 客户端吃 EOF
		}
	}()
	port2 := ln2.Addr().(*net.TCPAddr).Port
	l2 := &fakeLauncher{}
	if got := runWatchdog(wdDeps(port2, 2*time.Second, l2)); got != 0 {
		t.Fatalf("断连占用应退出 0（只告警）, got %d", got)
	}
	if l2.calls != 0 {
		t.Fatalf("断连占用绝不拉起, calls=%d", l2.calls)
	}
}

// 分支③兜底：Probe 缺失等装配空洞不 panic（Launch nil 在 refused 分支退出 1）。
func TestWatchdogRefusedNilLaunchExits1(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	got := runWatchdog(WatchdogDeps{Port: port, Timeout: 2 * time.Second, Probe: probeHTTP, Logf: silentLogf})
	if got != 1 {
		t.Fatalf("未装配拉起动作应退出 1, got %d", got)
	}
}

// 验收③：schtasks 建任务参数逐字（含 /MO 5 与 /TR 无窗口标志）。
func TestSchtasksCreateArgsVerbatim(t *testing.T) {
	tr := WatchdogTR(`C:\Tools\ferryman.exe`)
	got := schtasksCreateArgs(WatchdogTaskName, tr)
	want := []string{"/Create", "/F", "/TN", "FerrymanWatchdog", "/SC", "MINUTE", "/MO", "5", "/TR", tr}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("schtasks /Create 参数逐字不符:\n got: %q\nwant: %q", got, want)
	}
	if !strings.Contains(tr, "-WindowStyle Hidden") {
		t.Fatalf("TR 缺无窗口标志: %s", tr)
	}
}

// 删/查任务参数逐字（/F 免交互——无窗口任务里交互确认会挂死）。
func TestSchtasksDeleteAndQueryArgsVerbatim(t *testing.T) {
	if got, want := schtasksDeleteArgs(WatchdogTaskName),
		[]string{"/Delete", "/TN", "FerrymanWatchdog", "/F"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("schtasks /Delete 参数逐字不符: %q", got)
	}
	if got, want := schtasksQueryArgs(WatchdogTaskName),
		[]string{"/Query", "/TN", "FerrymanWatchdog", "/FO", "LIST"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("schtasks /Query 参数逐字不符: %q", got)
	}
}

// TR 逐字：无窗口 PS 包装拉起本 exe 的 watchdog 子命令。
func TestWatchdogTRVerbatim(t *testing.T) {
	got := WatchdogTR(`C:\Tools\ferryman.exe`)
	want := `powershell -NoProfile -WindowStyle Hidden -Command "Start-Process -FilePath 'C:\Tools\ferryman.exe' -ArgumentList 'watchdog' -WindowStyle Hidden"`
	if got != want {
		t.Fatalf("TR 逐字不符:\n got: %s\nwant: %s", got, want)
	}
}

// FERRYMAN_PORT：数值生效；坏值/缺省回落 7311。
func TestDaemonPortEnv(t *testing.T) {
	t.Setenv(DaemonPortEnv, "8123")
	if got := DaemonPort(); got != 8123 {
		t.Fatalf("env 数值应生效: got %d", got)
	}
	t.Setenv(DaemonPortEnv, "abc")
	if got := DaemonPort(); got != DefaultDaemonPort {
		t.Fatalf("坏值应回落 %d: got %d", DefaultDaemonPort, got)
	}
	t.Setenv(DaemonPortEnv, "")
	if got := DaemonPort(); got != DefaultDaemonPort {
		t.Fatalf("缺省应回落 %d: got %d", DefaultDaemonPort, got)
	}
}

// fakeRunner schtasks 执行替身：记录调用 + /Query 可模拟缺失（非零退出）。
type fakeRunner struct {
	calls     [][]string
	queryOut  string
	queryFail bool
}

func (f *fakeRunner) CombinedOutput(name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, append([]string{name}, args...))
	switch args[0] {
	case "/Query":
		if f.queryFail {
			return []byte("ERROR: The scheduled task \"FerrymanWatchdog\" does not exist."), &exec.ExitError{}
		}
		return []byte(f.queryOut), nil
	}
	return nil, nil
}

// install = Create 参数逐字经 runner 下发。
func TestWatchdogTaskInstallArgs(t *testing.T) {
	r := &fakeRunner{}
	tr := WatchdogTR(`C:\Tools\ferryman.exe`)
	if err := installWatchdogTask(taskDeps{tr: tr, runner: r}); err != nil {
		t.Fatalf("install: %v", err)
	}
	want := append([]string{"schtasks"}, schtasksCreateArgs(WatchdogTaskName, tr)...)
	if !reflect.DeepEqual(r.calls[0], want) {
		t.Fatalf("install 下发参数不符:\n got: %q\nwant: %q", r.calls[0], want)
	}
}

// uninstall 幂等：任务不在（/Query 非零）就不再发 /Delete。
func TestWatchdogTaskUninstallIdempotent(t *testing.T) {
	r := &fakeRunner{queryFail: true} // 任务缺失
	if err := uninstallWatchdogTask(taskDeps{tr: "x", runner: r}); err != nil {
		t.Fatalf("缺任务 uninstall 应幂等: %v", err)
	}
	if len(r.calls) != 1 {
		t.Fatalf("缺任务只应查询一次（不再 /Delete）, calls=%q", r.calls)
	}
	// 任务在 → /Delete 下发
	r2 := &fakeRunner{}
	if err := uninstallWatchdogTask(taskDeps{tr: "x", runner: r2}); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if len(r2.calls) != 2 || !reflect.DeepEqual(r2.calls[1],
		append([]string{"schtasks"}, schtasksDeleteArgs(WatchdogTaskName)...)) {
		t.Fatalf("在位任务应下发 /Delete, calls=%q", r2.calls)
	}
}

// status：EN 输出解析下次运行；缺失（非零退出）= Exists=false。
func TestWatchdogTaskStatusParse(t *testing.T) {
	r := &fakeRunner{queryOut: "Host name: DESKTOP\r\nTaskName: \\FerrymanWatchdog\r\n" +
		"Next Run Time: 2026/9/19 21:00:00\r\nStatus: Ready\r\n"}
	st, err := queryTask(taskDeps{tr: "x", runner: r})
	if err != nil {
		t.Fatalf("queryTask: %v", err)
	}
	if !st.Exists || st.NextRun != "2026/9/19 21:00:00" {
		t.Fatalf("解析不符: %+v", st)
	}
	r2 := &fakeRunner{queryFail: true}
	st, err = queryTask(taskDeps{tr: "x", runner: r2})
	if err != nil || st.Exists {
		t.Fatalf("缺任务应 Exists=false err=nil: %+v err=%v", st, err)
	}
}

// 探活 URL 钉死形态（/stats——daemon 无 /health 路由，票面指示探 stats）。
func TestDaemonProbeURL(t *testing.T) {
	if got, want := DaemonProbeURL(7311), "http://127.0.0.1:7311/stats"; got != want {
		t.Fatalf("探活 URL 不符: got %s want %s", got, want)
	}
}
