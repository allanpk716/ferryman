package update

// journal(票05,规格 §C 第2条):staging/swap/verify 三阶段,先写后动;
// 读写/清账原子性。换装文件助手(备份名/保留 2 份/残留清理/copy)同测。

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestJournalRoundTrip(t *testing.T) {
	dir := t.TempDir()

	// 无 journal → (不存在, nil)
	if _, exists, err := loadJournal(dir); err != nil || exists {
		t.Fatalf("无 journal: exists=%v err=%v", exists, err)
	}

	j := journal{Phase: PhaseSwap, From: "v0.1.0", To: "v0.2.0",
		TargetExe: `C:\x\ferryman.exe`, NewExe: `C:\x\ferryman.exe.new`,
		Backup: `C:\x\ferryman.exe.old-v0.1.0`, StartCmd: `C:\x\start-daemon.cmd`}
	if err := saveJournal(dir, j); err != nil {
		t.Fatal(err)
	}
	got, exists, err := loadJournal(dir)
	if err != nil || !exists {
		t.Fatalf("读回: exists=%v err=%v", exists, err)
	}
	if got.Phase != PhaseSwap || got.From != "v0.1.0" || got.To != "v0.2.0" ||
		got.TargetExe != j.TargetExe || got.NewExe != j.NewExe || got.Backup != j.Backup {
		t.Fatalf("读回内容漂移: %+v", got)
	}
	if got.UpdatedAt == "" {
		t.Fatalf("应自动盖时间戳")
	}

	// 覆写(阶段推进)→ 读回新阶段
	j.Phase = PhaseVerify
	if err := saveJournal(dir, j); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := loadJournal(dir); got.Phase != PhaseVerify {
		t.Fatalf("阶段推进失败: %+v", got)
	}

	// 清账 → 不存在;再清(幂等)不报错
	if err := clearJournal(dir); err != nil {
		t.Fatal(err)
	}
	if _, exists, _ := loadJournal(dir); exists {
		t.Fatalf("清账后 journal 应不存在")
	}
	if err := clearJournal(dir); err != nil {
		t.Fatalf("重复清账应幂等: %v", err)
	}

	// 半写坏内容 → loadJournal 报错(调用方按 staging 残留清理)
	if err := os.WriteFile(journalPath(dir), []byte("{{{"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loadJournal(dir); err == nil {
		t.Fatalf("坏内容应报错")
	}
}

// TestBackupPath 备份名:版本串白名单化进文件名,非法字符换 _。
func TestBackupPath(t *testing.T) {
	got := backupPath(`C:\x`, "v0.1.0")
	if got != `C:\x\ferryman.exe.old-v0.1.0` {
		t.Fatalf("备份名 = %q", got)
	}
	if got := backupPath(`C:\x`, "dev"); got != `C:\x\ferryman.exe.old-dev` {
		t.Fatalf("dev 备份名 = %q", got)
	}
	if got := backupPath(`C:\x`, "bad/version"); filepath.Base(got) != "ferryman.exe.old-bad_version" {
		t.Fatalf("非法字符应换 _: %q", got)
	}
}

// TestPruneBackups 旧备份自动只留 2 份(最新两份),带 mtime 排序。
func TestPruneBackups(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	names := []string{"ferryman.exe.old-a", "ferryman.exe.old-b", "ferryman.exe.old-c"}
	for i, n := range names {
		p := filepath.Join(dir, n)
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		// c 最新、a 最旧
		if err := os.Chtimes(p, now.Add(-time.Duration(3-i)*time.Hour), now.Add(-time.Duration(3-i)*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	// 无关文件不动
	if err := os.WriteFile(filepath.Join(dir, "ferryman.exe"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	pruneBackups(dir, 2)
	for _, n := range []string{"ferryman.exe.old-b", "ferryman.exe.old-c"} {
		if _, err := os.Stat(filepath.Join(dir, n)); err != nil {
			t.Fatalf("%s 应保留: %v", n, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "ferryman.exe.old-a")); err == nil {
		t.Fatalf("最旧的 old-a 应被清理")
	}
	if _, err := os.Stat(filepath.Join(dir, "ferryman.exe")); err != nil {
		t.Fatalf("正式 exe 不得被清理")
	}
	// 不超额时不动
	pruneBackups(dir, 2)
	if _, err := os.Stat(filepath.Join(dir, "ferryman.exe.old-b")); err != nil {
		t.Fatalf("不超额时不应再删: %v", err)
	}
}

// TestCleanSwapResidues 一切退出路径清理 .new/.part/.swap-tmp 残留;
// .old-* 备份与正式 exe 不在清理域。
func TestCleanSwapResidues(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"ferryman.exe.new", "ferryman.exe.new.part",
		"ferryman.exe.swap-tmp", "ferryman.exe.swap-tmp2", "ferryman.exe.old-v0.1.0", "ferryman.exe"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cleanSwapResidues(dir)
	for _, n := range []string{"ferryman.exe.new", "ferryman.exe.new.part",
		"ferryman.exe.swap-tmp", "ferryman.exe.swap-tmp2"} {
		if _, err := os.Stat(filepath.Join(dir, n)); err == nil {
			t.Fatalf("%s 应被清理", n)
		}
	}
	for _, n := range []string{"ferryman.exe.old-v0.1.0", "ferryman.exe"} {
		if _, err := os.Stat(filepath.Join(dir, n)); err != nil {
			t.Fatalf("%s 不得被清理: %v", n, err)
		}
	}
	// 空目录/不存在目录不炸
	cleanSwapResidues(filepath.Join(dir, "nope"))
}

// TestCopyFile copy 语义:内容逐字节一致;目标已存在则覆盖。
func TestCopyFile(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	dst := filepath.Join(dir, "dst")
	want := []byte("MZ-fake-binary-bytes")
	if err := os.WriteFile(src, want, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := copyFile(src, dst); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("copy 内容漂移: %q", got)
	}
	// 源保留(copy 非 move)
	if _, err := os.Stat(src); err != nil {
		t.Fatalf("copy 不得删源: %v", err)
	}
	// 源缺失报错
	if err := copyFile(filepath.Join(dir, "nope"), dst); err == nil {
		t.Fatalf("源缺失应报错")
	}
}
