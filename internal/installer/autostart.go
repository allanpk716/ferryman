// autostart.go — 票02：HKCU Run 键登录自启（常驻保障第 1 腿，规格
// docs/superpowers/specs/20260919-dock-heartbeat-spec.md「常驻保障」节）。
//
// 真实冒烟清单（runbook 票引用；本票单测全走 fake 注册表面，不碰真注册表）：
//   1. ferryman autostart install → reg query
//      "HKCU\Software\Microsoft\Windows\CurrentVersion\Run" /v Ferryman 值在
//   2. 再跑一次 install           → 值不重复写（幂等；值内容逐字不变）
//   3. ferryman autostart status  → installed
//   4. 注销并重新登录             → daemon 无窗口自拉起（127.0.0.1:7311 在听）
//   5. ferryman autostart uninstall → reg query 报系统找不到指定的注册表项或值
//   6. ferryman autostart status  → missing
//
// 启动形态沿用现有无窗口方式（钩子自举 ferryman-ensure.ps1 的 Start-Process
// -WindowStyle Hidden，生产已验证），Run 键值＝PS 包装拉起 ~/ferryman/
// start-daemon.cmd（与看门拉起共用 daemonStartScript 构造，见 watchdog.go，
// 杜绝两处漂移）。值形（\" 是 powershell.exe 原生命令行解析的内嵌引号转义；
// $env:ComSpec 由 PS 运行时解析，机器间不漂移）：
//
//	powershell -NoProfile -WindowStyle Hidden -Command "Start-Process -FilePath $env:ComSpec -ArgumentList '/c','\"<launcher>\"' -WindowStyle Hidden"
//
// Windows 专属：注册表走 golang.org/x/sys/windows/registry（go.mod 已有
// x/sys 依赖，无需新增）。
package installer

import (
	"errors"
	"fmt"
	"path/filepath"

	"golang.org/x/sys/windows/registry"
)

// RunKeyPath / RunValueName 自启键位（规格逐字：HKCU Run 键，值名 Ferryman）。
const (
	RunKeyPath   = `Software\Microsoft\Windows\CurrentVersion\Run`
	RunValueName = "Ferryman"
)

// autostartStatus 三态（doctor「存在/缺失/值不符」同口径）。
type autostartStatus int

const (
	autostartMissing autostartStatus = iota
	autostartInstalled
	autostartMismatch
)

// runKeyStore Run 键读写面（registry.Key 同签名，测试注 fake 密闭）。
type runKeyStore interface {
	GetStringValue(name string) (string, uint32, error)
	SetStringValue(name, value string) error
	DeleteValue(name string) error
	Close() error
}

// registry.Key 结构满足读写面（编译期钉住签名漂移；Key 是句柄命名类型，
// 零值即满足接口——仅作断言，不拿来读写）。
var _ runKeyStore = registry.Key(0)

// 注册表访问权（win32 原值；registry 包常量是 KeyAccess 类型且不导出 DELETE，
// 这里的面统一 uint32）。
const (
	regQueryValue uint32 = 0x0001      // KEY_QUERY_VALUE
	regSetValue   uint32 = 0x0002      // KEY_SET_VALUE
	regDelete     uint32 = 0x0001_0000 // DELETE
)

// autostartDeps 可注入面（真实入口 realAutostartDeps 装配；测试注 fake）。
type autostartDeps struct {
	open     func(access uint32) (runKeyStore, error)
	launcher string // 点火脚本绝对路径（~/ferryman/start-daemon.cmd）
}

// AutostartCommand Run 键值（与看门拉起共用 daemonStartScript，票面「抽公共
// 函数或引用同常量」的落点）。
func AutostartCommand(launcher string) string {
	return fmt.Sprintf(`powershell -NoProfile -WindowStyle Hidden -Command "%s"`, daemonStartScript(launcher))
}

// realAutostartDeps 真注册表面：launcher = ~/ferryman/start-daemon.cmd（与
// ferryman-ensure.ps1 拉的同一脚本，单一事实源）。
func realAutostartDeps() autostartDeps {
	return autostartDeps{
		open:     openRunKey,
		launcher: filepath.Join(homeDir(), "ferryman", LauncherName),
	}
}

