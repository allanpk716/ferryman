package daemon

// 规格：tests/test_qwatch_window.py（T51 票02 · 等答复窗口 32 例）的票16 落点。
//
// 归属拆分（316 清点闭环）：
//   - 台账级 2 例（new_write_closes_window / stale_mtime_does_not_close）——
//     票10 已收编转绿于 internal/ledger/ledger_test.go
//     TestTouchNewWriteClearsQWatchWindow（含旧 mtime 不关窗分支），不重复立目；
//   - 配置级 7 例（t51_config_*）——票09 已收编于 internal/config/config_test.go
//     （TestValidateAllBranchesExactText / TestValidateClampsLeadWithWarnings /
//     TestValidateLeadTooBigRejected / TestValidateLeadAboveWallClockNoWarn /
//     TestValidateSkipsLeadChecksWhenOff / TestLoadAllSectionsOverride /
//     TestDefaultValuesVerbatim），不重复立目；
//   - harness 接线 1 例（harness_wires_ferry_daemon_for_mutex）——由
//     test_qwatch_e2e_test.go TestHarnessWiresFerryDaemonForMutex 承接；
//   - 其余 22 例本文件 1:1 + 票13 评审 Minor A 的 D 补测（窗口读者 × Touch
//     写者并发用例）。
//
// 时间纪律：clock.Now 真实时钟；转录 mtime 一律经 os.Stat 读取（与生产
// pollCC/两道验②同口径）。共享引用读写经 qwRead/setLastWrite 等助手持锁。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"ferryman/internal/accounts"
	"ferryman/internal/beat"
	"ferryman/internal/clock"
	"ferryman/internal/config"
	"ferryman/internal/ledger"
	"ferryman/internal/qwatch"
)

// ---- 共享测试助手（本包四个 qwatch 测试文件通用） ----

// newTestWatcherW Python _watcher（__new__ 裸构造）的 Go 形：NewWatcher + 测试
// 注线——enqueue 恒真、富化即达标（条件④确定性）、harvest 置 nil 对齐裸构造
// （getattr 容错→不采集；需要采集的用例如 TestHarvestUsageFeedsNoteUsageMaxTS
// 自行 NewWatcher 重建）。sender/qs 传 nil = 旧测试形态。
func newTestWatcherW(cfg *config.Config, led *ledger.Ledger, acc *accounts.Accounts,
	d *Daemon, sender beat.Sender, qs *beat.QWatchStats) *Watcher {
	w := NewWatcher(cfg, led, nil, func(*ledger.SessionState) bool { return true }, 0, acc, d, sender, qs)
	w.harvest = nil
	w.enrich = func(st *ledger.SessionState) {
		led.Mu().Lock()
		st.PeakCtx = 50000
		led.Mu().Unlock()
	}
	return w
}

// qwatchState qwRead 的一次性台账快照（测试断言用）。
type qwatchState struct {
	opened    *float64
	fired     int
	plan      []float64
	snap      *ledger.QSnap
	lastWrite float64
	size      int
	peak      int
	handedOff float64
}

// qwRead 台账锁内抄窗口态（共享引用读写均须持锁——与生产同纪律）。
func qwRead(led *ledger.Ledger, st *ledger.SessionState) qwatchState {
	led.Mu().Lock()
	defer led.Mu().Unlock()
	return qwatchState{
		opened: st.QWatchOpenedTS, fired: st.QWatchBeatsFired,
		plan: append([]float64(nil), st.QWatchPlan...), snap: st.QWatchSnapshot,
		lastWrite: st.LastWrite, size: st.Size, peak: st.PeakCtx,
		handedOff: st.HandedOffAt,
	}
}

// setOpened 台账锁内置开窗态（造窗，生产者=开窗临界区持锁直改同款）。
func setOpened(led *ledger.Ledger, st *ledger.SessionState, ts float64) {
	led.Mu().Lock()
	st.QWatchOpenedTS = &ts
	led.Mu().Unlock()
}

// setLastWrite 台账锁内改 last_write。
func setLastWrite(led *ledger.Ledger, st *ledger.SessionState, v float64) {
	led.Mu().Lock()
	st.LastWrite = v
	led.Mu().Unlock()
}

