package daemon

// answer_gate_test.go — #36 作答闸门（spec #34 第二提交，ADR-0028）：
// AskUserQuestion 作答（PostToolUse 入口，entry=answer）与打字入口共用同一份
// 台账判定（拦截阈值、有效交接、待交接标记、判热、三态、fail-open），差异只在：
// 强续前缀不适用作答；拦截待续 prompt＝"问题→所选答案"；拦截文案「原话」换
// 「你的选择」并带稳定 Ferryman 拦截标记（#37 回写识别锚）；记账/通知带入口
// 标识。接缝 A（票面约定）：冻结时钟 + 伪造转录直调 Daemon.Gate / Restore。
//
// 转录夹具按 2026-10-15 本机实测（notes/auq-blocked-transcript.jsonl）：
// PostToolUse 触发时 tool_result 尚未落盘 → 尾部悬空 AskUserQuestion tool_use
// （#35 起等人工具悬空不豁免闸门——作答闸门的真实形态）。

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ferryman/internal/accounts"
	"ferryman/internal/beat"
	"ferryman/internal/cctrans"
	"ferryman/internal/config"
)

// answerBody 钩子 PostToolUse(AskUserQuestion) 分支发给闸门端点的请求体
//（prompt＝格式化后的"问题→所选答案"）。
func answerBody(sid, path, cwd, choice string) map[string]any {
	return map[string]any{"agent": "cc", "session_id": sid, "transcript_path": path,
		"cwd": cwd, "prompt": choice, "entry": GateEntryAnswer}
}

const testChoice = "午饭吃面还是吃饭?→面"

// TestAnswerGateBlockStoresChoiceAndCarriesMarker 凉会话＋有效交接＋enforce：
// 作答入口判拦截；待续 prompt 落库为"问题→所选答案"；文案同打字结构但
// 「原话」换「你的选择」，含闲置时长/缓存已失效/开新会话/强续/交接路径，
// 并带稳定拦截标记（#37 回写识别锚）。
func TestAnswerGateBlockStoresChoiceAndCarriesMarker(t *testing.T) {
	e := newGateEnv(t)
	proj := filepath.Join(e.tmp, "proj")
	e.reg("ans-1", filepath.Join(e.tmp, "ans-1.jsonl"), proj, testBlockS+5, 99999)
	en := e.store.SaveHandoff("ans-1", "cc", proj, "t", isoUTC(e.t0), "fresh", "md")
	r := e.d.Gate(answerBody("ans-1", "C:/ans-1.jsonl", proj, testChoice))
	if r["decision"] != "block" {
		t.Fatalf("decision = %v, want block（凉会话+有效交接+enforce）", r["decision"])
	}
	assertStr(t, r, "handoff_path", en.Path)
	reason, _ := r["reason"].(string)
	if !strings.Contains(reason, cctrans.AnswerGateBlockMarker) {
		t.Fatalf("reason 缺拦截标记 %q: %q", cctrans.AnswerGateBlockMarker, reason)
	}
	for _, want := range []string{"你的选择已保存", "分钟", "缓存已失效", "/clear", "强续"} {
		if !strings.Contains(reason, want) {
			t.Fatalf("reason 缺 %q: %q", want, reason)
		}
	}
	if !strings.Contains(reason, en.Path) {
		t.Fatalf("reason 缺交接路径: %q", reason)
	}
	if strings.Contains(reason, "原话") {
		t.Fatalf("作答拦截文案不得再带「原话」: %q", reason)
	}
	if got := e.store.PopPendingPrompt("ans-1", ""); got != testChoice {
		t.Fatalf("待续 prompt = %q, want %q", got, testChoice)
	}
	if handoffs := indexHandoffs(t, e.tmp); len(handoffs) == 0 || handoffs[0]["blocked_at"] == nil {
		t.Fatalf("block 事件应入 index（交接标记被拦）: %v", handoffs)
	}
}

