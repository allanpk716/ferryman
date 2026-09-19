package daemon

// 规格：tests/test_qwatch_scheduler.py（T51 票03 · 心跳调度器 26 例）的票16 落点。
//
// 归属拆分（316 清点闭环）：
//   - 纯逻辑 3 例（classify_three_states / breaker_unit_transparent_error_and_reset /
//     beat_sender_protocol_shape）——票09 已收编转绿于 internal/beat/beat_test.go
//     （TestClassifyThreeStates / TestBreakerUnitTransparentErrorAndReset /
//     TestNoopSenderObserveDrillContract），不重复立目；
//   - 配置死线余量 5 例（config_wall_timeout_* / config_load_warns_on_default_lead）
//     ——票09 已收编于 internal/config/config_test.go
//     （TestValidateClampsLeadWithWarnings / TestValidateLeadAboveWallClockNoWarn /
//     TestValidateSkipsLeadChecksWhenOff / TestLoadPartialSectionKeepsDefaults 等），
//     不重复立目；
//   - RLock 可重入 1 例（test_ledger_lock_reentrant_and_shared）——Go Mutex 不可
//     重入，Go 契约转译为"持锁临界区用 *Locked 内方法"（本文件 TestLedgerLock-
//     SharedShape）；
//   - 其余调度例本文件 1:1。
//
// 时间纪律：clock.Now 真实时钟；Touch 的 mtime 取文件真实 st_mtime（与生产
// pollCC 同口径——两道验②比对的就是它）；全局串行用例以真 goroutine + 阻塞
// sender 驱动。

import (
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"ferryman/internal/accounts"
	"ferryman/internal/beat"
	"ferryman/internal/clock"
	"ferryman/internal/config"
	"ferryman/internal/ledger"
)

// ---- 语料（与票02 同款；merge 助手复用 merge_t51_parking_mutex_test.go） ----

// ---- 装配 ----

// schedTouch mtime 用文件真实 st_mtime（与生产 pollCC 同口径）——两道验②
// 比对的就是它。
func schedTouch(t *testing.T, led *ledger.Ledger, projects, sid string) (*ledger.SessionState, string) {
	t.Helper()
	f := writeMergeTranscript(t, projects, sid, mergeSurge, nil)
	info, err := os.Stat(f)
	if err != nil {
		t.Fatal(err)
	}
	st := led.Touch("cc", sid, f, statMTime(info), int(info.Size()), 0)
	if !st.ObservedActive {
		t.Fatal("observed_active 应为 true")
	}
	return st, f
}

// bareSession 不经开窗谓词的裸会话（调度器单测直接摆窗口字段）。
func bareSession(t *testing.T, led *ledger.Ledger, dir, sid string) (*ledger.SessionState, string) {
	t.Helper()
	f := filepath.Join(dir, sid+".jsonl")
	if err := os.WriteFile(f, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(f)
	if err != nil {
		t.Fatal(err)
	}
	st := led.Touch("cc", sid, f, statMTime(info), int(info.Size()), 0)
	return st, f
}

// ---- fake sender ----

// recordingSender fake sender：记录收到的 BeatPlan，按脚本吐 BeatResult（默认 HIT）。
type recordingSender struct {
	mu      sync.Mutex
	plans   []beat.BeatPlan
	results []beat.BeatResult
	idx     int
}

func (r *recordingSender) Send(plan beat.BeatPlan) beat.BeatResult {
	r.mu.Lock()
	r.plans = append(r.plans, plan)
	r.mu.Unlock()
	if r.idx < len(r.results) {
		b := r.results[r.idx]
		r.idx++
		return b
	}
	return beat.BeatResult{Sent: true, OK: true, InputTokens: 100,
		CacheReadTokens: 1900, Provider: "cc-proxy", Model: "glm-4", CostActual: 0.01}
}

func (r *recordingSender) plansList() []beat.BeatPlan {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]beat.BeatPlan(nil), r.plans...)
}

