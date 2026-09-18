package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestResolveDataDir 数据根自动探测：根（无 *.jsonl、有 accounts/）下钻一层；
// 其余情况原样返回（accounts 直传、平铺 jsonl、两者皆无的空目录）。
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
