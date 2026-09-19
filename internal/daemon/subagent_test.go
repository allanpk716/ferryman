package daemon

// 规格：tests/test_subagent.py 10 例的票13 落点。
//
// 归属拆分（316 清点闭环）：
//   - 台账级 3 例（count_lifecycle / nested_two_levels / leak_guard）——已由
//     票10 收编转绿于 internal/ledger/ledger_test.go（TestSubagentCountLifecycle
//     等三例），此处不重复立目；
//   - 守望 5 例（skips_subagent_transcript_paths / maybe_enqueue_defers /
//     codex_watch_dirs / polls_all_codex_dirs / dedupes_same_sid）——票16
//     Watcher 装配后转绿（见下方实现）；
//   - HTTP 2 例（endpoint_roundtrip / auth_and_validation）——httpapi 归票15，
//     此处 t.Skip 占位保清点（400 语义的 daemon 面由本文件 TestSubagentInvalidEventRejected 钉住）。
//
// 票面补充钉子（daemon 面直驱，免等 HTTP/守望装配）：event 校验、返回形态与
// 计数、停车→续窗→恢复→过期全生命周期、goroutine+WaitGroup 并发（-race）。
// 时间纪律：clock.Now 包级可注入；测试用毕恢复。

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"ferryman/internal/accounts"
	"ferryman/internal/cctrans"
	"ferryman/internal/clock"
	"ferryman/internal/config"
	"ferryman/internal/ledger"
	"ferryman/internal/mathx"
)

// ---- tests/test_subagent.py 守望 5 例 → 票16 转绿（Watcher 装配完成） ----

func TestWatcherSkipsSubagentTranscriptPaths(t *testing.T) {
	// test_subagent.py::test_watcher_skips_subagent_transcript_paths 1:1：
	// <session-id>/subagents/agent-*.jsonl 不登记为独立会话（真实目录结构）。
	tmp := t.TempDir()
	projects := filepath.Join(tmp, "projects")
	writeMergeTranscript(t, projects, "main-1", "干完了，没有问题。", nil)
	sub := filepath.Join(projects, "C--proj", "main-1", "subagents", "agent-a1234.jsonl")
	if err := os.MkdirAll(filepath.Dir(sub), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sub,
		[]byte("{\"type\": \"user\", \"message\": {\"role\": \"user\", \"content\": \"子任务\"}}\n"),
		0o644); err != nil {
		t.Fatal(err)
	}

	led := ledger.New()
	cfg := config.Default()
	cfg.Watch.CCProjectsDir = projects
	w := NewWatcher(cfg, led, nil, func(*ledger.SessionState) bool { return true }, 0, nil, nil, nil, nil)
	w.cxDirs = []string{filepath.Join(tmp, "no-codex")}
	w.pollCC()

	ids := map[string]bool{}
	for _, st := range led.AllSessions() {
		ids[st.SessionID] = true
	}
	if !ids["main-1"] {
		t.Fatal("主会话应登记")
	}
	for id := range ids { // 子代理转录不再被摆渡
		if strings.HasPrefix(id, "agent-") {
			t.Fatalf("子代理转录不应登记: %q", id)
		}
	}
}

func TestMaybeEnqueueDefersWhileSubagentActive(t *testing.T) {
	// test_subagent.py::test_maybe_enqueue_defers_while_subagent_active 1:1：
	// 子代理计数 > 0 → 推迟摆渡（内存判定，先于 T31 悬空检测的磁盘扫描）。
	tmp := t.TempDir()
	cfg := config.Default()
	cfg.Thresholds = config.ThresholdCfg{SummarizeS: 0.1, BlockS: 1.0,
		MinCtxTokens: 10, CacheWarnS: 720}
	led := ledger.New()
	var mu sync.Mutex
	var enqueued []string
	w := NewWatcher(cfg, led, nil, func(st *ledger.SessionState) bool {
		mu.Lock()
		enqueued = append(enqueued, st.SessionID)
		mu.Unlock()
		return true
	}, 0, nil, nil, nil, nil)

	f := writeMergeTranscript(t, filepath.Join(tmp, "projects"), "sub-run-1", "干完了，没有问题。", nil)
	st := led.Touch("cc", "sub-run-1", f, clock.Now(), 10, 0)
	led.Mu().Lock()
	st.LastWrite -= 5 // 造闲置达阈值
	led.Mu().Unlock()

	led.SubagentEvent("cc", "sub-run-1", "start")
	for i := 0; i < 3; i++ {
		w.maybeEnqueue(st)
	}
	mu.Lock()
	if len(enqueued) != 0 { // 运行中 → 每轮推迟
		t.Fatalf("运行中应推迟, enqueued = %v", enqueued)
	}
	mu.Unlock()

	led.SubagentEvent("cc", "sub-run-1", "stop")
	w.maybeEnqueue(st)
	mu.Lock()
	defer mu.Unlock()
	if len(enqueued) != 1 || enqueued[0] != "sub-run-1" { // 停了 → 正常摆渡
		t.Fatalf("enqueued = %v, want [sub-run-1]", enqueued)
	}
}

