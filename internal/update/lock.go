package update

// update.lock(票05,规格 §C 第1条):<DataDir>/update.lock,O_CREATE|O_EXCL
// 原子创建;内容 PID+进程映像路径+generation。存活判定(seam B)= PID 活
// **且**映像路径 == 换装目标 exe 路径;持有者活 → ErrUpdateInProgress 退出;
// 陈旧(死 PID/映像不符/内容坏)→ 原子接管:remove 后 O_EXCL 重赛,
// generation 递增——并发接管者只有一方能在重赛中赢。

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
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

// holderAlive 存活判定(seam B):PID 活**且**映像路径 == 换装目标。
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
	return samePath(img, targetExe)
}

// selfImage 本进程映像路径(锁内容用;取不到留空,不阻塞取锁)。
func selfImage() string {
	p, err := os.Executable()
	if err != nil {
		return ""
	}
	return p
}
