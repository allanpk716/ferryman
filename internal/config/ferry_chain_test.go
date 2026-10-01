package config

// ferry_chain_test.go — 票02：[ferry] 段 chain 顺位链解析、provider 单键向后
// 兼容（等价单元素链）、并存以 chain 为准 + 一行警告、链引用缺名上抛
// （坏 TOML 同款语义）、空链 ≡ 未配置（降级骨架语义不变）。
//
// 文件落位注记：[ferry] 段解析在 config.go applyTOML（票面"以实际代码为
// 准"），故配置层钉子新建于本包；新建独立文件不碰 config_test.go 存量行。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeFerryChainToml 落一份测试配置，返回路径。
func writeFerryChainToml(t *testing.T, content string) string {
	t.Helper()
	f := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(f, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return f
}

// 回归钉（验收①）：[ferry] provider 单键旧配置解析行为与现状完全一致——
// FerryProvider 原样、FerryChain 保持 nil；names() 给出单元素链（provider
// 等价单元素链的向后兼容语义）。

func TestFerryProviderOnlyUnchanged(t *testing.T) {
	f := writeFerryChainToml(t, "[ferry]\nprovider = \"glm\"\n\n[providers.glm]\nmodel = \"m\"\n")
	cfg, err := Load(f, false)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.FerryProvider != "glm" {
		t.Fatalf("FerryProvider = %q, want glm", cfg.FerryProvider)
	}
	if cfg.FerryChain != nil {
		t.Fatalf("FerryChain = %v, want nil", cfg.FerryChain)
	}
	got := cfg.FerryChainNames()
	if len(got) != 1 || got[0] != "glm" {
		t.Fatalf("FerryChainNames = %v, want [glm]", got)
	}
}

// chain 多元素 → 有序链原样解析；provider 未配置时 FerryProvider 保持空。

func TestFerryChainOrdered(t *testing.T) {
	f := writeFerryChainToml(t, "[ferry]\nchain = [\"a\", \"b\", \"c\"]\n\n"+
		"[providers.a]\nmodel = \"m\"\n[providers.b]\nmodel = \"m\"\n[providers.c]\nmodel = \"m\"\n")
	cfg, err := Load(f, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.FerryChain) != 3 || cfg.FerryChain[0] != "a" ||
		cfg.FerryChain[1] != "b" || cfg.FerryChain[2] != "c" {
		t.Fatalf("FerryChain = %v, want [a b c]", cfg.FerryChain)
	}
	if cfg.FerryProvider != "" {
		t.Fatalf("FerryProvider = %q, want 空", cfg.FerryProvider)
	}
	if got := cfg.FerryChainNames(); len(got) != 3 || got[0] != "a" || got[2] != "c" {
		t.Fatalf("FerryChainNames = %v, want [a b c]", got)
	}
}

// 并存（验收②）：以 chain 为准 + 恰一行警告（点名 chain/provider 与被忽略
// 的 provider 值）。

func TestFerryChainWinsOverProviderWithOneWarningLine(t *testing.T) {
	f := writeFerryChainToml(t, "[ferry]\nprovider = \"old\"\nchain = [\"n1\", \"n2\"]\n\n"+
		"[providers.n1]\nmodel = \"m\"\n[providers.n2]\nmodel = \"m\"\n[providers.old]\nmodel = \"m\"\n")
	readOut := capturePipe(t, &os.Stdout)
	cfg, err := Load(f, false)
	out := readOut()
	if err != nil {
		t.Fatal(err)
	}
	got := cfg.FerryChainNames()
	if len(got) != 2 || got[0] != "n1" || got[1] != "n2" {
		t.Fatalf("并存应取 chain, got %v", got)
	}
	if strings.Count(out, "\n") != 1 {
		t.Fatalf("并存应留恰一行警告, got %d 行: %q", strings.Count(out, "\n"), out)
	}
	for _, want := range []string{"chain", "provider", "old"} {
		if !strings.Contains(out, want) {
			t.Fatalf("警告缺 %q: %q", want, out)
		}
	}
}

// 链引用缺名 → 配置错误上抛（坏 TOML 同款语义：Load 失败 → 装配处捕获降级
// 骨架）；错误点名缺失的 provider。供应商表整体缺失时链必缺名，同款上抛。

func TestFerryChainMissingNameRaises(t *testing.T) {
	f := writeFerryChainToml(t, "[ferry]\nchain = [\"a\", \"ghost\"]\n\n[providers.a]\nmodel = \"m\"\n")
	_, err := Load(f, false)
	if err == nil {
		t.Fatal("链引用缺名应上抛配置错误（坏 TOML 同款语义）")
	}
	if !strings.Contains(err.Error(), "ghost") {
		t.Fatalf("错误应点名缺失 provider: %v", err)
	}
	f2 := writeFerryChainToml(t, "[ferry]\nchain = [\"a\"]\n")
	if _, err := Load(f2, false); err == nil {
		t.Fatal("无供应商表时链引用应上抛")
	}
}

// 空链 ≡ 未配置（验收④的降级侧）：不校验、不告警；names() 回落 provider
// 单元素链；两者皆无 → nil（worker 现行 provider=="" 降级骨架路径不变）。

func TestFerryChainEmptyEqualsUnconfigured(t *testing.T) {
	f := writeFerryChainToml(t, "[ferry]\nchain = []\nprovider = \"glm\"\n\n[providers.glm]\nmodel = \"m\"\n")
	readOut := capturePipe(t, &os.Stdout)
	cfg, err := Load(f, false)
	out := readOut()
	if err != nil {
		t.Fatal(err)
	}
	if out != "" {
		t.Fatalf("空链 + provider 不应告警, got %q", out)
	}
	got := cfg.FerryChainNames()
	if len(got) != 1 || got[0] != "glm" {
		t.Fatalf("空链应回落 provider 单元素链, got %v", got)
	}
	if got := Default().FerryChainNames(); got != nil {
		t.Fatalf("未配置 names = %v, want nil", got)
	}
}

// chain 键非数组 → 解析错误（与 watch.codex_extra_dirs 同款纪律）。

func TestFerryChainNonArrayRejected(t *testing.T) {
	f := writeFerryChainToml(t, "[ferry]\nchain = \"a\"\n")
	_, err := Load(f, false)
	if err == nil || !strings.Contains(err.Error(), "不是数组") {
		t.Fatalf("chain 非数组应报解析错误: %v", err)
	}
}
