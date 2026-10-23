//go:build windows

// procalive_windows.go — PID 活性 Windows 实现（自 status.go 拆出，mini6/macOS 移植）。
package main

import "golang.org/x/sys/windows"

// stillActive GetExitCodeProcess 的「未退」惯例值（update 包同款本地常量——
// x/sys/windows 未导出 STILL_ACTIVE，supervisor 侧亦就地定义）。
const stillActive = 259

// procAliveWin PID 活性（update.procAliveImpl 就地镜像——非导出不能跨包复用）：
// OpenProcess(QUERY_LIMITED) 成功且退出码仍为 STILL_ACTIVE；打不开一律不活。
func procAliveWin(pid int) bool {
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
	return code == stillActive
}
