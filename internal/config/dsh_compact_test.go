package config

// dsh_compact_test.go — 票02（dsh-hot-compaction）：[dsh_compact] 节钉子
//（默认值/TOML 覆盖/节内缺字段回落/负值拒启）。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDshCompactDefaults(t *testing.T) {
	d := Default()
	dc := d.DshCompact
	if !dc.Enabled || dc.TriggerRatio != 0.8 || dc.MinPeakTokens != 20000 ||
		dc.CommandTTLRatio != 0.2 || dc.PollHintS != 30 || dc.CompressedFlagTTLRatio != 2.0 {
		t.Fatalf("默认 = %+v", dc)
	}
	if err := Validate(d, false); err != nil {
		t.Fatalf("默认配置应通过校验: %v", err)
	}
}

func TestDshCompactTOMLOverride(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(p, []byte(`
[dsh_compact]
enabled = false
trigger_ratio = 0.5
min_peak_tokens = 12345
command_ttl_ratio = 0.25
poll_hint_s = 45.5
compressed_flag_ttl_ratio = 3.0
`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(p, true)
	if err != nil {
		t.Fatal(err)
	}
	dc := cfg.DshCompact
	if dc.Enabled || dc.TriggerRatio != 0.5 || dc.MinPeakTokens != 12345 ||
		dc.CommandTTLRatio != 0.25 || dc.PollHintS != 45.5 || dc.CompressedFlagTTLRatio != 3.0 {
		t.Fatalf("覆盖 = %+v", dc)
	}
	// 节内缺字段回落默认（.get 语义，question_watch.dsh_mode 同款先例）。
	p2 := filepath.Join(dir, "c2.toml")
	if err := os.WriteFile(p2, []byte("[dsh_compact]\ntrigger_ratio = 0.9\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg2, err := Load(p2, true)
	if err != nil {
		t.Fatal(err)
	}
	dc2 := cfg2.DshCompact
	if dc2.TriggerRatio != 0.9 || !dc2.Enabled || dc2.MinPeakTokens != 20000 ||
		dc2.CommandTTLRatio != 0.2 || dc2.PollHintS != 30 || dc2.CompressedFlagTTLRatio != 2.0 {
		t.Fatalf("缺字段回落 = %+v", dc2)
	}
}

func TestDshCompactNegativeRejected(t *testing.T) {
	cases := []struct {
		key string
		set func(*DshCompactCfg)
	}{
		{"trigger_ratio", func(c *DshCompactCfg) { c.TriggerRatio = -0.1 }},
		{"min_peak_tokens", func(c *DshCompactCfg) { c.MinPeakTokens = -1 }},
		{"command_ttl_ratio", func(c *DshCompactCfg) { c.CommandTTLRatio = -0.2 }},
		{"poll_hint_s", func(c *DshCompactCfg) { c.PollHintS = -5 }},
		{"compressed_flag_ttl_ratio", func(c *DshCompactCfg) { c.CompressedFlagTTLRatio = -2 }},
	}
	for _, tc := range cases {
		t.Run(tc.key, func(t *testing.T) {
			cfg := Default()
			// 直改字段模拟解析结果（解析层 pyFloat/pyInt 已挡非数；负值是合法
			// 数——守门归 Validate，wait_window.manual_wait_cap_s 同分工）。
			tc.set(&cfg.DshCompact)
			err := Validate(cfg, true)
			if err == nil || !strings.Contains(err.Error(), "dsh_compact."+tc.key) {
				t.Fatalf("负 %s 应拒启（文案含 dsh_compact.%s）, err = %v", tc.key, tc.key, err)
			}
		})
	}
}
