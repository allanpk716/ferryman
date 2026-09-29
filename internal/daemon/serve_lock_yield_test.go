package daemon

// serve_lock_yield_test.go — 票04(规格 Implementation Decisions 第4条,D8):
// 守护层锁让路验收钉子。update.lock 被活监督者持有 → serve 静默让路(退出码
// 0 + 让路日志行,任何装配副作用之前);判定不持有 → 照常起(走到 pid 落盘)。
// 三个测试分层:①桩探针钉 serveConfig 接线(参数/退出码/文案/无 pid);
// ②真锁+真探针钉单源链路(serve→update.LockHeldByLiveSupervisor,daemon
// 侧零桩);③不持有钉照常启动。锁态→判定的映射矩阵在 update 包
// TestLockHeldByLiveSupervisor,此处不重复。

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"ferryman/internal/update"
)

// swapYieldSeams 换掉让路两缝(探针/自身映像),返回还原函数。var 形缝对齐
// FerrySession/logShutdownSource 惯例——fmt 直写 stdout 的行与真实进程面都
// 不进测试断言面。
func swapYieldSeams(t *testing.T, probe func(string, string, func(int) bool, func(int) (string, error)) (int, bool),
	selfExe func() (string, error)) {
	t.Helper()
	origProbe, origExe := lockYieldProbe, serveSelfExe
	t.Cleanup(func() { lockYieldProbe, serveSelfExe = origProbe, origExe })
	if probe != nil {
		lockYieldProbe = probe
	}
	if selfExe != nil {
		serveSelfExe = selfExe
	}
}

// TestServeYieldsWhenUpdateLockHeld 持有者活(桩判持有)→ 静默让路:退出码 0、
// 让路行逐字、判定参数正确(目录=数据目录,换装目标=注入的自身映像)、不落
// pid(让路先于一切装配副作用)。
func TestServeYieldsWhenUpdateLockHeld(t *testing.T) {
	tmp := t.TempDir()
	dataDir := filepath.Join(tmp, "data")
	cfg := serveTestCfg(t, freePort(t), dataDir)

	var gotDir, gotTarget string
	swapYieldSeams(t,
		func(dir, targetExe string, _ func(int) bool, _ func(int) (string, error)) (int, bool) {
			gotDir, gotTarget = dir, targetExe
			return 4242, true
		},
		func() (string, error) { return `C:\install\ferryman.exe`, nil })

	read := captureStdout(t)
	code := serveConfig(cfg, context.Background(), "dev")
	out := read()
	if code != 0 {
		t.Fatalf("让路应 return 0, got %d", code)
	}
	want := "[ferryman] 升级事务进行中（持有者 PID 4242）——本实例静默让路"
	if !containsLine(out, want) {
		t.Fatalf("让路文案缺失:\nwant: %s\ngot:  %q", want, out)
	}
	if gotDir != dataDir {
		t.Fatalf("判定目录 = %q, want 数据目录 %q", gotDir, dataDir)
	}
	if gotTarget != `C:\install\ferryman.exe` {
		t.Fatalf("换装目标 = %q, want 注入的自身映像", gotTarget)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "daemon.pid")); !os.IsNotExist(err) {
		t.Fatal("让路路径不得写 pid 文件")
	}
}

// TestServeYieldViaRealUpdateProbe 单源链路全真:不换任何缝——真锁文件
// (持有者 = 测试进程自身,映像 = 测试二进制 = serveSelfExe 缺省所得)+真
// LockHeldByLiveSupervisor+真平台探针。持有者活且映像==换装目标 → 让路,
// PID 逐字。此测试锁死「daemon 不复制判定」:判定一旦在 daemon 侧另写,
// 本测试与 update 包的矩阵测试就撕不开责任面。
func TestServeYieldViaRealUpdateProbe(t *testing.T) {
	tmp := t.TempDir()
	dataDir := filepath.Join(tmp, "data")
	cfg := serveTestCfg(t, freePort(t), dataDir)

	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	type lockJSON struct {
		PID        int    `json:"pid"`
		Image      string `json:"image"`
		Generation int    `json:"generation"`
		StartedAt  string `json:"started_at"`
	}
	b, err := json.Marshal(lockJSON{PID: os.Getpid(), Image: self, Generation: 1,
		StartedAt: time.Now().Format(time.RFC3339)})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil { // WriteFile 不建父目录
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "update.lock"), b, 0o644); err != nil {
		t.Fatal(err)
	}

	read := captureStdout(t)
	code := serveConfig(cfg, context.Background(), "dev")
	out := read()
	if code != 0 {
		t.Fatalf("让路应 return 0, got %d", code)
	}
	want := fmt.Sprintf("[ferryman] 升级事务进行中（持有者 PID %d）——本实例静默让路", os.Getpid())
	if !containsLine(out, want) {
		t.Fatalf("让路文案缺失:\nwant: %s\ngot:  %q", want, out)
	}
}

// TestServeStartsWhenLockNotHeld 判定不持有(桩)→ 照常启动:走到 pid 落盘
// (越过让路点、绑定成功),ctx 取消优雅停 return 0。锁不存在/陈旧/映像无关
// 的具体映射在 update 包矩阵测试;此处钉 daemon 侧「false → 正常起」。
func TestServeStartsWhenLockNotHeld(t *testing.T) {
	tmp := t.TempDir()
	dataDir := filepath.Join(tmp, "data")
	port := freePort(t)
	cfg := serveTestCfg(t, port, dataDir)

	swapYieldSeams(t,
		func(string, string, func(int) bool, func(int) (string, error)) (int, bool) {
			return 0, false
		},
		func() (string, error) { return `C:\install\ferryman.exe`, nil })

	ctx, cancel := context.WithCancel(context.Background())
	codeCh := make(chan int, 1)
	go func() { codeCh <- serveConfig(cfg, ctx, "dev") }()

	waitPidFile(t, filepath.Join(dataDir, "daemon.pid"), 10*time.Second) // 越过让路点的铁证
	cancel()
	select {
	case code := <-codeCh:
		if code != 0 {
			t.Fatalf("优雅停应 return 0, got %d", code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("serveConfig 未在 ctx 取消后返回")
	}
}

// TestServeSupervisorLaunchExemptYield 监督者自拉起豁免(票04 集成缺陷修):
// 判定面说"锁被持有",但守护带着监督者注入的环境标记启动 → 不让路,
// 照常起(pid 落盘)。豁免失效则本测试在让路早退处等不到 pid 超时。
func TestServeSupervisorLaunchExemptYield(t *testing.T) {
	tmp := t.TempDir()
	dataDir := filepath.Join(tmp, "data")
	port := freePort(t)
	cfg := serveTestCfg(t, port, dataDir)

	swapYieldSeams(t,
		func(string, string, func(int) bool, func(int) (string, error)) (int, bool) {
			return 4242, true
		},
		func() (string, error) { return `C:\install\ferryman.exe`, nil })
	t.Setenv(update.SupervisorLaunchEnv, "1")

	ctx, cancel := context.WithCancel(context.Background())
	codeCh := make(chan int, 1)
	go func() { codeCh <- serveConfig(cfg, ctx, "dev") }()

	waitPidFile(t, filepath.Join(dataDir, "daemon.pid"), 10*time.Second)
	cancel()
	select {
	case code := <-codeCh:
		if code != 0 {
			t.Fatalf("豁免路径优雅停应 return 0, got %d", code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("serveConfig 未在 ctx 取消后返回")
	}
}
