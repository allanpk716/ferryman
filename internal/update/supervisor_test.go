package update

// 监督者全流程测试(票05 验收面):httptest 伪 GitHub + 测试内替身 exe +
// 随机口 + TEMP 数据目录——绝不碰生产 7311/15900 与仓库根 ferryman.exe。

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// updateWorld 测试世界:数据目录(锁/journal/token/pid)+ exe 目录(换装
// 目标/点火脚本/备份)+ 伪 GitHub。rels nil = 缺省一个 v0.2.0 release。
type updateWorld struct {
	dataDir, exeDir    string
	exePath, cmdPath   string
	port               int
	token              string
	oldBytes, newBytes []byte
	srv                *httptest.Server
	logs               *syncBuf
	cfg                Config
}

// newUpdateWorld 造世界与监督者;mod 可再改 Config(如缩短等待)。
func newUpdateWorld(t *testing.T, rels []fakeRel, mod func(*Config, *updateWorld)) (*updateWorld, *Supervisor) {
	t.Helper()
	dataDir := t.TempDir()
	exeDir := t.TempDir()
	exePath := filepath.Join(exeDir, "ferryman.exe")
	oldBytes := markedTestBinary(t, "v0.1.0")
	if err := os.WriteFile(exePath, oldBytes, 0o755); err != nil {
		t.Fatal(err)
	}
	port := freePort(t)
	token := randToken(t)
	if err := os.WriteFile(filepath.Join(dataDir, "daemon.token"), []byte(token), 0o600); err != nil {
		t.Fatal(err)
	}
	cmdPath := writeStandinCmd(t, dataDir, exePath, port, token)
	newBytes := markedTestBinary(t, "v0.2.0")
	if rels == nil {
		rels = []fakeRel{{tag: "v0.2.0", bytes: newBytes}}
	}
	srv := startFakeGH(t, rels)
	logs := &syncBuf{}
	w := &updateWorld{dataDir: dataDir, exeDir: exeDir, exePath: exePath, cmdPath: cmdPath,
		port: port, token: token, oldBytes: oldBytes, newBytes: newBytes,
		srv: srv, logs: logs}
	cfg := Config{
		DataDir:      dataDir,
		Port:         port,
		Endpoints:    fakeEps(srv),
		Current:      "v0.1.0",
		StartCmd:     cmdPath,
		PortWait:     1500 * time.Millisecond,
		PollTimeout:  4 * time.Second,
		PollInterval: 80 * time.Millisecond,
		// 探针窗测试尺寸(生产缺省 2s/10s 由 TestProbeDefaultsMatchSpec 钉):
		// 首测 50ms 后开始,2s 内应答即过——替身启动常在数百 ms,过窗只是
		// 多一条告警日志,不影响流程断言(探针只报告不裁决)。
		ProbeDelay:  50 * time.Millisecond,
		ProbeTimeout: 2 * time.Second,
		Logf:         func(f string, a ...any) { fmt.Fprintf(logs, f+"\n", a...) },
	}
	if mod != nil {
		mod(&cfg, w)
	}
	w.cfg = cfg
	sup := NewSupervisor(cfg)
	t.Cleanup(func() { w.stopAll() })
	return w, sup
}

// stopAll 收尾不留孤儿替身:优雅 /shutdown 优先;pid 兜底杀(杀前核映像在
// TEMP 下,防 PID 复用误伤)。
func (w *updateWorld) stopAll() {
	if _, ok := httpStatsVersion(w.port, w.token); ok {
		client := &http.Client{Timeout: 2 * time.Second}
		req, err := http.NewRequest(http.MethodPost,
			fmt.Sprintf("http://127.0.0.1:%d/shutdown", w.port), nil)
		if err == nil {
			req.Header.Set("Authorization", "Bearer "+w.token)
			if resp, err := client.Do(req); err == nil {
				_ = resp.Body.Close()
			}
		}
		time.Sleep(250 * time.Millisecond)
	}
	if b, err := os.ReadFile(filepath.Join(w.dataDir, "daemon.pid")); err == nil {
		var pj struct {
			PID int `json:"pid"`
		}
		if err := json.Unmarshal(b, &pj); err == nil && pj.PID > 0 {
			// 护栏:映像必须落在本世界 exeDir 内才杀——go test 二进制本身也在
			// TEMP 下(go-build 缓存),只看 TEMP 会误杀测试进程。
			if img, ierr := procImageImpl(pj.PID); ierr == nil && strings.Contains(strings.ToLower(img), strings.ToLower(w.exeDir)) {
				_ = killImpl(pj.PID)
			}
		}
	}
}

