package update

// update.lock(票05,规格 §C 第1条):<DataDir>/update.lock,O_CREATE|O_EXCL
// 原子创建;内容 PID+进程映像路径+generation。存活判定(seam B)= PID 活
// **且**映像路径 ∈ 三族:{换装目标 exe, 自中继副本(换装目标+".supervisor-copy",
// 过渡保留), 换装目标同目录 ferryman.exe.old-* 备份族(含 .stale- 变体)}
// ——副本纳入是票04 盲区修:监督者 --self-relay 交棒后持锁者映像恒为副本,
// 修前恒判陈旧 → 并发第二次 update 误接管双监督者。备份族纳入是票01 三族
// 定案:两步换装第①步把目标改名为备份名后,Windows 运行映像路径随改名更新,
// 活监督者映像即 .old-* 形态,修前误判陈旧接管。持有者活 →
// ErrUpdateInProgress 退出;陈旧(死 PID/映像不符/内容坏)→ 原子接管:
// remove 后 O_EXCL 重赛,generation 递增——并发接管者只有一方能在重赛中赢。

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// lockName 锁文件名(~/ferryman/update.lock)。
const lockName = "update.lock"

// ErrUpdateInProgress 锁被活持有者占用(二次触发被拒并提示)。
var ErrUpdateInProgress = errors.New("升级进行中(另一监督者持有 update.lock)")

// lockInfo 锁内容:持有者身份 + 接管代数。
type lockInfo struct {
	PID        int    `json:"pid"`
	Image      string `json:"image"`
	Generation int    `json:"generation"`
	StartedAt  string `json:"started_at"`
}

// updateLock 已取得的锁;release 删文件(一切退出路径都 defer)。
type updateLock struct {
	path string
}

// release 释放锁;文件已不在(接管者清理等)静默。
func (l *updateLock) release() {
	if l == nil || l.path == "" {
		return
	}
	_ = os.Remove(l.path)
}

// acquireUpdateLock 取锁。alive/image 为进程身份探针(监督者注入,
// 测试可换桩);logf 记接管动作。
func acquireUpdateLock(dir, targetExe string,
	alive func(int) bool, image func(int) (string, error),
	logf func(string, ...any)) (*updateLock, error) {
	path := filepath.Join(dir, lockName)
	gen := 1
	// 接管循环:remove 与 O_EXCL 之间有竞窗,重赛至多 8 轮(每轮要么赢,
	// 要么撞上新持有者退出)。
	for attempt := 0; attempt < 8; attempt++ {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err == nil {
			info := lockInfo{PID: os.Getpid(), Image: selfImage(),
				Generation: gen, StartedAt: time.Now().Format(time.RFC3339)}
			b, merr := json.Marshal(info)
			if merr == nil {
				_, merr = f.Write(append(b, '\n'))
			}
			if cerr := f.Close(); merr == nil {
				merr = cerr
			}
			if merr != nil {
				_ = os.Remove(path)
				return nil, fmt.Errorf("写锁失败: %w", merr)
			}
			return &updateLock{path: path}, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return nil, fmt.Errorf("取锁失败: %w", err)
		}
		// 已存在 → 验持有者(seam B)
		old, rerr := readLockInfo(path)
		switch {
		case rerr != nil:
			// 读不了/内容坏:按陈旧残留处理(半写现场),走接管
			logf("锁内容不可读(%v)——按陈旧残留接管", rerr)
		case holderAlive(old, targetExe, alive, image):
			return nil, fmt.Errorf("%w(持有者 PID %d, generation %d)",
				ErrUpdateInProgress, old.PID, old.Generation)
		default:
			logf("锁陈旧(持有者 PID %d 不活/映像不符)——接管,generation %d → %d",
				old.PID, old.Generation, old.Generation+1)
			gen = old.Generation + 1
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("清理陈旧锁失败: %w", err)
		}
		// 下一轮 O_EXCL 重赛
	}
	return nil, fmt.Errorf("锁竞争未定(接管重赛耗尽)")
}

