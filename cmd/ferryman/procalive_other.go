//go:build !windows

// procalive_other.go — PID 活性 unix 实现（mini6/macOS 移植；保留 procAliveWin 名字
// 以复用 status.go 的装配点）。signal 0 探活：ESRCH=已退；EPERM=活着但非属主
// （同用户场景不出现，按不活处理可接受）。
package main

import (
	"os"
	"syscall"
)

func procAliveWin(pid int) bool {
	if pid <= 0 {
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return p.Signal(syscall.Signal(0)) == nil
}
