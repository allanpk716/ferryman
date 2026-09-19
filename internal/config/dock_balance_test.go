// dock_balance_test.go — 票07：[dock].balance_url 可选字段解析钉子
// （覆写原样保留；缺省回落 DefaultDockBalanceURL 单源，同 upstream_base_url
// 的解析层补默认惯例）。
package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDockBalanceURLOverrideAndDefault(t *testing.T) {
	// 覆写：显式给出 balance_url 则原样保留（测试/部署指 mock 端点用的洞）。
	f := filepath.Join(t.TempDir(), "dock_override.toml")
	if err := os.WriteFile(f, []byte(`
[dock]
api_key = "k-test"
balance_url = "http://127.0.0.1:19999/api/user/balance"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(f, false)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Dock == nil {
		t.Fatal("[dock] 节应构造 DockCfg")
	}
	if cfg.Dock.BalanceURL != "http://127.0.0.1:19999/api/user/balance" {
		t.Fatalf("BalanceURL = %q, want 覆写值", cfg.Dock.BalanceURL)
	}

	// 缺省：未给 balance_url 回落内置默认端点。
	f2 := filepath.Join(t.TempDir(), "dock_default.toml")
	if err := os.WriteFile(f2, []byte(`
[dock]
api_key = "k-test"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg2, err := Load(f2, false)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg2.Dock.BalanceURL != DefaultDockBalanceURL {
		t.Fatalf("BalanceURL = %q, want 默认 %s", cfg2.Dock.BalanceURL, DefaultDockBalanceURL)
	}
}
