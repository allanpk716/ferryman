package daemon

// answer_gate_writeback_test.go — #37 作答拦截回写不算会话内容（spec #34 第三
// 提交，ADR-0028「拦截回写不算会话内容」段）：作答被拦后 CC 仍向转录追加带
// 时间戳的一段（答案 tool_result、PostToolUse hook_success、带 Ferryman 拦截
// 标记的 hook_stopped_continuation、prompt_snapshot、turn_duration）——以锚按
// 其 toolUseID 认出整段，内容时钟取值与交接覆盖截止提取都跳过（判定单源
// cctrans，IsHookBlockNotice 同族）。接缝 A：伪造转录（2026-10-15 本机实测
// 形状）＋冻结时钟驱动守望与闸门。

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ferryman/internal/cctrans"
	"ferryman/internal/config"
	"ferryman/internal/ledger"
	"ferryman/internal/store"
)

// answerGateToolUseID 夹具里 AskUserQuestion 的 tool_use id。
const answerGateToolUseID = "toolu_016F8WSMD6TXerSGRxuDZMPp"

// answerGateMarkerMessage 生产锚 message（#36 answerBlock5Reason 文案前缀＝
// cctrans.AnswerGateBlockMarker）。
func answerGateMarkerMessage() string {
	return cctrans.AnswerGateBlockMarker + " 此会话已闲置 60 分钟（缓存已失效）。" +
		"你刚作的选择没有发给模型，你的选择已保存：午饭吃面还是吃饭?→面"
}

