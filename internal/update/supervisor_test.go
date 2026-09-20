package update

// 监督者全流程测试(票05 验收面):httptest 伪 GitHub + 测试内替身 exe +
// 随机口 + TEMP 数据目录——绝不碰生产 7311/15900 与仓库根 ferryman.exe。

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
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

// TestSelfRelayHandover 自身映像 == 换装目标 → 复制自身为 .supervisor-copy、
// detached 拉起副本接手、本进程 Relayed 返回;不动锁/journal、不进下载。
func TestSelfRelayHandover(t *testing.T) {
	w, sup := newUpdateWorld(t, nil, nil)
	var spawnExe string
	var spawnArgs []string
	sup.selfExe = func() (string, error) { return w.exePath, nil } // 自身即目标
	sup.spawnRelay = func(exe string, args []string) error {
		spawnExe, spawnArgs = exe, args
		return nil
	}

	res := sup.Run()
	if !res.Relayed || res.Err != nil {
		t.Fatalf("应自中继交棒: relayed=%v err=%v", res.Relayed, res.Err)
	}
	copyPath := w.exePath + ".supervisor-copy"
	got, err := os.ReadFile(copyPath)
	if err != nil {
		t.Fatalf("副本应落盘: %v", err)
	}
	if string(got) != string(w.oldBytes) {
		t.Fatal("副本内容应与自身逐字节一致")
	}
	if spawnExe != copyPath {
		t.Fatalf("拉起对象 = %q, want 副本 %q", spawnExe, copyPath)
	}
	wantArgs := []string{"update", "--supervise", "--self-relay"}
	if fmt.Sprint(spawnArgs) != fmt.Sprint(wantArgs) {
		t.Fatalf("副本参数 = %v, want %v", spawnArgs, wantArgs)
	}
	if _, err := os.Stat(filepath.Join(w.dataDir, "update.lock")); err == nil {
		t.Fatal("交棒不应持锁(锁归副本)")
	}
	if _, err := os.Stat(filepath.Join(w.dataDir, journalName)); err == nil {
		t.Fatal("交棒不应写 journal")
	}
}

// TestSelfRelayHandoverCarriesIntent 显式版本与 prerelease 意图随副本透传。
func TestSelfRelayHandoverCarriesIntent(t *testing.T) {
	w, sup := newUpdateWorld(t, nil, func(c *Config, _ *updateWorld) {
		c.Spec, c.Prerelease = "v0.3.0", true
	})
	var spawnArgs []string
	sup.selfExe = func() (string, error) { return w.exePath, nil }
	sup.spawnRelay = func(_ string, args []string) error { spawnArgs = args; return nil }

	if res := sup.Run(); !res.Relayed {
		t.Fatalf("应交棒: %+v", res)
	}
	want := []string{"update", "--supervise", "--self-relay", "v0.3.0", "--prerelease"}
	if fmt.Sprint(spawnArgs) != fmt.Sprint(want) {
		t.Fatalf("副本参数 = %v, want %v", spawnArgs, want)
	}
}

// TestSelfRelaySkippedWhenDifferentTarget 自身 != 换装目标 → 不交棒,全流程
// 照常(成功升级)。
func TestSelfRelaySkippedWhenDifferentTarget(t *testing.T) {
	_, sup := newUpdateWorld(t, nil, nil)
	other := filepath.Join(t.TempDir(), "not-the-target.exe")
	sup.selfExe = func() (string, error) { return other, nil }
	res := sup.Run()
	if res.Relayed || !res.Success {
		t.Fatalf("不同映像不应交棒且应正常升级: relayed=%v success=%v err=%v",
			res.Relayed, res.Success, res.Err)
	}
}

// TestSelfRelayMarkerSkips 副本携 --self-relay 标记:即便自身 == 目标也不再
// 自中继(防无限交棒),直接干活。
func TestSelfRelayMarkerSkips(t *testing.T) {
	w, sup := newUpdateWorld(t, nil, func(c *Config, _ *updateWorld) {
		c.SelfRelay = true
	})
	sup.selfExe = func() (string, error) { return w.exePath, nil } // 副本形态:自身即目标
	res := sup.Run()
	if res.Relayed || !res.Success {
		t.Fatalf("标记后不应再交棒且应正常升级: relayed=%v success=%v err=%v",
			res.Relayed, res.Success, res.Err)
	}
}

// TestSelfRelayCopyInResidueDomain 自中继副本在清扫域内(副本删不掉自己,
// 靠下次清扫收走)。
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
