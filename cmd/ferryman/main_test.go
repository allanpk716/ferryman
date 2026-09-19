package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// TestResolveDataDir 数据根自动探测：根（无 *.jsonl、有 accounts/）下钻一层；
// 其余情况原样返回（accounts 直传、平铺 jsonl、两者皆无的空目录）。
// （cmd/viewer 迁入，票22 附录#3：viewer 测试不删。）
func TestResolveDataDir(t *testing.T) {
	root := t.TempDir()
	acc := filepath.Join(root, "accounts")
	if err := os.MkdirAll(acc, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(acc, "202609.jsonl"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if got := resolveDataDir(root); got != acc {
		t.Fatalf("数据根 → %q, want 自动下钻 %q", got, acc)
	}
	if got := resolveDataDir(acc); got != acc {
		t.Fatalf("accounts 直传 → %q, want 原样 %q", got, acc)
	}

	// 根里直接放 *.jsonl：本身就是账本目录，不下钻
	flat := t.TempDir()
	if err := os.WriteFile(filepath.Join(flat, "a.jsonl"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := resolveDataDir(flat); got != flat {
		t.Fatalf("平铺 jsonl 目录 → %q, want 原样 %q", got, flat)
	}

	// 无 jsonl 也无 accounts/：原样返回，交服务端报「数据目录不存在」
	empty := t.TempDir()
	if got := resolveDataDir(empty); got != empty {
		t.Fatalf("空目录 → %q, want 原样 %q", got, empty)
	}
}

// TestParseEvents --events 逗号列表解析：空白裁剪、空段丢弃；
// 空/缺省 = nil（= installer.normalizeEvents 的全集语义）。
func TestParseEvents(t *testing.T) {
	got := parseEvents("SessionStart, SubagentStart ,SubagentStop")
	want := []string{"SessionStart", "SubagentStart", "SubagentStop"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseEvents 子集 = %v, want %v", got, want)
	}
	if v := parseEvents(""); v != nil {
		t.Fatalf("空串应得 nil（全集语义）, got %v", v)
	}
	if v := parseEvents(" , ,"); v != nil {
		t.Fatalf("全空段应得 nil（全集语义）, got %v", v)
	}
	if v := parseEvents("UserPromptSubmit"); !reflect.DeepEqual(v, []string{"UserPromptSubmit"}) {
		t.Fatalf("单事件 = %v, want [UserPromptSubmit]", v)
	}
}
