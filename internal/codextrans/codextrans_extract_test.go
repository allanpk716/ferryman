package codextrans

// 规格：tests/test_codex_extract.py 6 例中可独立移植的 4 例 1:1
// （extract_codex 全语义 / 防御 / 标题截断回落 / 注入 user 过滤）。
// 余下 2 例依赖尚未移植的宿主包，随其归属票落地、不在本包：
//   - test_ferry_session_dispatches_codex → 票 18 internal/ferry.FerrySession
//     （agent="codex" 走 codextrans 提取的分派用例，rev1 Task 18）；
//   - test_skeleton_fallback_uses_codex_extraction → daemon 骨架降级 e2e
//     （票 13/14/16/17 守望-降级链）。
// 验收钉子（票面：developer/system、function_call_output、reasoning 不进材料）
// 由 TestExtractCodexFullSemantics 覆盖——样本行含三类行，断言 items 仅剩
// user/assistant 两条。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ferryman/internal/mathx"
)

// lineX 对应 Python 的 _line(ts, typ, payload)（ensure_ascii=False → Go
// json.Marshal 同为非 ASCII 原样；<>& 的 \u003c 形转义在解码后等价，无碍）。
func lineX(ts, typ string, payload any) string {
	return lineOf(map[string]any{"timestamp": ts, "type": typ, "payload": payload})
}

// msgLine 对应 Python 的 _msg(ts, role, text)：assistant 用 output_text 块，
// 其余角色用 input_text 块。
func msgLine(ts, role, text string) string {
	blockType := "input_text"
	if role == "assistant" {
		blockType = "output_text"
	}
	return lineX(ts, "response_item", map[string]any{"type": "message", "role": role,
		"content": []any{map[string]any{"type": blockType, "text": text}}})
}

// tokLine 对应 Python 的 _tok(ts, inp, cached=0)。
func tokLine(ts string, inp, cached int) string {
	return lineX(ts, "event_msg", map[string]any{"type": "token_count",
		"info": map[string]any{"last_token_usage": map[string]any{
			"input_tokens": inp, "cached_input_tokens": cached,
			"cache_write_input_tokens": 0}}})
}

// callLine 对应 Python 的 _call(ts, name, **args)：arguments 是 args dict 的
// JSON 串（rollout 实态）。
func callLine(ts, name string, args map[string]any) string {
	argJSON, err := json.Marshal(args)
	if err != nil {
		panic(err)
	}
	return lineX(ts, "response_item", map[string]any{"type": "function_call",
		"name": name, "arguments": string(argJSON)})
}

// writeRollout 对应 Python 的 _write_rollout(tmp_path, lines)。文件名沿用
// 真机 rollout 形态；Python 侧的 os.utime 旧 mtime 只服务 daemon 观察窗
// （extract_codex 不读 mtime），Go 侧省去。
func writeRollout(t *testing.T, lines []string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(),
		"rollout-2026-09-17T10-51-25-01a0ad46-ac17-7d73-a203-072c58812fd0.jsonl")
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// sampleLines 对应 Python 的 _sample_lines()。
func sampleLines() []string {
	return []string{
		lineX("2026-09-17T02:52:19.862Z", "session_meta",
			map[string]any{"session_id": "01a0ad46", "cwd": "C:\\proj", "originator": "codex-tui"}),
		msgLine("2026-09-17T02:52:20.000Z", "developer", "<skills_instructions>系统提示</skills>"),
		msgLine("2026-09-17T02:52:21.000Z", "user", "修一下登录页的 bug"),
		msgLine("2026-09-17T02:52:30.000Z", "assistant", "好的，我先看下 auth.py 的登录分支"),
		lineX("2026-09-17T02:52:31.000Z", "response_item",
			map[string]any{"type": "reasoning",
				"summary": []any{map[string]any{"type": "summary_text", "text": "思考中"}}}),
		callLine("2026-09-17T02:52:32.000Z", "exec_command",
			map[string]any{"cmd": "rg def login C:\\proj"}),
		callLine("2026-09-17T02:52:40.000Z", "apply_patch",
			map[string]any{"input": "*** Begin Patch\n*** Update File: C:\\proj\\auth.py\n@@\n-old\n+new\n*** End Patch"}),
		lineX("2026-09-17T02:52:41.000Z", "response_item",
			map[string]any{"type": "function_call_output", "call_id": "x",
				"output": "Chunk ID: f07351\nOutput: 1385 tokens"}),
		"{broken",
		tokLine("2026-09-17T02:52:45.000Z", 20000, 18000),
		tokLine("2026-09-17T03:16:44.435Z", 25000, 22000),
	}
}

// ---------- extract_codex：骨架 + 正文 ----------

