// watchdog_forensics_test.go — P1 看门复位取证 + 拉起后复核（2026-09-30）。
//
// 取证五形态（pid 文件：不在/坏/活性不可判/PID 死/PID 活）走真临时目录 +
// 注入 pidAlive 桩；末次应答扫描对齐生产 watchdog.log 行文；拉起复核三分支
// （有响应/仍无监听/异常）用错误队列脚本探针。绝不写真 dataDir。

package installer

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// forensicsEnv 临时数据目录 + 日志收集器。
type forensicsEnv struct {
	dir  string
	logs []string
}

func newForensicsEnv(t *testing.T) *forensicsEnv {
	t.Helper()
	return &forensicsEnv{dir: t.TempDir()}
}

func (e *forensicsEnv) logf(f string, a ...any) {
	e.logs = append(e.logs, fmt.Sprintf(f, a...))
}

func (e *forensicsEnv) joined() string { return strings.Join(e.logs, "\n") }

// writePidFile 造 daemon.pid（daemon 侧 pidFileJSON 同形）。
func (e *forensicsEnv) writePidFile(t *testing.T, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(e.dir, "daemon.pid"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// writeWatchdogLog 造 watchdog.log（realWatchdogLogf 行文同形：时间戳前缀）。
func (e *forensicsEnv) writeWatchdogLog(t *testing.T, lines ...string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(e.dir, watchdogLogName),
		[]byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// wdOKLine 生产「daemon 有响应」行文同形。
func wdOKLine(ts string) string {
	return ts + " [watchdog] daemon 有响应（http://127.0.0.1:15700/stats）——正常退出"
}

// 取证形态①：pid 文件残留 + PID 已死 = 无痕死亡实锤，末次应答入行。
func TestRespawnForensicsPidGone(t *testing.T) {
	e := newForensicsEnv(t)
	e.writePidFile(t, `{"pid":4242,"port":15700,"started_at":"2026-09-29 08:10:05"}`)
	e.writeWatchdogLog(t,
		wdOKLine("2026-09-29 08:32:00"),
		wdOKLine("2026-09-29 08:37:00"),
		"2026-09-29 08:42:00 [watchdog] http://127.0.0.1:15700/stats 无监听（connection refused）——拉起 daemon",
	)
	logRespawnForensics(e.dir, func(int) bool { return false }, e.logf)
	got := e.joined()
	for _, want := range []string{
		"复位取证", "pid=4242", "started=2026-09-29 08:10:05",
		"PID 已消失", "无痕死亡", "08:37:00", "serve.out.log / serve.err.log",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("无痕死亡行缺 %q:\n%s", want, got)
		}
	}
}

// 取证形态②：pid 文件残留 + PID 仍在 = 半死形态或 PID 复用（不算无痕死亡）。
func TestRespawnForensicsPidAlive(t *testing.T) {
	e := newForensicsEnv(t)
	e.writePidFile(t, `{"pid":4242,"port":15700,"started_at":"2026-09-29 08:10:05"}`)
	logRespawnForensics(e.dir, func(int) bool { return true }, e.logf)
	got := e.joined()
	if !strings.Contains(got, "PID 仍在") || !strings.Contains(got, "半死形态或 PID 复用") {
		t.Fatalf("应记半死/复用形态: %v", e.logs)
	}
	if strings.Contains(got, "无痕死亡") {
		t.Fatalf("PID 活着不得记无痕死亡: %v", e.logs)
	}
}

// 取证形态③：pid 文件不在 = 上次已优雅退出（或从未启动），常规补位。
func TestRespawnForensicsNoPidFile(t *testing.T) {
	e := newForensicsEnv(t)
	logRespawnForensics(e.dir, func(int) bool { return false }, e.logf)
	got := e.joined()
	if !strings.Contains(got, "常规补位") {
		t.Fatalf("应记常规补位: %v", e.logs)
	}
	if strings.Contains(got, "无痕死亡") {
		t.Fatalf("无 pid 文件不得记无痕死亡: %v", e.logs)
	}
}

// 取证形态④：pid 文件坏（半写/非 JSON）= 残留但不可读，原样保留。
func TestRespawnForensicsBadPidFile(t *testing.T) {
	e := newForensicsEnv(t)
	e.writePidFile(t, "{half-written")
	logRespawnForensics(e.dir, func(int) bool { return false }, e.logf)
	if !strings.Contains(e.joined(), "不可读") {
		t.Fatalf("应记不可读: %v", e.logs)
	}
}

// 取证形态⑤：pidAlive 未装配 = 活性不可判，如实标注不猜。
func TestRespawnForensicsPidAliveNil(t *testing.T) {
	e := newForensicsEnv(t)
	e.writePidFile(t, `{"pid":4242,"port":15700,"started_at":"2026-09-29 08:10:05"}`)
	logRespawnForensics(e.dir, nil, e.logf)
	if !strings.Contains(e.joined(), "不可判") {
		t.Fatalf("应记活性不可判: %v", e.logs)
	}
}

// dataDir 空 = 不取证（最小装配零改动，旧行为）。
func TestRespawnForensicsEmptyDataDirSilent(t *testing.T) {
	e := newForensicsEnv(t)
	logRespawnForensics("", func(int) bool { return false }, e.logf)
	if len(e.logs) != 0 {
		t.Fatalf("空 dataDir 不应落取证行: %v", e.logs)
	}
}

// 末次应答扫描：取最后一行「有响应」；无应答行/文件不在 = 空。
func TestLastWatchdogOK(t *testing.T) {
	e := newForensicsEnv(t)
	e.writeWatchdogLog(t,
		wdOKLine("2026-09-29 08:32:00"),
		"2026-09-29 08:42:00 [watchdog] http://127.0.0.1:15700/stats 无监听（connection refused）——拉起 daemon",
		wdOKLine("2026-09-29 08:47:00"),
	)
	if got := lastWatchdogOK(filepath.Join(e.dir, watchdogLogName)); got != "2026-09-29 08:47:00" {
		t.Fatalf("应取末次应答 08:47:00, got %q", got)
	}
	// 只有拉起行（失败循环中）= 无记录。
	e2 := newForensicsEnv(t)
	e2.writeWatchdogLog(t,
		"2026-09-25 01:02:00 [watchdog] http://127.0.0.1:15700/stats 无监听（connection refused）——拉起 daemon")
	if got := lastWatchdogOK(filepath.Join(e2.dir, watchdogLogName)); got != "" {
		t.Fatalf("无应答行应空串, got %q", got)
	}
	// 文件不在 = 空串不炸。
	if got := lastWatchdogOK(filepath.Join(e2.dir, "nope.log")); got != "" {
		t.Fatalf("文件不在应空串, got %q", got)
	}
}

// 末次应答扫描：只读尾窗 64KiB——应答行被推出窗外（超 64KiB 噪声在前）
// 不再命中；窗内末行仍命中。
func TestLastWatchdogOKTailWindow(t *testing.T) {
	e := newForensicsEnv(t)
	var lines []string
	lines = append(lines, wdOKLine("2026-09-25 00:52:00")) // 会被推出窗外
	for i := 0; i < 1400; i++ {                             // ~70KiB 噪声行
		lines = append(lines, fmt.Sprintf("2026-09-25 01:%02d:00 [watchdog] 噪声填充行 %d，凑过 64KiB 尾窗。", i%60, i))
	}
	lines = append(lines, wdOKLine("2026-09-25 02:52:00"))
	e.writeWatchdogLog(t, lines...)
	if got := lastWatchdogOK(filepath.Join(e.dir, watchdogLogName)); got != "2026-09-25 02:52:00" {
		t.Fatalf("窗内末行应命中 02:52:00, got %q", got)
	}
	// 反证窗截断生效：把窗内末行删掉，只剩窗外旧行 → 空串。
	e.writeWatchdogLog(t, lines[:len(lines)-1]...)
	if got := lastWatchdogOK(filepath.Join(e.dir, watchdogLogName)); got != "" {
		t.Fatalf("窗外应答行不应命中（尾窗语义）, got %q", got)
	}
}

// ---- 分支②集成：取证行先于拉起行落日志 ----

// forensicsDeps 分支②集成装配：真拒绝口 + 假拉起 + 取证面注入。
func forensicsDeps(port int, l *fakeLauncher, e *forensicsEnv, pidAlive func(int) bool) WatchdogDeps {
	return WatchdogDeps{
		Port: port, Timeout: 2 * time.Second, Probe: probeHTTP, Launch: l.launch,
		Logf: e.logf, DataDir: e.dir, PidAlive: pidAlive,
	}
}

func freePortRefused(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close() // 真·拒绝
	return port
}

// 分支② + pid 文件残留：取证行先于「拉起 daemon」行，末次应答从生产同款
// watchdog.log 流入取证行。
func TestWatchdogBranch2ForensicsBeforeLaunch(t *testing.T) {
	e := newForensicsEnv(t)
	e.writePidFile(t, `{"pid":4242,"port":15700,"started_at":"2026-09-29 08:10:05"}`)
	e.writeWatchdogLog(t, wdOKLine("2026-09-29 08:37:00"))
	l := &fakeLauncher{}
	got := runWatchdog(forensicsDeps(freePortRefused(t), l, e, func(int) bool { return false }))
	if got != 0 {
		t.Fatalf("拉起成功应退出 0, got %d", got)
	}
	if l.calls != 1 {
		t.Fatalf("应恰好拉起一次, calls=%d", l.calls)
	}
	iFore := strings.Index(e.joined(), "复位取证")
	iLaunch := strings.Index(e.joined(), "拉起 daemon")
	if iFore < 0 || iLaunch < 0 || iFore > iLaunch {
		t.Fatalf("取证行应先于拉起行: %v", e.logs)
	}
	if !strings.Contains(e.joined(), "无痕死亡") || !strings.Contains(e.joined(), "08:37:00") {
		t.Fatalf("取证行应带无痕结论与末次应答: %v", e.logs)
	}
}

// ---- 拉起后复核（P1③）三分支 ----

// errQueue 探针脚本：按序吐错误，耗尽即 t.Fatal（多探一次=复核面越界）。
func errQueue(t *testing.T, errs ...error) func(string, time.Duration) error {
	idx := 0
	return func(string, time.Duration) error {
		if idx >= len(errs) {
			t.Fatalf("探针被多调一次（第 %d 次）", idx+1)
			return nil
		}
		e := errs[idx]
		idx++
		return e
	}
}

func refusedErr() error { return fmt.Errorf("dial: %w", wsaEConnRefused) }

// recheckDeps 复核装配：脚本探针 + 假拉起 + 复核延迟 1ms。
func recheckDeps(probe func(string, time.Duration) error, l *fakeLauncher, logs *[]string) WatchdogDeps {
	return WatchdogDeps{
		Port: 15700, Timeout: 2 * time.Second, Probe: probe, Launch: l.launch,
		Logf: func(f string, a ...any) { *logs = append(*logs, fmt.Sprintf(f, a...)) },
		VerifyDelay: time.Millisecond,
	}
}

// 复核有响应：复位确认行。
func TestWatchdogLaunchRecheckConfirms(t *testing.T) {
	l := &fakeLauncher{}
	var logs []string
	got := runWatchdog(recheckDeps(errQueue(t, refusedErr(), nil), l, &logs))
	if got != 0 || l.calls != 1 {
		t.Fatalf("应退出 0 且拉起一次: exit=%d calls=%d", got, l.calls)
	}
	if !strings.Contains(strings.Join(logs, "\n"), "拉起后复核有响应——复位确认") {
		t.Fatalf("应留复位确认行: %v", logs)
	}
}

// 复核仍拒绝：疑似引导即死行（09-25 形状的可见性件），退出仍 0（拉起动作
// 已尽，下轮 5 分钟自动再判）。
func TestWatchdogLaunchRecheckStillDead(t *testing.T) {
	l := &fakeLauncher{}
	var logs []string
	got := runWatchdog(recheckDeps(errQueue(t, refusedErr(), refusedErr()), l, &logs))
	if got != 0 {
		t.Fatalf("复核失败不应改变退出码（拉起动作已尽）: %d", got)
	}
	joined := strings.Join(logs, "\n")
	if !strings.Contains(joined, "拉起后复核仍无监听") || !strings.Contains(joined, "引导即死") {
		t.Fatalf("应留引导即死行: %v", logs)
	}
}

// 复核异常（非拒绝，如占用超时）：仅记录。
func TestWatchdogLaunchRecheckOddity(t *testing.T) {
	l := &fakeLauncher{}
	var logs []string
	got := runWatchdog(recheckDeps(errQueue(t, refusedErr(), errors.New("i/o timeout")), l, &logs))
	if got != 0 {
		t.Fatalf("复核异常不应改变退出码: %d", got)
	}
	if !strings.Contains(strings.Join(logs, "\n"), "拉起后复核异常") {
		t.Fatalf("应留复核异常行: %v", logs)
	}
}

// VerifyDelay=0 = 不复核（旧行为）：探针恰被调一次。
func TestWatchdogLaunchNoRecheckByDefault(t *testing.T) {
	l := &fakeLauncher{}
	var logs []string
	deps := WatchdogDeps{
		Port: 15700, Timeout: 2 * time.Second,
		Probe: errQueue(t, refusedErr()), Launch: l.launch,
		Logf: func(f string, a ...any) { *(&logs) = append(logs, fmt.Sprintf(f, a...)) },
	}
	if got := runWatchdog(deps); got != 0 {
		t.Fatalf("应退出 0, got %d", got)
	}
	if strings.Contains(strings.Join(logs, "\n"), "复核") {
		t.Fatalf("VerifyDelay=0 不应复核: %v", logs)
	}
}
