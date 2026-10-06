//go:build windows

package daemon

// hideconsole_windows_test.go — 测试内 console 子进程统一零闪窗（票07 闪窗
// 事故 2026-10-06 升级铁律）：go test 经 exec 起的 powershell/netstat 等控制台
// 子进程默认各开可见 conhost——正常单跑一闪而过无感，但任何把测试二进制
// 递归重跑的路径（如 restart 帮手未熔断时以位置参数自我再执行）会把整套
// 套件的 console 调用放大成用户桌面闪窗风暴（实测 93s ~150 次 conhost）。
// 本助手给 exec.Cmd 补 HideWindow，包内测试统一走（实现侧零闪窗由
// update.SpawnDetachedHidden/launchTxCmdImpl 承担，这里是测试面补齐）。

import (
	"os/exec"
	"syscall"
)

// hideConsole 给测试内 console 子进程补 HideWindow（零闪窗）。
func hideConsole(c *exec.Cmd) {
	c.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
}
