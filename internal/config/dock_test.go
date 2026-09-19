// dock_test.go — 票01：[dock] 配置节解析验收钉子。
// F11 裁定语义的可执行证明：节缺失＝cfg.Dock 为 nil（＝渡口完全不启动，
// daemon 侧据此不绑端口）；节存在＝缺字段回落默认值。
package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultDockNil(t *testing.T) {
	// 默认不配渡口：Default() 不构造 Dock——nil 即"零行为"判据（F11）
	if Default().Dock != nil {
		t.Fatalf("Default().Dock = %+v, want nil", Default().Dock)
	}
}

func TestLoadNoDockSectionKeepsNil(t *testing.T) {
	// 配了其他节但没配 [dock]：Dock 仍为 nil
	f := filepath.Join(t.TempDir(), "no-dock.toml")
	if err := os.WriteFile(f, []byte("[server]\nport = 7399\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(f, false)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Dock != nil {
		t.Fatalf("cfg.Dock = %+v, want nil（节缺失＝渡口不启动）", cfg.Dock)
	}
}

func TestLoadDockSectionDefaults(t *testing.T) {
	// 节存在但空：两字段回落默认值（上游=cc-switch 15721 / 监听=本机 15722）
	f := filepath.Join(t.TempDir(), "dock-empty.toml")
	if err := os.WriteFile(f, []byte("[dock]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(f, false)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Dock == nil {
		t.Fatal("cfg.Dock = nil, want 非空（节存在即启用）")
	}
	if cfg.Dock.UpstreamBaseURL != "http://127.0.0.1:15721" {
		t.Fatalf("upstream_base_url = %q, want 默认 http://127.0.0.1:15721", cfg.Dock.UpstreamBaseURL)
	}
	if cfg.Dock.Listen != "127.0.0.1:15722" {
		t.Fatalf("listen = %q, want 默认 127.0.0.1:15722", cfg.Dock.Listen)
	}
}

func TestLoadDockSectionExplicit(t *testing.T) {
	// 两字段显式覆盖；节内缺 listen 回落默认（.get 语义）
	f := filepath.Join(t.TempDir(), "dock.toml")
	if err := os.WriteFile(f, []byte(`
[dock]
upstream_base_url = "http://127.0.0.1:16001"
listen = "127.0.0.1:16002"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(f, false)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Dock == nil || cfg.Dock.UpstreamBaseURL != "http://127.0.0.1:16001" ||
		cfg.Dock.Listen != "127.0.0.1:16002" {
		t.Fatalf("dock = %+v, want 显式值", cfg.Dock)
	}

	f2 := filepath.Join(t.TempDir(), "dock-partial.toml")
	if err := os.WriteFile(f2, []byte("[dock]\nupstream_base_url = \"http://10.0.0.1:80\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg2, err := Load(f2, false)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg2.Dock == nil || cfg2.Dock.UpstreamBaseURL != "http://10.0.0.1:80" ||
		cfg2.Dock.Listen != "127.0.0.1:15722" {
		t.Fatalf("dock = %+v, want listen 回落默认", cfg2.Dock)
	}
}

func TestLoadDockBadSectionFails(t *testing.T) {
	// 节值不是表：与其他节同款报错
	f := filepath.Join(t.TempDir(), "dock-bad.toml")
	if err := os.WriteFile(f, []byte("dock = 123\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(f, false); err == nil {
		t.Fatal("Load(dock=123) err = nil, want 非空")
	}
}
