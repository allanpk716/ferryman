// backup_test.go — 票23：BackupData 单测（复制完整性/daemon.pid 不备/源零改动/
// 同秒幂等不覆盖）。

package cutover

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// seedDataDir 造一个齐装的数据目录：accounts/*.jsonl（含应忽略的非 jsonl）、
// handoffs/*.md（含应忽略的子目录）、index.json、config.toml、daemon.token、
// daemon.pid（明确不备）。返回 (dataDir, 源文件 mtime 快照)。
func seedDataDir(t *testing.T) (string, map[string]time.Time) {
	t.Helper()
	data := t.TempDir()
	mk := func(rel, content string) string {
		p := filepath.Join(data, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	paths := []string{
		mk(filepath.Join("accounts", "202609.jsonl"), `{"v":1,"kind":"handoff"}`+"\n"),
		mk(filepath.Join("accounts", "202608.jsonl"), `{"v":1,"kind":"beat"}`+"\n"),
		mk(filepath.Join("accounts", "readme.txt"), "非 jsonl，不备"),
		mk(filepath.Join("handoffs", "h1.md"), "# 交接"),
		mk(filepath.Join("handoffs", "h2.md"), "# 交接2"),
		mk(filepath.Join("handoffs", "sub", "nested.md"), "子目录不备"),
		mk("index.json", `{"handoffs":[],"pending_prompts":[]}`),
		mk("config.toml", "[server]\nport = 7311\n"),
		mk("daemon.token", "abcd1234"),
		mk("daemon.pid", `{"pid":1,"port":7311}`),
	}
	snap := map[string]time.Time{}
	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		snap[p] = info.ModTime()
	}
	return data, snap
}

// assertSourceUntouched 源零改动自证（只读复制的铁律）：文件集合与 mtime 一致。
func assertSourceUntouched(t *testing.T, before map[string]time.Time) {
	t.Helper()
	for p, mtime := range before {
		info, err := os.Stat(p)
		if err != nil {
			t.Fatalf("源文件消失: %s: %v", p, err)
		}
		if !info.ModTime().Equal(mtime) {
			t.Fatalf("源文件被改动: %s mtime %v → %v", p, mtime, info.ModTime())
		}
	}
}

func TestBackupDataCopiesVerifyAndSkipsPid(t *testing.T) {
	data, before := seedDataDir(t)
	destRoot := t.TempDir()

	dest, err := BackupData(data, destRoot)
	if err != nil {
		t.Fatal(err)
	}
	assertSourceUntouched(t, before)

	// 应备的都在且内容一致
	eq := func(rel, want string) {
		t.Helper()
		raw, err := os.ReadFile(filepath.Join(dest, rel))
		if err != nil {
			t.Fatalf("备份缺 %s: %v", rel, err)
		}
		if string(raw) != want {
			t.Fatalf("备份 %s 内容不一致: %q", rel, raw)
		}
	}
	eq(filepath.Join("accounts", "202609.jsonl"), `{"v":1,"kind":"handoff"}`+"\n")
	eq(filepath.Join("accounts", "202608.jsonl"), `{"v":1,"kind":"beat"}`+"\n")
	eq(filepath.Join("handoffs", "h1.md"), "# 交接")
	eq(filepath.Join("handoffs", "h2.md"), "# 交接2")
	eq("index.json", `{"handoffs":[],"pending_prompts":[]}`)
	eq("config.toml", "[server]\nport = 7311\n")
	eq("daemon.token", "abcd1234")

	// 不应备的不在
	for _, rel := range []string{
		filepath.Join("accounts", "readme.txt"),
		filepath.Join("handoffs", "sub"),
		"daemon.pid",
	} {
		if _, err := os.Stat(filepath.Join(dest, rel)); err == nil {
			t.Fatalf("不应备份的存在: %s", rel)
		}
	}
}

func TestBackupDataSameSecondDoesNotOverwrite(t *testing.T) {
	data, before := seedDataDir(t)
	destRoot := t.TempDir()

	d1, err := BackupData(data, destRoot)
	if err != nil {
		t.Fatal(err)
	}
	d2, err := BackupData(data, destRoot)
	if err != nil {
		t.Fatal(err)
	}
	assertSourceUntouched(t, before)
	if d1 == d2 {
		t.Fatalf("同秒两次备份应得不同目录（序号防覆盖）: %s", d1)
	}
	if _, err := os.Stat(filepath.Join(d2, "index.json")); err != nil {
		t.Fatalf("第二次备份不完整: %v", err)
	}
}

func TestBackupDataMissingSourceFails(t *testing.T) {
	if _, err := BackupData(filepath.Join(t.TempDir(), "nope"), t.TempDir()); err == nil {
		t.Fatal("数据目录不存在应报错")
	}
}
