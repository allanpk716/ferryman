package daemon

// 规格：tests/test_qwatch_events.py（T51 票04 · 事件/证据形态//stats/一键停）的
// 票16 落点。
//
// 归属拆分（316 清点闭环）：
//   - QWatchStats 纯计数 3 例（observe_drill_beats_counted_not_billed /
//     beat_stats_counters 的纯计数核 / snapshot 形态）——票09 已收编转绿于
//     internal/beat/beat_test.go（TestQWatchStatsObserveDrillNotBilled /
//     TestQWatchStatsCountersByOutcome / TestQWatchStatsHitsWindowsAndRound6），
//     不重复立目；计数器驱动侧由本文件 TestBeatStatsCountersByOutcome 承接；
//   - M5 配置 3 例（beat_interval_s ≤0 拒绝 / off 也不放行 / load 拒绝）——
//     票09 已收编于 internal/config/config_test.go
//     TestValidateBeatIntervalPositive + TestValidateAllBranchesExactText，
//     不重复立目；
//   - HTTP 集成 1 例（qwatch_stop_integration_over_http）——由
//     test_qwatch_e2e_test.go 的 Harness（真守望线程 + 真监听）承接；
//   - 其余本文件 1:1。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ferryman/internal/accounts"
	"ferryman/internal/beat"
	"ferryman/internal/clock"
	"ferryman/internal/ledger"
	"ferryman/internal/mathx"
)

// ---- 命中事件：证据形态五字段（验收②） ----

func TestHitEventBookedOncePerVersionWithEvidence(t *testing.T) {
	// 命中事件随写入版本去重；证据形态五字段齐：session_id＋命中时间（ts）＋
	// unit_count＋breakdown 三桶＋转录绝对路径。
	tmp := t.TempDir()
	projects := filepath.Join(tmp, "projects")
	led := ledger.New()
	accts, err := accounts.New(filepath.Join(tmp, "data"))
	if err != nil {
		t.Fatal(err)
	}
	stats := beat.NewQWatchStats()
	w := newTestWatcherW(mergeCfg(), led, accts, nil, nil, stats)
	f := writeMergeTranscript(t, projects, "hit-1", mergeSurge,
		[][2]string{{"tu_aq", "AskUserQuestion"}})
	info, err := os.Stat(f)
	if err != nil {
		t.Fatal(err)
	}
	st := led.Touch("cc", "hit-1", f, statMTime(info), int(info.Size()), 0)
	w.maybeQwatch(st)
	w.maybeQwatch(st) // 同版本重判：不重复记
	rows := accts.Read(accounts.ReadOpts{Kind: "qwatch_hit"})
	if len(rows) != 1 {
		t.Fatalf("qwatch_hit 行数 = %d, want 1", len(rows))
	}
	r := rows[0]
	if r["session_id"] != "hit-1" { // 证据① session_id
		t.Fatalf("session_id = %v, want hit-1", r["session_id"])
	}
	if _, ok := r["ts"].(float64); !ok { // 证据② 命中时间
		t.Fatalf("ts = %v, want float", r["ts"])
	}
	if r["unit_count"] != float64(8) { // 证据③ unit_count
		t.Fatalf("unit_count = %v, want 8", r["unit_count"])
	}
	if got := r["marker_lines"].(float64) + r["qmark_lines"].(float64) +
		r["numbered_lines"].(float64); got != 8 { // 证据④ breakdown 三桶合计
		t.Fatalf("breakdown 合计 = %v, want 8", got)
	}
	if r["transcript_path"] != f { // 证据⑤ 转录绝对路径
		t.Fatalf("transcript_path = %v, want %v", r["transcript_path"], f)
	}
	if !filepath.IsAbs(f) {
		t.Fatal("转录路径应绝对")
	}
	if snap := stats.Snapshot(); snap["hits"] != 1 {
		t.Fatalf("stats hits = %v, want 1", snap["hits"])
	}
}

