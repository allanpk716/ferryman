package harvest

// 规格：tests/test_harvest.py 全部 13 例 1:1（设计 §3.7：纯解析器与增量状态）。
// 测试 JSON 行为 raw UTF-8（Python json.dumps 默认 ensure_ascii 会转 \uXXXX——
// 解析端两形态等价；仅 test_partial_line_held_back 的 [:30] 切片按 Go 串字节切，
// 切点位置不同不影响语义：半行无换行 → 留待下轮）。

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ferryman/internal/accounts"
)

// mustJSON 对应测试里的 json.dumps（键序不敏感，解析端不看键序）。
func mustJSON(v any) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		panic(err)
	}
	return strings.TrimRight(buf.String(), "\n")
}

// userLine 对应 _user_line(ts="2026-09-18T01:00:00Z", cwd="C:/proj")。
func userLine(ts, cwd string) string {
	return mustJSON(map[string]any{
		"type": "user", "timestamp": ts, "cwd": cwd, "sessionId": "s1",
		"message": map[string]any{"role": "user", "content": "干活"},
	})
}

func defUserLine() string { return userLine("2026-09-18T01:00:00Z", "C:/proj") }

// asstLine 对应 _asst_line(ts="2026-09-18T01:00:05Z", model="glm-5.3",
// inp=100, cr=9000, cc=0, out=50)。
func asstLine(ts, model string, inp, cr, cc, out int) string {
	return mustJSON(map[string]any{
		"type": "assistant", "timestamp": ts,
		"message": map[string]any{
			"role": "assistant", "model": model,
			"content": []any{map[string]any{"type": "text", "text": "好"}},
			"usage": map[string]any{
				"input_tokens":                inp,
				"cache_read_input_tokens":     cr,
				"cache_creation_input_tokens": cc,
				"output_tokens":               out,
			},
		},
	})
}

func defAsstLine() string {
	return asstLine("2026-09-18T01:00:05Z", "glm-5.3", 100, 9000, 0, 50)
}

// asstMid 对应 _asst_mid(mid, ts="2026-09-18T05:00:00Z")。
func asstMid(mid, ts string) string {
	return mustJSON(map[string]any{
		"type": "assistant", "timestamp": ts,
		"message": map[string]any{
			"role": "assistant", "id": mid, "model": "glm-5.3",
			"content": []any{map[string]any{"type": "text", "text": "x"}},
			"usage": map[string]any{
				"input_tokens": 1, "cache_read_input_tokens": 2,
				"cache_creation_input_tokens": 0, "output_tokens": 3,
			},
		},
	})
}

func newAccounts(t *testing.T, dir string) *accounts.Accounts {
	t.Helper()
	acc, err := accounts.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	return acc
}

