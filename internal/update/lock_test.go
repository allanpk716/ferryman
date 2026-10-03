package update

// update.lock(票05,规格 §C 第1条):O_CREATE|O_EXCL 原子建;持有者活 →
// ErrUpdateInProgress;陈旧 → 接管(generation+1)。探针(alive/image)全注入,
// 真进程路径在监督者全流程测试里覆盖。

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// fakeProbes 可编排的进程身份探针。
type fakeProbes struct {
	alive   map[int]bool
	images  map[int]string
	imgErrs map[int]error
}

func (f fakeProbes) aliveFn(pid int) bool { return f.alive[pid] }
func (f fakeProbes) imageFn(pid int) (string, error) {
	if err, ok := f.imgErrs[pid]; ok {
		return "", err
	}
	return f.images[pid], nil
}

func quietLogf(string, ...any) {}

// writeLock 手工造锁现场。
func writeLock(t *testing.T, dir string, info lockInfo) {
	t.Helper()
	b, err := json.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, lockName), b, 0o644); err != nil {
		t.Fatal(err)
	}
}

// readLockFor 断言用:读回锁内容。
func readLockFor(t *testing.T, dir string) lockInfo {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, lockName))
	if err != nil {
		t.Fatal(err)
	}
	var info lockInfo
	if err := json.Unmarshal(b, &info); err != nil {
		t.Fatal(err)
	}
	return info
}

// TestLockFreshAcquire 无锁直接取:generation=1,内容带本进程 PID 与映像。
func TestLockFreshAcquire(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "ferryman.exe")

	lk, err := acquireUpdateLock(dir, target, fakeProbes{}.aliveFn, fakeProbes{}.imageFn, quietLogf)
	if err != nil {
		t.Fatal(err)
	}
	info := readLockFor(t, dir)
	if info.PID != os.Getpid() || info.Generation != 1 {
		t.Fatalf("锁内容 = %+v, want 本进程 PID + generation 1", info)
	}

	// 重复取(同进程,锁在)→ 已存在路径;本进程活且映像==目标 → 进行中
	lk.release()
	if _, err := os.Stat(filepath.Join(dir, lockName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("release 后锁文件应已删")
	}
}

// TestLockBusyHolder 持有者活且映像==换装目标(seam B)→「升级进行中」,
// 锁文件原样保留(持有者的,不是我们的)。
func TestLockBusyHolder(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "ferryman.exe")
	probes := fakeProbes{alive: map[int]bool{4242: true}, images: map[int]string{4242: target}}
	writeLock(t, dir, lockInfo{PID: 4242, Image: target, Generation: 5})

	lk, err := acquireUpdateLock(dir, target, probes.aliveFn, probes.imageFn, quietLogf)
	if !errors.Is(err, ErrUpdateInProgress) {
		t.Fatalf("持锁者活应报进行中, got %v", err)
	}
	if lk != nil {
		t.Fatalf("败者不应持有锁对象")
	}
	info := readLockFor(t, dir)
	if info.PID != 4242 || info.Generation != 5 {
		t.Fatalf("锁文件应原样保留持有者的, got %+v", info)
	}
}

