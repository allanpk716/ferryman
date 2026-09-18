package extract

// 规格：tests/test_extract.py 全部 13 例 1:1（T02 token 计量 3 + T04 extract 5 +
// T44a 末段定格/命令取段 5）。另加 1 例验收专测：中文长文本 TokenEstimate 与
// Python 实测对照钉死 4 例（票面验收#2，含全角标点样本钉住 CJK 范围下界 FF00）。

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ferryman/internal/cctrans"
	"ferryman/internal/mathx"
)

// writeJSONL 对应 Python 的 _write(tmp_path, lines)：dict 行 json 序列化（非
// ASCII 原样），str 行原样，按 "\n" 连接 + 尾部换行。
func writeJSONL(t *testing.T, lines []any) string {
	t.Helper()
	parts := make([]string, 0, len(lines))
	for _, x := range lines {
		if m, ok := x.(map[string]any); ok {
			b, err := json.Marshal(m)
			if err != nil {
				t.Fatal(err)
			}
			parts = append(parts, string(b))
		} else if s, ok := x.(string); ok {
			parts = append(parts, s)
		} else {
			t.Fatalf("不支持的行类型 %T", x)
		}
	}
	p := filepath.Join(t.TempDir(), "s.jsonl")
	if err := os.WriteFile(p, []byte(strings.Join(parts, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// fixtureLines 对应 Python 的 _fixture(tmp_path)。
func fixtureLines() []any {
	return []any{
		map[string]any{"type": "user", "timestamp": "2026-09-16T10:00:00.000Z",
			"cwd": "C:\\repo", "message": map[string]any{"role": "user", "content": "做点事"}},
		map[string]any{"type": "assistant", "timestamp": "2026-09-16T10:00:10.000Z",
			"message": map[string]any{"role": "assistant", "content": []any{
				map[string]any{"type": "tool_use", "name": "Edit",
					"input": map[string]any{"file_path": "C:\\repo\\a.ts", "old_string": "x", "new_string": "y"}},
				map[string]any{"type": "tool_use", "name": "Edit",
					"input": map[string]any{"file_path": "C:\\repo\\a.ts", "old_string": "y", "new_string": "z"}},
				map[string]any{"type": "tool_use", "name": "Bash",
					"input": map[string]any{"command": strings.Repeat("c", 200)}},
				map[string]any{"type": "tool_use", "name": "Bash",
					"input": map[string]any{"command": strings.Repeat("c", 200)}}, // 重复命令应去重
				map[string]any{"type": "tool_use", "name": "Bash", "input": map[string]any{"command": "echo hi"}},
				map[string]any{"type": "thinking", "thinking": "内心戏不应进正文"},
				map[string]any{"type": "text", "text": "改完了 a.ts"},
			}, "usage": map[string]any{"input_tokens": 1000, "cache_read_input_tokens": 2000,
				"cache_creation_input_tokens": 500, "output_tokens": 10}}},
		map[string]any{"type": "user", "timestamp": "2026-09-16T10:00:20.000Z",
			"message": map[string]any{"role": "user", "content": []any{ // tool_result 整块丢弃
				map[string]any{"type": "tool_result", "tool_use_id": "t1",
					"content": "巨大的工具输出不应进入正文"}}}},
		map[string]any{"type": "user", "timestamp": "2026-09-16T10:00:30.000Z",
			"message": map[string]any{"role": "user", "content": strings.Repeat("长", 5000)}}, // 截断到 4000
		map[string]any{"type": "assistant", "timestamp": "2026-09-16T10:00:40.000Z",
			"message": map[string]any{"role": "assistant", "content": []any{
				map[string]any{"type": "text", "text": "收尾"}},
				"usage": map[string]any{"input_tokens": 9000, "cache_read_input_tokens": 0,
					"cache_creation_input_tokens": 0, "output_tokens": 5}}},
		map[string]any{"type": "ai-title", "aiTitle": "测试会话标题"},
	}
}

// ---------- T02 token_estimate ----------

func TestTokenEstimatePureCJK(t *testing.T) {
	if got := TokenEstimate(strings.Repeat("中", 100)); got != 101 { // 100 + 1
		t.Fatalf("TokenEstimate(中×100) = %d, want 101", got)
	}
}

func TestTokenEstimateASCII(t *testing.T) {
	est := TokenEstimate(strings.Repeat("a", 35))
	if est < 10 || est > 12 { // ~35/3.5
		t.Fatalf("TokenEstimate(a×35) = %d, want 10..12", est)
	}
}

func TestTokenEstimateMixedAndEmpty(t *testing.T) {
	est := TokenEstimate(strings.Repeat("中a", 10)) // 10 CJK + 10 ASCII
	if est < 12 || est > 14 {
		t.Fatalf("TokenEstimate(中a×10) = %d, want 12..14", est)
	}
	if got := TokenEstimate(""); got != 1 {
		t.Fatalf("TokenEstimate(\"\") = %d, want 1", got)
	}
}

// 验收#2：中文长文本与 Python 实测一致（uv run python 逐例实算，含全角标点
// 样本钉住 CJK 范围 [U+3000-9FFF U+FF00-FFEF] 的下界——！U+FF01、：U+FF1A
// 均按 CJK 计 1）。
func TestTokenEstimatePythonPinned(t *testing.T) {
	cases := []struct {
		text string
		want int
	}{
		{strings.Repeat("中", 100), 101},
		{strings.Repeat("a", 100), 29},
		{strings.Repeat("配置文件已更新，缓存命中正常，心跳间隔按价格与实测 TTL 闭式推导", 40), 1218},
		{"执行 git rebase --onto main 后修复了 3 处冲突，全角标点：？！、（已截断）ＱＷＥＲＴ" + strings.Repeat("x", 137), 75},
	}
	for i, c := range cases {
		if got := TokenEstimate(c.text); got != c.want {
			t.Fatalf("case %d: TokenEstimate = %d, want %d（Python 实测）", i, got, c.want)
		}
	}
}

// ---------- T04 extract ----------

func TestExtractSkeleton(t *testing.T) {
	facts, _, _ := Extract(writeJSONL(t, fixtureLines()))
	if facts.Title != "测试会话标题" {
		t.Fatalf("Title = %q", facts.Title)
	}
	if facts.Cwd != "C:\\repo" {
		t.Fatalf("Cwd = %q", facts.Cwd)
	}
	if len(facts.Files) == 0 || facts.Files[0].Path != "C:\\repo\\a.ts" || facts.Files[0].Count != 2 { // 按次数降序
		t.Fatalf("Files[0] = %+v", facts.Files)
	}
	if len(facts.Commands) != 2 { // 去重后
		t.Fatalf("len(Commands) = %d, want 2", len(facts.Commands))
	}
	if mathx.RuneLen(facts.Commands[0]) != 160 { // 截断
		t.Fatalf("RuneLen(Commands[0]) = %d, want 160", mathx.RuneLen(facts.Commands[0]))
	}
	if facts.Commands[1] != "echo hi" {
		t.Fatalf("Commands[1] = %q", facts.Commands[1])
	}
	if facts.PeakCtx != 9000 { // 第二轮 9000 > 第一轮 3500
		t.Fatalf("PeakCtx = %d, want 9000", facts.PeakCtx)
	}
}

func TestExtractItems(t *testing.T) {
	_, items, _ := Extract(writeJSONL(t, fixtureLines()))
	texts := make([]string, len(items))
	for i, it := range items {
		texts[i] = it.Text
	}
	contains := func(sub string) bool {
		for _, s := range texts {
			if strings.Contains(s, sub) {
				return true
			}
		}
		return false
	}
	if !contains("做点事") {
		t.Fatal("缺 user 正文 做点事")
	}
	if !contains("改完了 a.ts") {
		t.Fatal("缺 assistant 正文")
	}
	if contains("内心戏") { // thinking 丢弃
		t.Fatal("thinking 泄入正文")
	}
	if contains("工具输出") { // tool_result 丢弃
		t.Fatal("tool_result 泄入正文")
	}
	var longUser string
	for _, s := range texts {
		if strings.HasPrefix(s, "长") {
			longUser = s
			break
		}
	}
	if longUser == "" || mathx.RuneLen(longUser) != 4000 { // 单条截断
		t.Fatalf("长文条目 RuneLen = %d, want 4000", mathx.RuneLen(longUser))
	}
}

func TestChunkItemsBudget(t *testing.T) {
	_, items, _ := Extract(writeJSONL(t, fixtureLines()))
	chunks := ChunkItems(items, 10) // 极小预算 → 每条一块
	if len(chunks) != len(items) {
		t.Fatalf("len(chunks) = %d, want %d", len(chunks), len(items))
	}
	chunks = ChunkItems(items, 100000)
	if len(chunks) != 1 {
		t.Fatalf("len(chunks) = %d, want 1", len(chunks))
	}
}

func TestAssistantTurnsSortedAndDefensive(t *testing.T) {
	f := writeJSONL(t, []any{
		map[string]any{"type": "assistant", "timestamp": "2026-09-16T10:00:40.000Z", // 无 Z 也行
			"message": map[string]any{"role": "assistant", "content": []any{},
				"usage": map[string]any{"input_tokens": 5, "cache_read_input_tokens": 0,
					"cache_creation_input_tokens": 0}}},
		"{broken json",
		map[string]any{"type": "assistant", "timestamp": "2026-09-16T10:00:10.000Z",
			"message": map[string]any{"role": "assistant", "content": []any{},
				"usage": map[string]any{"input_tokens": 7, "cache_read_input_tokens": 0,
					"cache_creation_input_tokens": 0}}},
		map[string]any{"type": "assistant", "timestamp": "2026-09-16T10:00:20.000Z", // 缺 usage
			"message": map[string]any{"role": "assistant", "content": []any{}}},
		map[string]any{"type": "user", "timestamp": "2026-09-16T10:00:30.000Z", // 非 assistant
			"message": map[string]any{"role": "user", "content": "hi"}},
	})
	turns := cctrans.AssistantTurns(f)
	if len(turns) != 2 || turns[0].InputTokens != 7 || turns[1].InputTokens != 5 { // 坏行跳过 + 按时间排序
		t.Fatalf("turns = %+v, want [7 5]", turns)
	}
	var _ cctrans.Turn = turns[0] // isinstance(t, Turn)
}

func TestMaterialTextContainsSkeletonAndItems(t *testing.T) {
	facts, items, _ := Extract(writeJSONL(t, fixtureLines()))
	md := MaterialText(facts, items)
	if !strings.Contains(md, "确定性骨架") || !strings.Contains(md, "a.ts") || !strings.Contains(md, "[user] 做点事") {
		t.Fatal("material_text 缺骨架或正文要素")
	}
}

// ---------- T44a 末段定格 + 命令取段 ----------

func TestFreezePlainTail(t *testing.T) {
	f := writeJSONL(t, []any{
		map[string]any{"type": "user", "timestamp": "2026-09-16T10:00:00.000Z",
			"message": map[string]any{"role": "user", "content": "前面的话"}},
		map[string]any{"type": "assistant", "timestamp": "2026-09-16T10:00:10.000Z",
			"message": map[string]any{"role": "assistant", "content": "早先回复"}},
		map[string]any{"type": "user", "timestamp": "2026-09-16T10:00:20.000Z",
			"message": map[string]any{"role": "user", "content": "最后问题"}},
		map[string]any{"type": "assistant", "timestamp": "2026-09-16T10:00:30.000Z",
			"message": map[string]any{"role": "assistant", "content": []any{
				map[string]any{"type": "text", "text": "最后回复"}}}},
	})
	facts, _, _ := Extract(f)
	sk := facts.SkeletonText()
	if !strings.Contains(sk, "## 末段定格") {
		t.Fatal("缺末段定格标题")
	}
	if !strings.Contains(sk, "[user] 最后问题") {
		t.Fatal("缺末条 user")
	}
	if !strings.Contains(sk, "[assistant] 最后回复") {
		t.Fatal("缺末条 assistant")
	}
	if strings.Contains(sk, "前面的话") || strings.Contains(sk, "早先回复") { // 只定格末轮，不带历史
		t.Fatal("历史泄入骨架")
	}
}

func TestFreezeCapsAndMarker(t *testing.T) {
	longText := strings.Repeat("前", 1500) + strings.Repeat("后", 500) // 2000 字助手文本
	f := writeJSONL(t, []any{
		map[string]any{"type": "user", "timestamp": "2026-09-16T10:00:00.000Z",
			"message": map[string]any{"role": "user", "content": "问"}},
		map[string]any{"type": "assistant", "timestamp": "2026-09-16T10:00:10.000Z",
			"message": map[string]any{"role": "assistant", "content": []any{
				map[string]any{"type": "text", "text": longText}}}},
	})
	facts, _, _ := Extract(f)
	sk := facts.SkeletonText()
	if !strings.Contains(sk, strings.Repeat("前", 1500)) { // 保留前 1500 字
		t.Fatal("截断保留段缺失")
	}
	if !strings.Contains(sk, "（已截断，全文见会话文件）") { // 尾标
		t.Fatal("缺截断尾标")
	}
	if strings.Contains(sk, strings.Repeat("后", 100)) { // 截断部分不出现
		t.Fatal("截断段泄入骨架")
	}
}

func TestFreezeChoiceTail(t *testing.T) {
	ask := map[string]any{"type": "tool_use", "name": "AskUserQuestion",
		"input": map[string]any{"questions": []any{
			map[string]any{"question": "选哪个",
				"options": []any{map[string]any{"label": "甲"}, map[string]any{"label": "乙"}}}}}}
	f := writeJSONL(t, []any{
		map[string]any{"type": "user", "timestamp": "2026-09-16T10:00:00.000Z",
			"message": map[string]any{"role": "user", "content": "怎么办"}},
		map[string]any{"type": "assistant", "timestamp": "2026-09-16T10:00:10.000Z",
			"message": map[string]any{"role": "assistant", "content": []any{
				map[string]any{"type": "text", "text": "请选择方案"},
				ask}}},
	})
	facts, _, _ := Extract(f)
	sk := facts.SkeletonText()
	if !strings.Contains(sk, "【上次停在选择】") {
		t.Fatal("缺选择标题")
	}
	if !strings.Contains(sk, "问题：选哪个") {
		t.Fatal("缺问题句")
	}
	if !strings.Contains(sk, "选项：甲") || !strings.Contains(sk, "选项：乙") {
		t.Fatal("缺选项行")
	}
	if strings.Contains(sk, `"options"`) || strings.Contains(sk, "questions") { // 不倒 JSON 原文
		t.Fatal("JSON 原文泄入骨架")
	}
}

func TestFreezeToolOnlyTail(t *testing.T) {
	f := writeJSONL(t, []any{
		map[string]any{"type": "user", "timestamp": "2026-09-16T10:00:00.000Z",
			"message": map[string]any{"role": "user", "content": "跑一下"}},
		map[string]any{"type": "assistant", "timestamp": "2026-09-16T10:00:10.000Z",
			"message": map[string]any{"role": "assistant", "content": []any{
				map[string]any{"type": "tool_use", "name": "Bash", "input": map[string]any{"command": "pytest -q"}}}}},
	})
	facts, _, _ := Extract(f)
	sk := facts.SkeletonText()
	if !strings.Contains(sk, "（末条为工具调用：Bash，无文字回复）") {
		t.Fatal("缺工具调用形态")
	}
}

func TestCommandsRecentTail(t *testing.T) {
	lines := []any{map[string]any{"type": "user", "timestamp": "2026-09-16T10:00:00.000Z",
		"message": map[string]any{"role": "user", "content": "开始"}}}
	for i := 0; i < 80; i++ {
		lines = append(lines, map[string]any{"type": "assistant",
			"timestamp": fmt.Sprintf("2026-09-16T10:%02d:%02d.000Z", i/60, i%60),
			"message": map[string]any{"role": "assistant", "content": []any{
				map[string]any{"type": "tool_use", "name": "Bash",
					"input": map[string]any{"command": fmt.Sprintf("cmd_%03d --flag %d", i, i)}}}}})
	}
	f := writeJSONL(t, lines)
	facts, _, _ := Extract(f)
	if len(facts.Commands) != 80 { // 收集端全量保留
		t.Fatalf("len(Commands) = %d, want 80", len(facts.Commands))
	}
	sk := facts.SkeletonText()
	if !strings.Contains(sk, "最近 20 条") { // 渲染端取尾部
		t.Fatal("缺「最近 20 条」文案")
	}
	if !strings.Contains(sk, "cmd_079") { // 含最后一条
		t.Fatal("缺最后一条命令")
	}
	if strings.Contains(sk, "cmd_000") { // 不含最早一条
		t.Fatal("最早命令泄入骨架")
	}
	if !strings.Contains(sk, "cmd_060") { // 尾 20 的首条（第 61 条）
		t.Fatal("缺尾窗首条")
	}
}