func TestCodexWatchDirsAutoDetectsOrcaRuntime(t *testing.T) {
	// test_subagent.py::test_codex_watch_dirs_auto_detects_orca_runtime 1:1：
	// Orca 运行时目录存在时自动追加（经 Orca 启动的 codex rollout 写在那里）。
	tmp := t.TempDir()
	cfg := config.WatchCfg{CodexSessionsDir: filepath.Join(tmp, "main")}

	if got := CodexWatchDirs(cfg, tmp); !reflect.DeepEqual(got,
		[]string{filepath.Join(tmp, "main")}) { // 无 orca → 只有一个
		t.Fatalf("dirs = %v", got)
	}

	orca := filepath.Join(tmp, "AppData", "Roaming", "orca",
		"codex-runtime-home", "home", "sessions")
	if err := os.MkdirAll(orca, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := CodexWatchDirs(cfg, tmp); !reflect.DeepEqual(got,
		[]string{filepath.Join(tmp, "main"), orca}) {
		t.Fatalf("dirs = %v, want 主目录+orca", got)
	}

	cfg2 := config.WatchCfg{CodexSessionsDir: "", CodexExtraDirs: []string{filepath.Join(tmp, "x")}}
	if got := CodexWatchDirs(cfg2, tmp); !reflect.DeepEqual(got,
		[]string{filepath.Join(tmp, ".codex", "sessions"),
			filepath.Join(tmp, "x"), orca}) {
		t.Fatalf("dirs = %v, want 默认主目录+额外+orca", got)
	}
}

func TestWatcherPollsAllCodexDirs(t *testing.T) {
	// test_subagent.py::test_watcher_polls_all_codex_dirs 1:1：
	// 两个目录里的 rollout 都要登记（Orca 会话不再漏摆渡）。
	tmp := t.TempDir()
	led := ledger.New()
	cfg := config.Default()
	cfg.Watch.CodexSessionsDir = filepath.Join(tmp, "a")
	w := NewWatcher(cfg, led, nil, func(*ledger.SessionState) bool { return true }, 0, nil, nil, nil, nil)
	w.ccDir = filepath.Join(tmp, "no-cc")
	w.cxDirs = []string{filepath.Join(tmp, "a"), filepath.Join(tmp, "orca-home")}
	for _, dir := range w.cxDirs {
		f := filepath.Join(dir, "2026", "09", "17", "rollout-2026-09-17T10-00-00-x.jsonl")
		if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeRollout(t, w.cxDirs[0], "aaa111")
	writeRollout(t, w.cxDirs[1], "bbb222")
	w.pollCodex()
	ids := map[string]bool{}
	for _, st := range led.AllSessions() {
		if st.Agent == "codex" {
			ids[st.SessionID] = true
		}
	}
	if !ids["aaa111"] || !ids["bbb222"] || len(ids) != 2 {
		t.Fatalf("codex 会话 = %v, want {aaa111, bbb222}", ids)
	}
}

func TestWatcherDedupesSameSidAcrossCodexDirs(t *testing.T) {
	// test_subagent.py::test_watcher_dedupes_same_sid_across_codex_dirs 1:1：
	// ~/.codex/sessions 与 Orca runtime 目录互为副本（2026-09-17 实测同 uuid
	// 两份）→ 同 sid 只登记一次，路径取主目录（在前）。
	tmp := t.TempDir()
	led := ledger.New()
	w := NewWatcher(config.Default(), led, nil, func(*ledger.SessionState) bool { return true }, 0, nil, nil, nil, nil)
	w.ccDir = filepath.Join(tmp, "no-cc")
	a, b := filepath.Join(tmp, "main"), filepath.Join(tmp, "orca")
	writeRollout(t, a, "dup111")
	writeRollout(t, b, "dup111")
	w.cxDirs = []string{a, b}
	w.pollCodex()
	sessions := []*ledger.SessionState{}
	for _, st := range led.AllSessions() {
		if st.Agent == "codex" {
			sessions = append(sessions, st)
		}
	}
	if len(sessions) != 1 || sessions[0].SessionID != "dup111" {
		t.Fatalf("登记 = %d 条, want 1 条 dup111", len(sessions))
	}
	if !strings.HasPrefix(sessions[0].TranscriptPath, a) { // 路径稳定取主目录
		t.Fatalf("transcript_path = %q, want 主目录前缀 %q", sessions[0].TranscriptPath, a)
	}
}

// writeRollout 造一份 rollout-<ts>-<sid>.jsonl（目录结构按真实布局）。
func writeRollout(t *testing.T, dir, sid string) string {
	t.Helper()
	f := filepath.Join(dir, "2026", "09", "17",
		"rollout-2026-09-17T10-00-00-"+sid+".jsonl")
	if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f,
		[]byte("{\"type\":\"session_meta\",\"payload\":{\"cwd\":\"C:/x\"}}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return f
}

// ---- tests/test_subagent.py HTTP 2 例 → 票15 转绿（真监听 + 真 Daemon 端到端） ----

func TestSubagentEndpointRoundtrip(t *testing.T) {
	// test_subagent.py::test_subagent_endpoint_roundtrip 1:1：HTTP /subagent 往返
	// + /stats 计数（Harness 的 Go 形：真监听临时端口，守望/工人不在此票面）。
	port := freePort(t)
	d := testDaemon(t, nil)
	serveBg(t, d, port, "tok")

	r := postJSON(t, port, "/subagent", "tok",
		map[string]any{"agent": "cc", "session_id": "ep-1", "event": "start"})
	if r["active"] != true {
		t.Fatalf("start 返回 = %v, want active=true", r)
	}
	st := getJSON(t, port, "/stats", "tok")
	if st["subagents_active"] != float64(1) { // 可观测性（T26 观察）
		t.Fatalf("subagents_active = %v, want 1", st["subagents_active"])
	}
	if st["subagent_events_total"].(float64) < 1 { // 累计事件数（端到端验证用）
		t.Fatalf("subagent_events_total = %v, want >= 1", st["subagent_events_total"])
	}
	r = postJSON(t, port, "/subagent", "tok",
		map[string]any{"agent": "cc", "session_id": "ep-1", "event": "stop"})
	if r["active"] != false {
		t.Fatalf("stop 返回 = %v, want active=false", r)
	}
	st = getJSON(t, port, "/stats", "tok")
	if st["subagents_active"] != float64(0) {
		t.Fatalf("subagents_active = %v, want 0", st["subagents_active"])
	}
	if st["subagent_events_total"].(float64) < 2 {
		t.Fatalf("subagent_events_total = %v, want >= 2", st["subagent_events_total"])
	}
}

func TestSubagentEndpointAuthAndValidation(t *testing.T) {
	// test_subagent.py::test_subagent_endpoint_auth_and_validation 1:1：
	// 错 token → 401；非法 event → 400（daemon 面报错经 HTTP error 通道）。
	port := freePort(t)
	serveBg(t, testDaemon(t, nil), port, "tok")

	code, _ := postRaw(t, port, "/subagent", "wrong-token",
		[]byte(`{"agent": "cc", "session_id": "ep-2", "event": "start"}`))
	if code != http.StatusUnauthorized {
		t.Fatalf("错 token = %d, want 401", code)
	}
	code, body := postRaw(t, port, "/subagent", "tok",
		[]byte(`{"agent": "cc", "session_id": "ep-2", "event": "boom"}`))
	if code != http.StatusBadRequest {
		t.Fatalf("非法 event = %d, want 400", code)
	}
	if !strings.HasPrefix(string(body), `{"error":"bad request: `) {
		t.Fatalf("400 体 = %q, want bad request 前缀", body)
	}
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
