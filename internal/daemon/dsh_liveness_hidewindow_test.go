//go:build windows

package daemon

import (
	"context"
	"testing"
)

// TestDshLivenessProbeCmdHideWindow 零闪窗铁律机器可验面（自 dsh_liveness_test.go 拆出，
// windows 构建 tag：HideWindow 字段 unix 侧不存在——原 runtime.GOOS skip 挡不住编译期
// 字段检查，整个 daemon 测试包在 darwin 上 build failed）。
func TestDshLivenessProbeCmdHideWindow(t *testing.T) {
	c := dshNewCmd(context.Background(), "tasklist")
	if c.SysProcAttr == nil || !c.SysProcAttr.HideWindow {
		t.Fatal("零闪窗铁律：宿主枚举命令缺 SysProcAttr.HideWindow")
	}
}