// blockingSender fake sender：阻塞在 send 里直到放行（观测全局串行）。
type blockingSender struct {
	entered chan struct{} // 首次 send 进入即关闭（Python Event.set）
	release chan struct{} // 测试关闭放行（Event.set 后恒通过，同语义）
	count   atomic.Int32
}

func newBlockingSender() *blockingSender {
	return &blockingSender{entered: make(chan struct{}), release: make(chan struct{})}
}

func (b *blockingSender) Send(beat.BeatPlan) beat.BeatResult {
	if b.count.Add(1) == 1 {
		close(b.entered)
	}
	<-b.release
	return beat.BeatResult{Sent: true, OK: true, InputTokens: 100, CacheReadTokens: 1900}
}

// panicSender 注入炸点（Python monkeypatch _send_beat=boom 的 Go 形——sender
// 协议内炸，sendBeat recover 承接）。
type panicSender struct{}

func (panicSender) Send(beat.BeatPlan) beat.BeatResult { panic("boom") }

var (
	beatHit  = beat.BeatResult{Sent: true, OK: true, InputTokens: 100, CacheReadTokens: 1900}
	beatMiss = beat.BeatResult{Sent: true, OK: true, InputTokens: 2000, CacheReadTokens: 0}
	beatErr  = beat.BeatResult{Sent: true, OK: false, Err: "429-after-retry"}
)

// captureStdout capsys 的 Go 形（fmt.Printf 直写 os.Stdout）。测试串行，无交叉。
func captureStdout(t *testing.T) (read func() string) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	done := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
		_ = r.Close()
	}()
	return func() string {
		_ = w.Close()
		os.Stdout = old
		return <-done
	}
}

// ---- 计划生成（验收①） ----

func TestPlanScheduledOnOpenNoImmediateFire(t *testing.T) {
	// 开窗排计划：max_beats 跳按 beat_interval_s 排定；首跳在 t0+interval。
	tmp := t.TempDir()
	projects := filepath.Join(tmp, "projects")
	led := ledger.New()
	cfg := mergeCfg()
	cfg.QuestionWatch.BeatIntervalS = 300.0
	cfg.QuestionWatch.MaxBeats = 3
	w := newTestWatcherW(cfg, led, nil, nil, nil, nil)
	st, _ := schedTouch(t, led, projects, "plan-1")
	w.maybeQwatch(st)
	q := qwRead(led, st)
	if q.opened == nil {
		t.Fatal("应已开窗")
	}
	t0 := *q.opened
	want := []float64{t0 + 300.0, t0 + 600.0, t0 + 900.0}
	if !reflect.DeepEqual(q.plan, want) {
		t.Fatalf("plan = %v, want %v", q.plan, want)
	}
	now := clock.Now()
	for _, ts := range q.plan {
		if ts <= now {
			t.Fatalf("plan %v 应全在未来（开窗瞬间不跳）", q.plan)
		}
	}
}

func TestNothingDueNoFire(t *testing.T) {
	// 未到期的跳不发；到期恰发一跳（出队＋beats_fired 计数）。
	led := ledger.New()
	tmp := t.TempDir()
	cfg := mergeCfg()
	cfg.QuestionWatch.Mode = "enforce"
	rec := &recordingSender{}
	w := newTestWatcherW(cfg, led, nil, nil, rec, nil)
	st, _ := bareSession(t, led, tmp, "due-1")
	openWindow(led, st, 9999.0, 2)
	w.maybeFireBeats(st)
	if got := len(rec.plansList()); got != 0 {
		t.Fatalf("全在未来不应发, plans = %d", got)
	}
	future := clock.Now() + 999
	setPlan(led, st, []float64{clock.Now() - 1, future})
	w.maybeFireBeats(st)
	if got := len(rec.plansList()); got != 1 {
		t.Fatalf("一轮至多一发, plans = %d", got)
	}
	if q := qwRead(led, st); q.fired != 1 {
		t.Fatalf("beats_fired = %d, want 1", q.fired)
	}
	if q := qwRead(led, st); !reflect.DeepEqual(q.plan, []float64{future}) {
		t.Fatalf("plan = %v, want 未到期跳保留 [future]", q.plan)
	}
}

