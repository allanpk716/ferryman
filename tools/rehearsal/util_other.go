//go:build !windows

package main

// util_other.go — 非 Windows 的进程面桩：彩排台是 Windows 升级链的演练资产
// （换装/让路/蜂群全依赖 Windows 语义），非 Windows 世界不可用——给明确报错
// 保持 `go build ./...` 可编译（对齐 internal/update/proc_other.go 惯例）。

import (
	"errors"
	"io"
	"os/exec"
	"time"
)

// newHiddenCmd 非 Windows 不可用（见文件头）：给会立即失败的 Cmd。
func newHiddenCmd(exe string, args []string, dir string, env []string, stdout, stderr io.Writer) *exec.Cmd {
	c := exec.Command(exe, args...)
	c.Dir = dir
	c.Env = env
	c.Stdout, c.Stderr = stdout, stderr
	return c
}

func spawnHidden(exe string, args, env []string, stdout, stderr io.Writer) (*exec.Cmd, error) {
	return nil, errors.New("彩排台仅支持 Windows（换装/让路/蜂群均为 Windows 语义）")
}

func pidAliveLocal(pid int) bool { return false }

func killByPID(pid int) error { return errors.New("彩排台仅支持 Windows") }

func waitProcExit(pid int, budget time.Duration) bool { return true }
