//go:build !windows

package daemon

// dsh_liveness_other.go — verify-dsh 票01 宿主旁证的非 Windows 存根：unix
// 无控制台闪窗面、无 tasklist，进程枚举如实 unknown（不冒充「不在」，端口
// 腿独立裁决）；只保证包可编译、语义尽力对齐——proc_other.go 同惯例。生产
// 主目标 Windows（ferryman_windows_amd64）。

import (
	"context"
	"os/exec"
)

func init() { dshHarnessCount = dshHarnessCountOther }

// dshNewCmd 非 Windows 形态：SysProcAttr 无 HideWindow 字段可设（unix 无
// 控制台闪窗面），context 超时语义与 Windows 分支同形。
func dshNewCmd(ctx context.Context, name string, args ...string) *exec.Cmd {
	return exec.CommandContext(ctx, name, args...)
}

// dshHarnessCountOther 非 Windows 不枚举：如实 unknown。
func dshHarnessCountOther() (int, bool, string) {
	return 0, false, "harness=unknown(非 Windows 不枚举)"
}
