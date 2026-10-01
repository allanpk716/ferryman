package ferry

// ferry_chain_test.go — 票02：供应商表 protocol/extra_body 两键解析钉 +
// 顺位链解析产物 ResolveChain 行为钉。
//
// 分工注记：[ferry] 段（chain 键、provider 单键兼容、并存警告、缺名校验）
// 解析归 config 包（config.go applyTOML——票面"以实际代码为准"），那一侧
// 的钉在 internal/config/ferry_chain_test.go；本文件只钉 ferry 包自己的面
// （[providers.*] 两新键 + 链名字表→有序 Provider 链的纯解析产物）。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeProvidersToml 落一份测试配置，返回路径。
func writeProvidersToml(t *testing.T, content string) string {
	t.Helper()
	f := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(f, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return f
}

// protocol 键缺失 → 缺省 openai；extra_body 键缺失 → 空（零值=无透传，
// 向后兼容：旧配置解析出的 Provider 仅多出缺省 Protocol）。

func TestLoadProvidersProtocolDefaultsOpenAI(t *testing.T) {
	f := writeProvidersToml(t, "[providers.p]\nbase_url = \"http://127.0.0.1:9/v1\"\nmodel = \"m\"\n")
	ps, err := LoadProviders(f)
	if err != nil {
		t.Fatal(err)
	}
	if got := ps["p"].Protocol; got != ProtocolOpenAI {
		t.Fatalf("protocol 缺省 = %q, want %q", got, ProtocolOpenAI)
	}
	if len(ps["p"].ExtraBody) != 0 {
		t.Fatalf("extra_body 缺省应为空, got %v", ps["p"].ExtraBody)
	}
}

// 两键显式配置 → 原样解析：anthropic 档 + 透传字典（标量/布尔/嵌套表齐全）。

func TestLoadProvidersProtocolAndExtraBodyParsed(t *testing.T) {
	f := writeProvidersToml(t, `[providers.p]
base_url = "http://127.0.0.1:9/v1"
model = "m"
protocol = "anthropic"
extra_body = { thinking = { type = "disabled" }, max_tokens = 128, enable = true }
`)
	ps, err := LoadProviders(f)
	if err != nil {
		t.Fatal(err)
	}
	p := ps["p"]
	if p.Protocol != ProtocolAnthropic {
		t.Fatalf("protocol = %q, want %q", p.Protocol, ProtocolAnthropic)
	}
	if len(p.ExtraBody) != 3 {
		t.Fatalf("extra_body 键数 = %d (%v), want 3", len(p.ExtraBody), p.ExtraBody)
	}
	if p.ExtraBody["max_tokens"] != int64(128) {
		t.Fatalf("extra_body.max_tokens = %v (%T), want int64(128)",
			p.ExtraBody["max_tokens"], p.ExtraBody["max_tokens"])
	}
	if p.ExtraBody["enable"] != true {
		t.Fatalf("extra_body.enable = %v, want true", p.ExtraBody["enable"])
	}
	th, ok := p.ExtraBody["thinking"].(map[string]any)
	if !ok || th["type"] != "disabled" {
		t.Fatalf("extra_body.thinking = %v, want {type: disabled}", p.ExtraBody["thinking"])
	}
}

// 顺位链解析产物：按名字表顺序给出 Provider 序列（票03 执行器的输入形状），
// 不去重、不重排。

func TestResolveChainOrdered(t *testing.T) {
	providers := map[string]Provider{
		"a": {Name: "a", Model: "ma"},
		"b": {Name: "b", Model: "mb"},
	}
	chain, err := ResolveChain([]string{"b", "a"}, providers)
	if err != nil {
		t.Fatal(err)
	}
	if len(chain) != 2 || chain[0].Name != "b" || chain[1].Name != "a" {
		t.Fatalf("链序 = [%s %s], want [b a]", chain[0].Name, chain[1].Name)
	}
}

// 空链/未配置 = 既有降级骨架语义不变：(nil, nil)——调用方按空链降级，
// 与 LoadProviders 无配置同形。

func TestResolveChainEmptyNotConfigured(t *testing.T) {
	for _, names := range [][]string{nil, {}} {
		chain, err := ResolveChain(names, map[string]Provider{"a": {Name: "a"}})
		if err != nil || chain != nil {
			t.Fatalf("空链 %v 应 (nil, nil), got (%v, %v)", names, chain, err)
		}
	}
}

// 链引用缺名 → error 上抛（坏 TOML 同款语义：装配处捕获降级骨架 + 警告，
// 不由本函数吞）；错误点名缺失的 provider。

func TestResolveChainMissingNameRaises(t *testing.T) {
	providers := map[string]Provider{"a": {Name: "a"}}
	chain, err := ResolveChain([]string{"a", "ghost"}, providers)
	if err == nil {
		t.Fatal("链引用缺名应上抛错误")
	}
	if !strings.Contains(err.Error(), "ghost") {
		t.Fatalf("错误应点名缺失 provider: %v", err)
	}
	if chain != nil {
		t.Fatalf("出错时链应为 nil, got %v", chain)
	}
}
