//go:build !windows

package daemon

// hideconsole_other_test.go — 非 Windows 空实现：HideWindow 是 Windows 专属
// SysProcAttr 字段，unix 上无控制台闪窗面（见 windows 同名文件的铁律说明）。

import "os/exec"

// hideConsole 非 Windows 空操作。
func hideConsole(*exec.Cmd) {}
