package daemon

// gate_exempt_test.go — 1.5 机器自发 prompt 豁免钉子（fix/gate-machine-exempt）：
//   A) 目录豁免 [gate].exempt_cwds：cwdExemptHit 单元表（相等/子目录/大小写/
//      尾随分隔符/前缀伪匹配/空）+ Gate 接线（命中放行不入账不存待续、伪前缀
//      不命中照拦、codex 同样生效）。
//   B) /loop 定时唤醒豁免：凉会话放行（loop-wakeup）/护选择拦截（de9af594
//      场景复刻：作答待续 0.6s 后被唤醒撞上，照拦但待续文本不变）/作答入口
//      不适用/活跃会话不受扰。
//   C) 作答→打字护选择的归还文案：injectHandoff 与"锚会话没有交接"分支都
//      带旧选择行，且在打字待续行之前。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ferryman/internal/accounts"
	"ferryman/internal/cctrans"
)

// wakeupPromptX de9af594 案的 /loop 唤醒 prompt 原文（钩子拿到的与 fire 记录
// prompt 字段逐字相同）。
const wakeupPromptX = "继续 /implement-spec-split spec #26 编排：检查 fix worker（review-fix 分支）状态"

// writeWakeupFile 复刻 de9af594 的转录尾部：悬空 AskUserQuestion（作答闸门的
// 前置形态）→ queue-operation 回放 → scheduled_task_fire（fireTS 为唤醒时刻）。
func writeWakeupFile(t *testing.T, p, prompt, fireTS string) string {
	t.Helper()
	mustLine := func(v any) string {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	lines := []string{
		mustLine(map[string]any{"type": "assistant",
			"message": map[string]any{"content": []any{map[string]any{
				"type": "tool_use", "id": "tu_1", "name": "AskUserQuestion",
				"input": map[string]any{}}}}}),
		mustLine(map[string]any{"type": "queue-operation", "operation": "enqueue",
			"content": prompt, "timestamp": fireTS}),
		mustLine(map[string]any{"type": "system", "subtype": "scheduled_task_fire",
			"content":   "Claude resuming /loop wakeup (Oct 17 8:09am)",
			"timestamp": fireTS,
			"taskId":    "ebb52f87", "cron": "46 0 * * *",
			"prompt": prompt, "taskKind": "loop", "cronKind": "loop",
			"sessionId": "de9af594", "cwd": "C:\\WorkSpace\\agent-taskboard"}),
	}
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// gateLogText 沙箱 gate.log 全文（不存在＝空）。
func gateLogText(t *testing.T, e *gateEnv) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(e.tmp, "data", "gate.log"))
	if err != nil {
		return ""
	}
	return string(b)
}

// pinContentClock 钉住台账内容时钟为“已算态”：复刻守望未及轮询 fire 写入的
// 拦后窗——de9af594 生产拦截正是落在陈旧缓存上（若缓存新鲜，闲置钟被 fire
// 记录顶新、根本不进拦窗）；同时保证钩子时序下 LastTimestamp 重扫不稀释闲置。
func pinContentClock(t *testing.T, e *gateEnv, sid string, contentTS float64) {
	t.Helper()
	e.led.Mu().Lock()
	defer e.led.Mu().Unlock()
	st := e.led.GetLocked("cc", sid)
	if st == nil {
		t.Fatalf("台账无 %s", sid)
	}
	st.ContentTS = contentTS
	st.ContentStamp = st.LastWrite // 版本章一致（已算态）
}

// acctRowsFor 该会话在账本里的指定科目行数（豁免"不入账"断言用）。
func acctRowsFor(w *wenvT, sid string, kinds ...string) int {
	n := 0
	for _, k := range kinds {
		for _, r := range w.acc.Read(accounts.ReadOpts{Kind: k}) {
			if r["session_id"] == sid {
				n++
			}
		}
	}
	return n
}

// ---- A) 目录豁免 ----