// shiftLastWrite 台账锁内 last_write += delta（Python st.last_write -= n 同位）。
func shiftLastWrite(led *ledger.Ledger, st *ledger.SessionState, delta float64) {
	led.Mu().Lock()
	st.LastWrite += delta
	led.Mu().Unlock()
}

// setPlan 台账锁内摆剩余计划。
func setPlan(led *ledger.Ledger, st *ledger.SessionState, plan []float64) {
	led.Mu().Lock()
	st.QWatchPlan = plan
	led.Mu().Unlock()
}

// openWindow 手工摆成已开窗态（计划/快照与 maybeQwatch 开窗路径同形状）。
func openWindow(led *ledger.Ledger, st *ledger.SessionState, interval float64, beats int) {
	led.Mu().Lock()
	defer led.Mu().Unlock()
	t0 := clock.Now()
	ts := t0
	st.QWatchOpenedTS = &ts
	st.QWatchBeatsFired = 0
	plan := make([]float64, 0, beats)
	for i := 1; i <= beats; i++ {
		plan = append(plan, t0+float64(i)*interval)
	}
	st.QWatchPlan = plan
	st.QWatchSnapshot = &ledger.QSnap{MTime: st.LastWrite, Size: st.Size}
}

// utimeFile Python os.utime(f, (t, t)) 的 Go 形。
func utimeFile(t *testing.T, path string, mtime float64) {
	t.Helper()
	mt := time.Unix(0, int64(mtime*1e9))
	if err := os.Chtimes(path, mt, mt); err != nil {
		t.Fatal(err)
	}
}

// appendJSONLine 向转录追加一行（Python f.open("a") 写 _line(...) 同位）。
func appendJSONLine(t *testing.T, path string, obj map[string]any) {
	t.Helper()
	b, err := json.Marshal(obj)
	if err != nil {
		t.Fatal(err)
	}
	fh, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer fh.Close()
	if _, err := fh.Write(append(b, '\n')); err != nil {
		t.Fatal(err)
	}
}

// ---- 台账窗口态：开窗字段（Python test_t51_window_fields_on_open） ----

func TestT51WindowFieldsOnOpen(t *testing.T) {
	// 四条件全真开窗：opened_ts/beats_fired=0/plan 按票03调度器排定/snapshot。
	tmp := t.TempDir()
	projects := filepath.Join(tmp, "projects")
	led := ledger.New()
	w := newTestWatcherW(mergeCfg(), led, nil, nil, nil, nil) // 默认 2 跳 × 420s
	f := writeMergeTranscript(t, projects, "win-1", mergeSurge, nil)
	st := mergeSession(t, led, f, "win-1")
	w.maybeQwatch(st)
	q := qwRead(led, st)
	if q.opened == nil {
		t.Fatal("应已开窗")
	}
	if q.fired != 0 {
		t.Fatalf("beats_fired = %d, want 0", q.fired)
	}
	t0 := *q.opened
	want := []float64{t0 + 420.0, t0 + 840.0} // 开窗即排计划
	if !reflect.DeepEqual(q.plan, want) {
		t.Fatalf("plan = %v, want %v", q.plan, want)
	}
	now := clock.Now()
	for _, ts := range q.plan {
		if ts <= now {
			t.Fatalf("plan %v 应全在未来（开窗瞬间不跳）", q.plan)
		}
	}
	if q.snap == nil || q.snap.MTime != q.lastWrite || q.snap.Size != q.size {
		t.Fatalf("snapshot = %+v, want (last_write=%v, size=%d)", q.snap, q.lastWrite, q.size)
	}
}

// ---- 命中谓词四条件真值表 ----

func TestT51PredicateAllFourTrueOpens(t *testing.T) {
	// 靶场景：提问潮 + AskUserQuestion 悬空 → 开窗（悬空⊆{AQ} 直用 Verdict）。
	tmp := t.TempDir()
	projects := filepath.Join(tmp, "projects")
	led := ledger.New()
	w := newTestWatcherW(mergeCfg(), led, nil, nil, nil, nil)
	f := writeMergeTranscript(t, projects, "pred-1", mergeSurge,
		[][2]string{{"t1", "AskUserQuestion"}})
	st := mergeSession(t, led, f, "pred-1")
	w.maybeQwatch(st)
	if qwRead(led, st).opened == nil {
		t.Fatal("四条件全真应开窗")
	}
}

