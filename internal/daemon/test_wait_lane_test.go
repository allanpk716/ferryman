package daemon

// test_wait_lane_test.go — 票04：等待窗泳道心跳排程（watcher 双泳道＋[wait_window]）。
//
// 验收全表（spec「心跳」节 F8/F9/F4）：
//   - 排跳判据：窗开（含停车未过期）→到点排跳；停车满 1h 过期→不排；
//     主会话恢复写入（窗闭）→不排；无窗（纯工具等待）→不排；
//   - policy 接线：间隔/上限来自计算器输出（fake policy 断言取值调用）；
//     manual_wait_cap_s 只向下夹紧（配置更大值不生效）；
//   - 窗口级熔断：1 MISS 停本窗＋告警；3 连 transport-ERROR 停本窗；
//   - enforce＋无 [dock] → 等待窗侧按 observe（告警一次），无真发送；
//   - 单在途：等待窗与问询窗同时到期→串行；
//   - 记账：beat 科目带泳道标记；无效保温行存在；
//   - Pin 接线：窗开→Pin、窗关且结算→Unpin；句柄 nil 不 panic；
//   - mode 缺省 off：无配置时行为与之前完全一致。

import (
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"ferryman/internal/accounts"
	"ferryman/internal/beat"
	"ferryman/internal/clock"
	"ferryman/internal/config"
	"ferryman/internal/ledger"
	"ferryman/internal/policy"
)

// ---- 助手 ----

// waitCfg mergeCfg 基础上拨 [wait_window]＋配 [dock]（渡口开＝不触发 enforce
// 降级；BeatSender 由用例注入）。
func waitCfg(mode string, manualCapS float64) *config.Config {
	cfg := mergeCfg()
	cfg.WaitWindow.Mode = mode
	cfg.WaitWindow.ManualWaitCapS = manualCapS
	cfg.Dock = &config.DockCfg{Listen: "127.0.0.1:15722",
		UpstreamBaseURL: "http://127.0.0.1:15721"}
	return cfg
}

// openActiveWindow 活跃窗（子代理在飞，未停车）。
func openActiveWindow(d *Daemon, sid string) {
	d.windows[winKey{"cc", sid}] = &waitWindow{OpenedTS: clock.Now()}
}

// openParkedWindow 停车未过期窗（异步子代理仍在跑，停表新鲜）。
func openParkedWindow(d *Daemon, sid string) {
	stop := clock.Now() - 10
	d.windows[winKey{"cc", sid}] = &waitWindow{OpenedTS: clock.Now() - 200,
		StopTS: &stop, SawAsync: true}
}

// fakeWaitPolicy 策略缝替身：记录前缀取值调用，回放固定参数组。
type fakeWaitPolicy struct {
	mu       sync.Mutex
	prefixes []int
	pol      policy.HeartbeatPolicy
	err      error
}

func (f *fakeWaitPolicy) compute(prefix int) (policy.HeartbeatPolicy, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.prefixes = append(f.prefixes, prefix)
	return f.pol, f.err
}

func (f *fakeWaitPolicy) calls() []int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]int(nil), f.prefixes...)
}

// polOK 常用参数组：τ=100s、cap=400s、min_prefix=1000。
func polOK() policy.HeartbeatPolicy {
	return policy.HeartbeatPolicy{TTLS: 125, TauS: 100, WorthwhileCapS: 400,
		MinPrefixTokens: 1000, PerBeatCost: 0.01, ExpireCost: 1.0}
}

// engage 泳道两连调：首调登记泳道 rec（开窗瞬间不跳），次调起判跳。
func engage(w *Watcher, st *ledger.SessionState) {
	w.maybeWaitBeats(st)
	w.maybeWaitBeats(st)
}

// newWaitDaemonAndWatcher 常用装配：Daemon＋Watcher 同 cfg、可选 sender。
func newWaitDaemonAndWatcher(cfg *config.Config, led *ledger.Ledger,
	sender beat.Sender, acc *accounts.Accounts) (*Daemon, *Watcher) {
	d := NewDaemon(cfg, led, nil, func(*ledger.SessionState) bool { return true }, acc, 0, nil)
	w := newTestWatcherW(cfg, led, acc, d, sender, nil)
	w.waitPolicyFn = (&fakeWaitPolicy{pol: polOK()}).compute
	return d, w
}