// TestAnswerGateDanglingAskUserQuestionStillBlocks 真实形态夹具：PostToolUse
// 触发时 tool_result 未落盘（实测 result_on_disk=0）→ 尾部仅悬空
// AskUserQuestion tool_use；#35 等人工具悬空不豁免 → 照判拦截。
func TestAnswerGateDanglingAskUserQuestionStillBlocks(t *testing.T) {
	e := newGateEnv(t)
	proj := filepath.Join(e.tmp, "proj")
	f := writeDanglingTools(t, filepath.Join(e.tmp, "ans-dangling.jsonl"), "AskUserQuestion")
	e.reg("ans-dangling", f, proj, testBlockS+5, 99999)
	e.store.SaveHandoff("ans-dangling", "cc", proj, "t", isoUTC(e.t0), "fresh", "md")
	r := e.d.Gate(answerBody("ans-dangling", f, proj, testChoice))
	if r["decision"] != "block" {
		t.Fatalf("decision = %v, want block（等人工具悬空不豁免）", r["decision"])
	}
}

// TestAnswerGateAnchoredRestoreCarriesChoice 锚窗内同目录新会话归还带出交接
// 与该选择；锚领走即消费（第二次归还零注入终态）。
func TestAnswerGateAnchoredRestoreCarriesChoice(t *testing.T) {
	e := newGateEnv(t)
	proj := filepath.Join(e.tmp, "proj")
	f := filepath.Join(e.tmp, "ans-2.jsonl")
	e.reg("ans-2", f, proj, testBlockS+5, 99999)
	e.store.SaveHandoff("ans-2", "cc", proj, "交接", isoUTC(e.t0), "fresh", "md")
	if r := e.d.Gate(answerBody("ans-2", f, proj, testChoice)); r["decision"] != "block" {
		t.Fatalf("前置：作答应被拦: %v", r)
	}
	r := e.d.Restore("cc", proj, "fresh-session")
	ctx, _ := r["context"].(string)
	if ctx == "" {
		t.Fatalf("锚窗内归还应注入交接: %v", r)
	}
	if !strings.Contains(ctx, "交接") || !strings.Contains(ctx, testChoice) {
		t.Fatalf("注入缺交接或选择: %q", ctx)
	}
	if _, ok := e.store.LatestPendingFor("cc", proj); ok {
		t.Fatal("锚领走即消费，待续不得残留")
	}
	assertZeroInjectTerminal(t, e.d.Restore("cc", proj, "fresh-2"))
}

// TestAnswerGateHotCacheAllows 缓存仍热（判热必活带）→ 放行；不落待续 prompt。
func TestAnswerGateHotCacheAllows(t *testing.T) {
	e := newGateEnv(t)
	e.d.Cfg.Heartbeat.TTLS = 1800 // 必活带 = 0.8·1800 = 1440s
	e.d.HeatClock = beat.NewLastRequestClock()
	proj := filepath.Join(e.tmp, "proj")
	e.reg("ans-hot", filepath.Join(e.tmp, "ans-hot.jsonl"), proj, 2400, 99999)
	e.store.SaveHandoff("ans-hot", "cc", proj, "t", isoUTC(e.t0), "fresh", "md")
	e.d.HeatClock.Note("ans-hot", e.t0-600) // 10min 前真发 → 热
	r := e.d.Gate(answerBody("ans-hot", "C:/ans-hot.jsonl", proj, testChoice))
	if r["decision"] != "allow" {
		t.Fatalf("decision = %v, want allow（判热）", r["decision"])
	}
	if ctx, _ := r["additional_context"].(string); !strings.Contains(ctx, "缓存仍热") {
		t.Fatalf("判热放行应带死线提示: %v", r)
	}
	if _, ok := e.store.LatestPendingFor("cc", proj); ok {
		t.Fatal("判热放行不得落待续 prompt")
	}
}