func TestT51PredicateNotSurgeNoOpen(t *testing.T) {
	tmp := t.TempDir()
	projects := filepath.Join(tmp, "projects")
	led := ledger.New()
	w := newTestWatcherW(mergeCfg(), led, nil, nil, nil, nil)
	f := writeMergeTranscript(t, projects, "pred-2", "干完了，没有问题。", nil)
	st := mergeSession(t, led, f, "pred-2")
	w.maybeQwatch(st)
	if qwRead(led, st).opened != nil {
		t.Fatal("非提问潮不应开窗")
	}
}

func TestT51PredicateDanglingOtherToolNoOpen(t *testing.T) {
	// 悬空含非 AskUserQuestion 工具（条件②假）→ 不开窗。
	tmp := t.TempDir()
	projects := filepath.Join(tmp, "projects")
	led := ledger.New()
	w := newTestWatcherW(mergeCfg(), led, nil, nil, nil, nil)
	f := writeMergeTranscript(t, projects, "pred-3", mergeSurge, [][2]string{{"t1", "Bash"}})
	st := mergeSession(t, led, f, "pred-3")
	w.maybeQwatch(st)
	if qwRead(led, st).opened != nil {
		t.Fatal("悬空非 AQ 工具不应开窗")
	}
}

func TestT51PredicateSubagentActiveNoOpenThenOpensAfterStop(t *testing.T) {
	// 条件③子代理在飞 → 不开窗（瞬态：不盖版本章，stop 后同版本仍可开）。
	tmp := t.TempDir()
	projects := filepath.Join(tmp, "projects")
	led := ledger.New()
	w := newTestWatcherW(mergeCfg(), led, nil, nil, nil, nil)
	f := writeMergeTranscript(t, projects, "pred-4", mergeSurge, nil)
	st := mergeSession(t, led, f, "pred-4")
	led.SubagentEvent("cc", "pred-4", "start")
	w.maybeQwatch(st)
	if qwRead(led, st).opened != nil {
		t.Fatal("子代理在飞不应开窗")
	}
	led.SubagentEvent("cc", "pred-4", "stop")
	w.maybeQwatch(st)
	if qwRead(led, st).opened == nil {
		t.Fatal("stop 后同版本仍应开窗（瞬态不盖版本章）")
	}
}

func TestT51PredicateCtxBelowMinNoOpen(t *testing.T) {
	// 条件④前缀 < min_ctx_tokens → 不开窗（结论性：盖版本章）。
	tmp := t.TempDir()
	projects := filepath.Join(tmp, "projects")
	led := ledger.New()
	w := newTestWatcherW(mergeCfg(), led, nil, nil, nil, nil)
	w.enrich = func(st *ledger.SessionState) { // 本例富化只到 100
		led.Mu().Lock()
		st.PeakCtx = 100
		led.Mu().Unlock()
	}
	f := writeMergeTranscript(t, projects, "pred-5", mergeSurge, nil)
	st := mergeSession(t, led, f, "pred-5")
	w.maybeQwatch(st)
	if qwRead(led, st).opened != nil {
		t.Fatal("前缀不足不应开窗")
	}
	lw := qwRead(led, st).lastWrite
	if got := w.stampGet(&w.qwatchSeen, winKey{"cc", "pred-5"}); got != lw {
		t.Fatalf("版本章 = %v, want %v（结论性盖章）", got, lw)
	}
}

func TestT51ModeOffNeverOpens(t *testing.T) {
	// mode=off 功能整体关闭：条件全真也不开窗（默认零开销）。
	tmp := t.TempDir()
	projects := filepath.Join(tmp, "projects")
	led := ledger.New()
	cfg := mergeCfg()
	cfg.QuestionWatch.Mode = "off"
	w := newTestWatcherW(cfg, led, nil, nil, nil, nil)
	f := writeMergeTranscript(t, projects, "pred-6", mergeSurge, nil)
	st := mergeSession(t, led, f, "pred-6")
	w.maybeQwatch(st)
	if qwRead(led, st).opened != nil {
		t.Fatal("mode=off 不应开窗")
	}
}