// ---- 两道验（验收②） ----

func TestTwoCheckFileStatChangedNoSendVoidsPlan(t *testing.T) {
	// 两道验②：预检通过但转录 mtime/size 变 → 不发并作废剩余计划。
	led := ledger.New()
	tmp := t.TempDir()
	accts, err := accounts.New(filepath.Join(tmp, "data"))
	if err != nil {
		t.Fatal(err)
	}
	w := newTestWatcherW(mergeCfg(), led, accts, nil, nil, nil)
	st, f := bareSession(t, led, tmp, "tc-1")
	openWindow(led, st, 420.0, 2)
	setPlan(led, st, []float64{clock.Now() - 1, clock.Now() + 999})
	if werr := os.WriteFile(f, []byte("{}\n{}\n"), 0o644); werr != nil { // 用户提交：文件变 + mtime 变
		t.Fatal(werr)
	}
	newT := qwRead(led, st).lastWrite + 5
	utimeFile(t, f, newT)
	w.maybeFireBeats(st)
	if rows := accts.Read(accounts.ReadOpts{Kind: "beat"}); len(rows) != 0 {
		t.Fatalf("未发, beat 行 = %v", rows)
	}
	q := qwRead(led, st)
	if len(q.plan) != 0 {
		t.Fatalf("剩余计划应作废, plan = %v", q.plan)
	}
	if q.fired != 0 {
		t.Fatalf("beats_fired = %d, want 0", q.fired)
	}
	if q.opened == nil {
		t.Fatal("窗留给 touch 关（不越权）")
	}
}

func TestTwoCheckLedgerVersionMovedNoSend(t *testing.T) {
	// 两道验①：台账 last_write 版本章前进（快照基线后见过新写入）→ 不发作废。
	led := ledger.New()
	tmp := t.TempDir()
	accts, err := accounts.New(filepath.Join(tmp, "data"))
	if err != nil {
		t.Fatal(err)
	}
	w := newTestWatcherW(mergeCfg(), led, accts, nil, nil, nil)
	st, _ := bareSession(t, led, tmp, "tc-2")
	openWindow(led, st, 420.0, 2)
	setPlan(led, st, []float64{clock.Now() - 1})
	snapMTime := qwRead(led, st).snap.MTime
	setLastWrite(led, st, snapMTime+5) // 模拟：写已入账、窗未及关
	w.maybeFireBeats(st)
	if rows := accts.Read(accounts.ReadOpts{Kind: "beat"}); len(rows) != 0 {
		t.Fatalf("不应发, beat 行 = %v", rows)
	}
	if q := qwRead(led, st); len(q.plan) != 0 {
		t.Fatalf("plan = %v, want 作废", q.plan)
	}
}

func TestNewWriteCancelsPendingBeats(t *testing.T) {
	// 任何新写入 → 关窗＋剩余跳全部作废（守望轮询粒度内生效）。
	led := ledger.New()
	tmp := t.TempDir()
	accts, err := accounts.New(filepath.Join(tmp, "data"))
	if err != nil {
		t.Fatal(err)
	}
	w := newTestWatcherW(mergeCfg(), led, accts, nil, nil, nil)
	st, f := bareSession(t, led, tmp, "cc-1")
	openWindow(led, st, 420.0, 2)
	setPlan(led, st, []float64{clock.Now() + 999}) // 未来跳
	if werr := os.WriteFile(f, []byte("{}\n{}\n"), 0o644); werr != nil {
		t.Fatal(werr)
	}
	info, err := os.Stat(f)
	if err != nil {
		t.Fatal(err)
	}
	// Python 同款 mtime=now_s()+5：Windows 文件时钟粒度 ~0.5-15ms，同 tick 内
	// 重写的 stat mtime 不前进——显式给未来值（os.utime 同位）保证"新写入"。
	newM := clock.Now() + 5
	utimeFile(t, f, newM)
	led.Touch("cc", "cc-1", f, newM, int(info.Size()), 0) // 新写入 → 关窗清计划
	w.maybeFireBeats(st)
	q := qwRead(led, st)
	if q.opened != nil || len(q.plan) != 0 {
		t.Fatalf("应关窗清计划: %+v", q)
	}
	if rows := accts.Read(accounts.ReadOpts{Kind: "beat"}); len(rows) != 0 {
		t.Fatalf("一跳都没发, beat 行 = %v", rows)
	}
}

