//go:build !windows

// procattr_other.go — unix 形态：nil（无控制台隐藏语义；launchd 会话本就无窗）。
package installer

import "syscall"

func newHiddenSysProcAttr() *syscall.SysProcAttr {
	return nil
}
