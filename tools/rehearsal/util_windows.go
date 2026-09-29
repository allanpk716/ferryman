//go:build windows

package main

// util_windows.go — 彩排台进程面（Windows 真实现）：隐藏拉起（零闪窗铁律）、
// PID 活性、硬杀。对齐 internal/update 的 proc_windows 手段（x/sys/windows，
// 依赖树既有）。非 Windows 世界在 util_other.go 给不可用桩。

import (
	"errors"
	"io"
	"os/exec"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
)

// stillActive GetExitCodeProcess 的「未退」惯例值（x/sys 未导出，字面量钉死）。
const winStillActive = 259

// newHiddenCmd 组装隐藏窗口的 Cmd（零闪窗铁律：彩排产生的一切子进程——
// 含 go build——都不许弹黑窗）。CREATE_NO_WINDOW 是 cmd.exe 批处理正确旗标
// （DETACHED_PROCESS 会让 cmd 自建可见控制台——v0.1.4 实测坑）。
func newHiddenCmd(exe string, args []string, dir string, env []string, stdout, stderr io.Writer) *exec.Cmd {
	c := exec.Command(exe, args...)
	c.Dir = dir
	if env != nil {
		c.Env = env
	}
	c.Stdout, c.Stderr = stdout, stderr
	c.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: windows.CREATE_NO_WINDOW | windows.CREATE_NEW_PROCESS_GROUP,
	}
	return c
}

// spawnHidden 隐藏窗口拉起并 Start。
func spawnHidden(exe string, args, env []string, stdout, stderr io.Writer) (*exec.Cmd, error) {
	c := newHiddenCmd(exe, args, "", env, stdout, stderr)
	if err := c.Start(); err != nil {
		return nil, err
	}
	return c, nil
}

// pidAliveLocal PID 活性（OpenProcess + STILL_ACTIVE；仅彩排清场判定用）。
func pidAliveLocal(pid int) bool {
	if pid <= 0 {
		return false
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)
	var code uint32
	if err := windows.GetExitCodeProcess(h, &code); err != nil {
		return false
	}
	return code == winStillActive
}

// killByPID 硬杀（TerminateProcess 退出码 1）：长跑终结纪律——彩排任何子
// 进程超时都必须收得走，不许留孤儿烧 CPU。
func killByPID(pid int) error {
	if pid <= 0 {
		return errors.New("非法 PID")
	}
	h, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)
	return windows.TerminateProcess(h, 1)
}

// waitProcExit 等进程退场（≤budget）。
func waitProcExit(pid int, budget time.Duration) bool {
	if pid <= 0 {
		return true
	}
	deadline := time.Now().Add(budget)
	for time.Now().Before(deadline) {
		if !pidAliveLocal(pid) {
			return true
		}
		time.Sleep(150 * time.Millisecond)
	}
	return !pidAliveLocal(pid)
}