// ---- 全局串行（验收④） ----

func TestGlobalSerialOneBeatInFlight(t *testing.T) {
	// 两窗同刻到期：sender 阻塞期间第二窗不进（全局同时最多 1 跳在途）。
	led := ledger.New()
	tmp := t.TempDir()
	blocker := newBlockingSender()
	cfg := mergeCfg()
	cfg.QuestionWatch.Mode = "enforce"
	w := newTestWatcherW(cfg, led, nil, nil, blocker, nil)
	stA, _ := bareSession(t, led, tmp, "ser-a")
	stB, _ := bareSession(t, led, tmp, "ser-b")
	openWindow(led, stA, 420.0, 2)
	openWindow(led, stB, 420.0, 2)
	due := clock.Now() - 1
	setPlan(led, stA, []float64{due})
	setPlan(led, stB, []float64{due})
	done := make(chan struct{})
	go func() {
		defer close(done)
		w.maybeFireBeats(stA)
	}()
	select { // A 已进 sender（在途）
	case <-blocker.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("A 未进 sender")
	}
	w.maybeFireBeats(stB) // B 到期但 A 在途 → 跳过
	if got := blocker.count.Load(); got != 1 {
		t.Fatalf("在途占位失效: count = %d, want 1", got)
	}
	close(blocker.release)
	<-done
	w.maybeFireBeats(stB) // A 出场后 B 下轮照发
	if got := blocker.count.Load(); got != 2 {
		t.Fatalf("count = %d, want 2", got)
	}
}

// ---- observe 零网络（验收⑤） ----

func TestObserveModeNeverCallsInjectedSender(t *testing.T) {
	// mode=observe：注入的 sender 不被调（零网络）；演练跳照落账（标 observe）。
	led := ledger.New()
	tmp := t.TempDir()
	accts, err := accounts.New(filepath.Join(tmp, "data"))
	if err != nil {
		t.Fatal(err)
	}
	rec := &recordingSender{}
	w := newTestWatcherW(mergeCfg(), led, accts, nil, rec, nil)
	st, _ := bareSession(t, led, tmp, "ob-1")
	openWindow(led, st, 420.0, 2)
	setPlan(led, st, []float64{clock.Now() - 1})
	w.maybeFireBeats(st)
	if got := rec.plansList(); len(got) != 0 {
		t.Fatalf("observe 应零网络, plans = %d", len(got))
	}
	rows := accts.Read(accounts.ReadOpts{Kind: "beat"})
	if len(rows) != 1 || rows[0]["outcome"] != "observe" {
		t.Fatalf("beat 行 = %v, want 1 条 observe", rows)
	}
	if rows[0]["cache_read"] != float64(0) {
		t.Fatalf("cache_read = %v, want 0", rows[0]["cache_read"])
	}
	if q := qwRead(led, st); q.fired != 1 {
		t.Fatalf("beats_fired = %d, want 1", q.fired)
	}
}

