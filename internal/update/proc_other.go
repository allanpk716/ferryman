//go:build !windows

package update

// 非 Windows 存根(票面:为主目标 Windows 之外的 CI 留形状;start-daemon.cmd
// 本身是 Windows 批处理,本文件只保证包可编译、语义尽力对齐)。

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

// procAliveImpl kill(pid, 0) 探活。
func procAliveImpl(pid int) bool {
	return pid > 0 && syscall.Kill(pid, 0) == nil
}

// procImageImpl /proc/<pid>/exe 符号链接(Linux;其余平台报错 → 调用方按
// 不可查的保守路径处理)。
func procImageImpl(pid int) (string, error) {
	if pid <= 0 {
		return "", fmt.Errorf("非法 PID")
	}
	return os.Readlink(fmt.Sprintf("/proc/%d/exe", pid))
}

// killImpl SIGKILL。
func killImpl(pid int) error {
	return syscall.Kill(pid, syscall.SIGKILL)
}

// launchCmdImpl 新会话直接执行(detached 等价)。
func launchCmdImpl(cmdPath string) error {
	c := exec.Command(cmdPath)
	c.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	return c.Start()
}
