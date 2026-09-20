//go:build windows

package update

// Windows 进程面(票05 seam B):PID 活性 / PID→映像路径 / kill / detached
// 隐藏拉起。全部经 golang.org/x/sys/windows(依赖树既有;票面明示手段)。

import (
	"errors"
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// stillActive GetExitCodeProcess 的「未退」惯例值(STATUS_PENDING 同值;
// x/sys 未导出,字面量 + 注释钉死)。
const stillActive = 259

// procAliveImpl PID 活性:OpenProcess(QUERY_LIMITED) 成功且退出码仍为
// STILL_ACTIVE。打不开(不存在/权限)一律不活。
func procAliveImpl(pid int) bool {
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

// procImageImpl PID→进程映像完整路径(QueryFullProcessImageName;与锁/拒杀
// 的换装目标做 samePath 比对)。
func procImageImpl(pid int) (string, error) {
	if pid <= 0 {
		return "", errors.New("非法 PID")
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return "", err
	}
	defer windows.CloseHandle(h)
	buf := make([]uint16, 1024)
	size := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(h, 0, &buf[0], &size); err != nil {
		return "", err
	}
	return windows.UTF16ToString(buf[:size]), nil
}

// killImpl TerminateProcess(退出码 1)。
func killImpl(pid int) error {
	h, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)
	return windows.TerminateProcess(h, 1)
}

// launchCmdImpl detached 隐藏拉起点火脚本(cmd.exe /c;DETACHED_PROCESS +
// CREATE_NEW_PROCESS_GROUP 脱离父控制台,HideWindow 双保险——与 Run 键/
// 看门的「无窗口拉起」同语义;Start 不 Wait,拉起即走)。脚本自身的 >>
// 重定向由 cmd 解释,daemon 输出照落 serve 日志。
func launchCmdImpl(cmdPath string) error {
	c := exec.Command("cmd.exe", "/c", cmdPath)
	c.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: windows.DETACHED_PROCESS | windows.CREATE_NEW_PROCESS_GROUP,
	}
	return c.Start()
}

// spawnRelayImpl detached 隐藏拉起自中继副本(直拉 exe 本体,不经 cmd.exe——
// 副本是可执行文件不是脚本;脱离语义与 launchCmdImpl 同款,stdout 无人看,
// 结果走 notify/journal/doctor)。
func spawnRelayImpl(exe string, args []string) error {
	c := exec.Command(exe, args...)
	c.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: windows.DETACHED_PROCESS | windows.CREATE_NEW_PROCESS_GROUP,
	}
	return c.Start()
}