// ---- 排跳判据（F8） ----

func TestWaitLaneActiveWindowFiresWhenDue(t *testing.T) {
	// 活跃窗（子代理在飞）＋闲置满 τ → 排跳（fake policy τ=100，闲置 150）。
	led := ledger.New()
	tmp := t.TempDir()
	rec := &recordingSender{}
	d, w := newWaitDaemonAndWatcher(waitCfg("enforce", 0), led, rec, nil)
	st, _ := bareSession(t, led, tmp, "wl-a1")
	setLastWrite(led, st, clock.Now()-150)
	openActiveWindow(d, "wl-a1")
	engage(w, st)
	if got := len(rec.plansList()); got != 1 {
		t.Fatalf("活跃窗到点应排 1 跳, plans = %d", got)
	}
	if plans := rec.plansList(); plans[0].SessionID != "wl-a1" || plans[0].BeatIndex != 1 {
		t.Fatalf("plan = %+v, want wl-a1 第 1 跳", plans[0])
	}
}

func TestWaitLaneParkedUnexpiredFiresWhenDue(t *testing.T) {
	// 停车未过期窗（异步子代理仍在跑）→ 照排。
	led := ledger.New()
	tmp := t.TempDir()
	rec := &recordingSender{}
	d, w := newWaitDaemonAndWatcher(waitCfg("enforce", 0), led, rec, nil)
	st, _ := bareSession(t, led, tmp, "wl-p1")
	setLastWrite(led, st, clock.Now()-150)
	openParkedWindow(d, "wl-p1")
	engage(w, st)
	if got := len(rec.plansList()); got != 1 {
		t.Fatalf("停车未过期窗到点应排 1 跳, plans = %d", got)
	}
}

func TestWaitLaneParkedExpiredNoBeat(t *testing.T) {
	// 停车满 1h（PARK_EXPIRE_S）懒过期 → 窗口已闭 → 不排（F8）。
	led := ledger.New()
	tmp := t.TempDir()
	rec := &recordingSender{}
	d, w := newWaitDaemonAndWatcher(waitCfg("enforce", 0), led, rec, nil)
	st, _ := bareSession(t, led, tmp, "wl-e1")
	setLastWrite(led, st, clock.Now()-150)
	stop := clock.Now() - (ParkExpireS + 30) // 停表超 1h
	d.windows[winKey{"cc", "wl-e1"}] = &waitWindow{OpenedTS: clock.Now() - 4000,
		StopTS: &stop, SawAsync: true}
	w.maybeWaitBeats(st)
	w.maybeWaitBeats(st)
	if got := len(rec.plansList()); got != 0 {
		t.Fatalf("停车过期窗不应排跳, plans = %d", got)
	}
}

func TestWaitLaneMainResumedNoBeat(t *testing.T) {
	// 主会话恢复写入 → 窗口已闭（NoteUsage 道）→ 停；闲置重置后也不排。
	led := ledger.New()
	tmp := t.TempDir()
	rec := &recordingSender{}
	d, w := newWaitDaemonAndWatcher(waitCfg("enforce", 0), led, rec, nil)
	st, _ := bareSession(t, led, tmp, "wl-m1")
	setLastWrite(led, st, clock.Now()-150)
	openParkedWindow(d, "wl-m1")
	w.maybeWaitBeats(st)                    // 登记泳道
	d.NoteUsage("cc", "wl-m1", clock.Now()+200) // 主会话恢复 → 闭窗
	setLastWrite(led, st, clock.Now())          // 恢复写入入账（闲置归零）
	w.maybeWaitBeats(st)
	if got := len(rec.plansList()); got != 0 {
		t.Fatalf("主会话恢复后不应排跳, plans = %d", got)
	}
}