func TestEnforceWithoutRealSenderDrillsWithWarning(t *testing.T) {
	// enforce 未注入真实 sender（Q14 前）：按 observe 演练记账＋告警一次。
	led := ledger.New()
	tmp := t.TempDir()
	accts, err := accounts.New(filepath.Join(tmp, "data"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := mergeCfg()
	cfg.QuestionWatch.Mode = "enforce"
	w := newTestWatcherW(cfg, led, accts, nil, nil, nil)
	st, _ := bareSession(t, led, tmp, "en-1")
	openWindow(led, st, 420.0, 2)
	read := captureStdout(t)
	setPlan(led, st, []float64{clock.Now() - 1})
	w.maybeFireBeats(st)
	setPlan(led, st, []float64{clock.Now() - 1})
	w.maybeFireBeats(st)
	out := read()
	if got := strings.Count(out, "observe 演练"); got != 1 {
		t.Fatalf("告警次数 = %d, want 1（只告警一次）\nout = %s", got, out)
	}
	rows := accts.Read(accounts.ReadOpts{Kind: "beat"})
	if len(rows) != 2 || rows[0]["outcome"] != "observe" || rows[1]["outcome"] != "observe" {
		t.Fatalf("beat 行 = %v, want observe ×2", rows)
	}
}

// ---- 三态（验收⑥；Classify 纯函数归 beat_test.go） ----

func TestThreeStatesBookedFromFakeSender(t *testing.T) {
	// fake sender 注入三态：每跳 outcome/实收逐条入既有 beat 科目。
	led := ledger.New()
	tmp := t.TempDir()
	accts, err := accounts.New(filepath.Join(tmp, "data"))
	if err != nil {
		t.Fatal(err)
	}
	rec := &recordingSender{results: []beat.BeatResult{
		beatHit,
		{Sent: true, OK: true, InputTokens: 1500, CacheReadTokens: 100,
			Provider: "cc-proxy", Model: "glm-4"},
		{Sent: true, OK: false, Err: "5xx"},
	}}
	cfg := mergeCfg()
	cfg.QuestionWatch.Mode = "enforce"
	w := newTestWatcherW(cfg, led, accts, nil, rec, nil)
	st, _ := bareSession(t, led, tmp, "tri-1")
	openWindow(led, st, 420.0, 2)
	for i := 0; i < 3; i++ {
		setPlan(led, st, []float64{clock.Now() - 1}) // 逐轮补一枚到期跳
		w.maybeFireBeats(st)
	}
	rows := accts.Read(accounts.ReadOpts{Kind: "beat"})
	if len(rows) != 3 {
		t.Fatalf("beat 行数 = %d, want 3", len(rows))
	}
	wantOutcomes := []any{"hit", "miss", "error"}
	for i, want := range wantOutcomes {
		if rows[i]["outcome"] != want {
			t.Fatalf("rows[%d][outcome] = %v, want %v", i, rows[i]["outcome"], want)
		}
	}
	if rows[0]["cache_read"] != float64(1900) {
		t.Fatalf("cache_read = %v, want 1900", rows[0]["cache_read"])
	}
	if rows[1]["provider"] != "cc-proxy" || rows[1]["model"] != "glm-4" {
		t.Fatalf("provider/model = %v/%v", rows[1]["provider"], rows[1]["model"])
	}
	if rows[0]["cost_actual"] != float64(0) || rows[2]["cache_read"] != float64(0) {
		t.Fatalf("cost_actual/cache_read = %v/%v", rows[0]["cost_actual"], rows[2]["cache_read"])
	}
	if q := qwRead(led, st); q.fired != 3 {
		t.Fatalf("beats_fired = %d, want 3", q.fired)
	}
	plans := rec.plansList()
	if len(plans) != 3 { // 发送器收到计划快照
		t.Fatalf("plans = %d, want 3", len(plans))
	}
	if plans[0].SessionID != "tri-1" {
		t.Fatalf("plan session_id = %q, want tri-1", plans[0].SessionID)
	}
	if plans[0].BeatIndex != 1 { // 第几跳（1 起）
		t.Fatalf("beat_index = %d, want 1", plans[0].BeatIndex)
	}
	for _, r := range rows { // 账面无内容字段（白名单）
		for k := range r {
			if strings.Contains(k, "content") {
				t.Fatalf("账面出现内容字段 %q", k)
			}
		}
	}
}

// ---- 熔断（验收⑦；Breaker 纯逻辑归 beat_test.go） ----

func TestBreakerTwoMissDemotesEnforceToObserve(t *testing.T) {
	// 连续 2 MISS → mode 自动 enforce→observe＋告警；剩余跳不作废（转演练）。
	led := ledger.New()
	tmp := t.TempDir()
	accts, err := accounts.New(filepath.Join(tmp, "data"))
	if err != nil {
		t.Fatal(err)
	}
	rec := &recordingSender{results: []beat.BeatResult{beatMiss, beatMiss}}
	cfg := mergeCfg()
	cfg.QuestionWatch.Mode = "enforce"
	w := newTestWatcherW(cfg, led, accts, nil, rec, nil)
	st, _ := bareSession(t, led, tmp, "bm-1")
	openWindow(led, st, 420.0, 2)
	future := clock.Now() + 999
	setPlan(led, st, []float64{clock.Now() - 1, future})
	read := captureStdout(t)
	w.maybeFireBeats(st)
	if w.qwatchMode() != "enforce" { // 1 次 MISS 不降
		t.Fatal("1 次 MISS 不应降级")
	}
	setPlan(led, st, []float64{clock.Now() - 1, future}) // 再补一枚到期跳
	w.maybeFireBeats(st)
	if w.qwatchMode() != "observe" { // 连续 2 → 降级
		t.Fatal("连续 2 MISS 应降级 enforce→observe")
	}
	out := read()
	if !strings.Contains(out, "MISS") {
		t.Fatalf("告警应落控制台, out = %s", out)
	}
	if q := qwRead(led, st); !reflect.DeepEqual(q.plan, []float64{future}) {
		t.Fatalf("plan = %v, want 剩余跳保留 [future]", q.plan)
	}
}

func TestBreakerErrorNotCountedIntoMissStreak(t *testing.T) {
	// MISS、ERROR、MISS：ERROR 不计入也不重置 MISS 连击 → 仍连续 2 MISS 降级。
	led := ledger.New()
	tmp := t.TempDir()
	rec := &recordingSender{results: []beat.BeatResult{beatMiss, beatErr, beatMiss}}
	cfg := mergeCfg()
	cfg.QuestionWatch.Mode = "enforce"
	w := newTestWatcherW(cfg, led, nil, nil, rec, nil)
	st, _ := bareSession(t, led, tmp, "bm-2")
	for i := 0; i < 3; i++ {
		openWindow(led, st, 420.0, 2)
		setPlan(led, st, []float64{clock.Now() - 1})
		w.maybeFireBeats(st)
	}
	if w.qwatchMode() != "observe" {
		t.Fatalf("mode = %q, want observe", w.qwatchMode())
	}
}

func TestBreakerHitResetsMissStreak(t *testing.T) {
	// MISS、HIT、MISS：真实命中打断连击 → 不降级。
	led := ledger.New()
	tmp := t.TempDir()
	rec := &recordingSender{results: []beat.BeatResult{beatMiss, beatHit, beatMiss}}
	cfg := mergeCfg()
	cfg.QuestionWatch.Mode = "enforce"
	w := newTestWatcherW(cfg, led, nil, nil, rec, nil)
	st, _ := bareSession(t, led, tmp, "bm-3")
	for i := 0; i < 3; i++ {
		openWindow(led, st, 420.0, 2)
		setPlan(led, st, []float64{clock.Now() - 1})
		w.maybeFireBeats(st)
	}
	if w.qwatchMode() != "enforce" {
		t.Fatalf("mode = %q, want enforce", w.qwatchMode())
	}
}

func TestBreakerThreeErrorsPauseWindowOnly(t *testing.T) {
	// 连续 3 ERROR → 暂停当前窗口剩余跳＋告警；mode 不动；他窗不累及。
	led := ledger.New()
	tmp := t.TempDir()
	accts, err := accounts.New(filepath.Join(tmp, "data"))
	if err != nil {
		t.Fatal(err)
	}
	rec := &recordingSender{results: []beat.BeatResult{beatErr, beatErr, beatErr}}
	cfg := mergeCfg()
	cfg.QuestionWatch.Mode = "enforce"
	w := newTestWatcherW(cfg, led, accts, nil, rec, nil)
	stA, _ := bareSession(t, led, tmp, "be-a")
	stB, _ := bareSession(t, led, tmp, "be-b")
	other := clock.Now() + 999
	openWindow(led, stB, 420.0, 2)
	setPlan(led, stB, []float64{other})
	read := captureStdout(t)
	for i := 0; i < 3; i++ {
		openWindow(led, stA, 420.0, 2)
		setPlan(led, stA, []float64{clock.Now() - 1, clock.Now() + 999})
		w.maybeFireBeats(stA)
	}
	out := read()
	if q := qwRead(led, stA); len(q.plan) != 0 { // 本窗剩余跳全停
		t.Fatalf("stA plan = %v, want 空", q.plan)
	}
	if q := qwRead(led, stB); !reflect.DeepEqual(q.plan, []float64{other}) { // 他窗不累及
		t.Fatalf("stB plan = %v, want [other]", q.plan)
	}
	if w.qwatchMode() != "enforce" { // ERROR 不降级模式
		t.Fatal("ERROR 不应降级模式")
	}
	if !strings.Contains(out, "ERROR") {
		t.Fatalf("告警应落控制台, out = %s", out)
	}
	rows := accts.Read(accounts.ReadOpts{Kind: "beat"})
	if len(rows) != 3 {
		t.Fatalf("beat 行数 = %d, want 3", len(rows))
	}
	for i, r := range rows {
		if r["outcome"] != "error" {
			t.Fatalf("rows[%d][outcome] = %v, want error", i, r["outcome"])
		}
	}
}

// ---- RLock 串行化（验收⑧） ----

func TestLedgerLockSharedShape(t *testing.T) {
	// Python 台账锁可重入（RLock：持锁临界区内嵌套 get/touch 同线程不卡死）。
	// Go 差异声明：sync.Mutex 不可重入——Go 契约为"持锁临界区内用 *Locked
	// 内方法 + 共享引用直改"，锁内禁调自带锁的公共方法（会死锁，纪律约束；
	// 票10 起已确立）。
	led := ledger.New()
	led.Touch("cc", "rl-0", "C:/x/rl-0.jsonl", 1, 1, 0)
	led.Mu().Lock()
	defer led.Mu().Unlock()
	if led.GetLocked("cc", "rl-0") == nil {
		t.Fatal("锁内 GetLocked 应可见")
	}
	st := led.GetLocked("cc", "rl-0")
	st.Size = 2 // 临界区内共享引用写（锁内只有内存操作）
	for _, s := range led.AllSessionsLocked() {
		if s.SessionID == "rl-0" && s.Size != 2 {
			t.Fatalf("锁内写不可见: %+v", s)
		}
	}
}

func TestRlockParkingFirstNoDoubleWindow(t *testing.T) {
	// 两窗互斥 TOCTOU 归零：等答复窗临界区持锁期间停车窗被并发打开
	// → 锁内复验发现已开，不重开（旧代码两窗并存）。
	tmp := t.TempDir()
	projects := filepath.Join(tmp, "projects")
	led := ledger.New()
	d := NewDaemon(mergeCfg(), led, nil, func(*ledger.SessionState) bool { return true }, nil, 0, nil)
	w := newTestWatcherW(mergeCfg(), led, nil, d, nil, nil)
	sid := "mx-9"
	st, _ := schedTouch(t, led, projects, sid)
	gotLock := make(chan struct{})

	go func() { // 模拟 HTTP 线程先抓住台账锁
		led.Mu().Lock()
		defer led.Mu().Unlock()
		close(gotLock) // 已持锁（主 goroutine 此后才进临界区）
		time.Sleep(200 * time.Millisecond)
		d.windows[winKey{"cc", sid}] = &waitWindow{OpenedTS: clock.Now()} // 锁内先开停车窗，制造并发时序
	}()
	select {
	case <-gotLock: // 确定化：锁已被线程抓走
	case <-time.After(5 * time.Second):
		t.Fatal("线程未抓锁")
	}
	w.maybeQwatch(st) // 临界区等锁 → 拿到后复验停车窗已开
	q := qwRead(led, st)
	if q.opened != nil {
		t.Fatal("等答复窗没开")
	}
	if _, ok := d.windows[winKey{"cc", sid}]; !ok { // 停车窗开着（先开者赢）
		t.Fatal("停车窗应保持")
	}
}

func TestRlockSubagentOpenWaitsOnLedgerLock(t *testing.T) {
	// 停车窗开窗 check-then-act 在台账锁内：锁被持时不判不开，释放后照常。
	led := ledger.New()
	d := NewDaemon(config.Default(), led, nil, func(*ledger.SessionState) bool { return true }, nil, 0, nil)
	led.Mu().Lock()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = d.Subagent(map[string]any{"event": "start", "agent": "cc", "session_id": "sb-9"})
	}()
	time.Sleep(150 * time.Millisecond) // 若不在锁内，停车窗这会儿已开
	if _, ok := d.windows[winKey{"cc", "sb-9"}]; ok {
		t.Fatal("台账锁被持时停车窗不应已开")
	}
	led.Mu().Unlock()
	<-done
	if _, ok := d.windows[winKey{"cc", "sb-9"}]; !ok { // 锁释放后照常开窗
		t.Fatal("锁释放后应照常开停车窗")
	}
}

// ---- 记账与契约（验收⑨；NoopSender/Classify 契约归 beat_test.go） ----

func TestBeatRowPrivacyNoMessageContent(t *testing.T) {
	// 账面字段白名单：只有元数据与金额，无任何消息内容字段。
	led := ledger.New()
	tmp := t.TempDir()
	accts, err := accounts.New(filepath.Join(tmp, "data"))
	if err != nil {
		t.Fatal(err)
	}
	rec := &recordingSender{}
	cfg := mergeCfg()
	cfg.QuestionWatch.Mode = "enforce"
	w := newTestWatcherW(cfg, led, accts, nil, rec, nil)
	st, _ := bareSession(t, led, tmp, "pv-1")
	openWindow(led, st, 420.0, 2)
	setPlan(led, st, []float64{clock.Now() - 1})
	w.maybeFireBeats(st)
	rows := accts.Read(accounts.ReadOpts{Kind: "beat"})
	if len(rows) != 1 {
		t.Fatalf("beat 行数 = %d, want 1", len(rows))
	}
	allowed := map[string]bool{"v": true, "ts": true, "ts_iso": true, "kind": true,
		"agent": true, "session_id": true, "lineage_id": true, "project": true,
		"provider": true, "model": true, "price_ver": true, "prefix_tokens": true,
		"cache_read": true, "outcome": true, "cost_pred": true, "cost_actual": true,
		"lane": true} // 票04：beat 科目泳道标记（qwatch|wait）
	for k := range rows[0] {
		if !allowed[k] {
			t.Fatalf("账面出现白名单外字段 %q（行 = %v）", k, rows[0])
		}
	}
}

func TestSchedulerExceptionSwallowed(t *testing.T) {
	// 调度任何异常吞掉不外抛（守望主路径不受累）——Python monkeypatch
	// _send_beat=boom 的 Go 形：注入 Send 内炸的 sender，sendBeat recover
	// 承接为 ERROR 一跳，调度循环不炸。
	led := ledger.New()
	tmp := t.TempDir()
	cfg := mergeCfg()
	cfg.QuestionWatch.Mode = "enforce"
	w := newTestWatcherW(cfg, led, nil, nil, panicSender{}, nil)
	st, _ := bareSession(t, led, tmp, "x-1")
	openWindow(led, st, 420.0, 2)
	setPlan(led, st, []float64{clock.Now() - 1})
	w.maybeFireBeats(st) // 不外抛
	if q := qwRead(led, st); q.fired != 1 {
		t.Fatalf("beats_fired = %d, want 1（已出队，发送 attempted）", q.fired)
	}
	if w.beatInFlight.Load() { // 在途旗必还
		t.Fatal("在途旗未还")
	}
}
