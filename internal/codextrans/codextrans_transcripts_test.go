package codextrans

// 规格：tests/test_codex_transcripts.py 全部 2 例 1:1（token_count 轮次：
// 解析/跳行/按 UTC 排序 + 缺文件防空）。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// lineOf dict 行 JSON 序列化（extract/cctrans 测试同款助手）。
func lineOf(d any) string {
	b, err := json.Marshal(d)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// tokenCountLine 对应 Python 的 _token_count_line(ts, inp, cached, write=0)
// （两处调用 write 均取默认 0，Go 侧省参）。
func tokenCountLine(ts string, inp, cached int) string {
	return lineOf(map[string]any{
		"timestamp": ts, "type": "event_msg",
		"payload": map[string]any{"type": "token_count",
			"info": map[string]any{"last_token_usage": map[string]any{
				"input_tokens": inp, "cached_input_tokens": cached,
				"cache_write_input_tokens": 0, "output_tokens": 10}}},
	})
}

func TestTokenCountTurnsParsesAndSkips(t *testing.T) {
	// 坏行/非 token_count/非 event_msg/input=0 全跳；时间更早的行排序后在前。
	lines := []string{
		tokenCountLine("2026-09-16T06:00:01Z", 1000, 800),
		"{broken",
		lineOf(map[string]any{"timestamp": "2026-09-16T06:00:02Z", "type": "event_msg",
			"payload": map[string]any{"type": "something_else"}}), // 非 token_count
		lineOf(map[string]any{"timestamp": "2026-09-16T06:00:03Z", "type": "response_item"}), // 非 event_msg
		tokenCountLine("2026-09-16T06:00:04Z", 0, 0),                                         // input=0 跳过
		tokenCountLine("2026-09-16T06:00:00Z", 3000, 2500),                                   // 时间更早，排序后应在前
	}
	p := filepath.Join(t.TempDir(), "rollout-x.jsonl")
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	turns := TokenCountTurns(p)
	if len(turns) != 2 ||
		turns[0].InputTokens != 3000 || turns[0].Cached != 2500 ||
		turns[1].InputTokens != 1000 || turns[1].Cached != 800 {
		t.Fatalf("turns = %+v, want [(3000,2500) (1000,800)]", turns)
	}
	if turns[0].TS > turns[1].TS { // UTC 排序
		t.Fatalf("ts 未升序: %v > %v", turns[0].TS, turns[1].TS)
	}
}

func TestMissingFileReturnsEmpty(t *testing.T) {
	if got := TokenCountTurns(filepath.Join(t.TempDir(), "nope.jsonl")); len(got) != 0 {
		t.Fatalf("缺文件应返回空, got %+v", got)
	}
}

// 验收专测（Go 侧补充）：SessionCwd 无 Python 对应用例（session_cwd 的调用方
// 用例归 daemon 门槛票），此处钉住本包实现——头 10 行窗口含行计数（坏行/不匹配
// 行也计数，Python enumerate(f) 语义）、payload 非 dict 不命中、缺文件空串。
func TestSessionCwdHeadWindow(t *testing.T) {
	meta := lineOf(map[string]any{"timestamp": "2026-09-17T02:52:19.862Z",
		"type":    "session_meta",
		"payload": map[string]any{"session_id": "01a0ad46", "cwd": "C:\\proj"}})
	filler := lineOf(map[string]any{"timestamp": "2026-09-17T02:52:20.000Z",
		"type": "response_item", "payload": map[string]any{"type": "reasoning"}})

	// 首行即 session_meta → cwd
	p1 := filepath.Join(t.TempDir(), "a.jsonl")
	if err := os.WriteFile(p1, []byte(meta+"\n"+filler+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := SessionCwd(p1); got != "C:\\proj" {
		t.Fatalf("SessionCwd = %q, want C:\\proj", got)
	}

	// session_meta 在第 11 行（窗口外）→ ""；窗口内夹坏行也计数
	p2 := filepath.Join(t.TempDir(), "b.jsonl")
	content := strings.Join([]string{filler, "{broken", filler, filler, filler,
		filler, filler, filler, filler, filler, meta}, "\n") + "\n"
	if err := os.WriteFile(p2, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := SessionCwd(p2); got != "" {
		t.Fatalf("超头部 SessionCwd = %q, want 空串", got)
	}

	// session_meta 有但 payload 非 dict → 不命中，继续扫窗口（Python 落空返回 ""）
	badPayload := lineOf(map[string]any{"timestamp": "2026-09-17T02:52:19.862Z",
		"type": "session_meta", "payload": "不是dict"})
	p3 := filepath.Join(t.TempDir(), "c.jsonl")
	if err := os.WriteFile(p3, []byte(badPayload+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := SessionCwd(p3); got != "" {
		t.Fatalf("payload 非 dict SessionCwd = %q, want 空串", got)
	}

	if got := SessionCwd(filepath.Join(t.TempDir(), "nope.jsonl")); got != "" {
		t.Fatalf("缺文件 SessionCwd = %q, want 空串", got)
	}
}
