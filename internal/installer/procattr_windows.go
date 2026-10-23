//go:build windows

// procattr_windows.go — 零闪窗铁律的 Windows 形态（对齐 dshsandbox.newHiddenSysProcAttr 单点工厂）。
package installer

import "syscall"

func newHiddenSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{HideWindow: true}
}