// TestLockBusyHolderViaOldBackupFamily 备份族判据(票01 定案):两步换装
// 第①步把目标改名为 ferryman.exe.old-<版本>(backupPath 备份位)后,Windows
// 运行映像路径随文件改名更新——活监督者的映像即 .old-* 形态,现行判据不含
// 它,并发第二个 update 会误判锁陈旧接管成双监督者齐跑。备份族=同目录
// ferryman.exe.old-* 全体,含 staleAsidePath 的 .stale-<日期>-<pid> 挪窝变体;
// 异目录同名前缀/同目录前缀不严格匹配不得误认。
func TestLockBusyHolderViaOldBackupFamily(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "ferryman.exe")

	// 持有者 PID 活且映像=目标同目录 ferryman.exe.old-v0.5.1 → 进行中
	backupImg := filepath.Join(dir, "ferryman.exe.old-v0.5.1")
	probes := fakeProbes{alive: map[int]bool{6060: true}, images: map[int]string{6060: backupImg}}
	writeLock(t, dir, lockInfo{PID: 6060, Image: backupImg, Generation: 9})
	_, err := acquireUpdateLock(dir, target, probes.aliveFn, probes.imageFn, quietLogf)
	if !errors.Is(err, ErrUpdateInProgress) {
		t.Fatalf("备份名映像(改名后活监督者)应判进行中, got %v", err)
	}
	if got := readLockFor(t, dir); got.PID != 6060 || got.Generation != 9 {
		t.Fatalf("锁文件应原样保留持有者的, got %+v", got)
	}

	// .stale- 挪窝变体(staleAsidePath:备份位被占先挪窝)→ 同判进行中
	staleImg := backupImg + ".stale-20261001-120000-6060"
	probes = fakeProbes{alive: map[int]bool{6061: true}, images: map[int]string{6061: staleImg}}
	writeLock(t, dir, lockInfo{PID: 6061, Image: staleImg, Generation: 10})
	_, err = acquireUpdateLock(dir, target, probes.aliveFn, probes.imageFn, quietLogf)
	if !errors.Is(err, ErrUpdateInProgress) {
		t.Fatalf(".stale- 变体映像应判进行中, got %v", err)
	}

	// 负例:前缀族但异目录 → 不认,判陈旧接管
	otherDir := filepath.Join(dir, "other")
	if err := os.MkdirAll(otherDir, 0o755); err != nil {
		t.Fatal(err)
	}
	foreignImg := filepath.Join(otherDir, "ferryman.exe.old-v0.5.1")
	probes = fakeProbes{alive: map[int]bool{6062: true}, images: map[int]string{6062: foreignImg}}
	writeLock(t, dir, lockInfo{PID: 6062, Image: foreignImg, Generation: 11})
	lk, err := acquireUpdateLock(dir, target, probes.aliveFn, probes.imageFn, quietLogf)
	if err != nil {
		t.Fatalf("异目录同名前缀映像应判陈旧接管, got %v", err)
	}
	lk.release()

	// 负例:同目录但前缀不严格匹配(ferryman2.exe.old-)→ 不认
	decoyImg := filepath.Join(dir, "ferryman2.exe.old-v0.5.1")
	probes = fakeProbes{alive: map[int]bool{6063: true}, images: map[int]string{6063: decoyImg}}
	writeLock(t, dir, lockInfo{PID: 6063, Image: decoyImg, Generation: 12})
	lk, err = acquireUpdateLock(dir, target, probes.aliveFn, probes.imageFn, quietLogf)
	if err != nil {
		t.Fatalf("前缀不严格匹配应判陈旧接管, got %v", err)
	}
	lk.release()
}

// TestLockTakeoverStale 陈旧两态接管:PID 死 / PID 活但映像≠换装目标
// (seam B:不匹配=不活)→ 接管,generation 递增。
func TestLockTakeoverStale(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "ferryman.exe")

	// PID 死
	probes := fakeProbes{alive: map[int]bool{}, images: map[int]string{}}
	writeLock(t, dir, lockInfo{PID: 4242, Image: target, Generation: 3})
	lk, err := acquireUpdateLock(dir, target, probes.aliveFn, probes.imageFn, quietLogf)
	if err != nil {
		t.Fatal(err)
	}
	if got := readLockFor(t, dir).Generation; got != 4 {
		t.Fatalf("接管后 generation = %d, want 4(3+1)", got)
	}
	lk.release()

	// PID 活但映像是无关进程(假 PID 指向无关映像)
	probes = fakeProbes{alive: map[int]bool{4242: true}, images: map[int]string{4242: `C:\Windows\System32\svchost.exe`}}
	writeLock(t, dir, lockInfo{PID: 4242, Image: target, Generation: 7})
	lk, err = acquireUpdateLock(dir, target, probes.aliveFn, probes.imageFn, quietLogf)
	if err != nil {
		t.Fatalf("映像不匹配应判陈旧接管, got %v", err)
	}
	if got := readLockFor(t, dir).Generation; got != 8 {
		t.Fatalf("接管后 generation = %d, want 8(7+1)", got)
	}
	lk.release()

	// 锁内容坏(半写)→ 视为陈旧接管
	if err := os.WriteFile(filepath.Join(dir, lockName), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	lk, err = acquireUpdateLock(dir, target, probes.aliveFn, probes.imageFn, quietLogf)
	if err != nil {
		t.Fatal(err)
	}
	lk.release()
}

