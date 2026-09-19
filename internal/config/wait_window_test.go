package config

// wait_window_test.go — 票04：[wait_window] 配置节（等待窗心跳三态）。
//
// 校验面（spec「心跳·配置」）：mode ∈ {off, observe, enforce}（默认 off，
// 缺节即 off）；manual_wait_cap_s 可选、≥0（0=未配置；只能向下夹紧计算器
// 输出——夹紧发生在使用点，Validate 不做夹取）。enforce＋无 [dock] 是
// 运行时降级告警（watcher 侧），不是配置拒启——本文件钉死"不拒绝"。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWaitWindowDefaultOff(t *testing.T) {
	// 缺省 off：无配置时 daemon 行为与本版之前完全一致（验收底线）。
	cfg := Default()
	if cfg.WaitWindow.Mode != "off" {
		t.Fatalf("默认 mode = %q, want off", cfg.WaitWindow.Mode)
	}
	if cfg.WaitWindow.ManualWaitCapS != 0 {
		t.Fatalf("默认 manual_wait_cap_s = %v, want 0（未配置）", cfg.WaitWindow.ManualWaitCapS)
	}
	if err := Validate(cfg, false); err != nil {
		t.Fatalf("默认配置应通过校验: %v", err)
	}
}

func TestWaitWindowTOMLSectionOverride(t *testing.T) {
	// [wait_window] 整节覆盖：mode 与 manual_wait_cap_s 皆可配。
	dir := t.TempDir()
	p := filepath.Join(dir, "config.toml")
	toml := `
[wait_window]
mode = "enforce"
manual_wait_cap_s = 1234.5

[dock]
upstream_base_url = "http://127.0.0.1:15721"
`
	if err := os.WriteFile(p, []byte(toml), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(p, true)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.WaitWindow.Mode != "enforce" {
		t.Fatalf("mode = %q, want enforce", cfg.WaitWindow.Mode)
	}
	if cfg.WaitWindow.ManualWaitCapS != 1234.5 {
		t.Fatalf("manual_wait_cap_s = %v, want 1234.5", cfg.WaitWindow.ManualWaitCapS)
	}
}

func TestWaitWindowPartialSectionKeepsDefaults(t *testing.T) {
	// 节存在但只配 mode：manual_wait_cap_s 回落默认 0（缺字段回落，同其他节）。
	dir := t.TempDir()
	p := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(p, []byte("[wait_window]\nmode = \"observe\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(p, true)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.WaitWindow.Mode != "observe" || cfg.WaitWindow.ManualWaitCapS != 0 {
		t.Fatalf("wait_window = %+v, want observe/0", cfg.WaitWindow)
	}
}

func TestWaitWindowBadModeRejected(t *testing.T) {
	cfg := Default()
	cfg.WaitWindow.Mode = "loud"
	err := Validate(cfg, false)
	if err == nil || !strings.Contains(err.Error(), "wait_window.mode") {
		t.Fatalf("非法 mode 应拒启（文案含 wait_window.mode）, err = %v", err)
	}
}

func TestWaitWindowNegativeCapRejected(t *testing.T) {
	// 负上限无意义（0 已是"未配置"）：拒绝，杜绝拍脑袋负值进夹紧逻辑。
	cfg := Default()
	cfg.WaitWindow.Mode = "observe"
	cfg.WaitWindow.ManualWaitCapS = -5
	err := Validate(cfg, false)
	if err == nil || !strings.Contains(err.Error(), "manual_wait_cap_s") {
		t.Fatalf("负 manual_wait_cap_s 应拒启, err = %v", err)
	}
}

func TestWaitWindowEnforceWithoutDockNotRejected(t *testing.T) {
	// enforce＋无 [dock]：运行时降级（启动告警一次＋按 observe 对待，watcher 侧），
	// 不是配置拒启——问询守望也不受此校验影响。
	cfg := Default()
	cfg.WaitWindow.Mode = "enforce"
	if cfg.Dock != nil {
		t.Fatal("前提：Default 无 [dock]")
	}
	if err := Validate(cfg, false); err != nil {
		t.Fatalf("enforce＋渡口关不应被 Validate 拒绝（运行时降级）: %v", err)
	}
}
