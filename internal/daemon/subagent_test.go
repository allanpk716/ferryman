package daemon

// 规格：tests/test_subagent.py 10 例的票13 落点。
//
// 归属拆分（316 清点闭环）：
//   - 台账级 3 例（count_lifecycle / nested_two_levels / leak_guard）——已由
//     票10 收编转绿于 internal/ledger/ledger_test.go（TestSubagentCountLifecycle
//     等三例），此处不重复立目；
//   - 守望 5 例（skips_subagent_transcript_paths / maybe_enqueue_defers /
//     codex_watch_dirs / polls_all_codex_dirs / dedupes_same_sid）——Watcher
//     归票16，此处 t.Skip 占位保清点，装配后转绿；
//   - HTTP 2 例（endpoint_roundtrip / auth_and_validation）——httpapi 归票15，
//     此处 t.Skip 占位保清点（400 语义的 daemon 面由本文件 TestSubagentInvalidEventRejected 钉住）。
//
// 票面补充钉子（daemon 面直驱，免等 HTTP/守望装配）：event 校验、返回形态与
// 计数、停车→续窗→恢复→过期全生命周期、goroutine+WaitGroup 并发（-race）。
// 时间纪律：clock.Now 包级可注入；测试用毕恢复。

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"ferryman/internal/accounts"
	"ferryman/internal/cctrans"
	"ferryman/internal/clock"
	"ferryman/internal/config"
	"ferryman/internal/ledger"
	"ferryman/internal/mathx"
)

// ---- tests/test_subagent.py 守望 5 例 → 票16 占位 ----

func TestWatcherSkipsSubagentTranscriptPaths(t *testing.T) {
	t.Skip("e2e→票16：Watcher._poll_cc 跳过 <sid>/subagents/agent-*.jsonl（守望装配后转绿）；Python: test_subagent.py::test_watcher_skips_subagent_transcript_paths")
}

func TestMaybeEnqueueDefersWhileSubagentActive(t *testing.T) {
	t.Skip("e2e→票16：_maybe_enqueue 子代理计数推迟道（守望装配后转绿）；Python: test_subagent.py::test_maybe_enqueue_defers_while_subagent_active")
}

func TestCodexWatchDirsAutoDetectsOrcaRuntime(t *testing.T) {
	t.Skip("e2e→票16：CodexWatchDirs 主目录+额外+Orca runtime 路径逐字；Python: test_subagent.py::test_codex_watch_dirs_auto_detects_orca_runtime")
}

func TestWatcherPollsAllCodexDirs(t *testing.T) {
	t.Skip("e2e→票16：两 codex 目录 rollout 都登记（Orca 会话不漏摆渡）；Python: test_subagent.py::test_watcher_polls_all_codex_dirs")
}

func TestWatcherDedupesSameSidAcrossCodexDirs(t *testing.T) {
	t.Skip("e2e→票16：跨 codex 目录同 sid 去重、路径取主目录；Python: test_subagent.py::test_watcher_dedupes_same_sid_across_codex_dirs")
}

// ---- tests/test_subagent.py HTTP 2 例 → 票15 占位 ----

func TestSubagentEndpointRoundtrip(t *testing.T) {
	t.Skip("e2e→票15：HTTP /subagent 往返 + /stats 计数（httpapi 装配后转绿；daemon 面由 TestSubagentReturnAndCounters 钉住）；Python: test_subagent.py::test_subagent_endpoint_roundtrip")
}

func TestSubagentEndpointAuthAndValidation(t *testing.T) {
	t.Skip("e2e→票15：401 错 token + 400 非法 event（httpapi 装配后转绿；400 的 daemon 面由 TestSubagentInvalidEventRejected 钉住）；Python: test_subagent.py::test_subagent_endpoint_auth_and_validation")
}

// ---- 票13 · daemon 面直驱 ----

