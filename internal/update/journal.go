package update

// update-journal.json(票05,规格 §C 第2/9条):升级事务日志,阶段
// staging / swap / verify,先写后动——崩溃恢复(监督者启动先读)的唯一依据。
// 写法走临时文件+rename 原子落盘,读不到 JSON 视为 staging 残留(半写)。

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// journalName journal 文件名(~/ferryman/update-journal.json)。
const journalName = "update-journal.json"

// 阶段常量(规格 §C 第2条)。
const (
	PhaseStaging = "staging" // 下载+SHA256 中(未动任何现场)
	PhaseSwap    = "swap"    // 停旧已过,备份/原子替换进行中
	PhaseVerify  = "verify"  // 换装已落,拉起+校验进行中
)

// journal 事务记录;绝对路径全用换装目标侧(exe 旁路目录)。
type journal struct {
	Phase     string `json:"phase"`
	From      string `json:"from"`       // 升级前版本
	To        string `json:"to"`         // 目标版本
	TargetExe string `json:"target_exe"` // 换装目标 exe
	NewExe    string `json:"new_exe"`    // 旁路 ferryman.exe.new
	Backup    string `json:"backup"`     // 备份 ferryman.exe.old-<旧版本>
	StartCmd  string `json:"start_cmd"`  // 拉起脚本
	UpdatedAt string `json:"updated_at"`
}

// journalPath journal 落点。
func journalPath(dir string) string {
	return filepath.Join(dir, journalName)
}

// saveJournal 先写后动:临时文件+rename 原子落盘(自动盖时间戳)。
func saveJournal(dir string, j journal) error {
	j.UpdatedAt = time.Now().Format(time.RFC3339)
	b, err := json.Marshal(j)
	if err != nil {
		return err
	}
	tmp := journalPath(dir) + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, journalPath(dir)); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// loadJournal 读 journal;exists=false = 无事务在册。
func loadJournal(dir string) (journal, bool, error) {
	b, err := os.ReadFile(journalPath(dir))
	if errors.Is(err, fs.ErrNotExist) {
		return journal{}, false, nil
	}
	if err != nil {
		return journal{}, true, err
	}
	var j journal
	if err := json.Unmarshal(b, &j); err != nil {
		return journal{}, true, fmt.Errorf("journal 内容坏: %w", err)
	}
	return j, true, nil
}

// clearJournal 清账(成功/回滚完毕/残留清理后);幂等。
func clearJournal(dir string) error {
	err := os.Remove(journalPath(dir))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	_ = os.Remove(journalPath(dir) + ".tmp") // 半写残留一并清
	return err
}