func TestWaitLaneNoWindowNoBeat(t *testing.T) {
	// 无窗（纯工具等待：无子代理 → 无窗）→ 不排。
	led := ledger.New()
	tmp := t.TempDir()
	rec := &recordingSender{}
	d, w := newWaitDaemonAndWatcher(waitCfg("enforce", 0), led, rec, nil)
	st, _ := bareSession(t, led, tmp, "wl-n1")
	setLastWrite(led, st, clock.Now()-999)
	w.maybeWaitBeats(st)
	w.maybeWaitBeats(st)
	_ = d
	if got := len(rec.plansList()); got != 0 {
		t.Fatalf("无窗不应排跳, plans = %d", got)
	}
}

func TestWaitLaneIdleBelowTauNoBeat(t *testing.T) {
	// 窗开但闲置未满 τ → 不排（起跳判据：闲置满 τ）。
	led := ledger.New()
	tmp := t.TempDir()
	rec := &recordingSender{}
	d, w := newWaitDaemonAndWatcher(waitCfg("enforce", 0), led, rec, nil)
	st, _ := bareSession(t, led, tmp, "wl-t1")
	setLastWrite(led, st, clock.Now()-50) // < τ=100
	openActiveWindow(d, "wl-t1")
	engage(w, st)
	if got := len(rec.plansList()); got != 0 {
		t.Fatalf("闲置未满 τ 不应排跳, plans = %d", got)
	}
}

func TestWaitLanePrefixBelowMinNoBeat(t *testing.T) {
	// 前缀 < 计算器 MinPrefixTokens → 不排（保温无经济性）。
	led := ledger.New()
	tmp := t.TempDir()
	rec := &recordingSender{}
	d, w := newWaitDaemonAndWatcher(waitCfg("enforce", 0), led, rec, nil)
	pol := polOK()
	pol.MinPrefixTokens = 999999 // 抬高阈值（计算器输出）
	w.waitPolicyFn = (&fakeWaitPolicy{pol: pol}).compute
	st, _ := bareSession(t, led, tmp, "wl-x1")
	setLastWrite(led, st, clock.Now()-150)
	openActiveWindow(d, "wl-x1")
	engage(w, st)
	if got := len(rec.plansList()); got != 0 {
		t.Fatalf("前缀不足不应排跳, plans = %d", got)
	}
}

// ---- policy 接线（公式单源） ----

func TestWaitLanePolicyWiringIntervalAndCap(t *testing.T) {
	// 间隔/上限全取计算器输出：τ=100 → 首跳闲置 100（不是更早）；cap=400 →
	// 第 4 跳（闲置 400）后停（第 5 跳 500 > cap）。fake 断言取值调用（前缀）。
	led := ledger.New()
	tmp := t.TempDir()
	rec := &recordingSender{}
	d, w := newWaitDaemonAndWatcher(waitCfg("enforce", 0), led, rec, nil)
	fake := &fakeWaitPolicy{pol: polOK()}
	w.waitPolicyFn = fake.compute
	st, _ := bareSession(t, led, tmp, "wl-w1")
	openActiveWindow(d, "wl-w1")
	// 闲置 99：不到点（τ=100）
	setLastWrite(led, st, clock.Now()-99)
	engage(w, st)
	if got := len(rec.plansList()); got != 0 {
		t.Fatalf("闲置 99 < τ=100 不应跳, plans = %d", got)
	}
	// 逐档闲置 100/200/300/400 → 4 跳（第 k 跳在闲置 k·τ 到点）
	for _, idle := range []float64{100, 200, 300, 400} {
		setLastWrite(led, st, clock.Now()-idle)
		w.maybeWaitBeats(st)
	}
	if got := len(rec.plansList()); got != 4 {
		t.Fatalf("cap=400 内应共 4 跳, plans = %d", got)
	}
	// 闲置 500：第 5 跳 dueIdle=500 > cap=400 → 不再跳（expire 档）
	setLastWrite(led, st, clock.Now()-500)
	w.maybeWaitBeats(st)
	if got := len(rec.plansList()); got != 4 {
		t.Fatalf("超 cap 不应再跳, plans = %d", got)
	}
	// 取值调用：fake 收到的前缀 = 富化后的 peak_ctx
	calls := fake.calls()
	if len(calls) == 0 {
		t.Fatal("策略计算器未被调用（间隔不得另有公式来源）")
	}
	for _, p := range calls {
		if p != 50000 { // newTestWatcherW 富化替身置 50000
			t.Fatalf("fake policy 前缀取值 = %d, want 50000（peak_ctx）", p)
		}
	}
}