// testDaemon 最小装配：Config 默认值 + 可选 Accounts（nil=不记账，旧测试形态）。
func testDaemon(t *testing.T, acc *accounts.Accounts) *Daemon {
	t.Helper()
	led := ledger.New()
	return NewDaemon(config.Default(), led, nil,
		func(*ledger.SessionState) bool { return true }, acc, 0, nil)
}

func TestSubagentInvalidEventRejected(t *testing.T) {
	// 非法 event 经 error 通道返回（→400 语义，httpapi 票15 映射）；空 session_id 同。
	d := testDaemon(t, nil)
	if _, err := d.Subagent(map[string]any{"event": "boom", "agent": "cc", "session_id": "v1"}); err == nil {
		t.Fatal("非法 event 应报错（400 语义）")
	}
	if _, err := d.Subagent(map[string]any{"event": "start", "agent": "cc", "session_id": ""}); err == nil {
		t.Fatal("空 session_id 应报错（400 语义）")
	}
	if _, err := d.Subagent(map[string]any{"event": "start", "agent": "cc", "session_id": "v1"}); err != nil {
		t.Fatalf("合法 start 不应报错: %v", err)
	}
}

func TestSubagentReturnAndCounters(t *testing.T) {
	// test_subagent_endpoint_roundtrip 的 daemon 面：返回 {"ok": true, "active": …}、
	// stats.subagent_events 累计、台账 subagents_active 联动。
	led := ledger.New()
	d := testDaemon(t, nil)
	d.Ledger = led

	r, err := d.Subagent(map[string]any{"event": "start", "agent": "cc", "session_id": "ep-1"})
	if err != nil {
		t.Fatal(err)
	}
	if r["ok"] != true || r["active"] != true {
		t.Fatalf("start 返回 = %v, want ok=true active=true", r)
	}
	if d.Stats.SubagentEvents < 1 {
		t.Fatalf("subagent_events = %d, want >= 1", d.Stats.SubagentEvents)
	}
	if got := led.SubagentsActiveCount(); got != 1 {
		t.Fatalf("subagents_active = %d, want 1", got)
	}
	r, err = d.Subagent(map[string]any{"event": "stop", "agent": "cc", "session_id": "ep-1"})
	if err != nil {
		t.Fatal(err)
	}
	if r["ok"] != true || r["active"] != false {
		t.Fatalf("stop 返回 = %v, want ok=true active=false", r)
	}
	if d.Stats.SubagentEvents < 2 {
		t.Fatalf("subagent_events = %d, want >= 2", d.Stats.SubagentEvents)
	}
	if got := led.SubagentsActiveCount(); got != 0 {
		t.Fatalf("subagents_active = %d, want 0", got)
	}
}