func fileBytes(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func noResidue(t *testing.T, exeDir string) {
	t.Helper()
	for _, n := range []string{"ferryman.exe.new", "ferryman.exe.new.part"} {
		if _, err := os.Stat(filepath.Join(exeDir, n)); err == nil {
			t.Fatalf("%s 应无残留", n)
		}
	}
	m, _ := filepath.Glob(filepath.Join(exeDir, "ferryman.exe.swap-tmp*"))
	if len(m) > 0 {
		t.Fatalf("swap-tmp 残留: %v", m)
	}
}

// ---- 验收 1:成功路径换文件+备份+新版校验通过 ----

func TestSupervisorSuccess(t *testing.T) {
	w, sup := newUpdateWorld(t, nil, nil)
	startStandinProcess(t, w.exePath, w.port, w.token, w.dataDir, false) // 旧守护
	waitServe(t, w.port, w.token, "v0.1.0")

	res := sup.Run()
	if !res.Success {
		t.Fatalf("应成功: %+v\n日志:\n%s", res, w.logs.String())
	}
	if res.From != "v0.1.0" || res.To != "v0.2.0" {
		t.Fatalf("版本结论 = %s → %s", res.From, res.To)
	}
	// 换文件:目标 exe 内容 == 新版字节
	if got := fileBytes(t, w.exePath); string(got) != string(w.newBytes) {
		t.Fatal("换装目标 exe 内容不是新版")
	}
	// 备份:旧字节
	if got := fileBytes(t, filepath.Join(w.exeDir, "ferryman.exe.old-v0.1.0")); string(got) != string(w.oldBytes) {
		t.Fatal("备份内容不是旧版")
	}
	// 新版在服务且报目标版本
	if v, ok := httpStatsVersion(w.port, w.token); !ok || v != "v0.2.0" {
		t.Fatalf("新版未在服务: %q %v", v, ok)
	}
	// journal/锁清账;无残留
	if _, exists, _ := loadJournal(w.dataDir); exists {
		t.Fatal("journal 未清账")
	}
	if _, err := os.Stat(filepath.Join(w.dataDir, lockName)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("锁未释放")
	}
	noResidue(t, w.exeDir)
}

// ---- 验收 2:SHA256 失败 → 拒绝替换,现场原样 ----

func TestSupervisorSHA256Fail(t *testing.T) {
	w, sup := newUpdateWorld(t, []fakeRel{{tag: "v0.2.0", bytes: markedTestBinary(t, "v0.2.0"), corruptSha: true}}, nil)
	startStandinProcess(t, w.exePath, w.port, w.token, w.dataDir, false)
	waitServe(t, w.port, w.token, "v0.1.0")

	res := sup.Run()
	if res.Success || res.Err == nil || !strings.Contains(res.Err.Error(), "SHA256") {
		t.Fatalf("应 SHA256 校验失败: %+v", res)
	}
	if res.RolledBack {
		t.Fatal("未到换装,不应有回滚标记")
	}
	// 现场原样:exe 内容未动、旧版仍在服务、journal 清、无残留
	if got := fileBytes(t, w.exePath); string(got) != string(w.oldBytes) {
		t.Fatal("exe 被动了——SHA256 失败必须拒绝替换")
	}
	if v, ok := httpStatsVersion(w.port, w.token); !ok || v != "v0.1.0" {
		t.Fatal("旧版服务应不受影响")
	}
	if _, exists, _ := loadJournal(w.dataDir); exists {
		t.Fatal("journal 未清账")
	}
	noResidue(t, w.exeDir)
}

// ---- 验收 3:新版探活/版本校验失败 → 自动回滚,旧版重新服务 ----

func TestSupervisorVerifyFailRollsBack(t *testing.T) {
	garbage := []byte("this is not a real windows executable - broken new build")
	w, sup := newUpdateWorld(t, []fakeRel{{tag: "v0.2.0", bytes: garbage}}, nil)
	startStandinProcess(t, w.exePath, w.port, w.token, w.dataDir, false)
	waitServe(t, w.port, w.token, "v0.1.0")

	res := sup.Run()
	if res.Success {
		t.Fatalf("垃圾新版应失败: %+v", res)
	}
	if !res.RolledBack {
		t.Fatalf("应已回滚: %+v", res)
	}
	if res.RollbackErr != "" {
		t.Fatalf("回滚不应再有错误: %s", res.RollbackErr)
	}
	// exe 恢复旧字节;备份保留
	if got := fileBytes(t, w.exePath); string(got) != string(w.oldBytes) {
		t.Fatal("回滚后 exe 不是旧版")
	}
	if got := fileBytes(t, filepath.Join(w.exeDir, "ferryman.exe.old-v0.1.0")); string(got) != string(w.oldBytes) {
		t.Fatal("回滚后备份应保留(copy 语义)")
	}
	// 旧版重新服务
	waitServe(t, w.port, w.token, "v0.1.0")
	if _, exists, _ := loadJournal(w.dataDir); exists {
		t.Fatal("回滚完成后 journal 未清账")
	}
	noResidue(t, w.exeDir)
}

// ---- 验收 4a:并发取锁——双 update 仅一胜,败者收「升级进行中」 ----

func TestSupervisorConcurrentLockOneWins(t *testing.T) {
	w, supA := newUpdateWorld(t, nil, nil)
	// 进程内模拟「监督者从换装目标 exe 跑」:本测试进程映像谎报为目标 exe,
	// 使锁的存活判定(seam B)走真路径。
	fakeImage := func(pid int) (string, error) {
		if pid == os.Getpid() {
			return w.exePath, nil
		}
		return procImageImpl(pid)
	}
	supA.procImage = fakeImage
	startStandinProcess(t, w.exePath, w.port, w.token, w.dataDir, false)
	waitServe(t, w.port, w.token, "v0.1.0")

	release := make(chan struct{})
	launched := make(chan struct{}, 1)
	supA.launch = func(cmdPath string) error {
		select { // 只在首次放行信号,此后阻塞持锁
		case launched <- struct{}{}:
		default:
		}
		<-release
		return launchCmdImpl(cmdPath)
	}
	doneA := make(chan Result, 1)
	go func() { doneA <- supA.Run() }()
	<-launched // A 已持锁进入 verify 期

	supB := NewSupervisor(w.cfg)
	supB.procImage = fakeImage
	resB := supB.Run()
	if resB.Success || !errors.Is(resB.Err, ErrUpdateInProgress) {
		t.Fatalf("败者应收「升级进行中」: %+v", resB)
	}
	if !strings.Contains(resB.Err.Error(), "升级进行中") {
		t.Fatalf("错误文案 = %q", resB.Err.Error())
	}

	close(release)
	resA := <-doneA
	if !resA.Success {
		t.Fatalf("胜者应完成: %+v\n日志:\n%s", resA, w.logs.String())
	}
}

// ---- 验收 4b:活锁持有者(真进程,映像==换装目标)→ 进行中;锁原样 ----

func TestSupervisorBusyOnLiveLock(t *testing.T) {
	w, sup := newUpdateWorld(t, nil, nil)
	// 持有者:从换装目标 exe 起的活替身(真实映像路径,seam B 全真)
	holderPID := startStandinProcess(t, w.exePath, w.port, w.token, t.TempDir(), false)
	waitServe(t, w.port, w.token, "v0.1.0") // 映像尾部标记 v0.1.0 即其报出版本
	writeLock(t, w.dataDir, lockInfo{PID: holderPID, Image: w.exePath, Generation: 2})

	res := sup.Run()
	if res.Success || !errors.Is(res.Err, ErrUpdateInProgress) {
		t.Fatalf("应报进行中: %+v", res)
	}
	if got := readLockFor(t, w.dataDir).PID; got != holderPID {
		t.Fatalf("锁文件应原样保留持有者: %d", got)
	}
}

// ---- 验收 4c:陈旧锁接管(真死 PID)——全流程可继续 ----

func TestSupervisorTakesOverStaleLock(t *testing.T) {
	w, sup := newUpdateWorld(t, nil, nil)
	deadPID := spawnDeadProcess(t)
	writeLock(t, w.dataDir, lockInfo{PID: deadPID, Image: w.exePath, Generation: 1})
	startStandinProcess(t, w.exePath, w.port, w.token, w.dataDir, false)
	waitServe(t, w.port, w.token, "v0.1.0")

	res := sup.Run()
	if !res.Success {
		t.Fatalf("陈旧锁应被接管并完成: %+v\n日志:\n%s", res, w.logs.String())
	}
	if !strings.Contains(w.logs.String(), "接管") {
		t.Fatal("应有接管日志")
	}
}

// ---- 验收 5:kill 身份校验——假 PID 指向无关映像 → 拒杀(真 Windows API) ----

func TestStopDaemonKillIdentityRefused(t *testing.T) {
	w, sup := newUpdateWorld(t, nil, nil)
	// 占口者:/stats 应答但 /shutdown 404(模拟端点不可达的老版本)
	hogPath := filepath.Join(t.TempDir(), "hog.exe")
	buildStandinExe(t, hogPath, "v9.9.9-hog")
	startStandinProcess(t, hogPath, w.port, w.token, t.TempDir(), true)
	waitServe(t, w.port, w.token, "v9.9.9-hog")

	// daemon.pid 指向「活但映像无关」的 PID(本测试进程)
	if err := os.WriteFile(filepath.Join(w.dataDir, "daemon.pid"),
		[]byte(fmt.Sprintf(`{"pid":%d,"port":%d}`, os.Getpid(), w.port)), 0o644); err != nil {
		t.Fatal(err)
	}
	sup.cfg.PortWait = 800 * time.Millisecond

	err := sup.stopDaemon(w.exePath, "停旧")
	if err == nil || !strings.Contains(err.Error(), "拒杀") {
		t.Fatalf("应拒杀无关映像: %v", err)
	}
	// 占口者未被杀
	if v, ok := httpStatsVersion(w.port, w.token); !ok || v != "v9.9.9-hog" {
		t.Fatal("占口者不应被杀")
	}
}

// ---- 验收 6:看门抢跑演练——verify 期旧版被重拉 → 复停重试路径命中 ----

func TestSupervisorRacerSeamC(t *testing.T) {
	w, sup := newUpdateWorld(t, nil, nil)
	startStandinProcess(t, w.exePath, w.port, w.token, w.dataDir, false)
	waitServe(t, w.port, w.token, "v0.1.0")

	// 抢跑者:另一个目录里报旧版的替身(模拟看门从旧映像重拉)
	racerPath := filepath.Join(t.TempDir(), "racer.exe")
	buildStandinExe(t, racerPath, "v0.1.0")
	racerData := t.TempDir()
	var calls int32
	var racerPID int32
	sup.launch = func(cmdPath string) error {
		if atomic.AddInt32(&calls, 1) == 1 {
			// 首次拉起 = 看门抢跑先占口
			atomic.StoreInt32(&racerPID, int32(startStandinProcess(t, racerPath, w.port, w.token, racerData, false)))
			waitServe(t, w.port, w.token, "v0.1.0")
			return nil
		}
		return launchCmdImpl(cmdPath)
	}

	res := sup.Run()
	if !res.Success {
		t.Fatalf("复停重拉后应成功: %+v\n日志:\n%s", res, w.logs.String())
	}
	if !strings.Contains(w.logs.String(), "复停") {
		t.Fatal("seam C 复停路径未命中")
	}
	if n := atomic.LoadInt32(&calls); n != 2 {
		t.Fatalf("拉起次数 = %d, want 2(抢跑占位 + 重拉)", n)
	}
	waitServe(t, w.port, w.token, "v0.2.0")
	if got := fileBytes(t, w.exePath); string(got) != string(w.newBytes) {
		t.Fatal("exe 未换为新版")
	}
	// 抢跑者已被复停(优雅退,进程不在)
	deadline := time.Now().Add(5 * time.Second)
	for procAliveImpl(int(atomic.LoadInt32(&racerPID))) && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}
	if procAliveImpl(int(atomic.LoadInt32(&racerPID))) {
		t.Fatal("抢跑者应已被复停")
	}
}