func TestT51DetectionCachedPerWriteVersion(t *testing.T) {
	// 同一写入版本只判一次（不逐轮读盘）；新写入才重判。
	tmp := t.TempDir()
	projects := filepath.Join(tmp, "projects")
	led := ledger.New()
	w := newTestWatcherW(mergeCfg(), led, nil, nil, nil, nil)
	f := writeMergeTranscript(t, projects, "pred-7", "干完了，没有问题。", nil)
	st := mergeSession(t, led, f, "pred-7")
	calls := 0
	real := w.detect
	w.detect = func(path string, minQuestions int) qwatch.Verdict {
		calls++
		return real(path, minQuestions)
	}
	w.maybeQwatch(st)
	w.maybeQwatch(st)
	w.maybeQwatch(st)
	if calls != 1 {
		t.Fatalf("detect 调用数 = %d, want 1（版本章缓存）", calls)
	}
}

// ---- 两窗互斥（先开者赢） ----

func TestT51ParkingWindowOpenBlocksQwatchOpen(t *testing.T) {
	// 停车窗开着 → 不开等答复窗（先开者赢；瞬态阻塞不盖版本章）。
	// Python 以 fake parking_open 替身驱动；Go Daemon 为具体类型——以真
	// Daemon 摆窗等价驱动（TestMergeParkedWindowBlocksQwatchOpen 同法）。
	tmp := t.TempDir()
	projects := filepath.Join(tmp, "projects")
	led := ledger.New()
	d := NewDaemon(mergeCfg(), led, nil, func(*ledger.SessionState) bool { return true }, nil, 0, nil)
	w := newTestWatcherW(mergeCfg(), led, nil, d, nil, nil)
	f := writeMergeTranscript(t, projects, "mutex-1", mergeSurge, nil)
	st := mergeSession(t, led, f, "mutex-1")
	key := winKey{"cc", "mutex-1"}
	d.windows[key] = &waitWindow{OpenedTS: clock.Now()}
	w.maybeQwatch(st)
	if qwRead(led, st).opened != nil {
		t.Fatal("停车窗开着不应开等答复窗")
	}
	delete(d.windows, key) // 停车窗闭 → 同版本仍可开
	w.maybeQwatch(st)
	if qwRead(led, st).opened == nil {
		t.Fatal("停车窗闭后应可开")
	}
}

func TestT51ParkingProbeLeakGuardAndNeverRaises(t *testing.T) {
	// parking_open 探测：泄漏期旧窗视同已闭；异常按未开（Go 无异常源：全内存
	// +吞错，由探测纯读承接）。
	led := ledger.New()
	d := NewDaemon(config.Default(), led, nil, func(*ledger.SessionState) bool { return true }, nil, 0, nil)
	if d.ParkingOpen("cc", "s") {
		t.Fatal("无窗应 false")
	}
	key := winKey{"cc", "s"}
	d.windows[key] = &waitWindow{OpenedTS: clock.Now()}
	if !d.ParkingOpen("cc", "s") {
		t.Fatal("活跃窗应 true")
	}
	d.windows[key].OpenedTS = clock.Now() - 3601
	if d.ParkingOpen("cc", "s") {
		t.Fatal("泄漏口径：超 SUBAGENT_EVENT_LEAK_S 应视同已闭")
	}
}

