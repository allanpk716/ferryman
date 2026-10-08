//go:build !windows

// proc_other.go — 进程创建工厂的非 Windows 分支（本工具生产目标 Windows；
// 非 Windows 只求编译可跑——junction 备料走 GOOS 守卫跳过，杀树退化为单进程
// Kill，dsh_liveness_other.go 同款纪律）。
package dshsandbox

import (
	"fmt"
	"os"
	"syscall"
)

// newHiddenSysProcAttr 非 Windows：SysProcAttr 无 HideWindow 字段（unix 无
// 「无控制台宿主拉 console 子进程」形态，子进程继承 pty/无窗）。
func newHiddenSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{}
}

// killTreeImpl 非 Windows：单进程 Kill（进程组树语义不做——生产面 Windows）。
func killTreeImpl(pid int) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return nil // unix FindProcess 只在 PID 非法时报错——已退视为已收
	}
	if err := p.Signal(syscall.SIGKILL); err != nil {
		return fmt.Errorf("kill %d: %w", pid, err)
	}
	return nil
}
