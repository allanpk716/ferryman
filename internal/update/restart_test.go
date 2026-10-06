package update

// restart_test.go — 票07 安全重启帮手（RunRestart）验收钉子。
//
// 验收对照（票面五条之 2 + 旁路钉子）：
//   2. 健康失败→还原上次健康配置→再拉起成功（缝级）：夹具真文件——
//      config.toml=坏内容、last-healthy.toml=好内容；StopOld/Launch 注桩成功、
//      Probe(ToPort) 恒 false、Probe(RollbackPort) 先假后真；断言
//      Result.Success+RolledBack 且 config.toml 字节==last-healthy 字节；
//   7. RunRestart 旁路：无回滚源（RollbackPort=0）→ Alert 缝被调+Success=false；
//      StopOld 失败 → Launch 缝未被调、配置字节未动；
//   补充：首拉即活（Probe(ToPort)=true）→ Success 且 !RolledBack、配置不动、
//      Launch 恰一次（序列早退钉子）。
//
// 时限全部显式压短（GraceBeforeStop=1ms/PollTimeout≤50ms/PollInterval=1ms），
// 缺省 90s 窗不进测试。Restore 缺省走真 copyFile+moveFileReplace（本包平台面），
// 验收2 因此同时钉住还原原语。

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// rrOpts 快速构装测试形 RestartOpts：缝全显式、时限压短。端口钉 25xxx
// 沙箱段、DataDir 钉 t.TempDir()——闪窗事故返工④：主防线是缝注桩（四个
// 测试全部显式注桩 StopOld/Launch/Probe，绝不走到缺省 StopOld=真 postShutdown
// /缺省 Launch=真拉 start-daemon.cmd），沙箱口/沙箱目录只是最后一道保险。
func rrOpts(tmp string) RestartOpts {
	return RestartOpts{
		DataDir:         tmp,
		ConfigPath:      filepath.Join(tmp, "config.toml"),
		LastHealthy:     filepath.Join(tmp, "last-healthy.toml"),
		FromPort:        25700,
		ToPort:          25701,
		RollbackPort:    25700,
		GraceBeforeStop: time.Millisecond,
		PollTimeout:     50 * time.Millisecond,
		PollInterval:    time.Millisecond,
		Logf:            func(string, ...any) {},
	}
}