// TestAnswerGateWarnOnceThenBlocks 无有效交接 → 警告放行一次并置待交接标记，
// 其后再提交即拦（分支 6 兜底拦截，作答变体文案＋选择落库）。
func TestAnswerGateWarnOnceThenBlocks(t *testing.T) {
	e := newGateEnv(t)
	proj := filepath.Join(e.tmp, "proj")
	f := filepath.Join(e.tmp, "ans-3.jsonl")
	e.reg("ans-3", f, proj, testBlockS+5, 99999)
	r1 := e.d.Gate(answerBody("ans-3", f, proj, testChoice))
	if r1["decision"] != "allow" {
		t.Fatalf("首次（无交接）应警告放行: %v", r1)
	}
	if ctx, _ := r1["additional_context"].(string); ctx == "" {
		t.Fatal("首次放行应带警告提示")
	}
	r2 := e.d.Gate(answerBody("ans-3", f, proj, testChoice))
	if r2["decision"] != "block" {
		t.Fatalf("其后提交应拦（待交接标记）: %v", r2)
	}
	reason, _ := r2["reason"].(string)
	if !strings.Contains(reason, cctrans.AnswerGateBlockMarker) || !strings.Contains(reason, "你的选择已保存") {
		t.Fatalf("分支 6 作答文案缺标记/选择保管: %q", reason)
	}
	if !strings.Contains(reason, "第 1 次") {
		t.Fatalf("分支 6 文案缺拦截计数: %q", reason)
	}
	if got := e.store.PopPendingPrompt("ans-3", ""); got != testChoice {
		t.Fatalf("分支 6 待续 prompt = %q, want %q", got, testChoice)
	}
}

// TestAnswerGateModeOffAndObserve off 不拦；observe 只提醒放行（不落待续）。
func TestAnswerGateModeOffAndObserve(t *testing.T) {
	proj := filepath.Join("C:", "proj-off")
	t.Run("off", func(t *testing.T) {
		e := newGateEnv(t)
		e.d.Cfg.GateCC = "off"
		e.reg("ans-off", "C:/ans-off.jsonl", proj, 9999, 99999)
		r := e.d.Gate(answerBody("ans-off", "C:/ans-off.jsonl", proj, testChoice))
		if r["decision"] != "allow" || r["reason"] != "mode-off" {
			t.Fatalf("off 应放行: %v", r)
		}
	})
	t.Run("observe", func(t *testing.T) {
		e := newGateEnv(t)
		e.d.Cfg.GateCC = "observe"
		proj := filepath.Join(e.tmp, "proj-obs")
		e.reg("ans-obs", "C:/ans-obs.jsonl", proj, testBlockS+5, 99999)
		r := e.d.Gate(answerBody("ans-obs", "C:/ans-obs.jsonl", proj, testChoice))
		if r["decision"] != "allow" {
			t.Fatalf("observe 只放行: %v", r)
		}
		ctx, _ := r["additional_context"].(string)
		if !strings.Contains(ctx, "只提醒不拦") {
			t.Fatalf("observe 应带提醒: %q", ctx)
		}
		if _, ok := e.store.LatestPendingFor("cc", proj); ok {
			t.Fatal("observe 放行不得落待续 prompt")
		}
	})
}

// TestAnswerGateQiangxuPrefixNotBypass 强续前缀不适用于作答入口：答案文本以
// 「强续」开头也不走 bypass（仍按台账判定拦截），bypass 计数不动。
func TestAnswerGateQiangxuPrefixNotBypass(t *testing.T) {
	e := newGateEnv(t)
	proj := filepath.Join(e.tmp, "proj")
	e.reg("ans-qx", "C:/ans-qx.jsonl", proj, testBlockS+5, 99999)
	e.store.SaveHandoff("ans-qx", "cc", proj, "t", isoUTC(e.t0), "fresh", "md")
	r := e.d.Gate(answerBody("ans-qx", "C:/ans-qx.jsonl", proj, "强续"+testChoice))
	if r["decision"] != "block" {
		t.Fatalf("decision = %v, want block（强续前缀不作答入口的 bypass）", r["decision"])
	}
	if e.d.Stats.Bypass != 0 {
		t.Fatalf("bypass = %d, want 0", e.d.Stats.Bypass)
	}
}

