//go:build !windows

// autostart_other.go — 非 Windows 平台的 Run 键自启占位（mini6/macOS 移植，2026-10-23）。
// Run 键/HKCU 是 Windows 专属；macOS 的常驻保障走 launchd LaunchAgent
// （~/Library/LaunchAgents/com.allan.ferryman.plist，RunAtLoad+KeepAlive）。
// 本文件只保证 CLI/doctor 的编译面与三态语义完整，不做任何真实安装动作。
package installer

import "fmt"

// autostartStatus 三态（与 autostart_windows.go 同口径；doctor「存在/缺失/值不符」同语义）。
type autostartStatus int

const (
	autostartMissing autostartStatus = iota
	autostartInstalled
	autostartMismatch
)

// autostartDeps 非 Windows 下无注册表面可注（launcher 保留占位；VBS/Run 键均不适用）。
type autostartDeps struct {
	launcher string
}

func realAutostartDeps() autostartDeps { return autostartDeps{} }

// autostartStatusOf 恒报 missing + 无错：macOS 的常驻保障在 launchd，注册表语义不适用。
// doctor 的「Run 键自启」项在 macOS 上读到的就是这个（missing），属预期差异。
func autostartStatusOf(d autostartDeps) (autostartStatus, error) {
	return autostartMissing, nil
}

// AutostartInstall / AutostartUninstall / AutostartStatus —— 非 Windows 明说 n/a 并给正路。
func AutostartInstall() int {
	fmt.Println("Run 键自启是 Windows 专属；macOS 常驻保障 = launchd：~/Library/LaunchAgents/com.allan.ferryman.plist（RunAtLoad+KeepAlive）")
	return 1
}

func AutostartUninstall() int {
	fmt.Println("Run 键自启是 Windows 专属；macOS 卸载 = 删除该 plist 并 launchctl bootout gui/$(id -u) ~/Library/LaunchAgents/com.allan.ferryman.plist")
	return 1
}

func AutostartStatus() int {
	fmt.Println("Run 键自启: n/a（macOS 走 launchd com.allan.ferryman：RunAtLoad+KeepAlive）")
	return 0
}