func TestWaitLaneManualCapOnlyTightens(t *testing.T) {
	// manual_wait_cap_s 只向下夹紧：250 < cap 400 → 第 3 跳（300 > 250）停；
	// 9999 > cap 400 → 不生效（仍 4 跳）。
	led := ledger.New()
	tmp := t.TempDir()
	rec := &recordingSender{}
	d, w := newWaitDaemonAndWatcher(waitCfg("enforce", 250), led, rec, nil)
	st, _ := bareSession(t, led, tmp, "wl-c1")
	openActiveWindow(d, "wl-c1")
	for _, idle := range []float64{100, 200, 300, 400} {
		setLastWrite(led, st, clock.Now()-idle)
		w.maybeWaitBeats(st)
	}
	if got := len(rec.plansList()); got != 2 {
		t.Fatalf("manual=250 应只 2 跳（第 3 跳 300>250 停）, plans = %d", got)
	}

	// 对照：manual 更大不生效
	led2 := ledger.New()
	rec2 := &recordingSender{}
	d2, w2 := newWaitDaemonAndWatcher(waitCfg("enforce", 9999), led2, rec2, nil)
	st2, _ := bareSession(t, led2, tmp, "wl-c2")
	openActiveWindow(d2, "wl-c2")
	for _, idle := range []float64{100, 200, 300, 400, 500} {
		setLastWrite(led2, st2, clock.Now()-idle)
		w2.maybeWaitBeats(st2)
	}
	if got := len(rec2.plansList()); got != 4 {
		t.Fatalf("manual=9999 > cap 不生效（仍 4 跳）, plans = %d", got)
	}
}

// ---- 窗口级熔断（F9，按窗计） ----

func TestWaitLaneOneMissStopsWindow(t *testing.T) {
	// 1 MISS → 停本窗剩余跳＋告警（建议复测 TTL、不自改配置）。
	led := ledger.New()
	tmp := t.TempDir()
	accts, err := accounts.New(filepath.Join(tmp, "data"))
	if err != nil {
		t.Fatal(err)
	}
	rec := &recordingSender{results: []beat.BeatResult{beatMiss}}
	d, w := newWaitDaemonAndWatcher(waitCfg("enforce", 0), led, rec, accts)
	st, _ := bareSession(t, led, tmp, "wl-b1")
	openActiveWindow(d, "wl-b1")
	read := captureStdout(t)
	setLastWrite(led, st, clock.Now()-100)
	w.maybeWaitBeats(st) // 登记泳道
	w.maybeWaitBeats(st) // 第 1 跳：MISS
	setLastWrite(led, st, clock.Now()-200)
	w.maybeWaitBeats(st) // 到点但本窗已停
	setLastWrite(led, st, clock.Now()-300)
	w.maybeWaitBeats(st)
	out := read()
	if got := len(rec.plansList()); got != 1 {
		t.Fatalf("1 MISS 后本窗应停, plans = %d", got)
	}
	if !strings.Contains(out, "MISS") || !strings.Contains(out, "停本窗") {
		t.Fatalf("告警应含 MISS/停本窗, out = %s", out)
	}
	if !strings.Contains(out, "TTL") {
		t.Fatalf("告警应建议复测 TTL, out = %s", out)
	}
}

