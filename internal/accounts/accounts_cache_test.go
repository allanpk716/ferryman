package accounts

// accounts_cache_test.go — 常驻解析缓存的增量/侦测/撕裂行语义（ADR-0027）。
// 既有 Read/ReadMonths 测试（六维过滤、坏行跳过、月滚动）经缓存路径全量
// 复跑，本文件只补缓存特有的行为：追加可见性、收缩重建、同长改写重建、
// 尾段计行与续写重解、新文件发现、与全新实例的逐字节等价。

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// rawWrite 直接写月文件（绕过 Record——模拟外部工具/T39 式改写）。
func rawWrite(t *testing.T, acc *Accounts, month string, content []byte) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(acc.dir, month+".jsonl"),
		content, 0o644); err != nil {
		t.Fatal(err)
	}
}

// rawAppend 追加字节到月文件（续写撕裂行用）。
func rawAppend(t *testing.T, acc *Accounts, month string, content []byte) {
	t.Helper()
	f, err := os.OpenFile(filepath.Join(acc.dir, month+".jsonl"),
		os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(content); err != nil {
		t.Fatal(err)
	}
	f.Close()
}

func handoffLine(sid string, ts float64) []byte {
	return []byte(fmt.Sprintf(
		`{"v":1,"kind":"handoff","ts":%.3f,"ts_iso":"x","agent":"cc","session_id":%q,"lineage_id":"L","project":"C:/proj","completion_tokens":1,"outcome":"fresh","prompt_tokens":1,"provider":"glm","wall_s":1}`+"\n",
		ts, sid))
}

func TestCacheAppendAfterRead(t *testing.T) {
	acc := newAcc(t)
	recHandoff(t, acc, AUG, "s1", nil)
	if rows := acc.Read(ReadOpts{}); len(rows) != 1 {
		t.Fatalf("首次读 rows = %d, want 1", len(rows))
	}
	recHandoff(t, acc, SEP, "s2", nil) // Read 之后追加 → 下轮 Read 必须可见
	rows := acc.Read(ReadOpts{})
	if len(rows) != 2 {
		t.Fatalf("追加后 rows = %d, want 2（增量解析漏了新行？）", len(rows))
	}
	if rows[1]["session_id"] != "s2" {
		t.Fatalf("第二行 session_id = %v, want s2", rows[1]["session_id"])
	}
}

func TestCacheRebuildOnShrink(t *testing.T) {
	acc := newAcc(t)
	rawWrite(t, acc, "202608", append(handoffLine("s1", AUG), handoffLine("s2", AUG+1)...))
	if rows := acc.Read(ReadOpts{}); len(rows) != 2 {
		t.Fatalf("首次读 rows = %d, want 2", len(rows))
	}
	rawWrite(t, acc, "202608", handoffLine("s9", AUG)) // 收缩改写
	rows := acc.Read(ReadOpts{})
	if len(rows) != 1 || rows[0]["session_id"] != "s9" {
		t.Fatalf("收缩后 rows = %v, want [s9]", rows)
	}
}

func TestCacheRebuildOnSameSizeRewrite(t *testing.T) {
	acc := newAcc(t)
	rawWrite(t, acc, "202608", append(handoffLine("s1", AUG), handoffLine("s2", AUG+1)...))
	if rows := acc.Read(ReadOpts{}); len(rows) != 2 {
		t.Fatalf("首次读 rows = %d, want 2", len(rows))
	}
	// 同长度不同内容（sid 等长替换）：尾样 64 字节与 mtime 双保险至少其一
	// 命中 → 重解。
	raw := append(handoffLine("s1", AUG), handoffLine("s3", AUG+1)...)
	rawWrite(t, acc, "202608", raw)
	rows := acc.Read(ReadOpts{})
	if len(rows) != 2 || rows[1]["session_id"] != "s3" {
		t.Fatalf("同长改写后 rows = %v, want [s1 s3]", rows)
	}
}

func TestCacheTailLineThenExtend(t *testing.T) {
	acc := newAcc(t)
	// 尾段无 \n：旧 splitlines 语义「末段仍计一行」→ 首读即见 2 行。
	rawWrite(t, acc, "202608",
		append(handoffLine("s1", AUG), bytes.TrimSuffix(handoffLine("s2", AUG+1), []byte("\n"))...))
	if rows := acc.Read(ReadOpts{}); len(rows) != 2 {
		t.Fatalf("尾段计行 rows = %d, want 2", len(rows))
	}
	// 追加 "\n" + 第三行：尾段行不得丢、不得翻倍。
	rawAppend(t, acc, "202608", append([]byte("\n"), handoffLine("s3", AUG+2)...))
	rows := acc.Read(ReadOpts{})
	if len(rows) != 3 {
		t.Fatalf("续写后 rows = %d, want 3（尾段丢失/翻倍？）: %v", len(rows), rows)
	}
	if rows[1]["session_id"] != "s2" || rows[2]["session_id"] != "s3" {
		t.Fatalf("续写后行序 = %v %v", rows[1]["session_id"], rows[2]["session_id"])
	}
}

func TestCacheTornLineCompletedLater(t *testing.T) {
	acc := newAcc(t)
	// 并发写下读到的半行：首读当坏行跳过（1 行）。
	rawWrite(t, acc, "202608", append(handoffLine("s1", AUG), []byte(`{"v":1,"kind":"han`)...))
	if rows := acc.Read(ReadOpts{}); len(rows) != 1 {
		t.Fatalf("撕裂首读 rows = %d, want 1", len(rows))
	}
	// 写完整行收尾：重解锚回退 → 该行转正。
	rawAppend(t, acc, "202608", []byte(`doff","ts":100.000,"agent":"cc","session_id":"s2"}`+"\n"))
	rows := acc.Read(ReadOpts{})
	if len(rows) != 2 || rows[1]["session_id"] != "s2" {
		t.Fatalf("补完后 rows = %v, want 2 行且第二行 s2", rows)
	}
}

func TestCacheNewFileDiscovered(t *testing.T) {
	acc := newAcc(t)
	rawWrite(t, acc, "202608", handoffLine("s1", AUG))
	if rows := acc.Read(ReadOpts{}); len(rows) != 1 {
		t.Fatalf("首读 rows = %d", len(rows))
	}
	rawWrite(t, acc, "202609", handoffLine("s2", SEP)) // 新月文件出现
	rows := acc.Read(ReadOpts{})
	if len(rows) != 2 {
		t.Fatalf("新文件发现 rows = %d, want 2", len(rows))
	}
}

func TestCacheParityWithFreshParse(t *testing.T) {
	dir := t.TempDir()
	acc, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	// 混合：多科目 + 坏行 + 尾段行；经缓存多轮读+追加后与全新实例逐行比对。
	recHandoff(t, acc, AUG, "s1", nil)
	raw := append(handoffLine("s2", AUG+1), []byte("{broken\n")...)
	raw = append(raw, handoffLine("s3", AUG+2)...)
	rawWrite(t, acc, "202608", raw)
	acc2, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	var cached, fresh []map[string]any
	for round := 0; round < 3; round++ {
		recHandoff(t, acc, SEP, fmt.Sprintf("r%d", round), nil) // 每轮追加
		cached = acc.Read(ReadOpts{})
		fresh = acc2.Read(ReadOpts{})
		if len(cached) != len(fresh) {
			t.Fatalf("round %d 行数不符: cached=%d fresh=%d", round, len(cached), len(fresh))
		}
		for i := range cached {
			cj, _ := json.Marshal(cached[i])
			fj, _ := json.Marshal(fresh[i])
			if !bytes.Equal(cj, fj) {
				t.Fatalf("round %d 第 %d 行不等:\n cached=%s\n fresh=%s", round, i, cj, fj)
			}
		}
	}
	if len(cached) == 0 {
		t.Fatal("空数据（fixture 失效）")
	}
}

func TestReadMonthsIncremental(t *testing.T) {
	acc := newAcc(t)
	month := time.Unix(int64(AUG), 0).Format("200601")
	if rows := acc.ReadMonths(ReadOpts{}, month); len(rows) != 0 {
		t.Fatalf("空月 rows = %d, want 0", len(rows))
	}
	recHandoff(t, acc, AUG, "s1", nil)
	if rows := acc.ReadMonths(ReadOpts{}, month); len(rows) != 1 {
		t.Fatalf("ReadMonths 追加可见 rows = %d, want 1", len(rows))
	}
	if rows := acc.Read(ReadOpts{}); len(rows) != 1 { // 与 Read 共享同一缓存
		t.Fatalf("Read rows = %d, want 1", len(rows))
	}
}

func TestPrewarmCoversAllFiles(t *testing.T) {
	acc := newAcc(t)
	rawWrite(t, acc, "202608", handoffLine("s1", AUG))
	rawWrite(t, acc, "202609", handoffLine("s2", SEP))
	acc.Prewarm()
	acc.cmu.Lock()
	n := len(acc.files)
	acc.cmu.Unlock()
	if n != 2 {
		t.Fatalf("prewarm files = %d, want 2", n)
	}
	if rows := acc.Read(ReadOpts{}); len(rows) != 2 { // 预热后零增量即全量
		t.Fatalf("prewarm 后 rows = %d, want 2", len(rows))
	}
}
