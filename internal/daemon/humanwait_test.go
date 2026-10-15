package daemon

// humanwait_test.go — #35 等人工具排出悬空道（spec #34「等人工具不推迟摆渡」
// 首提交）。
//
// 等人工具（CONTEXT.md）：AskUserQuestion 选择框、ExitPlanMode 批准计划——
// 悬空＝机器等人，与人不在同论：闲置照走，到总结阈值即摆渡；闸门的机器等
// 机器豁免不再因等人工具悬空成立。悬空集扣除等人工具后仍非空才算"仍有工具
// 在跑"（摆渡入队推迟与闸门豁免共用同一口径，单一定义点）。
//
// 接缝（票面预约定）：守护进程测试架——watcher.maybeEnqueue 断言入队与否、
// Daemon.Gate 断言闸门判定；伪造 CC 转录＋冻结/相对时钟驱动。先例＝T31 悬空
// 推迟入队测试（integration_test.go）与缺口A 闸门悬空豁免测试（gate_test.go）。
// qwatch"悬空集 ⊆ {AskUserQuestion}"开窗判据是另一套口径（仅 AskUserQuestion），
// 行为不变，下方另立防漏测钉边界。

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"ferryman/internal/clock"
	"ferryman/internal/ledger"
)

// writeDanglingTools 缺口A 夹具 mkDangling 的多工具变体：写一条尾部悬空
// tool_use 的 assistant 行（每人工具一个 tool_use 块，无 tool_result）。
func writeDanglingTools(t *testing.T, p string, names ...string) string {
	t.Helper()
	blocks := []any{}
	for i, n := range names {
		blocks = append(blocks, map[string]any{"type": "tool_use",
			"id": fmt.Sprintf("tu_%d", i+1), "name": n, "input": map[string]any{}})
	}
	line, err := json.Marshal(map[string]any{"type": "assistant",
		"message": map[string]any{"content": blocks}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, append(line, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// ---- 摆渡入队：等人工具悬空不推迟 ----

// TestHumanWaitToolDanglingEnqueues 转录尾部仅等人工具悬空（AskUserQuestion /
// ExitPlanMode）、闲置超总结阈值 → 摆渡入队；对照：真工具（长命令）悬空照旧
// 推迟（原行为，T31 语义）。
func TestHumanWaitToolDanglingEnqueues(t *testing.T) {
	tmp := t.TempDir()
	projects := filepath.Join(tmp, "projects")
	led := ledger.New()
	w := newTestWatcherW(mergeCfg(), led, nil, nil, nil, nil)
	var enq []string
	w.Enqueue = func(st *ledger.SessionState) bool {
		enq = append(enq, st.SessionID)
		return true
	}
	fAQ := writeMergeTranscript(t, projects, "hw-aq", "问完了，等你选。",
		[][2]string{{"t1", "AskUserQuestion"}})
	fEPM := writeMergeTranscript(t, projects, "hw-epm", "计划如下，等你批。",
		[][2]string{{"t1", "ExitPlanMode"}})
	fBash := writeMergeTranscript(t, projects, "hw-bash", "长命令跑着。",
		[][2]string{{"t1", "Bash"}})
	for _, tc := range []struct{ sid, path string }{
		{"hw-aq", fAQ}, {"hw-epm", fEPM}, {"hw-bash", fBash},
	} {
		st := mergeSession(t, led, tc.path, tc.sid)
		shiftLastWrite(led, st, -30) // ≥ summarize(10)；无窗 → 死线不涉
		w.maybeEnqueue(st)
	}
	if !reflect.DeepEqual(enq, []string{"hw-aq", "hw-epm"}) {
		t.Fatalf("enq = %v, want [hw-aq hw-epm]（等人工具悬空入队、真工具照旧推迟）", enq)
	}
}

// TestHumanWaitMixedDanglingStillDefers 等人工具与真工具同时悬空 → 仍按
// "在跑"推迟（悬空集扣除等人工具后仍非空）。
func TestHumanWaitMixedDanglingStillDefers(t *testing.T) {
	tmp := t.TempDir()
	projects := filepath.Join(tmp, "projects")
	led := ledger.New()
	w := newTestWatcherW(mergeCfg(), led, nil, nil, nil, nil)
	var enq []string
	w.Enqueue = func(st *ledger.SessionState) bool {
		enq = append(enq, st.SessionID)
		return true
	}
	f := writeMergeTranscript(t, projects, "hw-mix", "问完了，但还有活在跑。",
		[][2]string{{"t1", "AskUserQuestion"}, {"t2", "Bash"}})
	st := mergeSession(t, led, f, "hw-mix")
	shiftLastWrite(led, st, -30)
	w.maybeEnqueue(st)
	if len(enq) != 0 {
		t.Fatalf("混合悬空应推迟, enq = %v", enq)
	}
}

// ---- 闸门：等人工具悬空不豁免机器等机器 ----

// TestHumanWaitDanglingGateBlocks 等人工具悬空、闲置超拦截阈值、有有效交接
// → 闸门判拦截（不再以机器等机器豁免）；对照：真工具悬空照旧豁免放行
// （缺口A 语义零回归）。
func TestHumanWaitDanglingGateBlocks(t *testing.T) {
	e := newGateEnv(t)
	proj := filepath.Join(e.tmp, "proj")
	e.store.SaveHandoff("hw-gate", "cc", proj, "交接", isoUTC(e.t0), "fresh", "md")
	for _, tc := range []struct{ sid, tool string }{
		{"hw-g-aq", "AskUserQuestion"}, {"hw-g-epm", "ExitPlanMode"},
	} {
		f := writeDanglingTools(t, filepath.Join(e.tmp, tc.sid+".jsonl"), tc.tool)
		e.reg(tc.sid, f, proj, testBlockS+5, 99999)
		r := e.d.Gate(gateBody(tc.sid, f, proj))
		if r["decision"] != "block" {
			t.Fatalf("%s: decision = %v, want block（等人工具悬空不豁免）", tc.sid, r["decision"])
		}
		if _, ok := r["handoff_path"]; !ok {
			t.Fatalf("%s: block 应带 handoff_path: %v", tc.sid, r)
		}
		if !strings.Contains(pyStr(r["reason"]), "交接") {
			t.Fatalf("%s: reason 缺交接指引: %v", tc.sid, r["reason"])
		}
	}
	fBash := writeDanglingTools(t, filepath.Join(e.tmp, "hw-g-bash.jsonl"), "Bash")
	e.reg("hw-g-bash", fBash, proj, testBlockS+5, 99999)
	r := e.d.Gate(gateBody("hw-g-bash", fBash, proj))
	if r["decision"] != "allow" || r["reason"] != "machine-waiting" {
		t.Fatalf("真工具悬空豁免零回归: %v", r)
	}
}

// ---- 问询守望：等答复窗口语义不变 ----

// TestHumanWaitQWatchWindowDeadlineUnchanged 等答复窗口开着时死线前仍推迟、
// 死线后强制入队（原行为；靶场景＝提问潮＋AskUserQuestion 悬空）。
func TestHumanWaitQWatchWindowDeadlineUnchanged(t *testing.T) {
	tmp := t.TempDir()
	projects := filepath.Join(tmp, "projects")
	led := ledger.New()
	w := newTestWatcherW(mergeCfg(), led, nil, nil, nil, nil) // 死线 = 100−40 = 60s
	var enq []string
	w.Enqueue = func(st *ledger.SessionState) bool {
		enq = append(enq, st.SessionID)
		return true
	}
	f := writeMergeTranscript(t, projects, "hw-win", mergeSurge,
		[][2]string{{"t1", "AskUserQuestion"}})
	st := mergeSession(t, led, f, "hw-win")
	w.maybeQwatch(st)
	if qwRead(led, st).opened == nil {
		t.Fatal("前提：提问潮＋AQ 悬空应开窗")
	}
	shiftLastWrite(led, st, -30) // ≥ summarize(10)、< 死线(60)
	w.maybeEnqueue(st)
	if len(enq) != 0 {
		t.Fatalf("死线前应推迟, enq = %v", enq)
	}
	setLastWrite(led, st, clock.Now()-70) // ≥ 死线(60)
	w.maybeEnqueue(st)
	if !reflect.DeepEqual(enq, []string{"hw-win"}) {
		t.Fatalf("死线后应强制入队, enq = %v", enq)
	}
}

// TestHumanWaitExitPlanModeNoQWatchWindow 问询守望开窗判据"悬空集 ⊆
// {AskUserQuestion}"行为不变：ExitPlanMode 虽属等人工具（排出悬空道），但不
// 入问询守望靶集——悬空含它 → 不开窗。
func TestHumanWaitExitPlanModeNoQWatchWindow(t *testing.T) {
	tmp := t.TempDir()
	projects := filepath.Join(tmp, "projects")
	led := ledger.New()
	w := newTestWatcherW(mergeCfg(), led, nil, nil, nil, nil)
	f := writeMergeTranscript(t, projects, "hw-epm-nw", mergeSurge,
		[][2]string{{"t1", "ExitPlanMode"}})
	st := mergeSession(t, led, f, "hw-epm-nw")
	w.maybeQwatch(st)
	if qwRead(led, st).opened != nil {
		t.Fatal("ExitPlanMode 不属问询守望靶集，不应开窗")
	}
}
