//go:build windows

package update

// Windows 进程面(票05 seam B):PID 活性 / PID→映像路径 / kill / detached
// 隐藏拉起。全部经 golang.org/x/sys/windows(依赖树既有;票面明示手段)。

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
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

// hiddenLauncherName 隐藏点火脚本(installer 生成,与 start-daemon.cmd 同
// 目录同基名;Run 键/看门同款通道)。
const hiddenLauncherName = "start-daemon-hidden.vbs"

// hiddenLauncherSibling 点火脚本同目录的隐藏 VBS;在位返回其路径,不在空
// (测试世界/未安装环境无此文件,回落 cmd.exe 直拉)。
func hiddenLauncherSibling(cmdPath string) string {
	p := filepath.Join(filepath.Dir(cmdPath), hiddenLauncherName)
	if _, err := os.Stat(p); err == nil {
		return p
	}
	return ""
}

// launchCmdImpl 无窗口拉起点火脚本。优先 wscript 走隐藏点火 VBS——wscript
// 是 GUI 子系统宿主,永不创建控制台(Run 键/看门/af78f82 零闪窗铁律的同一
// 通道);脚本自身的 >> 重定向由 cmd 解释,daemon 输出照落 serve 日志。VBS
// 不在位(或 wscript 缺席,极罕见)回落 cmd.exe /c + CREATE_NO_WINDOW:
// 注意不能用 DETACHED_PROCESS——cmd.exe 被脱离控制台启动后执行批处理会
// **自建可见控制台**(2026-09-22 v0.1.4 生产实测:升级失败 restoreService
// Quiet 拉起的守护常驻一个控制台窗体即此坑),CREATE_NO_WINDOW 给它隐藏
// 控制台才是对的。Start 不 Wait,拉起即走。
func launchCmdImpl(cmdPath string) error {
	if vbs := hiddenLauncherSibling(cmdPath); vbs != "" {
		if _, err := exec.LookPath("wscript.exe"); err == nil {
			c := exec.Command("wscript.exe", "/B", vbs)
			c.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
			return c.Start()
		}
	}
	c := exec.Command("cmd.exe", "/c", cmdPath)
	c.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: windows.CREATE_NO_WINDOW | windows.CREATE_NEW_PROCESS_GROUP,
	}
	return c.Start()
}

// spawnRelayImpl detached 隐藏拉起自中继副本(直拉 exe 本体,不经 cmd.exe——
// 副本是可执行文件不是脚本,DETACHED_PROCESS 对它就是无控制台,无 cmd.exe
// 自建控制台的坑;stdout 无人看,结果走 notify/journal/update.log/doctor)。
func spawnRelayImpl(exe string, args []string) error {
	c := exec.Command(exe, args...)
	c.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: windows.DETACHED_PROCESS | windows.CREATE_NEW_PROCESS_GROUP,
	}
	return c.Start()
}
