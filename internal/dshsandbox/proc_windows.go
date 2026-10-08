// proc_windows.go — 进程创建工厂的 Windows 分支（零闪窗铁律，2026-10-06 事故
// 钉点：console 子进程一律 SysProcAttr{HideWindow:true}；dsh_liveness_windows.go
// / update.proc_windows.go 同款先例）。
package dshsandbox

import (
	"fmt"
	"syscall"
)

// newHiddenSysProcAttr Windows：HideWindow 置位（父无控制台时子不建可见窗）。
func newHiddenSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{HideWindow: true}
}

// killTreeImpl Windows：taskkill /F /T /PID 树形强杀（孙进程一并收）。
// taskkill 自身是 console 程序——经 runHidden（hiddenCmd 单点）隐藏窗口。
func killTreeImpl(pid int) error {
	out, err := runHidden(procSpec{Path: "taskkill", Args: []string{"/F", "/T", "/PID", fmt.Sprint(pid)}})
	if err != nil && !taskKillAlreadyGone(out) {
		return fmt.Errorf("taskkill /F /T /PID %d: %v: %s", pid, err, out)
	}
	return nil
}

// taskKillAlreadyGone taskkill 对已退进程的两种正常回话（进程不存在/找不到）
// 不算错——幂等杀树语义。
func taskKillAlreadyGone(out string) bool {
	for _, mark := range []string{"not found", "找不到", "不存在", "没有找到"} {
		if containsFold(out, mark) {
			return true
		}
	}
	return false
}

func containsFold(s, sub string) bool {
	if len(sub) == 0 {
		return true
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		if equalFold(s[i:i+len(sub)], sub) {
			return true
		}
	}
	return false
}

func equalFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if 'A' <= ca && ca <= 'Z' {
			ca += 'a' - 'A'
		}
		if 'A' <= cb && cb <= 'Z' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}