func TestCwdExemptHit(t *testing.T) {
	roots := []string{`C:\WorkSpace\AutoTasks`, "D:/orca"}
	cases := []struct {
		cwd  string
		want bool
		note string
	}{
		{`C:\WorkSpace\AutoTasks`, true, "相等"},
		{`C:\WorkSpace\AutoTasks\t1\deep`, true, "子目录"},
		{"c:/workspace/autotasks", true, "大小写+分隔符"},
		{`C:\WorkSpace\AutoTasks\`, true, "尾随分隔符"},
		{`C:\WorkSpace\AutoTasks2`, false, "前缀伪匹配不得命中"},
		{`C:\WorkSpace`, false, "父目录不在豁免内"},
		{"D:/orca/x", true, "第二根子目录"},
		{"D:/orc", false, "第二根前缀伪匹配"},
		{"", false, "空 cwd"},
	}
	for _, c := range cases {
		if got := cwdExemptHit(c.cwd, roots); got != c.want {
			t.Fatalf("cwdExemptHit(%q) = %v, want %v（%s）", c.cwd, got, c.want, c.note)
		}
	}
	if cwdExemptHit(`C:\WorkSpace\AutoTasks`, nil) || cwdExemptHit(`C:\x`, []string{""}) {
		t.Fatal("空根集/空根不得命中")
	}
}

func TestGateExemptCwdAllowsColdSessionWithoutSideEffects(t *testing.T) {
	// 2026-10-15 17:30 Orca 自动化案形态：凉会话（有有效交接，正常路径必走
	// 分支 5 拦截）+ cwd 命中豁免 → 放行 exempt-cwd；不警告/不存待续/不入账，
	// gate.log 记 [exempt] 行。
	w := newWenv(t)
	root := filepath.Join(w.tmp, "AutoTasks")
	w.d.Cfg.GateExemptCwds = []string{root}
	proj := filepath.Join(root, "task1", "deep") // 子目录同样命中
	w.reg("orca-1", "C:/orca-1.jsonl", proj, testBlockS+5, 99999)
	w.store.SaveHandoff("orca-1", "cc", proj, "t", isoUTC(w.t0), "fresh", "md")
	r := w.d.Gate(gateBody4("orca-1", "C:/orca-1.jsonl", proj, "Orca 定时任务 prompt"))
	assertStr(t, r, "decision", "allow")
	assertStr(t, r, "reason", "exempt-cwd")
	if got := w.store.PopPendingPrompt("orca-1", ""); got != "" {
		t.Fatalf("豁免不得存待续, got %q", got)
	}
	if w.d.Stats.Blocks != 0 {
		t.Fatalf("豁免不得计拦截, Blocks = %d", w.d.Stats.Blocks)
	}
	if n := acctRowsFor(w, "orca-1", "block", "bypass"); n != 0 {
		t.Fatalf("豁免不入账（不污染 bypass 对照组）, rows = %d", n)
	}
	if log := gateLogText(t, &w.gateEnv); !strings.Contains(log, "[exempt] cc sid=orca-1 reason=exempt-cwd") {
		t.Fatalf("gate.log 应记 [exempt] 行: %q", log)
	}
}

func TestGateExemptCwdPrefixPseudoMissStillBlocks(t *testing.T) {
	// `C:\a\b` 不得命中 `C:\a\bc`：豁免不生效 → 凉会话照常分支 5 拦截。
	e := newGateEnv(t)
	e.d.Cfg.GateExemptCwds = []string{`C:\a\b`}
	e.reg("pf-1", "C:/pf-1.jsonl", `C:/a/bc`, testBlockS+5, 99999)
	e.store.SaveHandoff("pf-1", "cc", `C:/a/bc`, "t", isoUTC(e.t0), "fresh", "md")
	r := e.d.Gate(gateBody4("pf-1", "C:/pf-1.jsonl", `C:/a/bc`, "继续"))
	assertStr(t, r, "decision", "block")
	if strings.Contains(gateLogText(t, e), "[exempt]") {
		t.Fatal("伪前缀不得命中豁免")
	}
	// 同根大小写/子目录形态命中（正面对照）
	r2 := e.d.Gate(gateBody4("pf-1", "C:/pf-1.jsonl", `c:/A/B\sub`, "继续"))
	assertStr(t, r2, "decision", "allow")
	assertStr(t, r2, "reason", "exempt-cwd")
}

func TestGateExemptCwdAppliesToCodex(t *testing.T) {
	// 目录豁免对 codex 同样生效，且先于 mode off（GateCodex 缺省 off，命中
	// 豁免时 reason 是 exempt-cwd 而非 mode-off＝接线序钉子）。
	e := newGateEnv(t)
	e.d.Cfg.GateExemptCwds = []string{`C:\WorkSpace\AutoTasks`}
	r := e.d.Gate(map[string]any{"agent": "codex", "session_id": "cx-1",
		"transcript_path": "C:/cx-1.jsonl",
		"cwd":             `C:\WorkSpace\AutoTasks\nightly`, "prompt": "定时任务"})
	assertStr(t, r, "decision", "allow")
	assertStr(t, r, "reason", "exempt-cwd")
}

// ---- B) /loop 定时唤醒豁免 ----

func TestGateLoopWakeupAllowOnColdSession(t *testing.T) {
	// de9af594 主形态的修复面：凉会话（有有效交接，正常路径必拦）+ 打字入口
	// 的 /loop 唤醒 → 放行 loop-wakeup；不警告/不存待续/不入账/不动 pending，
	// gate.log 记 [exempt] 行。
	w := newWenv(t)
	proj := filepath.Join(w.tmp, "agent-taskboard")
	f := writeWakeupFile(t, filepath.Join(w.tmp, "loop-1.jsonl"), wakeupPromptX, isoUTC(w.t0-0.5))
	w.reg("loop-1", f, proj, testBlockS+5, 99999)
	pinContentClock(t, &w.gateEnv, "loop-1", w.t0-(testBlockS+5))
	w.store.SaveHandoff("loop-1", "cc", proj, "t", isoUTC(w.t0), "fresh", "md")
	r := w.d.Gate(gateBody4("loop-1", f, proj, wakeupPromptX))
	assertStr(t, r, "decision", "allow")
	assertStr(t, r, "reason", "loop-wakeup")
	if got := w.store.PopPendingPrompt("loop-1", ""); got != "" {
		t.Fatalf("唤醒放行不得存待续, got %q", got)
	}
	if w.d.Stats.Blocks != 0 || w.d.Stats.Bypass != 0 {
		t.Fatalf("唤醒放行不入账: Blocks=%d Bypass=%d", w.d.Stats.Blocks, w.d.Stats.Bypass)
	}
	if n := acctRowsFor(w, "loop-1", "block", "bypass"); n != 0 {
		t.Fatalf("唤醒放行不入账, rows = %d", n)
	}
	if log := gateLogText(t, &w.gateEnv); !strings.Contains(log, "[exempt] cc sid=loop-1 reason=loop-wakeup") {
		t.Fatalf("gate.log 应记 [exempt] 行: %q", log)
	}
}

func TestGateLoopWakeupProtectsPendingChoice(t *testing.T) {
	// de9af594 全链复刻：同一凉会话，用户 08:09:29.0 在 AskUserQuestion 作答
	// 被拦（选择落待续）→ 08:09:29.6 /loop 唤醒到达。唤醒照拦（护住
	// "被拦→/clear 接力"流程），但绝不把唤醒文本存成待续、不覆盖已存选择。
	e := newGateEnv(t)
	proj := filepath.Join(e.tmp, "agent-taskboard")
	f := writeWakeupFile(t, filepath.Join(e.tmp, "de9af594.jsonl"), wakeupPromptX, isoUTC(e.t0-0.5))
	e.reg("de9af594", f, proj, testBlockS+5, 99999)
	pinContentClock(t, e, "de9af594", e.t0-(testBlockS+5))
	e.store.SaveHandoff("de9af594", "cc", proj, "t", isoUTC(e.t0), "fresh", "md")

	// ① 作答被拦：选择落待续
	r1 := e.d.Gate(answerBody("de9af594", f, proj, testChoice))
	assertStr(t, r1, "decision", "block")
	if got := e.store.PopPendingPrompt("de9af594", ""); got != testChoice {
		t.Fatalf("作答应存选择待续, got %q", got)
	}

	// ② 0.6s 后 /loop 唤醒（打字入口）：照拦，但待续仍是用户选择
	e.advance(0.6)
	r2 := e.d.Gate(gateBody4("de9af594", f, proj, wakeupPromptX))
	assertStr(t, r2, "decision", "block")
	if v, _ := r2["suppressOriginalPrompt"].(bool); !v {
		t.Fatal("打字入口拦截应带 suppressOriginalPrompt")
	}
	reason, _ := r2["reason"].(string)
	for _, want := range []string{"定时唤醒", "已保存", testChoice, "/clear"} {
		if !strings.Contains(reason, want) {
			t.Fatalf("拦截文案缺 %q: %q", want, reason)
		}
	}
	p, ok := e.store.PopPendingPromptFull("de9af594", "")
	if !ok || p.Prompt != testChoice || p.Entry != "answer" || p.PriorChoice != "" {
		t.Fatalf("待续必须原样是用户选择（唤醒文本不得覆盖/携带）: %+v ok=%v", p, ok)
	}
	if e.d.Stats.Blocks != 2 { // ① 作答拦截 + ② 唤醒拦截（照拦＝可数）
		t.Fatalf("Blocks = %d, want 2", e.d.Stats.Blocks)
	}
	if log := gateLogText(t, e); !strings.Contains(log, "mode=loop-wakeup-block") {
		t.Fatalf("gate.log 应记 loop-wakeup-block 行: %q", log)
	}
}

func TestGateLoopWakeupNotAppliedToAnswerEntry(t *testing.T) {
	// 唤醒豁免仅打字入口：作答入口即便转录里有匹配的 fire 记录也走正常作答
	// 闸门（分支 5 拦截 + 待续＝选择）。
	e := newGateEnv(t)
	proj := filepath.Join(e.tmp, "proj")
	f := writeWakeupFile(t, filepath.Join(e.tmp, "ans-wake.jsonl"), wakeupPromptX, isoUTC(e.t0-0.5))
	e.reg("ans-wx", f, proj, testBlockS+5, 99999)
	pinContentClock(t, e, "ans-wx", e.t0-(testBlockS+5))
	e.store.SaveHandoff("ans-wx", "cc", proj, "t", isoUTC(e.t0), "fresh", "md")
	r := e.d.Gate(answerBody("ans-wx", f, proj, wakeupPromptX))
	assertStr(t, r, "decision", "block")
	reason, _ := r["reason"].(string)
	if !strings.Contains(reason, cctrans.AnswerGateBlockMarker) {
		t.Fatalf("应走作答闸门文案（带拦截标记）: %q", reason)
	}
	if strings.Contains(gateLogText(t, e), "[exempt]") {
		t.Fatal("作答入口不得触发唤醒豁免")
	}
	if got := e.store.PopPendingPrompt("ans-wx", ""); got != wakeupPromptX {
		t.Fatalf("作答待续 = %q", got)
	}
}

func TestGateLoopWakeupActiveSessionUntouched(t *testing.T) {
	// 活跃会话（远未进拦窗）不进唤醒判定：正常放行（无 reason 键），零豁免
	// 痕迹——也钉住性能护栏（活跃会话不读转录）。
	e := newGateEnv(t)
	proj := filepath.Join(e.tmp, "proj")
	f := writeWakeupFile(t, filepath.Join(e.tmp, "hot-wake.jsonl"), wakeupPromptX, isoUTC(e.t0-0.5))
	e.reg("hot-wx", f, proj, 5, 99999) // idle 5s < summarize_s
	r := e.d.Gate(gateBody4("hot-wx", f, proj, wakeupPromptX))
	assertStr(t, r, "decision", "allow")
	if _, has := r["reason"]; has {
		t.Fatalf("活跃会话应走正常放行（无 reason）: %v", r)
	}
	if strings.Contains(gateLogText(t, e), "[exempt]") {
		t.Fatal("活跃会话不应留豁免痕迹")
	}
}

// ---- C) 作答→打字护选择的归还文案 ----

func TestRestoreInjectsPriorChoiceBeforePending(t *testing.T) {
	// 锚定归还带交接：旧选择行在打字待续行之前，两行都在。
	e := newGateEnv(t)
	saveCand(t, e, "src", "标题t", 100,
		"HEAD\n<<<INJECT>>>\n交接正文X\n<<</INJECT>>>\nTAIL")
	e.store.SavePendingPromptForEntry("cc", "C:/proj", "src", testChoice, "answer")
	e.store.SavePendingPromptForEntry("cc", "C:/proj", "src", "唤醒后的打字", "")
	r := e.d.Restore("cc", "C:/proj", "newsid")
	ctx, _ := r["context"].(string)
	iPrior := strings.Index(ctx, "\n\n用户被拦前的选择（待续）："+testChoice)
	iPending := strings.Index(ctx, "\n\n用户被拦时的原话（待续 prompt）：唤醒后的打字")
	if iPrior < 0 || iPending < 0 {
		t.Fatalf("应同时含旧选择行与待续原话行: %q", ctx)
	}
	if iPrior > iPending {
		t.Fatalf("旧选择行应在待续原话行之前: %q", ctx)
	}
}

func TestRestoreAnchoredNoHandoffAlsoCarriesPriorChoice(t *testing.T) {
	// "锚会话没有交接"分支同样带上旧选择行。
	e := newGateEnv(t)
	saveCand(t, e, "sibling-src", "别的线程", 100, "md-sibling")
	e.store.SavePendingPromptForEntry("cc", "C:/proj", "no-h", testChoice, "answer")
	e.store.SavePendingPromptForEntry("cc", "C:/proj", "no-h", "打字待续", "")
	r := e.d.Restore("cc", "C:/proj", "fresh")
	ctx, _ := r["context"].(string)
	if !strings.Contains(ctx, "用户被拦前的选择（待续）："+testChoice) {
		t.Fatalf("无交接分支应带旧选择行: %q", ctx)
	}
	if !strings.Contains(ctx, "「打字待续」") {
		t.Fatalf("无交接分支应带打字待续: %q", ctx)
	}
}