// TestRunRestartRestoresLastHealthyOnDeadLaunch 验收2：新口恒无应答 → 还原
// last-healthy → 重拉 → 回滚口先假后真地活 → Success+RolledBack；config.toml
// 字节==last-healthy 字节；Launch 恰两次（首拉+回滚拉）。
func TestRunRestartRestoresLastHealthyOnDeadLaunch(t *testing.T) {
	tmp := t.TempDir()
	o := rrOpts(tmp)
	bad := []byte("= [broken config\n")
	good := []byte("[server]\nport = 15700\n")
	if err := os.WriteFile(o.ConfigPath, bad, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(o.LastHealthy, good, 0o600); err != nil {
		t.Fatal(err)
	}
	o.StopOld = func() error { return nil }
	var launchCalls int32
	o.Launch = func() error { atomic.AddInt32(&launchCalls, 1); return nil }
	var rollbackProbes int32
	o.Probe = func(port int) bool {
		if port == o.ToPort {
			return false // 新口恒死
		}
		return atomic.AddInt32(&rollbackProbes, 1) >= 2 // 回滚口先假后真
	}

	res := RunRestart(o)
	if !res.Success || !res.RolledBack {
		t.Fatalf("Result = %+v, want Success+RolledBack", res)
	}
	got, err := os.ReadFile(o.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, good) {
		t.Fatalf("config.toml 未还原为 last-healthy 字节: got %q want %q", got, good)
	}
	if n := atomic.LoadInt32(&launchCalls); n != 2 {
		t.Fatalf("Launch 调用 = %d, want 2（首拉+回滚拉）", n)
	}
}

// TestRunRestartSuccessNoRollback 首拉即活：Probe(ToPort)=true → Success 且
// !RolledBack；配置字节不动（未触碰回滚源）；Launch 恰一次。
func TestRunRestartSuccessNoRollback(t *testing.T) {
	tmp := t.TempDir()
	o := rrOpts(tmp)
	good := []byte("[server]\nport = 15701\n")
	if err := os.WriteFile(o.ConfigPath, good, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(o.LastHealthy, []byte("[server]\nport = 15700\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	o.StopOld = func() error { return nil }
	var launchCalls int32
	o.Launch = func() error { atomic.AddInt32(&launchCalls, 1); return nil }
	o.Probe = func(port int) bool { return port == o.ToPort }

	res := RunRestart(o)
	if !res.Success || res.RolledBack {
		t.Fatalf("Result = %+v, want Success 且未回滚", res)
	}
	got, err := os.ReadFile(o.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, good) {
		t.Fatal("成功路径不得动配置字节")
	}
	if n := atomic.LoadInt32(&launchCalls); n != 1 {
		t.Fatalf("Launch 调用 = %d, want 1", n)
	}
}

// TestRunRestartNoRollbackAlerts 验收7a：无回滚源（RollbackPort=0）且新口恒死
// → Alert 缝被调（标题「Ferryman 安全重启失败」、正文含「无有效回滚源」）+
// Success=false；Restore/回滚不发生（配置字节不动）。
func TestRunRestartNoRollbackAlerts(t *testing.T) {
	tmp := t.TempDir()
	o := rrOpts(tmp)
	bad := []byte("= [broken config\n")
	if err := os.WriteFile(o.ConfigPath, bad, 0o600); err != nil {
		t.Fatal(err)
	}
	o.RollbackPort = 0
	o.StopOld = func() error { return nil }
	o.Launch = func() error { return nil }
	o.Probe = func(int) bool { return false }

	var mu sync.Mutex
	var titles, msgs []string
	o.Alert = func(title, msg string) {
		mu.Lock()
		defer mu.Unlock()
		titles = append(titles, title)
		msgs = append(msgs, msg)
	}

	res := RunRestart(o)
	if res.Success {
		t.Fatalf("Result = %+v, want Success=false", res)
	}
	if res.Err == nil {
		t.Fatal("无回滚源失败应带 Err")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(titles) != 1 {
		t.Fatalf("Alert 调用 = %d, want 1", len(titles))
	}
	if titles[0] != "Ferryman 安全重启失败" {
		t.Fatalf("Alert 标题 = %q", titles[0])
	}
	if !bytes.Contains([]byte(msgs[0]), []byte("无有效回滚源")) {
		t.Fatalf("Alert 正文应含 无有效回滚源: %q", msgs[0])
	}
	got, _ := os.ReadFile(o.ConfigPath)
	if !bytes.Equal(got, bad) {
		t.Fatal("无回滚源路径不得动配置字节")
	}
}

// TestRunRestartStopsFailedCandidateBeforeRestore 评审中·返工（可跟踪路径）：
// 回滚前先收敛首次拉起的失败进程——StopFailedCandidate 被调且发生在 Restore
// 之前（收敛时 config.toml 仍是坏内容）；收敛干净则成功 Detail 不附警示。
func TestRunRestartStopsFailedCandidateBeforeRestore(t *testing.T) {
	tmp := t.TempDir()
	o := rrOpts(tmp)
	bad := []byte("= [broken config\n")
	good := []byte("[server]\nport = 25700\n")
	if err := os.WriteFile(o.ConfigPath, bad, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(o.LastHealthy, good, 0o600); err != nil {
		t.Fatal(err)
	}
	o.StopOld = func() error { return nil }
	o.Launch = func() error { return nil }
	o.Probe = func(port int) bool {
		if port == o.ToPort {
			return false
		}
		return true // 回滚口即活
	}
	convergedBeforeRestore := false
	o.StopFailedCandidate = func() error {
		raw, rerr := os.ReadFile(o.ConfigPath)
		if rerr != nil {
			t.Fatal(rerr)
		}
		convergedBeforeRestore = bytes.Equal(raw, bad) // 收敛时还原尚未发生
		return nil
	}

	res := RunRestart(o)
	if !res.Success || !res.RolledBack {
		t.Fatalf("Result = %+v, want Success+RolledBack", res)
	}
	if !convergedBeforeRestore {
		t.Fatal("StopFailedCandidate 未在 Restore 之前被调（或调用时 config 已被还原）")
	}
	if bytes.Contains([]byte(res.Detail), []byte("未收敛")) {
		t.Fatalf("收敛干净的成功 Detail 不应附警示: %q", res.Detail)
	}
}

// TestRunRestartUntrackableCandidateStillRollsBack 评审中·返工（不可跟踪
// 路径）：收敛缝报错（活着但不可跟踪形态）→ 不 panic、不阻断回滚——仍
// Success+RolledBack、config 照常还原，成功 Detail 附「未收敛的失败进程」
// 警示。
func TestRunRestartUntrackableCandidateStillRollsBack(t *testing.T) {
	tmp := t.TempDir()
	o := rrOpts(tmp)
	bad := []byte("= [broken config\n")
	good := []byte("[server]\nport = 25700\n")
	if err := os.WriteFile(o.ConfigPath, bad, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(o.LastHealthy, good, 0o600); err != nil {
		t.Fatal(err)
	}
	o.StopOld = func() error { return nil }
	o.Launch = func() error { return nil }
	o.Probe = func(port int) bool {
		if port == o.ToPort {
			return false
		}
		return true
	}
	o.StopFailedCandidate = func() error {
		return errors.New("失败候选 PID 4242 活着但映像不可查")
	}

	res := RunRestart(o)
	if !res.Success || !res.RolledBack {
		t.Fatalf("Result = %+v, want 不可跟踪不阻断回滚（Success+RolledBack）", res)
	}
	got, err := os.ReadFile(o.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, good) {
		t.Fatal("不可跟踪路径 config 仍须还原为 last-healthy 字节")
	}
	if !bytes.Contains([]byte(res.Detail), []byte("未收敛的失败进程")) {
		t.Fatalf("成功 Detail 应附未收敛警示: %q", res.Detail)
	}
}

// TestRunRestartStopOldFailSkipsLaunch 验收7b：StopOld 失败 → Launch 缝未被
// 调、Probe 未被调、配置字节未动；Result 如实带停旧错误。
func TestRunRestartStopOldFailSkipsLaunch(t *testing.T) {
	tmp := t.TempDir()
	o := rrOpts(tmp)
	bad := []byte("= [broken config\n")
	if err := os.WriteFile(o.ConfigPath, bad, 0o600); err != nil {
		t.Fatal(err)
	}
	o.StopOld = func() error { return errors.New("端口被陌生进程占着") }
	var launchCalls, probeCalls int32
	o.Launch = func() error { atomic.AddInt32(&launchCalls, 1); return nil }
	o.Probe = func(int) bool { atomic.AddInt32(&probeCalls, 1); return true }

	res := RunRestart(o)
	if res.Success || res.Err == nil {
		t.Fatalf("Result = %+v, want 失败带 Err", res)
	}
	if !bytes.Contains([]byte(res.Err.Error()), []byte("停旧失败")) {
		t.Fatalf("Err 应点名停旧失败: %v", res.Err)
	}
	if n := atomic.LoadInt32(&launchCalls); n != 0 {
		t.Fatalf("StopOld 失败后 Launch 被调 %d 次, want 0", n)
	}
	if n := atomic.LoadInt32(&probeCalls); n != 0 {
		t.Fatalf("StopOld 失败后 Probe 被调 %d 次, want 0", n)
	}
	got, _ := os.ReadFile(o.ConfigPath)
	if !bytes.Equal(got, bad) {
		t.Fatal("停旧失败路径不得动配置字节")
	}
}