// ---- 验收 7:journal 崩溃恢复三态 ----

// 7a:staging 中断 → 清残留续跑。
func TestRecoverStagingResidue(t *testing.T) {
	w, sup := newUpdateWorld(t, nil, nil)
	if err := os.WriteFile(newExePath(w.exePath), []byte("half-written"), 0o644); err != nil {
		t.Fatal(err)
	}
	saveJournal(w.dataDir, journal{Phase: PhaseStaging, From: "v0.1.0", To: "v0.2.0",
		TargetExe: w.exePath, NewExe: newExePath(w.exePath),
		Backup: backupPath(w.exeDir, "v0.1.0"), StartCmd: w.cmdPath})
	startStandinProcess(t, w.exePath, w.port, w.token, w.dataDir, false)
	waitServe(t, w.port, w.token, "v0.1.0")

	res := sup.Run()
	if !res.Success {
		t.Fatalf("清残留续跑应成功: %+v\n日志:\n%s", res, w.logs.String())
	}
	if !strings.Contains(w.logs.String(), "staging 中断残留") {
		t.Fatal("应有 staging 残留识别日志")
	}
	waitServe(t, w.port, w.token, "v0.2.0")
}

// 7b:swap 后中断(旧版仍在服务,swap 实未发生)→ 健康清账续跑。
func TestRecoverSwapDaemonStillOld(t *testing.T) {
	w, sup := newUpdateWorld(t, nil, nil)
	saveJournal(w.dataDir, journal{Phase: PhaseSwap, From: "v0.1.0", To: "v0.2.0",
		TargetExe: w.exePath, NewExe: newExePath(w.exePath),
		Backup: backupPath(w.exeDir, "v0.1.0"), StartCmd: w.cmdPath})
	startStandinProcess(t, w.exePath, w.port, w.token, w.dataDir, false)
	waitServe(t, w.port, w.token, "v0.1.0")

	res := sup.Run()
	if !res.Success {
		t.Fatalf("健康清账后续跑应成功: %+v\n日志:\n%s", res, w.logs.String())
	}
	if !strings.Contains(w.logs.String(), "清账续跑") {
		t.Fatal("应有清账续跑日志")
	}
	waitServe(t, w.port, w.token, "v0.2.0")
}

// 7c:swap 后中断(服务也没了,盘上仍是旧版)→ 拉起核对后续跑。
func TestRecoverSwapNoService(t *testing.T) {
	w, sup := newUpdateWorld(t, nil, nil)
	saveJournal(w.dataDir, journal{Phase: PhaseSwap, From: "v0.1.0", To: "v0.2.0",
		TargetExe: w.exePath, NewExe: newExePath(w.exePath),
		Backup: backupPath(w.exeDir, "v0.1.0"), StartCmd: w.cmdPath})
	// 无守护在跑

	res := sup.Run()
	if !res.Success {
		t.Fatalf("无服务恢复后续跑应成功: %+v\n日志:\n%s", res, w.logs.String())
	}
	waitServe(t, w.port, w.token, "v0.2.0")
}

// 7d:verify 中断(盘上已是新版,服务未起)→ 拉起核对为健康 → 清账短路,
// 本次零下载(资产直链零命中)。
func TestRecoverVerifyAlreadyNew(t *testing.T) {
	w, sup := newUpdateWorld(t, nil, nil)
	if err := os.WriteFile(w.exePath, w.newBytes, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(backupPath(w.exeDir, "v0.1.0"), w.oldBytes, 0o755); err != nil {
		t.Fatal(err)
	}
	saveJournal(w.dataDir, journal{Phase: PhaseVerify, From: "v0.1.0", To: "v0.2.0",
		TargetExe: w.exePath, NewExe: newExePath(w.exePath),
		Backup: backupPath(w.exeDir, "v0.1.0"), StartCmd: w.cmdPath})

	var assetHits int32
	orig := w.srv.Config.Handler
	w.srv.Config.Handler = http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/download/") {
			atomic.AddInt32(&assetHits, 1)
		}
		orig.ServeHTTP(rw, r)
	})

	res := sup.Run()
	if !res.Success || res.To != "v0.2.0" {
		t.Fatalf("上次升级已落地应短路成功: %+v\n日志:\n%s", res, w.logs.String())
	}
	if n := atomic.LoadInt32(&assetHits); n != 0 {
		t.Fatalf("资产直链命中 = %d, want 0(不应重复下载)", n)
	}
	waitServe(t, w.port, w.token, "v0.2.0")
}