func TestHitEventDedupAcrossTransientRechecks(t *testing.T) {
	// 瞬态阻塞（子代理在飞）不盖版本章、逐轮重判——命中事件靠版本去重只记一次；
	// 解除后开窗也不补记。
	tmp := t.TempDir()
	projects := filepath.Join(tmp, "projects")
	led := ledger.New()
	accts, err := accounts.New(filepath.Join(tmp, "data"))
	if err != nil {
		t.Fatal(err)
	}
	stats := beat.NewQWatchStats()
	w := newTestWatcherW(mergeCfg(), led, accts, nil, nil, stats)
	f := writeMergeTranscript(t, projects, "hit-2", mergeSurge,
		[][2]string{{"tu_aq", "AskUserQuestion"}})
	info, err := os.Stat(f)
	if err != nil {
		t.Fatal(err)
	}
	st := led.Touch("cc", "hit-2", f, statMTime(info), int(info.Size()), 0)
	led.SubagentEvent("cc", "hit-2", "start")
	w.maybeQwatch(st)
	w.maybeQwatch(st)
	w.maybeQwatch(st) // 子代理在飞：反复重判
	if qwRead(led, st).opened != nil {
		t.Fatal("条件③应阻塞开窗")
	}
	if rows := accts.Read(accounts.ReadOpts{Kind: "qwatch_hit"}); len(rows) != 1 {
		t.Fatalf("qwatch_hit 行数 = %d, want 1", len(rows))
	}
	led.SubagentEvent("cc", "hit-2", "stop")
	w.maybeQwatch(st)
	if qwRead(led, st).opened == nil {
		t.Fatal("解除后应开窗")
	}
	if rows := accts.Read(accounts.ReadOpts{Kind: "qwatch_hit"}); len(rows) != 1 {
		t.Fatalf("解除后不补记, qwatch_hit 行数 = %d", len(rows))
	}
	if snap := stats.Snapshot(); snap["windows_opened"] != 1 {
		t.Fatalf("windows_opened = %v, want 1", snap["windows_opened"])
	}
}

