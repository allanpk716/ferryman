//go:build windows

package daemon

// dsh_liveness_windows.go — verify-dsh 票01 宿主旁证的 Windows 分支：枚举
// DeepSeek Harness.exe（tasklist 过滤直查，CSV/NH 好解析）。
//
// 零闪窗铁律（2026-09-21/2026-10-06 两事故立规）：daemon 生产形态是无控制台
// 进程（监督者 CREATE_NO_WINDOW 拉起），console 子进程 tasklist 被无控制台
// 宿主拉起必新建可见窗口——一律显式 SysProcAttr{HideWindow: true}（update.
// launchTxCmdImpl 同款）。HideWindow 是 Windows 专属 SysProcAttr 字段，平台
// 分支照 proc_windows.go/proc_other.go 惯例分文件——票面「Windows 分支」的
// 仓库既定实现形态（本文件与 dsh_liveness_other.go 即 dsh_liveness.go 的
// 平台面，同属票 01 落点）。

import (
	"context"
	"fmt"
	"os/exec"
	"syscall"
	"time"
)

func init() { dshHarnessCount = dshHarnessCountWindows }

// dshNewCmd 宿主枚举命令构造缝（测试断言 HideWindow 在位的机器可验面）：
// console 子进程一律 HideWindow；context 超时兜底由调用方给（旁证探针不拖
// 死 /dsh/health）。
func dshNewCmd(ctx context.Context, name string, args ...string) *exec.Cmd {
	c := exec.CommandContext(ctx, name, args...)
	c.SysProcAttr = &syscall.SysProcAttr{HideWindow: true} // 零闪窗铁律（Windows 分支）
	return c
}

// dshHarnessCountWindows tasklist /FI 直查映像名。无匹配时 tasklist 可能带
// 非零退出码且打本地化 INFO 行——出参有内容先解析，解析零行且带错才记
// unknown（旁证不可知不冒充「不在」，端口腿独立裁决）。
func dshHarnessCountWindows() (int, bool, string) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	c := dshNewCmd(ctx, "tasklist", "/FI", "IMAGENAME eq "+dshHarnessImage,
		"/FO", "CSV", "/NH")
	out, err := c.Output()
	n := dshParseTasklistOutput(string(out))
	if n > 0 {
		return n, true, fmt.Sprintf("harness=%d", n)
	}
	if err != nil {
		return 0, false, fmt.Sprintf("harness=unknown(%v)", err)
	}
	return 0, true, "harness=0"
}
