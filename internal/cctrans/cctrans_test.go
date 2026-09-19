package cctrans

// 规格：tests/test_transcripts.py 全部 18 例 1:1（T31 悬空 tool_use 7 例 +
// T48 async 派发 11 例）。另加 3 例验收专测：>10MB 单行不断流（票面验收#3）、
// AssistantTurns 排序/跳行/无时区按 UTC、AITitle/FirstUserMessageHash 基本面
// （后两函数的 Python 用例归 harvest/extract 票，此处先钉住本包实现）。

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ferryman/internal/jsonl"
)

func lineOf(d any) string {
	b, err := json.Marshal(d)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// assistantLine 对应 Python 的 _assistant(*tool_ids, text="好")：
// 带 usage 的 assistant 行（inp=10, cr=100, cc=0）。
func assistantLine(toolIDs ...string) map[string]any {
	blocks := []any{map[string]any{"type": "text", "text": "好"}}
	for _, tid := range toolIDs {
		blocks = append(blocks, map[string]any{
			"type": "tool_use", "id": tid, "name": "Bash", "input": map[string]any{}})
	}
	return map[string]any{
		"type": "assistant", "timestamp": "2026-09-16T12:00:00.000Z",
		"message": map[string]any{"role": "assistant", "content": blocks,
			"usage": map[string]any{"input_tokens": 10, "cache_read_input_tokens": 100,
				"cache_creation_input_tokens": 0, "output_tokens": 5}},
	}
}

// resultLine 对应 Python 的 _result(*tool_ids)。
func resultLine(toolIDs ...string) map[string]any {
	blocks := make([]any, 0, len(toolIDs))
	for _, tid := range toolIDs {
		blocks = append(blocks, map[string]any{
			"type": "tool_result", "tool_use_id": tid, "content": "ok"})
	}
	return map[string]any{
		"type": "user", "timestamp": "2026-09-16T12:00:01.000Z",
		"message": map[string]any{"role": "user", "content": blocks},
	}
}

// writeLines 对应 Python 的 _write(tmp_path, lines)。
func writeLines(t *testing.T, lines []string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "s.jsonl")
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// taskUse 对应 Python 的 _task_use(tid, name="Task", inp=None)。
func taskUse(tid, name string, inp any) map[string]any {
	if inp == nil {
		inp = map[string]any{"prompt": "干活"}
	}
	return map[string]any{
		"type": "assistant", "timestamp": "2026-09-16T12:00:00.000Z",
		"message": map[string]any{"role": "assistant", "content": []any{
			map[string]any{"type": "tool_use", "id": tid, "name": name, "input": inp}}},
	}
}

// taskResult 对应 Python 的 _task_result(tid, content)。
func taskResult(tid string, content any) map[string]any {
	return map[string]any{
		"type": "user", "timestamp": "2026-09-16T12:00:01.000Z",
		"message": map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "tool_result", "tool_use_id": tid, "content": content}}},
	}
}