func TestWindowParkResumeLifecycle(t *testing.T) {
	// 停车状态机全生命周期（server.py:365-442 语义钉子）：
	// 同步派发即闭（subagents_done）→ 异步派发停车（saw_async 锁存两道）→
	// 再派续窗 → ack 宽限不闭 → 恢复闭窗（main_resumed）→ 懒过期（expired，
	// closed 封顶）→ 探针零副作用。
	tmp := t.TempDir()
	acc, err := accounts.New(tmp)
	if err != nil {
		t.Fatal(err)
	}
	led := ledger.New()
	d := NewDaemon(config.Default(), led, nil,
		func(*ledger.SessionState) bool { return true }, acc, 0, nil)
	startBody := func(sid string) map[string]any {
		return map[string]any{"event": "start", "agent": "cc", "session_id": sid}
	}
	stopBody := func(sid string) map[string]any {
		return map[string]any{"event": "stop", "agent": "cc", "session_id": sid}
	}
	rows := func(sid string) []map[string]any {
		return acc.Read(accounts.ReadOpts{Kind: "window", Session: sid})
	}

	// ① 同步派发：stop 计数归零即闭窗（旧语义 subagents_done；无台账 → path 空，
	//    异步判据无从谈起 → 一律退回旧语义）。
	if _, err := d.Subagent(startBody("w1")); err != nil {
		t.Fatal(err)
	}
	key1 := [2]string{"cc", "w1"}
	if w := d.windows[key1]; w == nil {
		t.Fatal("start 应开窗")
	}
	if _, err := d.Subagent(stopBody("w1")); err != nil {
		t.Fatal(err)
	}
	if len(d.windows) != 0 {
		t.Fatalf("同步 stop 应闭窗, 残留 %d", len(d.windows))
	}
	r1 := rows("w1")
	if len(r1) != 1 || r1[0]["close_reason"] != "subagents_done" {
		t.Fatalf("w1 行 = %v, want 1 条 subagents_done", r1)
	}
	if r1[0]["dur_s"].(float64) < 0 {
		t.Fatal("dur_s >= 0")
	}

	// ② 异步派发：start 尾判置锁存（saw_async），stop 停车；窗口豁免道开启。
	asyncPath := filepath.Join(tmp, "w2.jsonl")
	asyncLine, _ := json.Marshal(map[string]any{"type": "assistant",
		"timestamp": "2026-09-18T12:00:00.000Z",
		"message": map[string]any{"role": "assistant", "content": []any{map[string]any{
			"type": "tool_use", "id": "t1", "name": "Task",
			"input": map[string]any{"prompt": "干活", "run_in_background": true},
		}}}})
	if err := os.WriteFile(asyncPath, append(asyncLine, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	if !cctrans.HasAsyncLaunch(asyncPath) { // 夹具自检：确为异步形态
		t.Fatal("夹具应判 async")
	}
	led.Touch("cc", "w2", asyncPath, clock.Now(), 10, 0)
	if _, err := d.Subagent(startBody("w2")); err != nil {
		t.Fatal(err)
	}
	key2 := [2]string{"cc", "w2"}
	w2 := d.windows[key2]
	if w2 == nil || !w2.SawAsync {
		t.Fatalf("start 尾判应置 saw_async 锁存: %+v", w2)
	}
	if _, err := d.Subagent(stopBody("w2")); err != nil {
		t.Fatal(err)
	}
	if w2.StopTS == nil {
		t.Fatal("异步 stop 应停表停车")
	}
	if !d.WindowWait("cc", "w2") {
		t.Fatal("停车窗未过期 → window_wait 豁免成立")
	}
	if !d.ParkingOpen("cc", "w2") {
		t.Fatal("停车新鲜 → 探针算开")
	}

	// ③ 停车窗又来 start：续窗（stop_ts 归 None）。
	if _, err := d.Subagent(startBody("w2")); err != nil {
		t.Fatal(err)
	}
	if w2.StopTS != nil {
		t.Fatal("续窗应清停表")
	}
	if _, err := d.Subagent(stopBody("w2")); err != nil {
		t.Fatal(err)
	}
	if w2.StopTS == nil {
		t.Fatal("再停应再停车")
	}

	// ④ gate 步 0 不闭停车窗（server.py:150：只有 stop_ts 为 None 的窗才因
	//    prompt 闭——async 真身仍在跑，等待没结束，缺口 A 由 machine-waiting
	//    豁免接住）。
	d.NoteGatePrompt("cc", "w2")
	if got := len(rows("w2")); got != 0 {
		t.Fatalf("停车窗不得因 prompt 闭, 行数 = %d", got)
	}

	// ⑤ ack 宽限：stop+ACK_GRACE_S 内的恢复行不闭窗；其后才闭（main_resumed）。
	stopTS := *w2.StopTS
	d.NoteUsage("cc", "w2", stopTS+AckGraceS-1)
	if len(rows("w2")) != 0 {
		t.Fatal("ack 宽限内不闭窗")
	}
	d.NoteUsage("cc", "w2", stopTS+AckGraceS+1)
	if len(d.windows) != 0 {
		t.Fatal("恢复行应闭窗")
	}
	r2 := rows("w2")
	if len(r2) != 1 || r2[0]["close_reason"] != "main_resumed" {
		t.Fatalf("w2 行 = %v, want 1 条 main_resumed", r2)
	}

	// ⑥ 懒过期：WindowWait 停表超 PARK_EXPIRE_S → 闭窗记 expired，
	//    closed=stop+PARK_EXPIRE_S（此刻必为过去，封顶 now）。
	now := clock.Now()
	expired := now - ParkExpireS - 10
	key3 := [2]string{"cc", "w3"}
	d.windows[key3] = &waitWindow{OpenedTS: now - 7200, StopTS: &expired}
	if d.WindowWait("cc", "w3") {
		t.Fatal("过期应闭窗并返回 false")
	}
	r3 := rows("w3")
	if len(r3) != 1 || r3[0]["close_reason"] != "expired" {
		t.Fatalf("w3 行 = %v, want 1 条 expired", r3)
	}
	if got, want := r3[0]["closed_ts"].(float64), mathx.Round(expired+ParkExpireS, 3); got != want {
		t.Fatalf("closed_ts = %v, want %v（stop+PARK_EXPIRE_S，封顶 now）", got, want)
	}

	// ⑦ 探针零副作用：过期停车窗视同已闭但不闭账；懒过期只在 WindowWait 收口。
	expired2 := clock.Now() - ParkExpireS - 10
	d.windows[key3] = &waitWindow{OpenedTS: clock.Now() - 7200, StopTS: &expired2}
	if d.ParkingOpen("cc", "w3") {
		t.Fatal("停表过期视同已闭")
	}
	if got := len(rows("w3")); got != 1 {
		t.Fatalf("探针零副作用（不闭账），行数 = %d, want 1", got)
	}
	if d.WindowWait("cc", "w3") {
		t.Fatal("懒过期归 WindowWait")
	}
	if got := len(rows("w3")); got != 2 {
		t.Fatalf("副作用在正规道收口，行数 = %d, want 2", got)
	}

	// ⑧ NoteUsage 无窗/未停车 → 无操作不炸；ParkingOpen 无窗 → false。
	d.NoteUsage("cc", "nosuch", clock.Now()+1e6)
	d.NoteUsage("cc", "w1", clock.Now()+1e6) // 窗早已闭
	if d.ParkingOpen("cc", "nosuch") {
		t.Fatal("无窗应 false")
	}
	if got := len(rows("nosuch")); got != 0 {
		t.Fatalf("nosuch 行数 = %d, want 0", got)
	}
}

// TestSubagentConcurrentWindowMachineNoRace 票面验收：goroutine+WaitGroup 复刻
// （Python 线程安全靠 RLock+GIL，Go 靠双锁同序）——Subagent 开窗/重锚/停车与
// WindowWait/ParkingOpen/NoteUsage/NoteGatePrompt 探测并发，-race 下无竞争、
// 锁序无死锁。有 gcc 时 CI 加 -race 强制绿。
func TestSubagentConcurrentWindowMachineNoRace(t *testing.T) {
	acc, err := accounts.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	d := testDaemon(t, acc)
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				sid := fmt.Sprintf("s%d", i%10)
				ev := "start"
				if g%2 == 0 {
					ev = "stop" // 交错派发：故意与 start 序错开，压锁存与续窗分支
				}
				if _, err := d.Subagent(map[string]any{"event": ev, "agent": "cc", "session_id": sid}); err != nil {
					t.Errorf("subagent: %v", err)
				}
				switch (g + i) % 4 {
				case 0:
					_ = d.WindowWait("cc", sid)
				case 1:
					_ = d.ParkingOpen("cc", sid)
				case 2:
					d.NoteUsage("cc", sid, clock.Now()+AckGraceS+1)
				default:
					d.NoteGatePrompt("cc", sid)
				}
			}
		}(g)
	}
	wg.Wait()
}