// writeAnswerGatePreBlock 写"作答被拦前"的真实内容（实测形状）：首条 prompt、
// assistant AskUserQuestion tool_use（带 usage——enrich 取峰用）、PreToolUse
// hook_success（末行 ts=lastReal＝拦截前最后一条真实内容）。
func writeAnswerGatePreBlock(t *testing.T, path string, lastReal float64) {
	t.Helper()
	j := func(v any) string {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	lines := []string{
		j(map[string]any{"type": "user", "timestamp": isoUTC(lastReal - 120), "cwd": "C:\\proj",
			"message": map[string]any{"role": "user", "content": "午饭前收个尾"}}),
		j(map[string]any{"type": "assistant", "timestamp": isoUTC(lastReal - 60),
			"message": map[string]any{"role": "assistant", "content": []any{
				map[string]any{"type": "tool_use", "id": answerGateToolUseID, "name": "AskUserQuestion",
					"input": map[string]any{"questions": []any{map[string]any{
						"question": "午饭吃面还是吃饭?", "options": []any{
							map[string]any{"label": "面"}, map[string]any{"label": "饭"}}}}}},
			}, "usage": map[string]any{"input_tokens": 1000, "cache_read_input_tokens": 2000,
				"cache_creation_input_tokens": 500, "output_tokens": 10}}}),
		j(map[string]any{"type": "attachment", "timestamp": isoUTC(lastReal),
			"attachment": map[string]any{"type": "hook_success",
				"hookName": "PreToolUse:AskUserQuestion", "toolUseID": answerGateToolUseID}}),
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// appendAnswerGateWriteback 追加"作答被拦"回写段（实测五件，ts≈writeBack）：
// 答案 tool_result、PostToolUse hook_success、带标记的 hook_stopped_continuation
// 锚、prompt_snapshot、turn_duration。时间戳故意晚于拦截前真实内容——证明
// 回写不推进内容时钟。
func appendAnswerGateWriteback(t *testing.T, path string, writeBack float64) {
	t.Helper()
	j := func(v any) string {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	appendFileLines(t, path,
		j(map[string]any{"type": "user", "timestamp": isoUTC(writeBack + 0.3),
			"message": map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "tool_result", "tool_use_id": answerGateToolUseID,
					"content": "Your questions have been answered: 午饭吃面还是吃饭?=面"}}}}),
		j(map[string]any{"type": "attachment", "timestamp": isoUTC(writeBack),
			"attachment": map[string]any{"type": "hook_success",
				"hookName": "PostToolUse:AskUserQuestion", "toolUseID": answerGateToolUseID}}),
		j(map[string]any{"type": "attachment", "timestamp": isoUTC(writeBack + 0.2),
			"attachment": map[string]any{"type": "hook_stopped_continuation",
				"hookName": "PostToolUse:AskUserQuestion", "toolUseID": answerGateToolUseID,
				"message": answerGateMarkerMessage()}}),
		j(map[string]any{"type": "attachment", "timestamp": isoUTC(writeBack + 0.4),
			"attachment": map[string]any{"type": "prompt_snapshot"}}),
		j(map[string]any{"type": "system", "subtype": "turn_duration",
			"timestamp": isoUTC(writeBack + 0.5), "durationMs": 18671}),
	)
}

// TestAnswerGateWritebackNoReferryHandoffStaysValid 守望面（AC1/AC2/AC6）：作答
// 被拦后回写落地（mtime 前进、闲置达标）——内容时钟停在拦截前最后一条真实
// 内容，守望不重新入队摆渡；交接（covers=拦截前内容）仍为有效交接；其后真实
// 新内容落地 → 恢复入队。
func TestAnswerGateWritebackNoReferryHandoffStaysValid(t *testing.T) {
	tmp := t.TempDir()
	cfg := config.Default()
	cfg.Thresholds = config.ThresholdCfg{SummarizeS: 0.1, BlockS: 1.0, MinCtxTokens: 10}
	cfg.Watch.CCProjectsDir = filepath.Join(tmp, "projects")
	cfg.Watch.CodexSessionsDir = filepath.Join(tmp, "no-codex")
	led := ledger.New()
	sto, err := store.New(filepath.Join(tmp, "data"))
	if err != nil {
		t.Fatal(err)
	}
	proj := filepath.Join(tmp, "proj")
	f := filepath.Join(tmp, "agb-1.jsonl")
	now := freezeClock(t, 1_800_000_000.0)
	lastReal := *now - 3600 // 最后真实内容＝1 小时前（用户离开过夜作答被拦）
	writeAnswerGatePreBlock(t, f, lastReal)

	var enqueued int
	w := NewWatcher(cfg, led, sto, func(*ledger.SessionState) bool {
		enqueued++
		return true
	}, 0, nil, nil, nil, nil)

	st := led.TouchFull("cc", "agb-1", f, lastReal, 10, proj, "", 99999, 0)
	w.maybeEnqueue(st) // 首次：正常入队
	if enqueued != 1 {
		t.Fatalf("首次入队 = %d, want 1", enqueued)
	}
	// 模拟摆渡完成：处置边界＝内容时钟（＝拦截前最后真实内容）。
	contentTS, ok := cctrans.LastTimestamp(f)
	if !ok || contentTS != lastReal {
		t.Fatalf("前置：内容时钟 = (%v,%v), want (%v,true)", contentTS, ok, lastReal)
	}
	led.Mu().Lock()
	st.HandledContentTS = contentTS
	st.HandedOffAt = lastReal + 10 // 摆渡恰在拦截前内容之后
	led.Mu().Unlock()
	sto.SaveHandoff("agb-1", "cc", proj, "交接", isoUTC(lastReal), "fresh", "md")

	// 作答回写落地：mtime 前进（TouchFull）、闲置远超总结阈值——不得重摆。
	appendAnswerGateWriteback(t, f, *now-10)
	led.TouchFull("cc", "agb-1", f, *now-10, 20, proj, "", 0, 0)
	w.maybeEnqueue(st)
	if enqueued != 1 {
		t.Fatalf("作答回写后入队 = %d, want 1（回写不是内容推进，交接不白重摆）", enqueued)
	}
	// 同场景交接仍为有效交接（covers 覆盖内容时钟基准）。
	if sto.ValidHandoff("cc", proj, lastReal) == nil {
		t.Fatal("作答回写后交接仍应有效（ValidHandoff 命中）")
	}

	// 回写段之后出现真实新内容 → 内容时钟推进、恢复入队。
	appendFileLines(t, f, fmt.Sprintf(`{"type":"user","timestamp":%q,"cwd":%q,"message":{"role":"user","content":"强续 继续干"}}`,
		isoUTC(*now-5), proj))
	led.TouchFull("cc", "agb-1", f, *now-5, 30, proj, "", 0, 0)
	w.maybeEnqueue(st)
	if enqueued != 2 {
		t.Fatalf("真实新内容后入队 = %d, want 2（内容时钟正常推进）", enqueued)
	}
}

// TestAnswerGateWritebackPlainPromptBlockedQiangxuAllowed 闸门面（AC3/AC4）：
// 作答被拦后回写落地（mtime 刚动）——闲置按内容时钟（＝拦截前真实内容）判，
// 随后一条普通 prompt 经闸门 → 拦截（若无跳过，闲置被回写归零即冷缓存静默
// 放行）；「强续」→ 放行（打字入口 bypass，模型可见原答案）。
func TestAnswerGateWritebackPlainPromptBlockedQiangxuAllowed(t *testing.T) {
	e := newGateEnv(t)
	proj := filepath.Join(e.tmp, "proj")
	f := filepath.Join(e.tmp, "agb-2.jsonl")
	lastReal := e.t0 - 3600
	writeAnswerGatePreBlock(t, f, lastReal)
	appendAnswerGateWriteback(t, f, e.t0-10) // 回写刚落地：mtime/最后时间戳都在 10s 前
	e.reg("agb-2", f, proj, 10, 99999)       // lastWrite=10s 前（回写 mtime）
	en := e.store.SaveHandoff("agb-2", "cc", proj, "交接", isoUTC(lastReal), "fresh", "md")

	r := e.d.Gate(gateBody("agb-2", f, proj)) // 普通打字（无强续前缀）
	if r["decision"] != "block" {
		t.Fatalf("decision = %v, want block（闲置按内容时钟=1h，普通话仍拦）", r["decision"])
	}
	assertStr(t, r, "handoff_path", en.Path)

	r2 := e.d.Gate(gateBody4("agb-2", f, proj, "强续继续干"))
	if r2["decision"] != "allow" || r2["reason"] != "bypass" {
		t.Fatalf("强续 = %v, want allow/bypass", r2)
	}
}
