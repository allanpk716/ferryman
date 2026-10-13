// watchdog_launch_test.go — 看门拉起链真跑验收（2026-10-13 train3060 事故）。
//
// 看门 48 次拉起 0 次复位确认：daemonStartScript 把 hooks/ferryman-ensure.ps1
// 的 PS 反引号转义错译成 `\"`，经 Go exec 再转义一层后 PS 拿到字面量
// `\"<path>\"`，cmd /c 报「不是内部或外部命令」，powershell 本身却退出 0——
// LaunchDaemon 返回 nil，点火脚本一行没跑。构造层逐字断言测不出这类转义叠加，
// 这里真拉 powershell → Start-Process → cmd → 点火脚本，以脚本落标记为准。

package installer

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLaunchDaemonActuallyRunsLauncher(t *testing.T) {
	if _, err := exec.LookPath("powershell"); err != nil {
		t.Skip("无 powershell")
	}
	dir := filepath.Join(t.TempDir(), "data dir") // 带空格：生产路径不保证无空格
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(dir, "ran.txt")
	launcher := filepath.Join(dir, LauncherName)
	body := "@echo off\r\necho ran> \"" + marker + "\"\r\n"
	if err := os.WriteFile(launcher, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := LaunchDaemon(launcher); err != nil {
		t.Fatalf("LaunchDaemon: %v", err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(marker); err == nil && strings.TrimSpace(string(b)) == "ran" {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("点火脚本未被执行（15s 内无标记 %s）——拉起链静默失败", marker)
}
