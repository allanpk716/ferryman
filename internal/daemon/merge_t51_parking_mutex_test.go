package daemon

// T51×T48 合并专属回归：两窗互斥移植进停车状态机后的双向互斥＋死线口径。
// （规格：tests/test_merge_t51_parking_mutex.py 4 例 1:1）
//
// 合并语义锚定（本文件钉住 merge 时手工移植的三处，机械取侧必丢）：
//   - 互斥复验移植进 server.subagent 停车状态机的首开与重锚共用点——新窗落表
//     在 windowsMu 外层 → ledgerMu 内层复验等答复窗之后（锁序与既有路径一致）；
//   - parking_open 是无副作用探针：委托 T48 窗口状态（活跃窗按泄漏口径、
//     停车窗按停表过期口径 PARK_EXPIRE_S 判——与 window_wait 豁免口径一致但
//     不做懒过期闭账记账）；
//   - _maybe_enqueue 三道推迟并存：等答复窗推迟＋悬空推迟＋window_wait 推迟；
//     死线到线只跳过悬空让步，不跳过 window_wait（保守——两窗互斥成立时
//     "死线遇停车窗"本不可达，此为防御性口径）。
//
// 票13 归属拆分：方向一/探针口径两例纯 daemon 面，票13 转绿；方向二/死线
// 口径两例的驱动方是 Watcher._maybe_qwatch / _maybe_enqueue，票16 Watcher
// 装配后转绿（见下方实现）。

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"ferryman/internal/accounts"
	"ferryman/internal/clock"
	"ferryman/internal/config"
	"ferryman/internal/ledger"
)

var mergeSurge = func() string {
	lines := make([]string, 0, 8)
	for i := 1; i <= 8; i++ {
		lines = append(lines, fmt.Sprintf("❓ **Q%d** - **标题%d**：这个问题该怎么答？", i, i))
	}
	return strings.Join(lines, "\n")
}()

func mergeLine(d map[string]any) string {
	b, err := json.Marshal(d)
	if err != nil {
		panic(err)
	}
	return string(b)
}

func mergeAssistant(text string, tools [][2]string, mid string) map[string]any {
	blocks := []any{map[string]any{"type": "text", "text": text}}
	for _, tn := range tools {
		blocks = append(blocks, map[string]any{"type": "tool_use", "id": tn[0],
			"name": tn[1], "input": map[string]any{}})
	}
	return map[string]any{"type": "assistant", "timestamp": "2026-09-18T12:00:00.000Z",
		"message": map[string]any{"id": mid, "role": "assistant", "content": blocks,
			"usage": map[string]any{"input_tokens": 2000,
				"cache_read_input_tokens":     100,
				"cache_creation_input_tokens": 0,
				"output_tokens":               5}}}
}