// openRunKey 真 HKCU Run 键开启；安装（SET_VALUE）路径键缺失自建（正常系统
// Run 键恒在，兜底），读/删路径键缺失原样上抛（上层按 missing 语义收口）。
func openRunKey(access uint32) (runKeyStore, error) {
	k, err := registry.OpenKey(registry.CURRENT_USER, RunKeyPath, access)
	if err == nil || !errors.Is(err, registry.ErrNotExist) || access&regSetValue == 0 {
		return k, err
	}
	k2, _, cerr := registry.CreateKey(registry.CURRENT_USER, RunKeyPath, access) // CreateKey 三返回（openedExisting 忽略）
	return k2, cerr
}

// installAutostart 幂等安装：值已对不重写（验收「再 Install 幂等（值不重复
// 写）」的可观察面）；值不符（exe 挪窝/手改）覆盖修复。
func installAutostart(d autostartDeps) error {
	k, err := d.open(regQueryValue | regSetValue)
	if err != nil {
		return err
	}
	defer k.Close()
	want := AutostartCommand(d.launcher)
	if cur, _, err := k.GetStringValue(RunValueName); err == nil && cur == want {
		return nil
	}
	return k.SetStringValue(RunValueName, want)
}

// uninstallAutostart 幂等卸载：键缺/值缺都算已卸载（幂等）。
func uninstallAutostart(d autostartDeps) error {
	k, err := d.open(regDelete)
	if err != nil {
		if errors.Is(err, registry.ErrNotExist) {
			return nil
		}
		return err
	}
	defer k.Close()
	if err := k.DeleteValue(RunValueName); err != nil && !errors.Is(err, registry.ErrNotExist) {
		return err
	}
	return nil
}

// autostartStatusOf 三态判定：键/值缺 = missing；值全等 = installed；
// 其余 = mismatch。读失败（非缺失语义）响亮上抛——绝不误报"已卸载"。
func autostartStatusOf(d autostartDeps) (autostartStatus, error) {
	k, err := d.open(regQueryValue)
	if err != nil {
		if errors.Is(err, registry.ErrNotExist) {
			return autostartMissing, nil
		}
		return autostartMissing, err
	}
	defer k.Close()
	cur, _, err := k.GetStringValue(RunValueName)
	if err != nil {
		if errors.Is(err, registry.ErrNotExist) {
			return autostartMissing, nil
		}
		return autostartMissing, err
	}
	if cur == AutostartCommand(d.launcher) {
		return autostartInstalled, nil
	}
	return autostartMismatch, nil
}

// ---- CLI 真实入口（cmd/ferryman 薄分发；打印 + 退出码，install-cc 同风格） ----

// AutostartInstall 装 Run 键自启（ferryman autostart install）。
func AutostartInstall() int {
	deps := realAutostartDeps()
	if err := installAutostart(deps); err != nil {
		fmt.Println(err)
		return 1
	}
	fmt.Printf("Run 键自启已安装（HKCU %s\\%s；登录时无窗口拉起 %s）\n",
		RunKeyPath, RunValueName, deps.launcher)
	return 0
}

// AutostartUninstall 卸 Run 键自启（ferryman autostart uninstall）。
func AutostartUninstall() int {
	if err := uninstallAutostart(realAutostartDeps()); err != nil {
		fmt.Println(err)
		return 1
	}
	fmt.Printf("Run 键自启已卸载（HKCU %s\\%s 已移除）\n", RunKeyPath, RunValueName)
	return 0
}

// AutostartStatus 报三态（ferryman autostart status）。
func AutostartStatus() int {
	st, err := autostartStatusOf(realAutostartDeps())
	if err != nil {
		fmt.Println(err)
		return 1
	}
	switch st {
	case autostartInstalled:
		fmt.Println("Run 键自启: installed")
	case autostartMismatch:
		fmt.Println("Run 键自启: mismatch（值不符——exe 挪窝/手改，重跑 ferryman autostart install 修复）")
	default:
		fmt.Println("Run 键自启: missing")
	}
	return 0
}