func TestHitEventPrivacyNoMessageBody(t *testing.T) {
	// 隐私回归（验收②硬线）：命中事件原始 JSON 行不含消息正文标记；
	// 字段集合不超白名单（只有元数据与计数）。
	tmp := t.TempDir()
	projects := filepath.Join(tmp, "projects")
	led := ledger.New()
	accts, err := accounts.New(filepath.Join(tmp, "data"))
	if err != nil {
		t.Fatal(err)
	}
	w := newTestWatcherW(mergeCfg(), led, accts, nil, nil, nil)
	marker := "隐私正文绝不入账XYZZY" // 隐私回归：消息正文里的独特标记
	surge := mergeSurge + "\n" + marker + "（这一句只在消息正文里）"
	f := writeMergeTranscript(t, projects, "pv-9", surge,
		[][2]string{{"tu_aq", "AskUserQuestion"}})
	info, err := os.Stat(f)
	if err != nil {
		t.Fatal(err)
	}
	st := led.Touch("cc", "pv-9", f, statMTime(info), int(info.Size()), 0)
	w.maybeQwatch(st)
	rows := accts.Read(accounts.ReadOpts{Kind: "qwatch_hit"})
	if len(rows) == 0 {
		t.Fatal("前提：命中事件已落账")
	}
	allowed := map[string]bool{"v": true, "ts": true, "ts_iso": true, "kind": true,
		"agent": true, "session_id": true, "lineage_id": true, "project": true,
		"unit_count": true, "marker_lines": true, "qmark_lines": true,
		"numbered_lines": true, "transcript_path": true}
	entries, err := os.ReadDir(filepath.Join(tmp, "data", "accounts"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(tmp, "data", "accounts", e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(string(raw), "\n") {
			if !strings.Contains(line, `"qwatch_hit"`) {
				continue
			}
			if strings.Contains(line, marker) {
				t.Fatal("正文标记不入账")
			}
			var obj map[string]any
			if err := json.Unmarshal([]byte(line), &obj); err != nil {
				t.Fatalf("行非 JSON: %v", err)
			}
			for k := range obj {
				if !allowed[k] {
					t.Fatalf("字段超白名单: %q（行 = %s）", k, line)
				}
			}
		}
	}
}

// ---- 开窗/关窗事件（验收①） ----

func TestOpenAndCloseEventsBookedViaPollLoop(t *testing.T) {
	// 走真实 pollCC：开窗落 qwatch_open（unit_count＋prefix_tokens）；
	// 新写入关窗落 qwatch_close（opened_ts/closed_ts/dur_s/beats_fired/
	// close_reason）；无写入的轮次不重复记关窗。
	tmp := t.TempDir()
	projects := filepath.Join(tmp, "projects")
	led := ledger.New()
	accts, err := accounts.New(filepath.Join(tmp, "data"))
	if err != nil {
		t.Fatal(err)
	}
	stats := beat.NewQWatchStats()
	w := newTestWatcherW(mergeCfg(), led, accts, nil, nil, stats)
	w.ccDir = projects // 走真实 poll_cc 的用例需要
	w.cxDirs = nil
	f := writeMergeTranscript(t, projects, "oc-1", mergeSurge,
		[][2]string{{"tu_aq", "AskUserQuestion"}})
	w.pollCC()
	st := led.Get("cc", "oc-1")
	if st == nil {
		t.Fatal("守望应登记会话")
	}
	if qwRead(led, st).opened == nil {
		t.Fatal("应已开窗")
	}
	openRows := accts.Read(accounts.ReadOpts{Kind: "qwatch_open"})
	if len(openRows) != 1 {
		t.Fatalf("qwatch_open 行数 = %d, want 1", len(openRows))
	}
	if openRows[0]["unit_count"] != float64(8) {
		t.Fatalf("unit_count = %v, want 8", openRows[0]["unit_count"])
	}
	if openRows[0]["prefix_tokens"] != float64(50000) {
		t.Fatalf("prefix_tokens = %v, want 50000", openRows[0]["prefix_tokens"])
	}
	if rows := accts.Read(accounts.ReadOpts{Kind: "qwatch_close"}); len(rows) != 0 {
		t.Fatalf("尚不应有关窗行: %v", rows)
	}

	openedTS := *qwRead(led, st).opened
	appendJSONLine(t, f, mergeAssistant("收到，开工。", nil, "msg_2")) // 用户提交：非提问潮 assistant 落盘
	newT := clock.Now() + 5
	utimeFile(t, f, newT)
	w.pollCC()
	q := qwRead(led, st)
	if q.opened != nil { // 新写入关窗（既有语义）
		t.Fatal("新写入应关窗")
	}
	closeRows := accts.Read(accounts.ReadOpts{Kind: "qwatch_close"})
	if len(closeRows) != 1 {
		t.Fatalf("qwatch_close 行数 = %d, want 1", len(closeRows))
	}
	cr := closeRows[0]
	if cr["opened_ts"] != mathx.Round(openedTS, 3) {
		t.Fatalf("opened_ts = %v, want %v", cr["opened_ts"], mathx.Round(openedTS, 3))
	}
	if cr["closed_ts"] != mathx.Round(q.lastWrite, 3) {
		t.Fatalf("closed_ts = %v, want %v", cr["closed_ts"], mathx.Round(q.lastWrite, 3))
	}
	if cr["dur_s"].(float64) < 0 {
		t.Fatalf("dur_s = %v, want >= 0", cr["dur_s"])
	}
	if cr["beats_fired"] != float64(0) {
		t.Fatalf("beats_fired = %v, want 0", cr["beats_fired"])
	}
	if cr["close_reason"] != "write" {
		t.Fatalf("close_reason = %v, want write", cr["close_reason"])
	}

	w.pollCC() // 无写入：不重复记
	if rows := accts.Read(accounts.ReadOpts{Kind: "qwatch_close"}); len(rows) != 1 {
		t.Fatalf("无写入轮次不应重复记, 行数 = %d", len(rows))
	}
	if snap := stats.Snapshot(); snap["windows_opened"] != 1 {
		t.Fatalf("windows_opened = %v, want 1", snap["windows_opened"])
	}
}

func TestCloseEventBeatsFiredReflectsActualFires(t *testing.T) {
	// 票04 评审 Important 防回潮：关窗事件 beats_fired 必须是窗口实发跳数。
	// 窗口先 observe 演练实发 1 跳 → 新写入关窗 → 断言 beats_fired == 1。
	// （touch 关窗时先把 qwatch_beats_fired 清零——不快照则落账恒 0。）
	tmp := t.TempDir()
	projects := filepath.Join(tmp, "projects")
	led := ledger.New()
	accts, err := accounts.New(filepath.Join(tmp, "data"))
	if err != nil {
		t.Fatal(err)
	}
	w := newTestWatcherW(mergeCfg(), led, accts, nil, nil, nil)
	w.ccDir = projects
	w.cxDirs = nil
	f := writeMergeTranscript(t, projects, "oc-2", mergeSurge,
		[][2]string{{"tu_aq", "AskUserQuestion"}})
	w.pollCC()
	st := led.Get("cc", "oc-2")
	if st == nil || qwRead(led, st).opened == nil {
		t.Fatal("前提：自然开窗")
	}
	setPlan(led, st, []float64{clock.Now() - 1}) // 演练跳提前到期：observe 路径实发
	w.maybeFireBeats(st)
	if q := qwRead(led, st); q.fired != 1 { // 前提：窗口实发 1 跳
		t.Fatalf("beats_fired = %d, want 1", q.fired)
	}
	if rows := accts.Read(accounts.ReadOpts{Kind: "beat"}); len(rows) != 1 {
		t.Fatalf("beat 行数 = %d, want 1", len(rows))
	}

	appendJSONLine(t, f, mergeAssistant("收到，开工。", nil, "msg_2")) // 用户提交：新写入关窗
	newT := clock.Now() + 5
	utimeFile(t, f, newT)
	w.pollCC()
	if qwRead(led, st).opened != nil {
		t.Fatal("新写入应关窗")
	}
	closeRows := accts.Read(accounts.ReadOpts{Kind: "qwatch_close"})
	if len(closeRows) != 1 {
		t.Fatalf("qwatch_close 行数 = %d, want 1", len(closeRows))
	}
	if closeRows[0]["beats_fired"] != float64(1) { // 修复点：实发跳数，非恒 0
		t.Fatalf("beats_fired = %v, want 1（实发跳数）", closeRows[0]["beats_fired"])
	}
	if closeRows[0]["close_reason"] != "write" {
		t.Fatalf("close_reason = %v, want write", closeRows[0]["close_reason"])
	}
}

// ---- /stats 计数（驱动侧；纯计数核归 beat_test.go） ----

func TestBeatStatsCountersByOutcome(t *testing.T) {
	// enforce fake sender：hit/miss 两跳 → 跳数、四道 outcome 计数、累计实收。
	led := ledger.New()
	tmp := t.TempDir()
	accts, err := accounts.New(filepath.Join(tmp, "data"))
	if err != nil {
		t.Fatal(err)
	}
	stats := beat.NewQWatchStats()
	rec := &recordingSender{results: []beat.BeatResult{
		{Sent: true, OK: true, InputTokens: 100, CacheReadTokens: 1900, CostActual: 0.01},
		{Sent: true, OK: true, InputTokens: 2000, CacheReadTokens: 0},
	}}
	cfg := mergeCfg()
	cfg.QuestionWatch.Mode = "enforce"
	w := newTestWatcherW(cfg, led, accts, nil, rec, stats)
	st, _ := bareSession(t, led, tmp, "tri-s")
	openWindow(led, st, 420.0, 2)
	for i := 0; i < 2; i++ {
		setPlan(led, st, []float64{clock.Now() - 1})
		w.maybeFireBeats(st)
	}
	snap := stats.Snapshot()
	if snap["beats_fired"] != 2 {
		t.Fatalf("beats_fired = %v, want 2", snap["beats_fired"])
	}
	bo, _ := snap["beats_by_outcome"].(map[string]int)
	if bo["hit"] != 1 || bo["miss"] != 1 || bo["error"] != 0 || bo["observe"] != 0 {
		t.Fatalf("beats_by_outcome = %v, want hit1/miss1", bo)
	}
	if got := snap["cost_actual"].(float64); mathx.Round(got-0.01, 9) != 0 {
		t.Fatalf("cost_actual = %v, want 0.01", got)
	}
	// 既有科目通道：两跳逐条入 beat 科目（hit/miss 各一）
	rows := accts.Read(accounts.ReadOpts{Kind: "beat"})
	if len(rows) != 2 {
		t.Fatalf("beat 行数 = %d, want 2", len(rows))
	}
}

// ---- /stats（验收③） ----

func TestHealthReportsQwatchSection(t *testing.T) {
	// health() 带 qwatch 节：计数器快照＋当前 mode（读配置活值）。
	led := ledger.New()
	cfg := mergeCfg() // observe
	stats := beat.NewQWatchStats()
	stats.RecordHit()
	stats.RecordBeat("miss", 0.5)
	d := NewDaemon(cfg, led, nil, func(*ledger.SessionState) bool { return true }, nil, 0, stats)
	q := d.Health()["qwatch"].(map[string]any)
	if q["mode"] != "observe" {
		t.Fatalf("mode = %v, want observe", q["mode"])
	}
	if q["hits"] != 1 {
		t.Fatalf("hits = %v, want 1", q["hits"])
	}
	if q["windows_opened"] != 0 {
		t.Fatalf("windows_opened = %v, want 0", q["windows_opened"])
	}
	if q["beats_fired"] != 1 {
		t.Fatalf("beats_fired = %v, want 1", q["beats_fired"])
	}
	if bo := q["beats_by_outcome"].(map[string]int); bo["miss"] != 1 {
		t.Fatalf("beats_by_outcome = %v", bo)
	}
	if q["cost_actual"] != 0.5 {
		t.Fatalf("cost_actual = %v, want 0.5", q["cost_actual"])
	}
	// 未注入计数器（旧调用零改动）→ 全零占位，mode 照报
	cfg2 := mergeCfg()
	cfg2.QuestionWatch.Mode = "enforce"
	d2 := NewDaemon(cfg2, led, nil, func(*ledger.SessionState) bool { return true }, nil, 0, nil)
	q2 := d2.Health()["qwatch"].(map[string]any)
	if q2["mode"] != "enforce" {
		t.Fatalf("mode = %v, want enforce", q2["mode"])
	}
	if q2["beats_fired"] != 0 || q2["hits"] != 0 || q2["cost_actual"] != 0.0 {
		t.Fatalf("未接线应全零: %+v", q2)
	}
}

// ---- 一键停（验收④） ----

func TestQWatchStopClearsWindowsAndDisablesReopen(t *testing.T) {
	// 一键停：mode→off＋全部在飞计划/未关窗口取消（关窗事件 close_reason=
	// stop 落账）；置 off 后不再开窗。
	tmp := t.TempDir()
	projects := filepath.Join(tmp, "projects")
	led := ledger.New()
	accts, err := accounts.New(filepath.Join(tmp, "data"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := mergeCfg()
	d := NewDaemon(cfg, led, nil, func(*ledger.SessionState) bool { return true }, accts, 0, nil)
	w := newTestWatcherW(cfg, led, accts, d, nil, nil)
	f := writeMergeTranscript(t, projects, "stop-1", mergeSurge,
		[][2]string{{"tu_aq", "AskUserQuestion"}})
	info, err := os.Stat(f)
	if err != nil {
		t.Fatal(err)
	}
	stA := led.Touch("cc", "stop-1", f, statMTime(info), int(info.Size()), 0)
	w.maybeQwatch(stA)
	if qwRead(led, stA).opened == nil {
		t.Fatal("前提：stA 应已开窗")
	}
	stB, _ := bareSession(t, led, tmp, "stop-2")
	openWindow(led, stB, 420.0, 2)
	out := d.QWatchStop()
	if out["ok"] != true || out["mode"] != "off" || out["cancelled"] != 2 {
		t.Fatalf("qwatch_stop 返回 = %v, want ok/mode=off/cancelled=2", out)
	}
	if cfg.QuestionWatch.Mode != "off" {
		t.Fatalf("mode = %q, want off", cfg.QuestionWatch.Mode)
	}
	qa, qb := qwRead(led, stA), qwRead(led, stB)
	if qa.opened != nil || len(qa.plan) != 0 || qb.opened != nil || len(qb.plan) != 0 {
		t.Fatalf("窗口/计划应全清: stA=%+v stB=%+v", qa, qb)
	}
	closeRows := accts.Read(accounts.ReadOpts{Kind: "qwatch_close"})
	sids := map[string]bool{}
	for _, r := range closeRows {
		sids[r["session_id"].(string)] = true
		if r["close_reason"] != "stop" {
			t.Fatalf("close_reason = %v, want stop", r["close_reason"])
		}
	}
	if !sids["stop-1"] || !sids["stop-2"] {
		t.Fatalf("关窗行会话 = %v, want {stop-1, stop-2}", sids)
	}
	// 置 off 后：即使新写入且末条仍提问潮，也不再开窗
	led.Mu().Lock()
	stA.LastWrite += 5
	led.Mu().Unlock()
	w.maybeQwatch(stA)
	if qwRead(led, stA).opened != nil {
		t.Fatal("mode=off 不应再开窗")
	}
	if q := d.Health()["qwatch"].(map[string]any); q["mode"] != "off" {
		t.Fatalf("health mode = %v, want off", q["mode"])
	}
}