// TestLockAliveButImageUnqueryable PID 活但映像查不出(权限面)→ 保守按活,
// 不敢接管(宁误报进行中,不误双跑)。
func TestLockAliveButImageUnqueryable(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "ferryman.exe")
	probes := fakeProbes{alive: map[int]bool{4242: true}, imgErrs: map[int]error{4242: errors.New("denied")}}
	writeLock(t, dir, lockInfo{PID: 4242, Image: target, Generation: 1})

	_, err := acquireUpdateLock(dir, target, probes.aliveFn, probes.imageFn, quietLogf)
	if !errors.Is(err, ErrUpdateInProgress) {
		t.Fatalf("映像查不出应保守按活, got %v", err)
	}
}

// TestLockHeldByLiveSupervisor 导出只读助手(票04 守护层让路的单源判定):
// 锁态到 (持有者PID, 是否持有) 的映射——不存在/内容坏/持有者死/映像无关一律
// (0,false)=照常启动;目标映像与 .old-* 备份族映像皆判持有。探针全注入,测试
// 不依赖真进程面;daemon 侧只经本助手判定,不复制锁逻辑。
func TestLockHeldByLiveSupervisor(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "ferryman.exe")

	// 锁不存在 → (0,false)
	if pid, held := LockHeldByLiveSupervisor(dir, target, fakeProbes{}.aliveFn, fakeProbes{}.imageFn); held || pid != 0 {
		t.Fatalf("无锁应不判持有, got pid=%d held=%v", pid, held)
	}

	// 锁内容坏(半写) → (0,false)
	if err := os.WriteFile(filepath.Join(dir, lockName), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, held := LockHeldByLiveSupervisor(dir, target, fakeProbes{}.aliveFn, fakeProbes{}.imageFn); held {
		t.Fatal("坏锁应不判持有")
	}

	// 持有者死 → (0,false)
	dead := fakeProbes{alive: map[int]bool{}, images: map[int]string{}}
	writeLock(t, dir, lockInfo{PID: 4242, Image: target, Generation: 1})
	if _, held := LockHeldByLiveSupervisor(dir, target, dead.aliveFn, dead.imageFn); held {
		t.Fatal("死持有者应不判持有")
	}

	// PID 活但映像无关进程 → (0,false)
	unrelated := fakeProbes{alive: map[int]bool{4242: true},
		images: map[int]string{4242: `C:\Windows\System32\svchost.exe`}}
	if _, held := LockHeldByLiveSupervisor(dir, target, unrelated.aliveFn, unrelated.imageFn); held {
		t.Fatal("映像无关应不判持有")
	}

	// 映像 == 换装目标 → (4242,true)
	live := fakeProbes{alive: map[int]bool{4242: true}, images: map[int]string{4242: target}}
	if pid, held := LockHeldByLiveSupervisor(dir, target, live.aliveFn, live.imageFn); !held || pid != 4242 {
		t.Fatalf("目标映像持有者应判持有, got pid=%d held=%v", pid, held)
	}

	// 映像 == 同目录 .old-* 备份族(改名后监督者,含 .stale- 变体)→ (4242,true)
	// (票01 定案同步暴露给守护层让路面——与 holderAlive 同源,只钉不改)
	backup := fakeProbes{alive: map[int]bool{4242: true},
		images: map[int]string{4242: filepath.Join(dir, "ferryman.exe.old-v0.5.1")}}
	if pid, held := LockHeldByLiveSupervisor(dir, target, backup.aliveFn, backup.imageFn); !held || pid != 4242 {
		t.Fatalf("备份族映像持有者应判持有, got pid=%d held=%v", pid, held)
	}
}

// TestSamePath 路径同义判定:大小写与斜杠差异等价,空串不等。
func TestSamePath(t *testing.T) {
	a := `C:\WorkSpace\agent\Ferryman\ferryman.exe`
	if !samePath(a, `c:\workspace\AGENT\ferryman\ferryman.EXE`) {
		t.Fatalf("大小写不敏感应同义")
	}
	if !samePath(a, `C:/WorkSpace/agent/Ferryman/ferryman.exe`) {
		t.Fatalf("正反斜杠应同义")
	}
	if samePath(a, `C:\WorkSpace\agent\Ferryman\ferryman2.exe`) {
		t.Fatalf("不同路径不得同义")
	}
	if samePath("", a) || samePath(a, "") {
		t.Fatalf("空串不得同义")
	}
}