func TestWaitLaneThreeConsecutiveErrorsStopWindow(t *testing.T) {
	// 连续 3 transport-ERROR → 停本窗＋告警；HIT 清 ERROR 连击
	// （err,err,hit 后重新攒 err,err,err 才停）。cap 放宽到 1000 容 6 跳。
	led := ledger.New()
	tmp := t.TempDir()
	rec := &recordingSender{results: []beat.BeatResult{
		beatErr, beatErr, beatHit, beatErr, beatErr, beatErr}}
	d, w := newWaitDaemonAndWatcher(waitCfg("enforce", 0), led, rec, nil)
	pol := polOK()
	pol.WorthwhileCapS = 1000
	w.waitPolicyFn = (&fakeWaitPolicy{pol: pol}).compute
	st, _ := bareSession(t, led, tmp, "wl-b2")
	openActiveWindow(d, "wl-b2")
	w.maybeWaitBeats(st) // 登记泳道
	read := captureStdout(t)
	fire := func(idle float64) {
		setLastWrite(led, st, clock.Now()-idle)
		w.maybeWaitBeats(st)
	}
	fire(100) // err（连击 1）
	fire(200) // err（连击 2）
	fire(300) // hit（清连击）
	if got := len(rec.plansList()); got != 3 {
		t.Fatalf("2 ERROR 未满限不应停, plans = %d", got)
	}
	fire(400) // err（连击 1）
	fire(500) // err（连击 2）
	fire(600) // err（连击 3 → 停本窗＋告警）
	out := read()
	if got := len(rec.plansList()); got != 6 {
		t.Fatalf("应共 6 跳, plans = %d", got)
	}
	if !strings.Contains(out, "ERROR") || !strings.Contains(out, "停本窗") {
		t.Fatalf("告警应含 ERROR/停本窗, out = %s", out)
	}
	fire(700) // 已停：不跳
	if got := len(rec.plansList()); got != 6 {
		t.Fatalf("停窗后不应再跳, plans = %d", got)
	}
}

func TestWaitLaneMissStopIsPerWindow(t *testing.T) {
	// 熔断按窗计：本窗停后，重开新窗（新 OpenedTS）另起泳道照跳。
	led := ledger.New()
	tmp := t.TempDir()
	rec := &recordingSender{results: []beat.BeatResult{beatMiss, beatHit}}
	d, w := newWaitDaemonAndWatcher(waitCfg("enforce", 0), led, rec, nil)
	st, _ := bareSession(t, led, tmp, "wl-b3")
	openActiveWindow(d, "wl-b3")
	setLastWrite(led, st, clock.Now()-100)
	w.maybeWaitBeats(st) // 登记泳道
	w.maybeWaitBeats(st) // 第 1 跳 MISS → 停本窗
	setLastWrite(led, st, clock.Now()-200)
	w.maybeWaitBeats(st) // 停窗中：不跳
	if got := len(rec.plansList()); got != 1 {
		t.Fatalf("停窗后不应跳, plans = %d", got)
	}
	// 重开新窗：另起泳道（重置 fired/停窗位）。显式给新 OpenedTS——Windows
	// 时钟粒度内两次 clock.Now() 可同值，同值则"新窗"不可识别（生产无害：
	// 泳道沿旧窗状态多停一窗；测试需确定性）。
	delete(d.windows, winKey{"cc", "wl-b3"})
	setLastWrite(led, st, clock.Now()-10) // 新基线（闲置重新攒）
	newTS := clock.Now() + 1
	d.windows[winKey{"cc", "wl-b3"}] = &waitWindow{OpenedTS: newTS}
	w.maybeWaitBeats(st) // 旧泳道结算＋新窗登记
	setLastWrite(led, st, clock.Now()-150)
	w.maybeWaitBeats(st) // 新窗第 1 跳：HIT
	if got := len(rec.plansList()); got != 2 {
		t.Fatalf("新窗应另起照跳, plans = %d", got)
	}
}

// ---- enforce＋渡口关 → observe 演练 ----