func writeMergeTranscript(t *testing.T, projects, sid, text string, tools [][2]string) string {
	t.Helper()
	dir := filepath.Join(projects, "C--proj")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	f := filepath.Join(dir, sid+".jsonl")
	lines := []string{mergeLine(map[string]any{"type": "user",
		"timestamp": "2026-09-18T12:00:00.000Z",
		"message":   map[string]any{"role": "user", "content": "答你的问题"}}),
		mergeLine(mergeAssistant(text, tools, "msg_1"))}
	if err := os.WriteFile(f, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return f
}

// mergeCfg 对应 Python _qw_cfg：死线 = block_s − lead = 60s。
func mergeCfg() *config.Config {
	cfg := config.Default()
	cfg.Thresholds = config.ThresholdCfg{SummarizeS: 10, BlockS: 100, MinCtxTokens: 1000}
	cfg.QuestionWatch.Mode = "observe"
	cfg.QuestionWatch.FerryDeadlineLeadS = 40
	return cfg
}

func mergeSession(t *testing.T, led *ledger.Ledger, f, sid string) *ledger.SessionState {
	t.Helper()
	st := led.Touch("cc", sid, f, clock.Now(), 42, 0)
	if !st.ObservedActive {
		t.Fatal("observed_active 应为 true")
	}
	return st
}

func fp(v float64) *float64 { return &v }

// ---------- 方向一：等答复窗开着 → 停车窗不可开（首开与重锚两点） ----------

func TestMergeQwatchOpenBlocksFirstOpenAndReanchor(t *testing.T) {
	// 等答复窗开着时 /subagent start 不落停车窗：首开挡住；重锚照常如实闭账
	// 旧停车窗（记 expired、无未来时刻）但不落新窗；写入关窗后 start 照常开。
	tmp := t.TempDir()
	led := ledger.New()
	accts, err := accounts.New(filepath.Join(tmp, "data"))
	if err != nil {
		t.Fatal(err)
	}
	d := NewDaemon(mergeCfg(), led, nil, func(*ledger.SessionState) bool { return true }, accts, 0, nil)
	projects := filepath.Join(tmp, "projects")
	f := writeMergeTranscript(t, projects, "mmx-1", mergeSurge, nil)
	st := mergeSession(t, led, f, "mmx-1")
	qw := clock.Now()
	st.QWatchOpenedTS = &qw
	key := [2]string{"cc", "mmx-1"}
	sub := func() {
		t.Helper()
		if _, err := d.Subagent(map[string]any{"event": "start", "agent": "cc", "session_id": "mmx-1"}); err != nil {
			t.Fatal(err)
		}
	}

	// 首开被挡：无停车窗行，但台账计数照常（豁免道仍由计数兜底）
	sub()
	if _, ok := d.windows[key]; ok {
		t.Fatal("等答复窗开着时不应落停车窗")
	}
	if !led.SubagentActive("cc", "mmx-1") {
		t.Fatal("台账计数应照常")
	}

	// 重锚点被挡：塞一条已泄漏的旧停车窗，再来 start——旧窗如实闭账记
	// expired，新窗仍不落（互斥优先于记账完整性，与分支语义一致）
	d.windows[key] = &waitWindow{
		OpenedTS: clock.Now() - (ledger.SubagentEventLeakS + 600),
		StopTS:   fp(clock.Now() - (ledger.SubagentEventLeakS + 500)),
		SawAsync: true}
	t0 := clock.Now()
	sub()
	if _, ok := d.windows[key]; ok {
		t.Fatal("互斥优先：新窗仍不落")
	}
	rowsAccts := accts.Read(accounts.ReadOpts{Kind: "window", Session: "mmx-1"})
	if len(rowsAccts) != 1 || rowsAccts[0]["close_reason"] != "expired" {
		t.Fatalf("window 行 = %v, want 1 条 expired", rowsAccts)
	}
	if rowsAccts[0]["closed_ts"].(float64) > t0+5 { // 封顶 now，无未来时刻
		t.Fatalf("closed_ts = %v, want <= t0+5", rowsAccts[0]["closed_ts"])
	}

	// 写入关等答复窗后：start 照常开停车窗（互斥解除自愈）
	raw, err := os.ReadFile(f)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f, raw, 0o644); err != nil { // 触发新写入口径
		t.Fatal(err)
	}
	info, err := os.Stat(f)
	if err != nil {
		t.Fatal(err)
	}
	led.Touch("cc", "mmx-1", f, clock.Now(), int(info.Size()), 0)
	if st.QWatchOpenedTS != nil {
		t.Fatal("新写入应关等答复窗")
	}
	sub()
	if _, ok := d.windows[key]; !ok {
		t.Fatal("互斥解除后 start 应照常开停车窗")
	}
}

// ---------- 方向二：停车窗开着 → 等答复窗不可开（真探针，非替身） ----------

func TestMergeParkedWindowBlocksQwatchOpen(t *testing.T) {
	// test_merge_t51_parking_mutex.py::test_merge_parked_window_blocks_qwatch_open
	// 1:1：真 Daemon 停车窗（停表未过期）→ 守望不开等答复窗；停车窗闭后
	// 同写入版本仍可开（瞬态阻塞不盖版本章）。
	tmp := t.TempDir()
	projects := filepath.Join(tmp, "projects")
	led := ledger.New()
	d := NewDaemon(mergeCfg(), led, nil, func(*ledger.SessionState) bool { return true }, nil, 0, nil)
	w := newTestWatcherW(mergeCfg(), led, nil, d, nil, nil)
	f := writeMergeTranscript(t, projects, "mmx-2", mergeSurge, nil)
	st := mergeSession(t, led, f, "mmx-2")
	key := winKey{"cc", "mmx-2"}
	d.windows[key] = &waitWindow{OpenedTS: clock.Now() - 120,
		StopTS: fp(clock.Now() - 10), SawAsync: true}
	w.maybeQwatch(st)
	if qwRead(led, st).opened != nil { // 先开者赢
		t.Fatal("停车窗开着不应开等答复窗")
	}

	d.NoteUsage("cc", "mmx-2", clock.Now()+200) // 主会话恢复 → 停车窗闭
	if d.ParkingOpen("cc", "mmx-2") {
		t.Fatal("停车窗应已闭")
	}
	w.maybeQwatch(st)
	if qwRead(led, st).opened == nil { // 解除后同版本仍可开
		t.Fatal("解除后同版本仍应开窗")
	}
}