// TestAnswerGateBlockAccountRowCarriesEntry 拦截按既有闸门科目记账并带入口
// 标识：作答 block 行 entry=answer；打字 block 行不带 entry。
func TestAnswerGateBlockAccountRowCarriesEntry(t *testing.T) {
	w := newWenv(t)
	proj := filepath.Join(w.tmp, "proj")
	covers := isoUTC(w.t0)
	w.writeSession(t, "acc-ans", nil, testBlockS+5)
	w.store.SaveHandoff("acc-ans", "cc", proj, "t", covers, "fresh", "md")
	w.writeSession(t, "acc-typ", nil, testBlockS+5)
	w.store.SaveHandoff("acc-typ", "cc", proj, "t2", covers, "fresh", "md")
	if r := w.d.Gate(answerBody("acc-ans", filepath.Join(w.tmp, "acc-ans.jsonl"), proj, testChoice)); r["decision"] != "block" {
		t.Fatalf("作答前置失败: %v", r)
	}
	if r := w.d.Gate(gateBody("acc-typ", filepath.Join(w.tmp, "acc-typ.jsonl"), proj)); r["decision"] != "block" {
		t.Fatalf("打字前置失败: %v", r)
	}
	rows := w.acc.Read(accounts.ReadOpts{Kind: "block"})
	var ansRow, typRow map[string]any
	for _, row := range rows {
		switch row["session_id"] {
		case "acc-ans":
			ansRow = row
		case "acc-typ":
			typRow = row
		}
	}
	if ansRow == nil || typRow == nil {
		t.Fatalf("block 行不齐: ans=%v typ=%v", ansRow != nil, typRow != nil)
	}
	if got, _ := ansRow["entry"].(string); got != string(GateEntryAnswer) {
		t.Fatalf("作答 block 行 entry = %v, want %q", ansRow["entry"], GateEntryAnswer)
	}
	if _, has := typRow["entry"]; has {
		t.Fatalf("打字 block 行不应带 entry（零漂移）: %v", typRow)
	}
}

// TestAnswerGateBlockNotifyCarriesEntry 拦截通知按既有 block 事件分级并带
// 入口标识：作答 entry=answer、打字 entry=""（seam 录制）。
func TestAnswerGateBlockNotifyCarriesEntry(t *testing.T) {
	e := newGateEnv(t)
	proj := filepath.Join(e.tmp, "proj")
	covers := isoUTC(e.t0)
	fired := make(chan string, 2)
	e.d.NotifyBlock = func(_, _, _, _, _, entry string, _ *config.Config) {
		fired <- entry
	}
	e.reg("ntf-ans", "C:/ntf-ans.jsonl", proj, testBlockS+5, 99999)
	e.store.SaveHandoff("ntf-ans", "cc", proj, "t", covers, "fresh", "md")
	if r := e.d.Gate(answerBody("ntf-ans", "C:/ntf-ans.jsonl", proj, testChoice)); r["decision"] != "block" {
		t.Fatalf("作答前置失败: %v", r)
	}
	e.reg("ntf-typ", "C:/ntf-typ.jsonl", proj, testBlockS+5, 99999)
	e.store.SaveHandoff("ntf-typ", "cc", proj, "t2", covers, "fresh", "md")
	if r := e.d.Gate(gateBody("ntf-typ", "C:/ntf-typ.jsonl", proj)); r["decision"] != "block" {
		t.Fatalf("打字前置失败: %v", r)
	}
	seen := map[string]bool{}
	for i := 0; i < 2; i++ {
		select {
		case entry := <-fired:
			seen[entry] = true
		case <-time.After(5 * time.Second):
			t.Fatal("异步通知未触发")
		}
	}
	if !seen[string(GateEntryAnswer)] || !seen[""] {
		t.Fatalf("通知入口标识不齐: %v（want answer 与 空）", seen)
	}
}