// writeBlocks 对应 Python 的 _write_blocks(tmp_path, blocks, name="s.jsonl")。
func writeBlocks(t *testing.T, name string, blocks []map[string]any) string {
	t.Helper()
	lines := make([]string, 0, len(blocks))
	for _, b := range blocks {
		lines = append(lines, lineOf(b))
	}
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// ---- T31 · 悬空 tool_use 判定 ----

func TestT31DanglingUseIsTrue(t *testing.T) {
	// 最后 assistant 消息带 tool_use 且无后续 tool_result → 运行中。
	f := writeLines(t, []string{lineOf(assistantLine("t1"))})
	if !HasDanglingToolUse(f) {
		t.Fatalf("HasDanglingToolUse(%s) = false, want true", f)
	}
}

func TestT31MatchedUseIsFalse(t *testing.T) {
	// tool_use 已有同 id tool_result → 静止。
	f := writeLines(t, []string{lineOf(assistantLine("t1")), lineOf(resultLine("t1"))})
	if HasDanglingToolUse(f) {
		t.Fatalf("HasDanglingToolUse(%s) = true, want false", f)
	}
}

func TestT31TextOnlyTailIsFalse(t *testing.T) {
	// 尾部无 tool_use（纯文本收尾）→ 静止。
	f := writeLines(t, []string{lineOf(assistantLine()), lineOf(assistantLine())})
	if HasDanglingToolUse(f) {
		t.Fatalf("HasDanglingToolUse(%s) = true, want false", f)
	}
}

func TestT31EmptyFileIsFalse(t *testing.T) {
	// 空文件/无 assistant 行 → 静止（宁可多摆渡）。
	empty := filepath.Join(t.TempDir(), "empty.jsonl")
	if err := os.WriteFile(empty, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if HasDanglingToolUse(empty) {
		t.Fatal("空文件应为 false")
	}
	if HasDanglingToolUse(filepath.Join(t.TempDir(), "nope.jsonl")) {
		t.Fatal("缺文件也静止（应为 false）")
	}
}

func TestT31EarlierPairPlusNewDanglingIsTrue(t *testing.T) {
	// 历史配对完整、仅最新的 tool_use 悬空 → 运行中。
	f := writeLines(t, []string{lineOf(assistantLine("t1")), lineOf(resultLine("t1")),
		lineOf(assistantLine("t2"))})
	if !HasDanglingToolUse(f) {
		t.Fatal("最新 tool_use 悬空应为 true")
	}
}

func TestT31MultiBlockNeedsAllMatched(t *testing.T) {
	// 一条 assistant 多个 tool_use：全部收到 result 才算静止。
	f := writeLines(t, []string{lineOf(assistantLine("t1", "t2")), lineOf(resultLine("t1"))})
	if !HasDanglingToolUse(f) {
		t.Fatal("t2 未收 result 应为 true")
	}
}

func TestT31WindowCutToleratesMiss(t *testing.T) {
	// 悬空 tool_use 被挤出尾部窗口 → 允许漏判（best-effort，covers_until 兜底）。
	filler := lineOf(assistantLine("old")) + "\n" + lineOf(resultLine("old")) + "\n" + strings.Repeat("x", 400)
	f := writeLines(t, []string{filler, lineOf(assistantLine("t1"))})
	if !HasDanglingToolUseWindow(f, 1024) {
		t.Fatal("窗口装得下整条 t1 行 → 应检出 true")
	}
	if HasDanglingToolUseWindow(f, 64) {
		t.Fatal("t1 行被切成半行丢弃 → 应漏判 false")
	}
}

// ---- T48 票01 · has_async_launch 尾部异步派发判定 ----

func TestT48AsyncLaunchByInputFlag(t *testing.T) {
	// input.run_in_background 为真 → async 派发。
	f := writeBlocks(t, "s.jsonl", []map[string]any{
		taskUse("t1", "Task", map[string]any{"prompt": "干活", "run_in_background": true}),
		taskResult("t1", "ok"),
	})
	if !HasAsyncLaunch(f) {
		t.Fatal("run_in_background=true 应为 true")
	}
}

func TestT48AsyncLaunchByBackgroundAlias(t *testing.T) {
	// background 别名标志位同样命中（CC 字段名两形态）。
	f := writeBlocks(t, "s.jsonl", []map[string]any{
		taskUse("t1", "Task", map[string]any{"prompt": "干活", "background": true}),
		taskResult("t1", "ok"),
	})
	if !HasAsyncLaunch(f) {
		t.Fatal("background=true 应为 true")
	}
}

func TestT48AsyncLaunchByResultMarker(t *testing.T) {
	// input 无标志位，但 tool_result 文本以实机文案为前缀 → async（实机兜底）。
	f := writeBlocks(t, "s.jsonl", []map[string]any{
		taskUse("t1", "Agent", nil),
		taskResult("t1", "Async agent launched successfully (agent-abc)"),
	})
	if !HasAsyncLaunch(f) {
		t.Fatal("result 文案严格前缀应为 true")
	}
}

func TestT48AsyncLaunchResultFirstTextBlockPrefix(t *testing.T) {
	// result 为块列表时取首个 text 块做前缀匹配（实机 result 形态）。
	f := writeBlocks(t, "s.jsonl", []map[string]any{
		taskUse("t1", "Task", nil),
		taskResult("t1", []any{map[string]any{"type": "text",
			"text": "Async agent launched successfully (agent-abc)"}}),
	})
	if !HasAsyncLaunch(f) {
		t.Fatal("首个 text 块前缀命中应为 true")
	}
}

func TestT48SyncTaskNotAsync(t *testing.T) {
	// 同步派发（无标志位、result 无文案）→ False。
	f := writeBlocks(t, "s.jsonl", []map[string]any{
		taskUse("t1", "Task", nil),
		taskResult("t1", "done"),
	})
	if HasAsyncLaunch(f) {
		t.Fatal("同步派发应为 false")
	}
}

func TestT48AsyncLaunchLastDispatchWins(t *testing.T) {
	// 尾部最后一个 Task/Agent 派发说了算：旧的 async 标记不算数。
	//
	// 交错派发（async A 在飞 + 再派 sync B）在本函数层面就是 False——
	// async 等待的保留由 server 侧 saw_async latch 兜住（round 0 e2 实验）。
	f := writeBlocks(t, "s.jsonl", []map[string]any{
		taskUse("t1", "Task", map[string]any{"run_in_background": true}),
		taskResult("t1", "ok"),
		taskUse("t2", "Task", map[string]any{"prompt": "同步活"}),
		taskResult("t2", "done"),
	})
	if HasAsyncLaunch(f) {
		t.Fatal("最后派发为同步 → 应为 false")
	}
}

func TestT48SyncResultMidtextRepeatIsFalse(t *testing.T) {
	// 附录#6 负例：同步 result 中段复读实机文案（非前缀）→ 不误判 async。
	f := writeBlocks(t, "s.jsonl", []map[string]any{
		taskUse("t1", "Task", nil),
		taskResult("t1", "子代理报告：之前的派发 Async agent launched successfully，已收尾"),
	})
	if HasAsyncLaunch(f) {
		t.Fatal("中段复读非前缀应为 false")
	}
}

func TestT48ResultMarkerOnlyInSecondTextBlockIsFalse(t *testing.T) {
	// 附录#6 严格化：只认首个 text 块——文案出现在第二块不算。
	f := writeBlocks(t, "s.jsonl", []map[string]any{
		taskUse("t1", "Task", nil),
		taskResult("t1", []any{
			map[string]any{"type": "text", "text": "同步任务完成"},
			map[string]any{"type": "text", "text": "Async agent launched successfully"},
		}),
	})
	if HasAsyncLaunch(f) {
		t.Fatal("文案在第二块应为 false")
	}
}

func TestT48AsyncLaunchIgnoresNonAgentTools(t *testing.T) {
	// 非 Task/Agent 工具（如 Bash 的 run_in_background）不算子代理派发。
	f := writeBlocks(t, "s.jsonl", []map[string]any{
		taskUse("t1", "Bash", map[string]any{"command": "ls", "run_in_background": true}),
		taskResult("t1", "ok"),
	})
	if HasAsyncLaunch(f) {
		t.Fatal("Bash 后台运行不算子代理派发 → false")
	}
}

func TestT48AsyncLaunchMissingOrEmptyFileIsFalse(t *testing.T) {
	// 文件缺失/空文件 → False（判不中一律退回旧语义）。
	if HasAsyncLaunch(filepath.Join(t.TempDir(), "none.jsonl")) {
		t.Fatal("缺文件应为 false")
	}
	empty := filepath.Join(t.TempDir(), "empty.jsonl")
	if err := os.WriteFile(empty, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if HasAsyncLaunch(empty) {
		t.Fatal("空文件应为 false")
	}
}

func TestT48AsyncLaunchBadLinesSkippedNotRaised(t *testing.T) {
	// 附录#11：message 非 dict（字符串/列表）与顶层非 dict 的坏行安全跳过。
	//
	// 坏行均含 "tool_use"/"tool_result" 字样以穿过预过滤、真正命中守卫；
	// 只含坏行 → False；坏行夹在好行间 → 跳过后照常判定，绝不抛 AttributeError。
	badMsgStr := map[string]any{"type": "assistant", "message": "字符串不是dict",
		"tool_use": map[string]any{"id": "t9"}} // 行内含 "tool_use" → 穿过预过滤
	badMsgList := map[string]any{"type": "user", "message": []any{"列表也不是dict"},
		"tool_result": map[string]any{"tool_use_id": "t9"}}
	nonDictLine := lineOf([]any{"tool_use", "顶层是列表"}) // 解析出来顶层不是 dict

	badOnly := writeBlocks(t, "bad_only.jsonl", []map[string]any{badMsgStr, badMsgList})
	if HasAsyncLaunch(badOnly) {
		t.Fatal("只含坏行应为 false")
	}

	// 顶层非 dict 的坏行以行字符串直接混入（writeBlocks 收 map，故手拼）。
	p := filepath.Join(t.TempDir(), "s.jsonl")
	content := strings.Join([]string{
		lineOf(badMsgStr),
		lineOf(taskUse("t1", "Task", map[string]any{"run_in_background": true})),
		lineOf(badMsgList),
		lineOf(taskResult("t1", "ok")),
		nonDictLine,
	}, "\n") + "\n"
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if !HasAsyncLaunch(p) {
		t.Fatal("坏行夹在好行间应跳过并照常判定 → true")
	}
}

// ---- 票10 骑手收口 · 坏 UTF-8 尾窗用例 ----

func TestTailWindowBadUTF8ReplacedThenParsedPerLine(t *testing.T) {
	// 尾窗含非法 UTF-8 字节：统一 Python decode("utf-8", errors="replace") 语义
	// （qwatch 版 ToValidUTF8 收口进 internal/jsonl.TailWindow 单源；修复旧
	// cctrans 私有版直 Split 原始字节的保真缺口）——坏字节替换为 U+FFFD 后
	// 照常按行切分，其余合法行不受影响照常解析。
	good1 := lineOf(assistantLine("t1"))
	good2 := lineOf(resultLine("t1"))
	raw := good1 + "\n" + "\xff\xfe 非法字节行 \xc3\x28 更多\n" + good2
	p := filepath.Join(t.TempDir(), "badutf8.jsonl")
	if err := os.WriteFile(p, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}

	// 单源直断：TailWindow 输出的坏字节行已替换为 U+FFFD，无原始非法字节残留。
	lines, ok := jsonl.TailWindow(p, 1<<20)
	if !ok {
		t.Fatal("TailWindow 应成功")
	}
	if len(lines) != 3 {
		t.Fatalf("应按行切出 3 行, got %d", len(lines))
	}
	if i := strings.IndexByte(lines[1], 0xff); i >= 0 { // 原始 0xFF 不得残留
		t.Fatalf("坏字节行应已替换, 仍含原始 0xFF 字节: %q", lines[1])
	}
	if !strings.Contains(lines[1], "�") {
		t.Fatalf("坏字节应替换为 U+FFFD, got %q", lines[1])
	}

	// 行语义不变：坏行按坏行跳过，前后合法行照常参与判定——
	// t1 已收 result → 静止 false。
	if HasDanglingToolUse(p) {
		t.Fatal("t1 已收 result 应为 false（坏行不阻断其余行解析）")
	}
	// 坏行之后追加悬空行 → 照常检出 true（坏行只跳过，不断流）。
	p2 := filepath.Join(t.TempDir(), "badutf8_dangling.jsonl")
	if err := os.WriteFile(p2, []byte(raw+"\n"+lineOf(assistantLine("t2"))), 0o644); err != nil {
		t.Fatal(err)
	}
	if !HasDanglingToolUse(p2) {
		t.Fatal("坏行后的悬空 t2 应检出 true")
	}
}

// ---- 验收专测（Go 侧补充）----

func TestOver10MBLineKeepsFollowingLines(t *testing.T) {
	// 票面验收#3：>10MB 单行不得断流，其后的合法行必须照常读到。
	// 巨行内含 "usage" 字样以穿过子串预筛、强制走一次完整 JSON 解析失败路径。
	huge := `{"usage":"` + strings.Repeat("x", 11*1024*1024) + `"}`
	good := lineOf(assistantLine("t1"))
	p := filepath.Join(t.TempDir(), "huge.jsonl")
	content := huge + "\n" + good + "\n"
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	turns := AssistantTurns(p)
	if len(turns) != 1 {
		t.Fatalf("AssistantTurns 巨行后应读到 1 轮, got %d", len(turns))
	}
	if turns[0].CtxTokens() != 110 {
		t.Fatalf("CtxTokens = %d, want 110", turns[0].CtxTokens())
	}
	if !HasDanglingToolUse(p) {
		t.Fatal("巨行后尾窗应读到 t1 的悬空 tool_use")
	}
}

func TestAssistantTurnsSortSkipAndUTCNaive(t *testing.T) {
	// AssistantTurns 补充面：乱序输入按 ts 稳定升序；usage 全零跳过；
	// 无时区时间戳按 UTC（与 Z 形态同值）；int 宽松转换失败跳行。
	mk := func(ts string, inp, cr, cc any) string {
		return lineOf(map[string]any{
			"type": "assistant", "timestamp": ts,
			"message": map[string]any{"role": "assistant",
				"content": []any{map[string]any{"type": "text", "text": "好"}},
				"usage": map[string]any{"input_tokens": inp,
					"cache_read_input_tokens": cr, "cache_creation_input_tokens": cc}},
		})
	}
	badInt := lineOf(map[string]any{
		"type": "assistant", "timestamp": "2026-09-16T12:00:05.000Z",
		"message": map[string]any{"role": "assistant",
			"content": []any{},
			"usage":   map[string]any{"input_tokens": "abc", "cache_read_input_tokens": 1, "cache_creation_input_tokens": 1}},
	})
	p := writeLines(t, []string{
		mk("2026-09-16T12:00:02.000Z", 2, 3, 0), // 排第二
		mk("2026-09-16T12:00:01.000Z", 0, 0, 0), // 全零 → 跳
		mk("2026-09-16T12:00:00", 1, 0, 0),      // 无时区 → 按 UTC，排最前
		badInt,                                  // int 转换失败 → 跳
		mk("2026-09-16T12:00:03.000Z", 4, 5, 1), // 排最后
	})
	turns := AssistantTurns(p)
	if len(turns) != 3 {
		t.Fatalf("应剩 3 轮, got %d", len(turns))
	}
	wantTS := float64(time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC).Unix())
	if turns[0].TS != wantTS || turns[0].CtxTokens() != 1 {
		t.Fatalf("turn0 = %+v, want ts=%v ctx=1（无时区按 UTC）", turns[0], wantTS)
	}
	if turns[1].CtxTokens() != 5 || turns[2].CtxTokens() != 10 {
		t.Fatalf("排序错: turn1=%+v turn2=%+v", turns[1], turns[2])
	}
}

func TestAITitleAndFirstUserHashBasics(t *testing.T) {
	// AITitle：取最后一个有效 ai-title 并 strip；无 → ""。
	title1 := lineOf(map[string]any{"aiTitle": "  标题一  ", "type": "ai-title"})
	title2 := lineOf(map[string]any{"aiTitle": "标题二", "type": "ai-title"})
	f := writeLines(t, []string{title1, title2})
	if got := AITitle(f); got != "标题二" {
		t.Fatalf("AITitle = %q, want 标题二（末条且 strip）", got)
	}
	if got := AITitle(filepath.Join(t.TempDir(), "none.jsonl")); got != "" {
		t.Fatalf("缺文件 AITitle = %q, want 空串", got)
	}

	// FirstUserMessageHash：首条 user 正文 strip 后 sha256 hex；
	// content 字符串与 []text 块拼接同值；无 user 行 → ""。
	want := sha256.Sum256([]byte("你好"))
	wantHex := hex.EncodeToString(want[:])
	strForm := lineOf(map[string]any{"type": "user",
		"message": map[string]any{"role": "user", "content": "  你好  "}})
	blockForm := lineOf(map[string]any{"type": "user",
		"message": map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "text", "text": "你"},
			map[string]any{"type": "tool_result", "tool_use_id": "t0", "content": "x"},
			map[string]any{"type": "text", "text": "好"},
		}}})
	f1 := writeLines(t, []string{strForm})
	if got := FirstUserMessageHash(f1); got != wantHex {
		t.Fatalf("FirstUserMessageHash(str) = %q, want %q", got, wantHex)
	}
	f2 := writeLines(t, []string{blockForm})
	if got := FirstUserMessageHash(f2); got != wantHex {
		t.Fatalf("FirstUserMessageHash(blocks) = %q, want %q", got, wantHex)
	}
	f3 := writeLines(t, []string{lineOf(assistantLine())})
	if got := FirstUserMessageHash(f3); got != "" {
		t.Fatalf("无 user 行应为空串, got %q", got)
	}
}