// ---------- 探针口径：委托 T48 停车状态、无副作用 ----------

func TestMergeParkingProbeDelegatesToParkingState(t *testing.T) {
	// 探针按窗口状态分口径：活跃窗按泄漏口径；停车窗按停表过期口径——
	// opened_ts 早已过泄漏期但停表新鲜的停车窗仍算开（合并语义，旧探针漏判）；
	// 过期停车窗视同已闭且**不产生闭账记账**（window_wait 才有懒过期副作用）。
	tmp := t.TempDir()
	led := ledger.New()
	accts, err := accounts.New(filepath.Join(tmp, "data"))
	if err != nil {
		t.Fatal(err)
	}
	d := NewDaemon(mergeCfg(), led, nil, func(*ledger.SessionState) bool { return true }, accts, 0, nil)
	key := [2]string{"cc", "mmx-3"}

	d.windows[key] = &waitWindow{OpenedTS: clock.Now() - 120}
	if !d.ParkingOpen("cc", "mmx-3") {
		t.Fatal("活跃窗应算开")
	}

	d.windows[key] = &waitWindow{
		OpenedTS: clock.Now() - (ledger.SubagentEventLeakS + 300),
		StopTS:   fp(clock.Now() - 30),
		SawAsync: true}
	if !d.ParkingOpen("cc", "mmx-3") {
		t.Fatal("停车新鲜应算开（opened_ts 再旧也算开）")
	}

	*d.windows[key].StopTS = clock.Now() - (ParkExpireS + 30)
	if d.ParkingOpen("cc", "mmx-3") {
		t.Fatal("停表过期视同已闭")
	}
	if got := accts.Read(accounts.ReadOpts{Kind: "window"}); len(got) != 0 {
		t.Fatalf("探针零副作用（不闭账），行 = %v", got)
	}
	if d.WindowWait("cc", "mmx-3") {
		t.Fatal("window_wait 才做懒过期")
	}
	rowsAccts := accts.Read(accounts.ReadOpts{Kind: "window"})
	if len(rowsAccts) != 1 || rowsAccts[0]["close_reason"] != "expired" {
		t.Fatalf("副作用应在正规道收口, 行 = %v", rowsAccts)
	}
}

// ---------- 死线口径：只跳悬空让步，不跳 window_wait ----------

func TestMergeDeadlineSkipsDanglingButNotWindowWait(t *testing.T) {
	// test_merge_t51_parking_mutex.py::test_merge_deadline_skips_dangling_but_
	// not_window_wait 1:1：死线到线＋悬空 AskUserQuestion：无停车窗 → 强制入队
	// （跳过悬空让步）；同状态下若停车窗在停（防御性并存态）→ window_wait 仍
	// 推迟（死线不豁免它）。
	tmp := t.TempDir()
	projects := filepath.Join(tmp, "projects")
	led := ledger.New()
	d := NewDaemon(mergeCfg(), led, nil, func(*ledger.SessionState) bool { return true }, nil, 0, nil)
	w := newTestWatcherW(mergeCfg(), led, nil, d, nil, nil)
	var enq []string
	w.Enqueue = func(st *ledger.SessionState) bool {
		enq = append(enq, st.SessionID)
		return true
	}
	f := writeMergeTranscript(t, projects, "mmx-4", mergeSurge,
		[][2]string{{"t1", "AskUserQuestion"}})
	st := mergeSession(t, led, f, "mmx-4")
	now := clock.Now()
	led.Mu().Lock()
	ts := now
	st.QWatchOpenedTS = &ts
	st.LastWrite = now - 60 // 恰到死线（block 100 − lead 40）
	led.Mu().Unlock()
	key := winKey{"cc", "mmx-4"}

	d.windows[key] = &waitWindow{OpenedTS: now - 120,
		StopTS: fp(now - 5), SawAsync: true}
	w.maybeEnqueue(st)
	if len(enq) != 0 { // window_wait 推迟不被死线豁免
		t.Fatalf("防御性并存态应被 window_wait 推迟, enq = %v", enq)
	}

	delete(d.windows, key) // 停车窗撤（防御态解除）
	w.maybeEnqueue(st)
	if !reflect.DeepEqual(enq, []string{"mmx-4"}) { // 死线强制入队，跳过悬空
		t.Fatalf("enq = %v, want [mmx-4]", enq)
	}
	if qwRead(led, st).handedOff <= 0 {
		t.Fatal("入队即记 handed_off")
	}
}