// 7e:verify 中断且不健康(盘上是垃圾新版)→ 按备份回滚恢复旧版,再续跑
// 本次升级至成功。
func TestRecoverVerifyUnhealthyRollsBack(t *testing.T) {
	garbage := []byte("broken half-swapped exe")
	w, sup := newUpdateWorld(t, nil, nil)
	if err := os.WriteFile(w.exePath, garbage, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(backupPath(w.exeDir, "v0.1.0"), w.oldBytes, 0o755); err != nil {
		t.Fatal(err)
	}
	saveJournal(w.dataDir, journal{Phase: PhaseVerify, From: "v0.1.0", To: "v0.2.0",
		TargetExe: w.exePath, NewExe: newExePath(w.exePath),
		Backup: backupPath(w.exeDir, "v0.1.0"), StartCmd: w.cmdPath})

	res := sup.Run()
	if !res.Success {
		t.Fatalf("回滚后续跑应成功: %+v\n日志:\n%s", res, w.logs.String())
	}
	if !strings.Contains(w.logs.String(), "按备份") {
		t.Fatal("应有按备份回滚日志")
	}
	waitServe(t, w.port, w.token, "v0.2.0")
}

// ---- 验收 8:备份保留 2 份自动清理(全流程) ----

func TestSupervisorPrunesBackups(t *testing.T) {
	w, sup := newUpdateWorld(t, nil, nil)
	stale1 := backupPath(w.exeDir, "v0.0.1")
	stale2 := backupPath(w.exeDir, "v0.0.2")
	if err := os.WriteFile(stale1, []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stale2, []byte("b"), 0o644); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	_ = os.Chtimes(stale1, now.Add(-2*time.Hour), now.Add(-2*time.Hour))
	_ = os.Chtimes(stale2, now.Add(-1*time.Hour), now.Add(-1*time.Hour))

	startStandinProcess(t, w.exePath, w.port, w.token, w.dataDir, false)
	waitServe(t, w.port, w.token, "v0.1.0")

	res := sup.Run()
	if !res.Success {
		t.Fatalf("应成功: %+v\n日志:\n%s", res, w.logs.String())
	}
	// 只留 2 份:本次备份(v0.1.0)+ 较新的 v0.0.2;最老 v0.0.1 被清
	if _, err := os.Stat(filepath.Join(w.exeDir, "ferryman.exe.old-v0.1.0")); err != nil {
		t.Fatal("本次备份应保留")
	}
	if _, err := os.Stat(stale2); err != nil {
		t.Fatal("较新陈旧备份应保留")
	}
	if _, err := os.Stat(stale1); err == nil {
		t.Fatal("超出 2 份的最老备份应被清理")
	}
}

// ---- W2 票03:事务拉起直连 cmd + 拉起验证探针(ADR-0015 2026-09-29 补记) ----

// TestTransactionalLaunchIsCmdDirect 事务拉起不再走 wscript/VBS:隐藏 VBS 在位
// (launchCmdImpl 旧形态在此必优先命中 wscript)时,launch 缺省注入仍直拉
// cmd.exe /c——标记只出现 CMD、绝不出现 VBS。VBS 三级转手吞 stderr 且成败
// 不验(07:31 事故第一根因),事务级拉起必须直连可验;Run 键/看门/蜂群的
// VBS 链不经此路径(launchCmdImpl 与 installer 包通道维持不变)。
func TestTransactionalLaunchIsCmdDirect(t *testing.T) {
	w, sup := newUpdateWorld(t, nil, nil)
	marker := filepath.Join(w.dataDir, "marker.txt")
	// 覆写点火脚本:写 CMD 标记即退(探针语义另测,此处只验拉起通道)。
	if err := os.WriteFile(w.cmdPath,
		[]byte("@echo off\r\necho CMD>> \""+marker+"\"\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// 同目录摆上隐藏点火 VBS:若走 VBS 链,它会先写 VBS 标记再转拉 cmd。
	vbs := filepath.Join(filepath.Dir(w.cmdPath), hiddenLauncherName)
	vbsSrc := "CreateObject(\"Scripting.FileSystemObject\").OpenTextFile(\"" + marker +
		"\", 8, True).WriteLine \"VBS\"\r\n"
	if err := os.WriteFile(vbs, []byte(vbsSrc), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := sup.launch(w.cmdPath); err != nil { // 缺省注入 = 事务拉起形态
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(marker); err == nil {
			if strings.Contains(string(b), "VBS") {
				t.Fatal("事务拉起走了 VBS 链——应直连 cmd.exe")
			}
			if strings.Contains(string(b), "CMD") {
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("10s 内未见 CMD 标记——事务拉起未执行点火脚本")
}

// TestProbeDefaultsMatchSpec 探针窗缺省:launch 后 2s 起测、10s(自拉起计)
// 截止(spec W2 Implementation Decisions 3)。(测试世界已显式给小窗,故直构
// NewSupervisor 验缺省,同 TestDefaultPortWaitCoversDrain 手法。)
func TestProbeDefaultsMatchSpec(t *testing.T) {
	sup := NewSupervisor(Config{DataDir: t.TempDir(),
		Logf: func(string, ...any) {}})
	if sup.cfg.ProbeDelay != 2*time.Second || sup.cfg.ProbeTimeout != 10*time.Second {
		t.Fatalf("探针缺省 = delay %v / timeout %v, want 2s / 10s",
			sup.cfg.ProbeDelay, sup.cfg.ProbeTimeout)
	}
}

// TestVerifyLaunchProbeTargetsConfiguredPort 探针端点取 cfg.Port:同款拉起,
// Port 指向有应答者则通过且零告警;指向死口则报「拉起验证失败（相位错误）」
// + 告警,且 launchTx 不因此报错(探针只报告,不改变事务走向)。死口分支同时
// 钉死「不硬编码 15700」——若探针偷瞄生产口,生产守护在场时死口分支必假通过。
func TestVerifyLaunchProbeTargetsConfiguredPort(t *testing.T) {
	w, sup := newUpdateWorld(t, nil, func(c *Config, _ *updateWorld) {
		c.ProbeDelay, c.ProbeTimeout = 30*time.Millisecond, 400*time.Millisecond
	})
	startStandinProcess(t, w.exePath, w.port, w.token, t.TempDir(), false)
	waitServe(t, w.port, w.token, "v0.1.0")

	var alerts []string
	sup.cfg.Alert = func(title, msg string) { alerts = append(alerts, title+"|"+msg) }
	sup.launch = func(string) error { return nil } // 只验探针语义,不真 spawn

	if err := sup.launchTx(w.cmdPath); err != nil {
		t.Fatalf("launchTx = %v(探针失败不得改变事务走向)", err)
	}
	if len(alerts) != 0 {
		t.Fatalf("端点应答不应有告警: %v", alerts)
	}
	if !strings.Contains(w.logs.String(), "拉起验证通过") {
		t.Fatalf("应有通过日志:\n%s", w.logs.String())
	}

	// 同一世界换监督者:Port 指向随机死口(无监听)。
	logs2 := &syncBuf{}
	var alerts2 []string
	cfg2 := w.cfg
	cfg2.Port = freePort(t)
	cfg2.ProbeDelay, cfg2.ProbeTimeout = 30*time.Millisecond, 400*time.Millisecond
	cfg2.Logf = func(f string, a ...any) { fmt.Fprintf(logs2, f+"\n", a...) }
	cfg2.Alert = func(title, msg string) { alerts2 = append(alerts2, title+"|"+msg) }
	sup2 := NewSupervisor(cfg2)
	sup2.launch = func(string) error { return nil }

	if err := sup2.launchTx(w.cmdPath); err != nil {
		t.Fatalf("launchTx = %v(探针失败只报告,不报错)", err)
	}
	if len(alerts2) != 1 || !strings.Contains(alerts2[0], "相位错误") {
		t.Fatalf("死口应恰一条相位错误告警: %v", alerts2)
	}
	if !strings.Contains(logs2.String(), "拉起验证失败（相位错误）") {
		t.Fatalf("死口应有相位错误日志:\n%s", logs2.String())
	}
}

// TestVerifyLaunchProbeAcceptsAnyStatus 成功谓词=任意 HTTP 应答(含 404,
// probeDaemonAny 同语义):占口者只会 404,探针仍须判过——守护起来即应答,
// 鉴权与路由不构成前提。
func TestVerifyLaunchProbeAcceptsAnyStatus(t *testing.T) {
	w, sup := newUpdateWorld(t, nil, func(c *Config, _ *updateWorld) {
		c.ProbeDelay, c.ProbeTimeout = 20*time.Millisecond, 600*time.Millisecond
	})
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	})
	srv := &http.Server{Handler: mux}
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", w.port))
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })

	sup.launch = func(string) error { return nil }
	if err := sup.launchTx(w.cmdPath); err != nil {
		t.Fatalf("launchTx = %v", err)
	}
	if !strings.Contains(w.logs.String(), "拉起验证通过") {
		t.Fatalf("404 应答应判探针通过:\n%s", w.logs.String())
	}
}

// TestAlertFailureDoesNotBlock 告警通道故障(坏回调 panic)绝不传染事务:
// alert 侧 recover 吞掉、只落日志留证(与 notify 包「尽力而为的旁路」同纪律)。
func TestAlertFailureDoesNotBlock(t *testing.T) {
	w, sup := newUpdateWorld(t, nil, nil)
	sup.cfg.Alert = func(string, string) { panic("通知通道坏了") }
	sup.alert("Ferryman 升级", "拉起验证失败（相位错误）") // 不得外泄 panic
	if !strings.Contains(w.logs.String(), "告警通道故障") {
		t.Fatalf("通道故障应落日志留证:\n%s", w.logs.String())
	}
}

// TestLaunchVerifyFailAlertsButStillRollsBack 端到端:垃圾新版拉不起 →
// 探针报「拉起验证失败（相位错误）」+ 告警;事务走向不变——回滚仍由既有
// 版本校验裁决(结论为校验失败)并成功恢复旧版服务。
func TestLaunchVerifyFailAlertsButStillRollsBack(t *testing.T) {
	garbage := []byte("this is not a real windows executable - probe phase regression")
	w, sup := newUpdateWorld(t, []fakeRel{{tag: "v0.2.0", bytes: garbage}}, nil)
	var alerts []string
	sup.cfg.Alert = func(title, msg string) { alerts = append(alerts, title+"|"+msg) }
	startStandinProcess(t, w.exePath, w.port, w.token, w.dataDir, false)
	waitServe(t, w.port, w.token, "v0.1.0")

	res := sup.Run()
	if res.Success || !res.RolledBack {
		t.Fatalf("垃圾新版应回滚: %+v\n日志:\n%s", res, w.logs.String())
	}
	if !strings.Contains(res.Err.Error(), "校验失败") {
		t.Fatalf("回滚依据应是版本校验(非探针): %v", res.Err)
	}
	phased := false
	for _, a := range alerts {
		if strings.Contains(a, "相位错误") {
			phased = true
		}
	}
	if !phased {
		t.Fatalf("应有相位错误告警: %v", alerts)
	}
	if !strings.Contains(w.logs.String(), "拉起验证失败（相位错误）") {
		t.Fatalf("应有相位错误日志:\n%s", w.logs.String())
	}
	waitServe(t, w.port, w.token, "v0.1.0") // 旧版已恢复服务
}

// spawnDeadProcess 起一个即刻退出的进程,返回其(已死)PID——陈旧锁造现场。
func spawnDeadProcess(t *testing.T) int {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(self, "-test.run=^$") // 无匹配测试 → 立即退出
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	return cmd.Process.Pid
}

// ---- 自中继(v0.1.0 首发实测补):监督者自身 == 换装目标时交棒副本 ----

// ---- 自中继副本机制已删除(v0.5.2 票02) ----
// 自身映像==换装目标 → 直接两步换装的零副本端到端钉(TestSelfImageTargetSwapsDirectly)
// 在 swap_locked_windows_test.go——以节映射复刻「监督者自己就是目标的运行映像
// 持有者」。此处保留清扫域钉:supervisor-copy* 模式在 cleanSwapResidues 域内
// 保留(收 ≤v0.5.1 旧茬残留,spec F7:首跳后一过性红由下次 update 清扫自愈;
// 模式与 lock.go 家族②同于 v0.5.3 一并退役)。

// TestSelfRelayCopyInResidueDomain 自中继副本在清扫域内(≤v0.5.1 旧茬残留
// 靠下次清扫收走;副本机制本体已删,此钉守清扫模式不被提前拆)。
func TestSelfRelayCopyInResidueDomain(t *testing.T) {
	dir := t.TempDir()
	copyPath := filepath.Join(dir, "ferryman.exe.supervisor-copy")
	if err := os.WriteFile(copyPath, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	cleanSwapResidues(dir)
	if _, err := os.Stat(copyPath); !os.IsNotExist(err) {
		t.Fatal("supervisor-copy 应被清扫域收走")
	}
}

// TestStopDaemonWaitsForProcessExit 端口释放后还须等进程真正退出(v0.1.1
// 演练实证的镜像解锁竞态:优雅停机里监听口先关、进程后走,swap 抢跑会
// Access denied)。procAlive 前两轮活、之后死 → stopDaemon 应轮询到死才返回。
func TestStopDaemonWaitsForProcessExit(t *testing.T) {
	w, sup := newUpdateWorld(t, nil, func(c *Config, _ *updateWorld) {
		c.PortWait = 2 * time.Second
	})
	// 世界没起守护,端口天然空;daemon.pid 指向一个"活着的"旧 PID。
	if err := os.WriteFile(filepath.Join(w.dataDir, "daemon.pid"),
		[]byte(`{"pid":424242,"port":7311}`), 0o644); err != nil {
		t.Fatal(err)
	}
	var alive int32
	sup.procAlive = func(pid int) bool { return atomic.AddInt32(&alive, 1) <= 2 }

	if err := sup.stopDaemon(w.exePath, "测试停旧"); err != nil {
		t.Fatalf("stopDaemon = %v", err)
	}
	if n := atomic.LoadInt32(&alive); n < 3 {
		t.Fatalf("应轮询到进程退出(≥3 次判定), got %d——端口释放后没等镜像解锁", n)
	}
}

// TestWaitProcessExitBudgetExhausted 等满预算如实返回 false(pid 恒活)——
// 2026-09-29 起软等待改硬门:超时仍活由调用方报错中止,不再"放弃前进"。
func TestWaitProcessExitBudgetExhausted(t *testing.T) {
	_, sup := newUpdateWorld(t, nil, func(c *Config, _ *updateWorld) {
		c.PortWait = 300 * time.Millisecond
	})
	sup.procAlive = func(int) bool { return true }
	start := time.Now()
	if sup.waitProcessExit(999, sup.cfg.PortWait) {
		t.Fatal("预算耗尽进程仍活应返回 false(硬门)")
	}
	if el := time.Since(start); el < 250*time.Millisecond {
		t.Fatalf("应等满预算: elapsed=%v", el)
	}
}

// TestStopDaemonHardGateOnStuckProcess 排水卡死硬门:控制口已释但旧进程
// 恒活(排水中/退出卡住) → stopDaemon 必须报错中止,绝不带着在场旧进程换装
// (2026-09-29 复盘:软等待前进会让新守护渡口绑定失败进半死形态)。
func TestStopDaemonHardGateOnStuckProcess(t *testing.T) {
	_, sup := newUpdateWorld(t, nil, func(c *Config, _ *updateWorld) {
		c.PortWait = 300 * time.Millisecond
	})
	if err := os.WriteFile(filepath.Join(sup.cfg.DataDir, "daemon.pid"),
		[]byte(`{"pid":424243,"port":7311}`), 0o644); err != nil {
		t.Fatal(err)
	}
	sup.procAlive = func(int) bool { return true }
	err := sup.stopDaemon("whatever.exe", "测试停旧")
	if err == nil {
		t.Fatal("进程恒活时 stopDaemon 应报错(硬门),不得前进")
	}
	if !strings.Contains(err.Error(), "424243") {
		t.Fatalf("报错应点名卡住的 PID: %v", err)
	}
}

// TestDefaultPortWaitCoversDrain 停旧预算缺省 240s——必须覆盖 v0.2.4+ 排水窗
// (drain_timeout_s 缺省 180s)加余量;30s 会抢跑。(newUpdateWorld 为测试速度
// 硬编码短 PortWait,故此处直构 NewSupervisor 验缺省。)
func TestDefaultPortWaitCoversDrain(t *testing.T) {
	sup := NewSupervisor(Config{DataDir: t.TempDir(),
		Logf: func(string, ...any) {}})
	if sup.cfg.PortWait != 240*time.Second {
		t.Fatalf("defaultPortWait = %v, want 240s", sup.cfg.PortWait)
	}
}

// TestFileTeeLogfAppends 缺省日志应落盘 update.log 留证(副本/派生场景
// stdout 无人看——2026-09-22 v0.1.4 失败现场只活在转瞬即逝的 stdout 里)。
func TestFileTeeLogfAppends(t *testing.T) {
	dir := t.TempDir()
	f := fileTeeLogf(dir)
	f("第一行 %d", 1)
	f("第二行")
	b, err := os.ReadFile(filepath.Join(dir, "update.log"))
	if err != nil {
		t.Fatalf("update.log 应落盘: %v", err)
	}
	s := string(b)
	if !strings.Contains(s, "[ferryman-update] 第一行 1") || !strings.Contains(s, "[ferryman-update] 第二行") {
		t.Fatalf("日志内容缺失: %q", s)
	}
}

// ---- 票02:监督者静默门(门前置三分支 + 判据 + 等待/交互/旗标) ----

// forceNonInteractive 钉死 stdinInteractive 缝为「非交互」:缺省问询路径
// (告警硬切)的确定性验证——与真实 shell 的 stdin 形态无关(go test 的 stdin
// 常是 NUL,字符设备,会被启发式误判成交互终端)。
func forceNonInteractive(t *testing.T) {
	t.Helper()
	orig := stdinInteractive
	stdinInteractive = func() bool { return false }
	t.Cleanup(func() { stdinInteractive = orig })
}

// startStatsStub 在端口 port 上起可控 /stats 桩(quietGate 直测用:不拉真
// 替身进程,在途/最后请求字段逐案注入)。
func startStatsStub(t *testing.T, port int, handler http.HandlerFunc) {
	t.Helper()
	srv := &http.Server{Handler: handler}
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
}

// statsHandler /stats 桩应答:status 非 200 模拟不可达;inflight/lastTS 逐案注入。
func statsHandler(status int, inflight int, lastTS int64) http.HandlerFunc {
	return func(rw http.ResponseWriter, _ *http.Request) {
		if status != http.StatusOK {
			http.Error(rw, "unavailable", status)
			return
		}
		rw.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(rw).Encode(map[string]any{
			"version":         "v0.1.0",
			"dock_inflight":   inflight,
			"last_request_ts": lastTS,
		})
	}
}

// TestDockQuietCriterion 判据真值表:在途 0 且距最后请求 ≥10s;
// last_request_ts==0(从未有请求)视为静默成立。
func TestDockQuietCriterion(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	cases := []struct {
		name     string
		inflight int
		lastTS   int64
		want     bool
	}{
		{"零值=从未有请求→静默成立", 0, 0, true},
		{"最后请求 11s 前且无在途→静默", 0, now.Unix() - 11, true},
		{"恰好 10s 边界→静默", 0, now.Unix() - 10, true},
		{"9s 前有请求→不静默", 0, now.Unix() - 9, false},
		{"在途 1(纵使请求久远)→不静默", 1, now.Unix() - 600, false},
		{"在途且从未完成→不静默", 2, 0, false},
	}
	for _, tc := range cases {
		if got := dockQuiet(tc.inflight, tc.lastTS, now); got != tc.want {
			t.Fatalf("%s: dockQuiet(%d,%d) = %v, want %v", tc.name, tc.inflight, tc.lastTS, got, tc.want)
		}
	}
}

// TestQuietWaitDefaultMatchesSpec 静默门等待预算缺省 60s(spec W1)。
func TestQuietWaitDefaultMatchesSpec(t *testing.T) {
	sup := NewSupervisor(Config{DataDir: t.TempDir(),
		Logf: func(string, ...any) {}})
	if sup.cfg.WaitQuiet != 60*time.Second {
		t.Fatalf("WaitQuiet 缺省 = %v, want 60s", sup.cfg.WaitQuiet)
	}
}

// TestQuietGatePassesImmediatelyOnQuiet 分支③判据满足即放行不额外等待:
// 可达且静默(/stats 零值=从未有请求)→ nil + 放行日志,零告警。
func TestQuietGatePassesImmediatelyOnQuiet(t *testing.T) {
	w, sup := newUpdateWorld(t, nil, nil)
	startStatsStub(t, w.port, statsHandler(http.StatusOK, 0, 0))
	start := time.Now()
	if err := sup.quietGate(); err != nil {
		t.Fatalf("静默应放行: %v", err)
	}
	if el := time.Since(start); el > 2*time.Second {
		t.Fatalf("判据满足不应等待: elapsed=%v", el)
	}
	if !strings.Contains(w.logs.String(), "静默门放行") {
		t.Fatalf("应有放行日志:\n%s", w.logs.String())
	}
}

// TestQuietGatePassesAfterTrafficDrains 判据先不满足、等待窗内转为静默 →
// 即刻放行,不等满预算。
func TestQuietGatePassesAfterTrafficDrains(t *testing.T) {
	w, sup := newUpdateWorld(t, nil, func(c *Config, _ *updateWorld) {
		c.WaitQuiet = 5 * time.Second
	})
	var inflight int32 = 2
	oldTS := time.Now().Unix() - 30 // 最后请求在 30s 前,只差在途清零
	startStatsStub(t, w.port, func(rw http.ResponseWriter, _ *http.Request) {
		rw.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(rw).Encode(map[string]any{
			"version": "v0.1.0", "dock_inflight": atomic.LoadInt32(&inflight),
			"last_request_ts": oldTS,
		})
	})
	go func() {
		time.Sleep(150 * time.Millisecond)
		atomic.StoreInt32(&inflight, 0)
	}()
	start := time.Now()
	if err := sup.quietGate(); err != nil {
		t.Fatalf("流量排空后应放行: %v", err)
	}
	if el := time.Since(start); el >= 4*time.Second {
		t.Fatalf("判据转满足即放行,不应等满预算: elapsed=%v", el)
	}
	if !strings.Contains(w.logs.String(), "静默门放行") {
		t.Fatalf("应有放行日志:\n%s", w.logs.String())
	}
}

// TestQuietGateWaitsBudgetThenAlertsHardCut 判据不满足:等满预算后非交互
// 缺省(测试 stdin 非终端)→ notify 告警一条并硬切放行;logf(update.log)有
// 硬切兜底记录行(带在途观测)。
func TestQuietGateWaitsBudgetThenAlertsHardCut(t *testing.T) {
	forceNonInteractive(t)
	w, sup := newUpdateWorld(t, nil, func(c *Config, _ *updateWorld) {
		c.WaitQuiet = 400 * time.Millisecond
	})
	startStatsStub(t, w.port, statsHandler(http.StatusOK, 2, time.Now().Unix()-3))
	var alerts []string
	sup.cfg.Alert = func(title, msg string) { alerts = append(alerts, title+"|"+msg) }
	start := time.Now()
	if err := sup.quietGate(); err != nil {
		t.Fatalf("兜底应放行硬切(不中止): %v", err)
	}
	if el := time.Since(start); el < 350*time.Millisecond {
		t.Fatalf("应等满预算再兜底: elapsed=%v", el)
	}
	logs := w.logs.String()
	if !strings.Contains(logs, "硬切兜底：门未达成") {
		t.Fatalf("应有硬切兜底记录行:\n%s", logs)
	}
	if !strings.Contains(logs, "末次在途 2") {
		t.Fatalf("硬切行应带在途观测:\n%s", logs)
	}
	if len(alerts) != 1 || !strings.Contains(alerts[0], "静默门未达成，硬切兜底") {
		t.Fatalf("应恰一条静默门告警: %v", alerts)
	}
}

// TestQuietGateAlertPanicDoesNotBlock 告警通道故障(panic)不阻塞兜底放行,
// 硬切行照记。
func TestQuietGateAlertPanicDoesNotBlock(t *testing.T) {
	forceNonInteractive(t)
	w, sup := newUpdateWorld(t, nil, func(c *Config, _ *updateWorld) {
		c.WaitQuiet = 200 * time.Millisecond
	})
	startStatsStub(t, w.port, statsHandler(http.StatusOK, 1, 0))
	sup.cfg.Alert = func(string, string) { panic("通道坏") }
	if err := sup.quietGate(); err != nil {
		t.Fatalf("告警失败不得阻塞事务: %v", err)
	}
	if !strings.Contains(w.logs.String(), "硬切兜底") {
		t.Fatalf("硬切行应照记:\n%s", w.logs.String())
	}
}

// TestQuietGateWaitQuietZeroSkipsWait --wait-quiet=0(CLI 映射为负值哨兵):
// 判据不满足不等待,直接进兜底(非交互 → 告警硬切),硬切行记等待 0s。
func TestQuietGateWaitQuietZeroSkipsWait(t *testing.T) {
	forceNonInteractive(t)
	w, sup := newUpdateWorld(t, nil, func(c *Config, _ *updateWorld) {
		c.WaitQuiet = -time.Second
	})
	startStatsStub(t, w.port, statsHandler(http.StatusOK, 3, time.Now().Unix()))
	start := time.Now()
	if err := sup.quietGate(); err != nil {
		t.Fatalf("零预算应立即兜底放行: %v", err)
	}
	if el := time.Since(start); el > time.Second {
		t.Fatalf("零预算不应等待: elapsed=%v", el)
	}
	if !strings.Contains(w.logs.String(), "等待 0s") {
		t.Fatalf("硬切行应记等待 0s:\n%s", w.logs.String())
	}
}

// TestQuietGateForceSkipsGate --force:跳过门直接放行(可达且繁忙的 /stats
// 也不看),零等待零告警。
func TestQuietGateForceSkipsGate(t *testing.T) {
	w, sup := newUpdateWorld(t, nil, func(c *Config, _ *updateWorld) {
		c.WaitQuiet = 5 * time.Second
		c.Force = true
	})
	startStatsStub(t, w.port, statsHandler(http.StatusOK, 5, time.Now().Unix()))
	start := time.Now()
	if err := sup.quietGate(); err != nil {
		t.Fatalf("--force 应直接放行: %v", err)
	}
	if el := time.Since(start); el > time.Second {
		t.Fatalf("--force 不应等待: elapsed=%v", el)
	}
	if !strings.Contains(w.logs.String(), "--force") {
		t.Fatalf("应有跳过日志:\n%s", w.logs.String())
	}
}

// TestQuietGateBranchAbsentDaemon 门前置分支①:端口空且 daemon.pid 无活进程
// (文件缺失或死 PID)→ 跳过门直接进停旧/拉新流程,不看 /stats。
func TestQuietGateBranchAbsentDaemon(t *testing.T) {
	w, sup := newUpdateWorld(t, nil, nil)
	// 无桩、无 pid 文件
	if err := sup.quietGate(); err != nil {
		t.Fatalf("无守护应跳过门: %v", err)
	}
	if !strings.Contains(w.logs.String(), "静默门跳过") {
		t.Fatalf("应有跳过日志:\n%s", w.logs.String())
	}
	// 死 PID 同判:pid 文件在场但进程已退
	if err := os.WriteFile(filepath.Join(w.dataDir, "daemon.pid"),
		[]byte(fmt.Sprintf(`{"pid":%d,"port":%d}`, spawnDeadProcess(t), w.port)), 0o644); err != nil {
		t.Fatal(err)
	}
	logs2 := &syncBuf{}
	cfg2 := w.cfg
	cfg2.Logf = func(f string, a ...any) { fmt.Fprintf(logs2, f+"\n", a...) }
	sup2 := NewSupervisor(cfg2)
	if err := sup2.quietGate(); err != nil {
		t.Fatalf("死 PID 应视作无守护跳过门: %v", err)
	}
	if !strings.Contains(logs2.String(), "静默门跳过") {
		t.Fatalf("死 PID 应有跳过日志:\n%s", logs2.String())
	}
}

// TestQuietGateBranchStatsUnreachable 门前置分支②:守护在(pid 活)但 /stats
// 不可达(端口无监听)→ 不干等静默窗(预算 5s 不消耗),直接进非静默兜底
// (非交互 → 告警硬切),硬切行注明 /stats 不可达。
func TestQuietGateBranchStatsUnreachable(t *testing.T) {
	forceNonInteractive(t)
	w, sup := newUpdateWorld(t, nil, func(c *Config, _ *updateWorld) {
		c.WaitQuiet = 5 * time.Second
	})
	// 守护「在」:daemon.pid 指向活进程(本测试进程);端口无监听 → /stats 不可达
	if err := os.WriteFile(filepath.Join(w.dataDir, "daemon.pid"),
		[]byte(fmt.Sprintf(`{"pid":%d,"port":%d}`, os.Getpid(), w.port)), 0o644); err != nil {
		t.Fatal(err)
	}
	var alerts []string
	sup.cfg.Alert = func(title, msg string) { alerts = append(alerts, title+"|"+msg) }
	start := time.Now()
	if err := sup.quietGate(); err != nil {
		t.Fatalf("分支②应直接进兜底: %v", err)
	}
	if el := time.Since(start); el > 2*time.Second {
		t.Fatalf("分支②不应干等静默窗: elapsed=%v", el)
	}
	if !strings.Contains(w.logs.String(), "硬切兜底：门未达成（等待 0s，/stats 不可达）") {
		t.Fatalf("硬切行应注明 /stats 不可达:\n%s", w.logs.String())
	}
	if len(alerts) != 1 || !strings.Contains(alerts[0], "静默门未达成，硬切兜底") {
		t.Fatalf("应恰一条静默门告警: %v", alerts)
	}
}

// TestQuietGateInteractiveAbort 交互问询缝:三选提示经注入缝可断言;选择
// 放弃 → 门返回非 nil(调用方清账中止事务)。
func TestQuietGateInteractiveAbort(t *testing.T) {
	w, sup := newUpdateWorld(t, nil, func(c *Config, _ *updateWorld) {
		c.WaitQuiet = 200 * time.Millisecond
	})
	startStatsStub(t, w.port, statsHandler(http.StatusOK, 1, time.Now().Unix()))
	var gotPrompt string
	var gotOptions []string
	sup.cfg.QuietAsk = func(prompt string, options []string) string {
		gotPrompt, gotOptions = prompt, options
		return quietChoiceAbort
	}
	err := sup.quietGate()
	if err == nil || !strings.Contains(err.Error(), "放弃") {
		t.Fatalf("放弃应中止升级: %v", err)
	}
	if !strings.Contains(gotPrompt, "静默门未达成") {
		t.Fatalf("提示应说明门未达成: %q", gotPrompt)
	}
	if len(gotOptions) != 3 {
		t.Fatalf("应三选: %v", gotOptions)
	}
}

// TestQuietGateInteractiveWaitThenQuiet 继续等 → 下一窗内转静默 → 放行,
// 问询恰好一次。
func TestQuietGateInteractiveWaitThenQuiet(t *testing.T) {
	w, sup := newUpdateWorld(t, nil, func(c *Config, _ *updateWorld) {
		c.WaitQuiet = 300 * time.Millisecond
	})
	var inflight int32 = 1
	oldTS := time.Now().Unix() - 30
	startStatsStub(t, w.port, func(rw http.ResponseWriter, _ *http.Request) {
		rw.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(rw).Encode(map[string]any{
			"version": "v0.1.0", "dock_inflight": atomic.LoadInt32(&inflight),
			"last_request_ts": oldTS,
		})
	})
	go func() {
		time.Sleep(500 * time.Millisecond)
		atomic.StoreInt32(&inflight, 0)
	}()
	asks := 0
	sup.cfg.QuietAsk = func(string, []string) string {
		asks++
		return quietChoiceWait
	}
	if err := sup.quietGate(); err != nil {
		t.Fatalf("继续等后转静默应放行: %v", err)
	}
	if asks != 1 {
		t.Fatalf("应恰好问一次(下一窗转静默): asks=%d", asks)
	}
	if !strings.Contains(w.logs.String(), "静默门放行") {
		t.Fatalf("应有放行日志:\n%s", w.logs.String())
	}
}

// TestQuietGateInteractiveSwitchNow 现在切 → 放行硬切并留用户选择日志。
func TestQuietGateInteractiveSwitchNow(t *testing.T) {
	w, sup := newUpdateWorld(t, nil, func(c *Config, _ *updateWorld) {
		c.WaitQuiet = 200 * time.Millisecond
	})
	startStatsStub(t, w.port, statsHandler(http.StatusOK, 2, time.Now().Unix()))
	sup.cfg.QuietAsk = func(string, []string) string { return quietChoiceSwitch }
	if err := sup.quietGate(); err != nil {
		t.Fatalf("现在切应放行硬切: %v", err)
	}
	if !strings.Contains(w.logs.String(), "用户选择立即切换") {
		t.Fatalf("应有用户选择日志:\n%s", w.logs.String())
	}
}

// TestDefaultQuietAskNonInteractive 缺省问询实现:stdin 判定缝为非交互
// (go test/计划任务/脚本形态)→ 返回 ""(调用方走告警硬切)。
func TestDefaultQuietAskNonInteractive(t *testing.T) {
	forceNonInteractive(t)
	if got := defaultQuietAsk("提示", []string{"1) 继续等", "2) 现在切换", "3) 放弃"}); got != "" {
		t.Fatalf("非交互应返回空(告警硬切): %q", got)
	}
}