func TestWaitLaneEnforceNoDockDrillsObserve(t *testing.T) {
	// mode=enforce 且无 [dock]：启动告警一次＋等待窗侧按 observe——无真发送，
	// 演练跳照入账（outcome=observe、lane=wait）。问询守望不受此校验影响。
	led := ledger.New()
	tmp := t.TempDir()
	accts, err := accounts.New(filepath.Join(tmp, "data"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := mergeCfg()
	cfg.WaitWindow.Mode = "enforce"
	cfg.Dock = nil // 渡口关
	d := NewDaemon(cfg, led, nil, func(*ledger.SessionState) bool { return true }, nil, 0, nil)
	rec := &recordingSender{}
	read := captureStdout(t)
	w := newTestWatcherW(cfg, led, accts, d, rec, nil)
	out := read()
	if got := strings.Count(out, "等待窗侧按 observe"); got != 1 {
		t.Fatalf("启动应告警一次（enforce＋渡口关→observe）, got %d\nout = %s", got, out)
	}
	w.waitPolicyFn = (&fakeWaitPolicy{pol: polOK()}).compute
	st, _ := bareSession(t, led, tmp, "wl-d1")
	setLastWrite(led, st, clock.Now()-150)
	openActiveWindow(d, "wl-d1")
	engage(w, st)
	if got := len(rec.plansList()); got != 0 {
		t.Fatalf("渡口关不得真发送, plans = %d", got)
	}
	rows := accts.Read(accounts.ReadOpts{Kind: "beat"})
	if len(rows) != 1 || rows[0]["outcome"] != "observe" || rows[0]["lane"] != "wait" {
		t.Fatalf("beat 行 = %v, want 1 条 observe/wait 演练", rows)
	}
}

// ---- 全局单在途（两泳道串行） ----

func TestWaitLaneSerialWithQWatchSingleInFlight(t *testing.T) {
	// 等待窗与问询窗同时到期：单在途共用——问询跳在途时等待跳不进，
	// 出场后下一轮照发（一前一后，绝不并行）。
	led := ledger.New()
	tmp := t.TempDir()
	cfg := waitCfg("enforce", 0)
	cfg.QuestionWatch.Mode = "enforce"
	blocker := newBlockingSender()
	d, w := newWaitDaemonAndWatcher(cfg, led, blocker, nil)
	stQ, _ := bareSession(t, led, tmp, "wl-sq") // 问询窗
	openWindow(led, stQ, 420.0, 2)
	setPlan(led, stQ, []float64{clock.Now() - 1})
	stW, _ := bareSession(t, led, tmp, "wl-sw") // 等待窗
	setLastWrite(led, stW, clock.Now()-150)
	openActiveWindow(d, "wl-sw")
	w.maybeWaitBeats(stW) // 登记泳道
	done := make(chan struct{})
	go func() {
		defer close(done)
		w.maybeFireBeats(stQ) // 问询跳进 sender 并阻塞（在途）
	}()
	select {
	case <-blocker.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("问询跳未进 sender")
	}
	w.maybeWaitBeats(stW) // 等待跳到期但在途 → 跳过
	if got := blocker.count.Load(); got != 1 {
		t.Fatalf("在途占位失效: count = %d, want 1", got)
	}
	close(blocker.release)
	<-done
	w.maybeWaitBeats(stW) // 问询跳出场后等待跳照发
	if got := blocker.count.Load(); got != 2 {
		t.Fatalf("出场后应照发: count = %d, want 2", got)
	}
}

// ---- 记账形状 ----

func TestWaitLaneBeatRowsCarryLaneAndUselessWarm(t *testing.T) {
	// beat 科目带泳道标记（lane=wait）；窗口收尾主会话未回归 → 无效保温一行
	// （wait_close 行 useless_warm=true）；主会话回归 → main_resumed=true
	// 且无 useless_warm 标记。
	led := ledger.New()
	tmp := t.TempDir()
	accts, err := accounts.New(filepath.Join(tmp, "data"))
	if err != nil {
		t.Fatal(err)
	}
	rec := &recordingSender{}
	d, w := newWaitDaemonAndWatcher(waitCfg("enforce", 0), led, rec, accts)
	stA, _ := bareSession(t, led, tmp, "wl-k1")
	setLastWrite(led, stA, clock.Now()-150)
	openParkedWindow(d, "wl-k1")
	w.maybeWaitBeats(stA) // 登记泳道
	w.maybeWaitBeats(stA) // 第 1 跳（HIT）
	rows := accts.Read(accounts.ReadOpts{Kind: "beat"})
	if len(rows) != 1 || rows[0]["lane"] != "wait" {
		t.Fatalf("beat 行 = %v, want 1 条 lane=wait", rows)
	}
	// 无效保温：窗关（主会话未回归——last_write 不动）
	delete(d.windows, winKey{"cc", "wl-k1"})
	w.maybeWaitBeats(stA) // 收尾结算
	closes := accts.Read(accounts.ReadOpts{Kind: "wait_close"})
	if len(closes) != 1 {
		t.Fatalf("wait_close 行数 = %d, want 1", len(closes))
	}
	if closes[0]["useless_warm"] != true || closes[0]["main_resumed"] != false {
		t.Fatalf("无效保温行 = %v, want useless_warm=true/main_resumed=false", closes[0])
	}
	if closes[0]["beats_fired"] != float64(1) || closes[0]["lane"] != "wait" {
		t.Fatalf("wait_close 行 = %v", closes[0])
	}

	// 对照：主会话回归 → main_resumed=true、无 useless_warm
	stB, _ := bareSession(t, led, tmp, "wl-k2")
	setLastWrite(led, stB, clock.Now()-150)
	openParkedWindow(d, "wl-k2")
	w.maybeWaitBeats(stB)
	w.maybeWaitBeats(stB) // 第 1 跳
	d.NoteUsage("cc", "wl-k2", clock.Now()+200) // 主会话恢复 → 闭窗
	setLastWrite(led, stB, clock.Now()+1)       // 恢复写入入账
	w.maybeWaitBeats(stB)                        // 收尾结算
	closes2 := accts.Read(accounts.ReadOpts{Kind: "wait_close", Session: "wl-k2"})
	if len(closes2) != 1 || closes2[0]["main_resumed"] != true {
		t.Fatalf("回归收尾行 = %v, want main_resumed=true", closes2)
	}
	if _, has := closes2[0]["useless_warm"]; !has || closes2[0]["useless_warm"] != false {
		t.Fatalf("回归收尾 useless_warm 应为 false: %v", closes2[0])
	}
}

// ---- Pin 接线（F4） ----

func TestWaitLanePinWiring(t *testing.T) {
	// 窗开 → Pin 被调；窗关且最后一跳结算后 → Unpin 被调（fake 快照句柄断言）。
	led := ledger.New()
	tmp := t.TempDir()
	rec := &recordingSender{}
	d, w := newWaitDaemonAndWatcher(waitCfg("enforce", 0), led, rec, nil)
	var mu sync.Mutex
	var pinned, unpinned []string
	w.dockPin = func(sid string) {
		mu.Lock()
		pinned = append(pinned, sid)
		mu.Unlock()
	}
	w.dockUnpin = func(sid string) {
		mu.Lock()
		unpinned = append(unpinned, sid)
		mu.Unlock()
	}
	st, _ := bareSession(t, led, tmp, "wl-pin1")
	setLastWrite(led, st, clock.Now()-150)
	openActiveWindow(d, "wl-pin1")
	w.maybeWaitBeats(st) // 登记泳道
	w.reconcilePins(st)  // 窗开 → Pin
	mu.Lock()
	if len(pinned) != 1 || pinned[0] != "wl-pin1" {
		mu.Unlock()
		t.Fatalf("pinned = %v, want [wl-pin1]", pinned)
	}
	mu.Unlock()
	// 窗开期间重复对账：幂等（不重复 Pin）
	w.reconcilePins(st)
	mu.Lock()
	if len(pinned) != 1 {
		mu.Unlock()
		t.Fatalf("重复 Pin = %v", pinned)
	}
	mu.Unlock()
	// 窗关＋结算 → Unpin
	delete(d.windows, winKey{"cc", "wl-pin1"})
	w.maybeWaitBeats(st) // 收尾（fired=0：只解泳道）
	w.reconcilePins(st)
	mu.Lock()
	defer mu.Unlock()
	if len(unpinned) != 1 || unpinned[0] != "wl-pin1" {
		t.Fatalf("unpinned = %v, want [wl-pin1]", unpinned)
	}
}

func TestWaitLanePinQWatchWindowAlsoPins(t *testing.T) {
	// 问询窗接线同款：qwatch 窗开 → Pin；窗关（新写入）→ Unpin。
	led := ledger.New()
	tmp := t.TempDir()
	d := NewDaemon(waitCfg("off", 0), led, nil,
		func(*ledger.SessionState) bool { return true }, nil, 0, nil)
	w := newTestWatcherW(mergeCfg(), led, nil, d, nil, nil)
	var mu sync.Mutex
	var pinned, unpinned []string
	w.dockPin = func(sid string) {
		mu.Lock()
		pinned = append(pinned, sid)
		mu.Unlock()
	}
	w.dockUnpin = func(sid string) {
		mu.Lock()
		unpinned = append(unpinned, sid)
		mu.Unlock()
	}
	st, _ := bareSession(t, led, tmp, "wl-pin2")
	setOpened(led, st, clock.Now()) // 问询窗开（等待窗泳道 off 不影响问询 Pin）
	w.reconcilePins(st)
	mu.Lock()
	if len(pinned) != 1 || pinned[0] != "wl-pin2" {
		mu.Unlock()
		t.Fatalf("pinned = %v, want [wl-pin2]", pinned)
	}
	mu.Unlock()
	led.Mu().Lock()
	st.QWatchOpenedTS = nil // 新写入关窗（Touch 同款清窗）
	led.Mu().Unlock()
	w.reconcilePins(st)
	mu.Lock()
	defer mu.Unlock()
	if len(unpinned) != 1 || unpinned[0] != "wl-pin2" {
		t.Fatalf("unpinned = %v, want [wl-pin2]", unpinned)
	}
}

func TestWaitLaneNilSnapshotHandleNoPanic(t *testing.T) {
	// 渡口关（快照句柄 nil）：Pin/Unpin 安全跳过，泳道全程不 panic。
	led := ledger.New()
	tmp := t.TempDir()
	cfg := waitCfg("enforce", 0)
	d := NewDaemon(cfg, led, nil, func(*ledger.SessionState) bool { return true }, nil, 0, nil)
	// DockSnap 保持 nil（NewDaemon 不设）；BeatSender 亦 nil → enforce 无 sender
	// 走 observe 演练告警分支，同样不 panic。
	w := newTestWatcherW(cfg, led, nil, d, nil, nil)
	w.waitPolicyFn = (&fakeWaitPolicy{pol: polOK()}).compute
	st, _ := bareSession(t, led, tmp, "wl-nil1")
	setLastWrite(led, st, clock.Now()-150)
	openActiveWindow(d, "wl-nil1")
	w.maybeWaitBeats(st)
	w.maybeWaitBeats(st)
	w.reconcilePins(st)
	delete(d.windows, winKey{"cc", "wl-nil1"})
	w.maybeWaitBeats(st)
	w.reconcilePins(st) // 走到这里＝句柄 nil 全程安全
}

// ---- mode 缺省 off：零行为 ----

func TestWaitLaneModeOffZeroBehavior(t *testing.T) {
	// 无 [wait_window]（默认 off）：窗开到点也不排跳、不 Pin——daemon 行为与
	// 本版之前完全一致。
	led := ledger.New()
	tmp := t.TempDir()
	d := NewDaemon(mergeCfg(), led, nil, func(*ledger.SessionState) bool { return true }, nil, 0, nil)
	rec := &recordingSender{}
	w := newTestWatcherW(mergeCfg(), led, nil, d, rec, nil)
	fake := &fakeWaitPolicy{pol: polOK()}
	w.waitPolicyFn = fake.compute
	var pinCalls int
	w.dockPin = func(string) { pinCalls++ }
	st, _ := bareSession(t, led, tmp, "wl-off1")
	setLastWrite(led, st, clock.Now()-999)
	openActiveWindow(d, "wl-off1")
	w.maybeWaitBeats(st)
	w.maybeWaitBeats(st)
	w.reconcilePins(st)
	if got := len(rec.plansList()); got != 0 {
		t.Fatalf("mode=off 不应排跳, plans = %d", got)
	}
	if len(fake.calls()) != 0 {
		t.Fatalf("mode=off 不应调策略计算器, calls = %v", fake.calls())
	}
	if pinCalls != 0 {
		t.Fatalf("mode=off 不应 Pin, calls = %d", pinCalls)
	}
}