// readLockInfo 读锁内容。
func readLockInfo(path string) (lockInfo, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return lockInfo{}, err
	}
	var info lockInfo
	if err := json.Unmarshal(b, &info); err != nil {
		return lockInfo{}, fmt.Errorf("锁内容坏: %w", err)
	}
	if info.PID <= 0 {
		return lockInfo{}, fmt.Errorf("锁内容坏: PID %d", info.PID)
	}
	return info, nil
}

// holderAlive 存活判定(seam B):PID 活**且**映像路径 ∈ 三族——
//   ① 换装目标本体;
//   ② 自中继副本(relayCopyPath,票04 盲区修)。过渡保留:副本机制次版
//      (v0.5.3,票02 删副本)实施时本分支一并删除;
//   ③ 换装目标同目录 ferryman.exe.old-* 备份族(票01 三族定案:两步换装
//      第①步把目标改名为备份名后,Windows 运行映像路径随改名更新,活监督者
//      映像即此形态;含 staleAsidePath 的 .stale-<日期>-<pid> 挪窝变体)。
// PID 活但映像查不出(权限面)→ 保守按活——宁误报进行中,不误双跑。
func holderAlive(info lockInfo, targetExe string,
	alive func(int) bool, image func(int) (string, error)) bool {
	if info.PID <= 0 || !alive(info.PID) {
		return false
	}
	img, err := image(info.PID)
	if err != nil {
		return true
	}
	return samePath(img, targetExe) ||
		samePath(img, relayCopyPath(targetExe)) || // ②过渡:v0.5.3 随副本机制删
		isOldBackupImage(img, targetExe)
}

// isOldBackupImage ③族判据(匹配语义钉死):映像与换装目标**同目录**且文件名
// 前缀为 ferryman.exe.old-(oldPrefixBase,swap.go backupPath 的备份名生成域,
// staleAsidePath 的挪窝名也留在该前缀域内)即认;不校验版本段与 .stale-* 后缀
// 形态——版本经 sanitizeFileToken 白名单化,判活从严只会漏认=误接管双跑,
// 故从宽。异目录/异前缀一律不认(负例见 lock_test.go 备份族测试)。
func isOldBackupImage(img, targetExe string) bool {
	if !samePath(filepath.Dir(normSlash(targetExe)), filepath.Dir(normSlash(img))) {
		return false
	}
	return strings.HasPrefix(strings.ToLower(filepath.Base(normSlash(img))), oldPrefixBase)
}

// LockHeldByLiveSupervisor 只读判定 <dir>/update.lock 是否被活监督者持有
// (票04 守护层让路的单源判定:daemon serve 启动早期调用本助手,不复制第二
// 份锁逻辑;持有判据与 acquireUpdateLock 的接管判定同走 holderAlive——同一
// 语义只会有一份实现)。alive/image 传 nil 用平台真实现(procAliveImpl/
// procImageImpl,daemon 不碰进程面);测试注入桩。锁不存在/不可读/持有者
// 死/映像无关 → (0,false)=照常启动;持有者活 → (持有者PID,true)。
func LockHeldByLiveSupervisor(dir, targetExe string,
	alive func(int) bool, image func(int) (string, error)) (int, bool) {
	info, err := readLockInfo(filepath.Join(dir, lockName))
	if err != nil {
		return 0, false
	}
	if alive == nil {
		alive = procAliveImpl
	}
	if image == nil {
		image = procImageImpl
	}
	if !holderAlive(info, targetExe, alive, image) {
		return 0, false
	}
	return info.PID, true
}

// PIDAlive PID 活性只读单源助手（P1 看门复位取证跨包复用，2026-09-30：
// installer 包不复制第二份进程面——与 LockHeldByLiveSupervisor 同一导出
// 纪律）。daemon 侧让路判定/监督者 seam B 与看门取证共用 procAliveImpl
// 同一实现。
func PIDAlive(pid int) bool { return procAliveImpl(pid) }

// selfImage 本进程映像路径(锁内容用;取不到留空,不阻塞取锁)。
func selfImage() string {
	p, err := os.Executable()
	if err != nil {
		return ""
	}
	return p
}