func TestT51QWatchWindowBlocksParkingOpen(t *testing.T) {
	// /subagent start 不开停车窗（"反之亦然"侧）；等答复窗不受 start/stop 影响。
	led := ledger.New()
	f := "C:/nonexistent/mx.jsonl"
	st := led.Touch("cc", "mx-1", f, clock.Now(), 1, 0)
	setOpened(led, st, clock.Now())
	d := NewDaemon(config.Default(), led, nil, func(*ledger.SessionState) bool { return true }, nil, 0, nil)
	if _, err := d.Subagent(map[string]any{"event": "start", "agent": "cc", "session_id": "mx-1"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := d.windows[winKey{"cc", "mx-1"}]; ok {
		t.Fatal("等答复窗开着时不应落停车窗")
	}
	if !led.SubagentActive("cc", "mx-1") {
		t.Fatal("台账计数应照常")
	}
	if _, err := d.Subagent(map[string]any{"event": "stop", "agent": "cc", "session_id": "mx-1"}); err != nil {
		t.Fatal(err)
	}
	if qwRead(led, st).opened == nil {
		t.Fatal("等答复窗只有新写入才关")
	}
}

func TestT51ParkingOpensNormallyWithoutQwatchWindow(t *testing.T) {
	// 无等答复窗时停车窗照开（既有 T41 行为零改动）。
	led := ledger.New()
	d := NewDaemon(config.Default(), led, nil, func(*ledger.SessionState) bool { return true }, nil, 0, nil)
	if _, err := d.Subagent(map[string]any{"event": "start", "agent": "cc", "session_id": "mx-2"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := d.windows[winKey{"cc", "mx-2"}]; !ok {
		t.Fatal("无等答复窗时 start 应照常开停车窗")
	}
}

// ---- 摆渡推迟与死线 ----

func TestT51WindowDefersRegularEnqueue(t *testing.T) {
	// 窗口期间：闲置达 summarize 也不入队（推迟）；无窗对照照常入队。
	tmp := t.TempDir()
	projects := filepath.Join(tmp, "projects")
	led := ledger.New()
	w := newTestWatcherW(mergeCfg(), led, nil, nil, nil, nil)
	var enq []string
	w.Enqueue = func(st *ledger.SessionState) bool {
		enq = append(enq, st.SessionID)
		return true
	}
	f1 := writeMergeTranscript(t, projects, "defer-1", mergeSurge, nil)
	st1 := mergeSession(t, led, f1, "defer-1")
	w.maybeQwatch(st1)
	if qwRead(led, st1).opened == nil {
		t.Fatal("前提：窗口应已开")
	}
	shiftLastWrite(led, st1, -30) // ≥ summarize(10)，< 死线(60)
	w.maybeEnqueue(st1)
	if len(enq) != 0 {
		t.Fatalf("窗口期间应推迟, enq = %v", enq)
	}
	f2 := writeMergeTranscript(t, projects, "defer-2", "干完了，没有问题。", nil)
	st2 := mergeSession(t, led, f2, "defer-2") // 对照：无窗
	shiftLastWrite(led, st2, -30)
	w.maybeEnqueue(st2)
	if !reflect.DeepEqual(enq, []string{"defer-2"}) {
		t.Fatalf("enq = %v, want [defer-2]", enq)
	}
}

func TestT51DeadlineForcesEnqueuePastDangling(t *testing.T) {
	// 死线（idle ≥ block_s − lead）：强制入队，不再因悬空（含 AQ）让步。
	tmp := t.TempDir()
	projects := filepath.Join(tmp, "projects")
	led := ledger.New()
	w := newTestWatcherW(mergeCfg(), led, nil, nil, nil, nil)
	var enq []string
	w.Enqueue = func(st *ledger.SessionState) bool {
		enq = append(enq, st.SessionID)
		return true
	}
	f := writeMergeTranscript(t, projects, "dl-1", mergeSurge,
		[][2]string{{"t1", "AskUserQuestion"}})
	st := mergeSession(t, led, f, "dl-1")
	w.maybeQwatch(st)
	setLastWrite(led, st, clock.Now()-70) // ≥ 死线 60
	w.maybeEnqueue(st)
	if !reflect.DeepEqual(enq, []string{"dl-1"}) {
		t.Fatalf("enq = %v, want [dl-1]", enq)
	}
	if qwRead(led, st).handedOff <= 0 {
		t.Fatal("入队即记（防重复入队）")
	}
}

func TestT51DeadlineBoundaryExactIdleIsDue(t *testing.T) {
	// idle 恰等于死线（block_s − lead）→ 已到，强制入队。
	tmp := t.TempDir()
	projects := filepath.Join(tmp, "projects")
	led := ledger.New()
	w := newTestWatcherW(mergeCfg(), led, nil, nil, nil, nil)
	var enq []string
	w.Enqueue = func(st *ledger.SessionState) bool {
		enq = append(enq, st.SessionID)
		return true
	}
	f := writeMergeTranscript(t, projects, "dl-2", mergeSurge, nil)
	st := mergeSession(t, led, f, "dl-2")
	w.maybeQwatch(st)
	setLastWrite(led, st, clock.Now()-60)
	w.maybeEnqueue(st)
	if !reflect.DeepEqual(enq, []string{"dl-2"}) {
		t.Fatalf("enq = %v, want [dl-2]", enq)
	}
}

func TestT51NoWindowDanglingDefersUnchanged(t *testing.T) {
	// 无窗会话语义零改动：悬空照旧推迟、摆渡阈值照旧。
	tmp := t.TempDir()
	projects := filepath.Join(tmp, "projects")
	led := ledger.New()
	w := newTestWatcherW(mergeCfg(), led, nil, nil, nil, nil)
	var enq []string
	w.Enqueue = func(st *ledger.SessionState) bool {
		enq = append(enq, st.SessionID)
		return true
	}
	f := writeMergeTranscript(t, projects, "old-1", mergeSurge, [][2]string{{"t1", "Bash"}})
	st := mergeSession(t, led, f, "old-1")
	shiftLastWrite(led, st, -70)
	w.maybeEnqueue(st)
	if len(enq) != 0 {
		t.Fatalf("悬空推迟仍在（无窗不受死线豁免）, enq = %v", enq)
	}
}

func TestT51QWatchDeadlineHelperTruthTable(t *testing.T) {
	// qwatch_deadline 二元组真值表：无窗 (F,F)；窗内未到 (F,T)；到线 (T,T)。
	led := ledger.New()
	w := newTestWatcherW(mergeCfg(), led, nil, nil, nil, nil)
	th := w.Cfg.ThresholdFor("cc")
	if due, window := w.qwatchDeadline(false, clock.Now(), th); due || window {
		t.Fatalf("无窗 = (%v,%v), want (false,false)", due, window)
	}
	if due, window := w.qwatchDeadline(true, clock.Now()-59, th); due || !window {
		t.Fatalf("窗内未到 = (%v,%v), want (false,true)", due, window)
	}
	if due, window := w.qwatchDeadline(true, clock.Now()-60, th); !due || !window {
		t.Fatalf("死线已到 = (%v,%v), want (true,true)", due, window)
	}
}

// ---- 异常吞掉（T48 同款纪律） ----

func TestT51DetectExceptionSwallowed(t *testing.T) {
	tmp := t.TempDir()
	projects := filepath.Join(tmp, "projects")
	led := ledger.New()
	w := newTestWatcherW(mergeCfg(), led, nil, nil, nil, nil)
	f := writeMergeTranscript(t, projects, "boom-1", mergeSurge, nil)
	st := mergeSession(t, led, f, "boom-1")
	w.detect = func(string, int) qwatch.Verdict { panic("boom") }
	w.maybeQwatch(st) // 不外抛（recover 兜底）
	if qwRead(led, st).opened != nil {
		t.Fatal("炸点路径不应开窗")
	}
}

func TestT51ParkingProbeExceptionSwallowed(t *testing.T) {
	// parking_open 抛错 → 按未开处理路径吞掉，不影响守望。
	// Go 差异声明：Daemon 为具体类型，ParkingOpen 无炸点可注（全内存+吞错）
	// ——本例以接线真 Daemon + detect 炸点走同一条外层 recover 兜底路径，
	// 证守望主路径不炸（与 Python 探针炸点同机位）。
	tmp := t.TempDir()
	projects := filepath.Join(tmp, "projects")
	led := ledger.New()
	d := NewDaemon(mergeCfg(), led, nil, func(*ledger.SessionState) bool { return true }, nil, 0, nil)
	w := newTestWatcherW(mergeCfg(), led, nil, d, nil, nil)
	f := writeMergeTranscript(t, projects, "boom-2", mergeSurge, nil)
	st := mergeSession(t, led, f, "boom-2")
	w.detect = func(string, int) qwatch.Verdict { panic("boom") }
	w.maybeQwatch(st) // 不外抛（外层兜底）
	if qwRead(led, st).opened != nil {
		t.Fatal("炸点路径不应开窗")
	}
}

// ---- 关窗后重开（重新计时） ----

func TestT51ReopenAfterWriteResetsTimer(t *testing.T) {
	// 新写入关窗 → 同轮末条仍是提问潮 → 重开新窗（opened_ts/snapshot 刷新）。
	tmp := t.TempDir()
	projects := filepath.Join(tmp, "projects")
	led := ledger.New()
	w := newTestWatcherW(mergeCfg(), led, nil, nil, nil, nil)
	f := writeMergeTranscript(t, projects, "re-1", mergeSurge, nil)
	st := mergeSession(t, led, f, "re-1")
	w.maybeQwatch(st)
	q1 := qwRead(led, st)
	if q1.opened == nil {
		t.Fatal("前提：应已开窗")
	}
	newMtime := clock.Now() + 10
	utimeFile(t, f, newMtime)
	info, err := os.Stat(f)
	if err != nil {
		t.Fatal(err)
	}
	st2 := led.Touch("cc", "re-1", f, statMTime(info), int(info.Size()), 0) // 用户提交 → 关窗
	if st2 != st {
		t.Fatal("lineage 应复用同一状态")
	}
	if qwRead(led, st).opened != nil {
		t.Fatal("新写入应关窗")
	}
	w.maybeQwatch(st) // 同轮判定：末条仍是提问潮
	q3 := qwRead(led, st)
	if q3.opened == nil {
		t.Fatal("应重开新窗")
	}
	if *q3.opened < *q1.opened {
		t.Fatal("应重新计时")
	}
	if q3.snap == q1.snap {
		t.Fatal("snapshot 应刷新")
	}
	if q3.snap.MTime != q3.lastWrite || q3.snap.Size != q3.size {
		t.Fatalf("snapshot = %+v, want (last_write=%v, size=%d)", q3.snap, q3.lastWrite, q3.size)
	}
}

func TestT51CloseResumesRegularFerryScheduling(t *testing.T) {
	// 关窗即恢复常规摆渡调度：窗口推迟解除，达 summarize 照常入队。
	tmp := t.TempDir()
	projects := filepath.Join(tmp, "projects")
	led := ledger.New()
	w := newTestWatcherW(mergeCfg(), led, nil, nil, nil, nil)
	var enq []string
	w.Enqueue = func(st *ledger.SessionState) bool {
		enq = append(enq, st.SessionID)
		return true
	}
	f := writeMergeTranscript(t, projects, "rs-1", mergeSurge, nil)
	st := mergeSession(t, led, f, "rs-1")
	w.maybeQwatch(st)
	shiftLastWrite(led, st, -30)
	w.maybeEnqueue(st)
	if len(enq) != 0 {
		t.Fatalf("窗口期间推迟, enq = %v", enq)
	}
	appendJSONLine(t, f, mergeAssistant("收到，逐条答如下。没有问题。", nil, "msg_2")) // 用户新一轮落盘（末条非提问潮）
	newM := clock.Now() + 5
	utimeFile(t, f, newM)
	info, err := os.Stat(f)
	if err != nil {
		t.Fatal(err)
	}
	led.Touch("cc", "rs-1", f, statMTime(info), int(info.Size()), 0) // 新写入 → 关窗
	w.maybeQwatch(st)
	if qwRead(led, st).opened != nil {
		t.Fatal("末条非提问潮不重开")
	}
	setLastWrite(led, st, clock.Now()-30) // 重新攒闲置达 summarize
	w.maybeEnqueue(st)
	if !reflect.DeepEqual(enq, []string{"rs-1"}) {
		t.Fatalf("关窗后应恢复常规入队, enq = %v", enq)
	}
}

func TestT51CloseThenCalmNoReopen(t *testing.T) {
	// 关窗后末条非提问潮 → 不重开（恢复常规摆渡调度）。
	tmp := t.TempDir()
	projects := filepath.Join(tmp, "projects")
	led := ledger.New()
	w := newTestWatcherW(mergeCfg(), led, nil, nil, nil, nil)
	f := writeMergeTranscript(t, projects, "re-2", mergeSurge, nil)
	st := mergeSession(t, led, f, "re-2")
	w.maybeQwatch(st)
	if qwRead(led, st).opened == nil {
		t.Fatal("前提：应已开窗")
	}
	appendJSONLine(t, f, mergeAssistant("收到，逐条答如下。没有问题。", nil, "msg_2")) // 用户作答落盘（末条非提问潮）
	newM := clock.Now() + 5
	utimeFile(t, f, newM)
	info, err := os.Stat(f)
	if err != nil {
		t.Fatal(err)
	}
	led.Touch("cc", "re-2", f, statMTime(info), int(info.Size()), 0)
	w.maybeQwatch(st)
	if qwRead(led, st).opened != nil {
		t.Fatal("末条非提问潮不应重开")
	}
}

// ---- 票13 评审 Minor A · D 补测：窗口读者 × Touch 写者并发 ----

func TestWatcherWindowReaderVsTouchWriterNoRace(t *testing.T) {
	// goroutine 交错 TouchFull（带 cwd/peak_ctx 写者）× 窗口读者
	// （recordWindowLocked/Acct 快照、WindowWait/ParkingOpen、maybeQwatch 开窗
	// 临界区、maybeEnqueue 读点）——A 修复（GetLocked 双锁快照）的并发回归：
	// 逻辑断言不 race（-race 下无数据竞争、无死锁、终态台账一致）。
	tmp := t.TempDir()
	projects := filepath.Join(tmp, "projects")
	led := ledger.New()
	accts, err := accounts.New(filepath.Join(tmp, "data"))
	if err != nil {
		t.Fatal(err)
	}
	d := NewDaemon(mergeCfg(), led, nil, func(*ledger.SessionState) bool { return true }, accts, 0, nil)
	w := newTestWatcherW(mergeCfg(), led, accts, d, nil, nil)

	const nsess = 4
	sessions := make([]*ledger.SessionState, nsess)
	for g := 0; g < nsess; g++ {
		sid := "dw-" + string(rune('0'+g))
		f := writeMergeTranscript(t, projects, sid, mergeSurge,
			[][2]string{{"tu_aq_" + string(rune('0'+g)), "AskUserQuestion"}})
		info, err := os.Stat(f)
		if err != nil {
			t.Fatal(err)
		}
		st := led.TouchFull("cc", sid, f, statMTime(info), int(info.Size()),
			"C:/dw-proj", "", 20000, 0)
		sessions[g] = st
	}

	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			st := sessions[g%nsess]
			sid := st.SessionID
			for i := 0; i < 25; i++ {
				if g < 4 { // 写者：TouchFull 携 cwd/peak_ctx（A 修复针对的写者面）
					led.TouchFull("cc", sid, st.TranscriptPath,
						clock.Now()+float64(i)*1e-3, st.Size+i, "C:/dw-proj", "", 20000+i, 0)
					continue
				}
				// 读者：窗口机 + 守望判定（recordWindow 经 start/stop 闭账驱动）
				switch (g + i) % 4 {
				case 0:
					_, _ = d.Subagent(map[string]any{"event": "start", "agent": "cc", "session_id": sid})
					_, _ = d.Subagent(map[string]any{"event": "stop", "agent": "cc", "session_id": sid})
				case 1:
					_ = d.WindowWait("cc", sid)
					_ = d.ParkingOpen("cc", sid)
				case 2:
					w.maybeQwatch(st)
				default:
					w.maybeEnqueue(st)
				}
			}
		}(g)
	}
	wg.Wait()

	// 终态逻辑断言：每会话仍恰一条台账、窗口态字段自洽（无撕裂）。
	seen := map[string]int{}
	for _, s := range led.AllSessions() {
		seen[s.SessionID]++
		if s.QWatchOpenedTS != nil && s.QWatchSnapshot == nil {
			t.Fatalf("%s 窗口态撕裂：开窗而无快照", s.SessionID)
		}
	}
	for g := 0; g < nsess; g++ {
		sid := "dw-" + string(rune('0'+g))
		if seen[sid] != 1 {
			t.Fatalf("%s 台账条目数 = %d, want 1", sid, seen[sid])
		}
	}
	if len(seen) != nsess {
		t.Fatalf("台账会话数 = %d, want %d", len(seen), nsess)
	}
}