func TestExtractCodexFullSemantics(t *testing.T) {
	f := writeRollout(t, sampleLines())
	facts, items := ExtractCodex(f)

	if facts.Cwd != "C:\\proj" {
		t.Fatalf("Cwd = %q, want C:\\proj", facts.Cwd)
	}
	if facts.Title != "修一下登录页的 bug" { // 首条用户消息作标题（rollout 无 ai-title）
		t.Fatalf("Title = %q", facts.Title)
	}
	if facts.FirstTS != "2026-09-17T02:52:19.862Z" || facts.LastTS != "2026-09-17T03:16:44.435Z" {
		t.Fatalf("起止 = %q ~ %q", facts.FirstTS, facts.LastTS)
	}
	if facts.NTurns != 2 || facts.PeakCtx != 25000 { // token_count 口径
		t.Fatalf("n_turns=%d peak=%d, want 2/25000", facts.NTurns, facts.PeakCtx)
	}
	if facts.TotalInputTokens != 45000 { // 20000+25000，Python extract_codex 实测对照
		t.Fatalf("TotalInputTokens = %d, want 45000", facts.TotalInputTokens)
	}
	if len(facts.Commands) != 1 || facts.Commands[0] != "rg def login C:\\proj" {
		t.Fatalf("Commands = %v", facts.Commands)
	}
	if len(facts.Files) == 0 || facts.Files[0].Path != "C:\\proj\\auth.py" {
		t.Fatalf("Files = %+v", facts.Files)
	}

	// 正文只要 user/assistant 文本：developer/reasoning/工具输出全丢
	if len(items) != 2 || items[0].Role != "user" || items[1].Role != "assistant" {
		t.Fatalf("items = %+v, want [user assistant] 各一条", items)
	}
	if !strings.Contains(items[0].Text, "修一下登录页") {
		t.Fatalf("items[0].Text = %q", items[0].Text)
	}
	if !strings.Contains(items[1].Text, "auth.py") {
		t.Fatalf("items[1].Text = %q", items[1].Text)
	}
}

func TestExtractCodexDefensive(t *testing.T) {
	// 文件不存在 / 空文件 / 全坏行 → 空骨架不抛错
	facts, items := ExtractCodex(filepath.Join(t.TempDir(), "nope.jsonl"))
	if facts.NTurns != 0 || facts.PeakCtx != 0 || facts.Cwd != "" {
		t.Fatalf("缺文件骨架 = %+v", facts)
	}
	if len(items) != 0 {
		t.Fatalf("缺文件正文 = %+v", items)
	}

	f := writeRollout(t, []string{"{broken", "", "not json"})
	facts, items = ExtractCodex(f)
	if len(items) != 0 || facts.Title != "" {
		t.Fatalf("全坏行应空: title=%q items=%d", facts.Title, len(items))
	}
}

func TestExtractCodexTitleCapAndFallback(t *testing.T) {
	longMsg := strings.Repeat("这是一条特别长的用户消息", 30) // 12 字 × 30 = 360 字 → 截到 60
	f := writeRollout(t, []string{msgLine("2026-09-17T02:52:21.000Z", "user", longMsg)})
	facts, _ := ExtractCodex(f)
	if facts.Title == "" || mathx.RuneLen(facts.Title) > 60 {
		t.Fatalf("Title = %q（码点 %d），want 非空且 ≤60", facts.Title, mathx.RuneLen(facts.Title))
	}

	// 无用户消息 → 无标题（不用 assistant 兜底）
	f2 := writeRollout(t, []string{msgLine("2026-09-17T02:52:21.000Z", "assistant", "回复")})
	facts2, _ := ExtractCodex(f2)
	if facts2.Title != "" {
		t.Fatalf("无 user 消息 Title = %q, want 空", facts2.Title)
	}
}

func TestExtractCodexSkipsInjectedUserMessages(t *testing.T) {
	// codex 把环境注入伪装成 user 消息（2026-09-17 真机实测）：
	// AGENTS.md 指令、<turn_aborted> 中断标记——不进正文、不作标题（防注入面）。
	// 真人消息不受影响（哪怕以 < 开头粘贴 HTML，未命中已知标记即保留）。
	lines := []string{
		msgLine("2026-09-17T02:52:19.895Z", "user", "# AGENTS.md instructions\n\n<INSTRUCTIONS>"),
		msgLine("2026-09-17T02:52:27.126Z", "user", "我去查一下流人第六季什么时候上线"),
		msgLine("2026-09-17T03:21:14.546Z", "user",
			"<turn_aborted>\nThe user interrupted the previous turn on purpose."),
		msgLine("2026-09-17T03:21:20.000Z", "user", "<div>粘贴的 HTML 片段</div>"),
	}
	f := writeRollout(t, lines)
	facts, items := ExtractCodex(f)
	if len(items) != 2 ||
		items[0].Text != "我去查一下流人第六季什么时候上线" ||
		items[1].Text != "<div>粘贴的 HTML 片段</div>" {
		t.Fatalf("items = %+v", items)
	}
	if facts.Title != "我去查一下流人第六季什么时候上线" {
		t.Fatalf("Title = %q", facts.Title)
	}
}