func writeFile(t *testing.T, p, content string) {
	t.Helper()
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func appendFile(t *testing.T, p, content string) {
	t.Helper()
	fh, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer fh.Close()
	if _, err := fh.WriteString(content); err != nil {
		t.Fatal(err)
	}
}

func fileSize(t *testing.T, p string) int64 {
	t.Helper()
	st, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	return st.Size()
}

func mkSession(t *testing.T, p string) {
	t.Helper()
	writeFile(t, p, defUserLine()+"\n"+defAsstLine()+"\n")
}

func TestParseExtractsAssistantUsageAndCwd(t *testing.T) {
	chunk := defUserLine() + "\n" + defAsstLine() + "\n"
	rows, title, cwd := ParseUsageChunk(chunk, "", "")
	if cwd != "C:/proj" {
		t.Fatalf("cwd = %q, want C:/proj", cwd)
	}
	if title != "" {
		t.Fatalf("title = %q, want 空", title)
	}
	if len(rows) != 1 {
		t.Fatalf("len(rows) = %d, want 1", len(rows))
	}
	r := rows[0]
	if r.InputTokens != 100 || r.CacheReadTokens != 9000 {
		t.Fatalf("tokens = %d/%d, want 100/9000", r.InputTokens, r.CacheReadTokens)
	}
	if r.CacheCreationTokens != 0 || r.OutputTokens != 50 {
		t.Fatalf("tokens = %d/%d, want 0/50", r.CacheCreationTokens, r.OutputTokens)
	}
	if r.Model != "glm-5.3" {
		t.Fatalf("model = %q, want glm-5.3", r.Model)
	}
	expected := float64(time.Date(2026, 9, 18, 1, 0, 5, 0, time.UTC).Unix())
	if r.TS == nil || *r.TS != expected {
		t.Fatalf("ts = %v, want %v", r.TS, expected)
	}
	// 隐私：无消息内容——Python 检 "content"/"message" 不在行 dict；Go 侧 Row
	// 结构无这些字段，结构性保证。
}

func TestParseTracksAITitle(t *testing.T) {
	titleLine := mustJSON(map[string]any{"type": "ai-title", "aiTitle": "修登录bug"})
	chunk := titleLine + "\n" + defAsstLine() + "\n"
	rows, title, _ := ParseUsageChunk(chunk, "", "")
	if title != "修登录bug" {
		t.Fatalf("title = %q, want 修登录bug", title)
	}
	if len(rows) != 1 {
		t.Fatalf("len(rows) = %d, want 1", len(rows))
	}
}

func TestParseSkipsMalformedAndBareLines(t *testing.T) {
	chunk := "{broken json\n" + defAsstLine() + "\n" +
		mustJSON(map[string]any{"type": "user",
			"message": map[string]any{"role": "user", "content": "x"}}) + "\n" +
		mustJSON(map[string]any{"type": "assistant",
			"message": map[string]any{"role": "assistant", "content": "无用量"}}) + "\n"
	rows, _, _ := ParseUsageChunk(chunk, "", "")
	if len(rows) != 1 { // 只有带 usage 的 assistant 出行
		t.Fatalf("len(rows) = %d, want 1", len(rows))
	}
}

func TestParseSkipsNondictAndNullTokenLines(t *testing.T) {
	// 病态行防御：数组行/message 为字符串/token 为 null——跳过不抛且不伤及好行。
	chunk := "[1, 2]\n" +
		mustJSON(map[string]any{"type": "assistant", "message": "字符串消息"}) + "\n" +
		mustJSON(map[string]any{"type": "assistant",
			"message": map[string]any{"role": "assistant",
				"usage": map[string]any{"input_tokens": nil}}}) + "\n" +
		defAsstLine() + "\n"
	rows, _, _ := ParseUsageChunk(chunk, "", "")
	if len(rows) != 1 || rows[0].OutputTokens != 50 {
		t.Fatalf("len(rows) = %d, out = %d, want 1 行 out=50", len(rows), rows[0].OutputTokens)
	}
}

func TestIncrementalAndOffsets(t *testing.T) {
	tmp := t.TempDir()
	acc := newAccounts(t, tmp)
	f := filepath.Join(tmp, "s1.jsonl")
	mkSession(t, f)
	hs := NewHarvestState(acc)
	size := fileSize(t, f)
	rows := hs.MaybeHarvest(f, size, "cc")
	if len(rows) != 1 {
		t.Fatalf("len(rows) = %d, want 1", len(rows))
	}
	if rows[0].Offset != size { // 本批结束偏移
		t.Fatalf("offset = %d, want %d", rows[0].Offset, size)
	}
	if rows[0].Title != "" || rows[0].Project != "C:/proj" {
		t.Fatalf("title/project = %q/%q, want 空/C:/proj", rows[0].Title, rows[0].Project)
	}
	// 无新增 → 空
	if rows := hs.MaybeHarvest(f, size, "cc"); len(rows) != 0 {
		t.Fatalf("len(rows) = %d, want 0", len(rows))
	}
	// 追加一条 assistant → 只有新增量
	appendFile(t, f, asstLine("2026-09-18T01:01:00Z", "glm-5.3", 100, 9000, 0, 50)+"\n")
	size2 := fileSize(t, f)
	rows2 := hs.MaybeHarvest(f, size2, "cc")
	if len(rows2) != 1 || rows2[0].Offset != size2 {
		t.Fatalf("len = %d offset = %d, want 1/%d", len(rows2), rows2[0].Offset, size2)
	}
}

func TestPartialLineHeldBack(t *testing.T) {
	tmp := t.TempDir()
	acc := newAccounts(t, tmp)
	f := filepath.Join(tmp, "s1.jsonl")
	writeFile(t, f, defUserLine()+"\n")
	hs := NewHarvestState(acc)
	hs.MaybeHarvest(f, fileSize(t, f), "cc")
	half := defAsstLine()[:30] // 无换行的半行（Python [:30] 字符；ASCII 等价字节切）
	appendFile(t, f, half)
	if rows := hs.MaybeHarvest(f, fileSize(t, f), "cc"); len(rows) != 0 {
		t.Fatalf("半行应留待下轮，got %d 行", len(rows))
	}
	appendFile(t, f, defAsstLine()[30:]+"\n") // 补完
	rows := hs.MaybeHarvest(f, fileSize(t, f), "cc")
	if len(rows) != 1 {
		t.Fatalf("len(rows) = %d, want 1", len(rows))
	}
}

func TestTitleCarriedAcrossBatches(t *testing.T) {
	tmp := t.TempDir()
	acc := newAccounts(t, tmp)
	f := filepath.Join(tmp, "s1.jsonl")
	titleLine := mustJSON(map[string]any{"type": "ai-title", "aiTitle": "起名了"})
	writeFile(t, f, titleLine+"\n")
	hs := NewHarvestState(acc)
	hs.MaybeHarvest(f, fileSize(t, f), "cc")
	appendFile(t, f, defAsstLine()+"\n")
	rows := hs.MaybeHarvest(f, fileSize(t, f), "cc")
	if len(rows) == 0 || rows[0].Title != "起名了" { // 标题跨批次携带
		t.Fatalf("rows = %v, want 标题 起名了", rows)
	}
}

func TestResumeFromAccounts(t *testing.T) {
	// 重启恢复：新 HarvestState 从账本行恢复偏移与标题，不重复采集。
	tmp := t.TempDir()
	acc := newAccounts(t, tmp)
	f := filepath.Join(tmp, "s1.jsonl")
	writeFile(t, f, mustJSON(map[string]any{"type": "ai-title", "aiTitle": "旧名"})+"\n"+
		defAsstLine()+"\n")
	hs := NewHarvestState(acc)
	for _, r := range hs.MaybeHarvest(f, fileSize(t, f), "cc") {
		ts := -1.0 // ts=None → clock.Now()（本例 ts 恒非 nil）
		if r.TS != nil {
			ts = *r.TS
		}
		if _, err := acc.Record("usage", ts, accounts.Fields{
			"agent": "cc", "session_id": "s1", "lineage_id": "L",
			"project": r.Project, "model": r.Model, "title": r.Title,
			"input_tokens": r.InputTokens, "cache_read_tokens": r.CacheReadTokens,
			"cache_creation_tokens": r.CacheCreationTokens,
			"output_tokens":         r.OutputTokens, "offset": r.Offset,
		}); err != nil {
			t.Fatal(err)
		}
	}
	appendFile(t, f, asstLine("2026-09-18T02:00:00Z", "glm-5.3", 100, 9000, 0, 50)+"\n") // 停机期间新增
	hs2 := NewHarvestState(acc)                                                          // "重启"
	rows := hs2.MaybeHarvest(f, fileSize(t, f), "cc")
	if len(rows) != 1 { // 只采新增
		t.Fatalf("len(rows) = %d, want 1", len(rows))
	}
	if rows[0].Title != "旧名" { // 标题也已恢复
		t.Fatalf("title = %q, want 旧名", rows[0].Title)
	}
}

func TestShrinkRereads(t *testing.T) {
	tmp := t.TempDir()
	acc := newAccounts(t, tmp)
	f := filepath.Join(tmp, "s1.jsonl")
	mkSession(t, f)
	hs := NewHarvestState(acc)
	hs.MaybeHarvest(f, fileSize(t, f), "cc")
	writeFile(t, f, asstLine("2026-09-18T03:00:00Z", "glm-5.3", 100, 9000, 0, 50)+"\n")
	rows := hs.MaybeHarvest(f, fileSize(t, f), "cc") // 收缩→从头重采
	if len(rows) != 1 {
		t.Fatalf("len(rows) = %d, want 1", len(rows))
	}
}

func TestResumeSurvivesNullOffsetRow(t *testing.T) {
	// 账本毒药行防御：usage 行 offset 为 null——构造不抛，从 0 正常恢复。
	tmp := t.TempDir()
	acc := newAccounts(t, tmp)
	f := filepath.Join(tmp, "s1.jsonl")
	mkSession(t, f)
	poison := mustJSON(map[string]any{"kind": "usage", "agent": "cc", "session_id": "s1",
		"offset": nil, "model": "glm-5.3",
		"input_tokens": 1, "cache_read_tokens": 0,
		"cache_creation_tokens": 0, "output_tokens": 0})
	// 手写毒药行，不经 Record()（对应 acc.dir / "209901.jsonl"）
	appendFile(t, filepath.Join(tmp, "accounts", "209901.jsonl"), poison+"\n")
	hs := NewHarvestState(acc) // 不抛
	rows := hs.MaybeHarvest(f, fileSize(t, f), "cc")
	if len(rows) != 1 || rows[0].Offset != fileSize(t, f) {
		t.Fatalf("len = %d offset = %d, want 1/%d", len(rows), rows[0].Offset, fileSize(t, f))
	}
}

func TestDuplicateMessageIDSuppressedInBatch(t *testing.T) {
	tmp := t.TempDir()
	acc := newAccounts(t, tmp)
	f := filepath.Join(tmp, "s1.jsonl")
	writeFile(t, f, asstMid("m1", "2026-09-18T05:00:00Z")+"\n"+
		asstMid("m1", "2026-09-18T05:00:04Z")+"\n")
	rows := NewHarvestState(acc).MaybeHarvest(f, fileSize(t, f), "cc")
	if len(rows) != 1 {
		t.Fatalf("len(rows) = %d, want 1", len(rows))
	}
	if rows[0].MsgID != "" { // "msg_id" not in rows[0]——pop 语义，出行不带 id
		t.Fatalf("MsgID = %q, want 空（已 pop）", rows[0].MsgID)
	}
}

func TestDuplicateMessageIDSuppressedAcrossBatches(t *testing.T) {
	tmp := t.TempDir()
	acc := newAccounts(t, tmp)
	f := filepath.Join(tmp, "s1.jsonl")
	writeFile(t, f, asstMid("m1", "2026-09-18T05:00:00Z")+"\n")
	hs := NewHarvestState(acc)
	if rows := hs.MaybeHarvest(f, fileSize(t, f), "cc"); len(rows) != 1 {
		t.Fatalf("len(rows) = %d, want 1", len(rows))
	}
	appendFile(t, f, asstMid("m1", "2026-09-18T05:00:09Z")+"\n") // 数秒后的重写
	if rows := hs.MaybeHarvest(f, fileSize(t, f), "cc"); len(rows) != 0 {
		t.Fatalf("同 id 跨批应去重，got %d 行", len(rows))
	}
}

func TestDistinctOrAbsentIDsPass(t *testing.T) {
	tmp := t.TempDir()
	acc := newAccounts(t, tmp)
	f := filepath.Join(tmp, "s1.jsonl")
	writeFile(t, f, asstMid("m1", "2026-09-18T05:00:00Z")+"\n"+
		asstMid("m2", "2026-09-18T05:00:00Z")+"\n"+defAsstLine()+"\n") // m2 不同 id；第三条无 id
	rows := NewHarvestState(acc).MaybeHarvest(f, fileSize(t, f), "cc")
	if len(rows) != 3 {
		t.Fatalf("len(rows) = %d, want 3", len(rows))
	}
}
