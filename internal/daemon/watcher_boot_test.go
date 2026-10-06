package daemon

// watcher_boot_test.go — 重启观察窗 + 摆渡去重（2026-09-30 重启无交接保护案，
// v0.4.4）：daemon 重启后台账清零，存量闲置会话 observed=false 且 enrich 不跑
// → 摆渡永不入队、用户回来无交接（分支6跑步机）。观察窗=24h（FreshWindowS）：
// 窗内 mtime 的存量会话补观察（重启前的活动是真实活动）；窗外古老文件维持
// 不观察（全量摆渡风暴守卫，Python 期有意设计）。去重=ValidHandoff 同口径
// （与闸门分支5同一判定：命中 ⇔ 闸门本来就能用现存交接拦，跳过重摆无回归）。

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"ferryman/internal/accounts"
	"ferryman/internal/config"
	"ferryman/internal/ledger"
	"ferryman/internal/pathsx"
	"ferryman/internal/store"
)

// bootFixture 带可控时间戳的 CC 转录（writeMergeTranscript 的时间戳烤死在
// 2026-09-18，与本组冻结钟不匹配——去重用的 ValidHandoff 有 24h 新鲜窗，
// covers 必须与冻结钟同窗，故本地自写）。ts=末条带时间戳记录的 epoch 秒。
func bootFixture(t *testing.T, projects, sid string, ts float64) string {
	t.Helper()
	dir := filepath.Join(projects, "C--proj")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	f := filepath.Join(dir, sid+".jsonl")
	tsISO := time.Unix(int64(ts), 0).UTC().Format("2006-01-02T15:04:05.000Z")
	lines := []string{
		`{"type": "user", "timestamp": "` + tsISO + `", "message": {"role": "user", "content": "干活"}}`,
		`{"type": "assistant", "timestamp": "` + tsISO + `", "message": {"role": "assistant",` +
			` "content": [{"type": "text", "text": "干完了"}]}}`,
	}
	if err := os.WriteFile(f, []byte(lines[0]+"\n"+lines[1]+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return f
}

// bootChmtime 把转录 mtime 拨到指定时刻（闲置模拟的磁盘面：mtime=末次写入）。
func bootChmtime(t *testing.T, f string, at float64) {
	t.Helper()
	tv := time.Unix(int64(at), 0)
	if err := os.Chtimes(f, tv, tv); err != nil {
		t.Fatal(err)
	}
}

// bootWatcher 装配"刚重启"形态的守望者：StartedAt=冻结 now（TouchFull 只认
// mtime ≥ StartedAt 的新写入——存量会话若无观察窗就永不被观察，即本组回归
// 所钉的缺口）。
func bootWatcher(t *testing.T, cfg *config.Config, led *ledger.Ledger, st *store.Store,
	now float64, enqueued *[]string) *Watcher {
	t.Helper()
	w := NewWatcher(cfg, led, st, func(s *ledger.SessionState) bool {
		*enqueued = append(*enqueued, s.SessionID)
		return true
	}, now, nil, nil, nil, nil) // StartedAt=now：daemon 刚起
	w.enrich = func(st *ledger.SessionState) { // 富化替身（同款测试先例）：peak+cwd
		led.Mu().Lock()
		st.PeakCtx = 50000
		st.Cwd = "C:/proj-x"
		led.Mu().Unlock()
	}
	return w
}

func bootCfg(projects string) *config.Config {
	cfg := config.Default()
	cfg.Watch.CCProjectsDir = projects
	cfg.Thresholds = config.ThresholdCfg{SummarizeS: 10, BlockS: 100,
		MinCtxTokens: 1000, CacheWarnS: 720}
	return cfg
}

func TestWatcherObserveRecentWindowFerriesIdleSessionAfterRestart(t *testing.T) {
	now := freezeClock(t, 1_800_000_000.0)
	tmp := t.TempDir()
	projects := filepath.Join(tmp, "projects")
	// 存量会话：重启前 2h 停笔（mtime 早于 daemon 启动时刻 → 旧逻辑永不观察）
	f := bootFixture(t, projects, "boot-1", *now-7200)
	bootChmtime(t, f, *now-7200)

	stt, err := store.New(filepath.Join(tmp, "data"))
	if err != nil {
		t.Fatal(err)
	}
	var enqueued []string
	led := ledger.New()
	w := bootWatcher(t, bootCfg(projects), led, stt, *now, &enqueued)
	w.cxDirs = []string{filepath.Join(tmp, "no-codex")}
	w.pollCC()

	st := led.Get("cc", "boot-1")
	led.Mu().Lock()
	obs, handed := st.ObservedActive, st.HandedOffAt
	led.Mu().Unlock()
	if !obs {
		t.Fatal("重启观察窗：24h 内停笔的存量会话应被补观察")
	}
	if len(enqueued) != 1 || enqueued[0] != "boot-1" {
		t.Fatalf("应入队摆渡 boot-1（重启后首个轮询周期内补交接）: %v", enqueued)
	}
	if handed == 0 {
		t.Fatal("入队应记 HandedOffAt")
	}
}

func TestWatcherObserveRecentWindowSkipsAncientFiles(t *testing.T) {
	// 风暴守卫不破：72h 前的古老文件不补观察、不入队（否则每次重启全量摆渡）。
	now := freezeClock(t, 1_800_000_000.0)
	tmp := t.TempDir()
	projects := filepath.Join(tmp, "projects")
	f := bootFixture(t, projects, "boot-old", *now-3*86400)
	bootChmtime(t, f, *now-3*86400)

	stt, err := store.New(filepath.Join(tmp, "data"))
	if err != nil {
		t.Fatal(err)
	}
	var enqueued []string
	led := ledger.New()
	w := bootWatcher(t, bootCfg(projects), led, stt, *now, &enqueued)
	w.cxDirs = []string{filepath.Join(tmp, "no-codex")}
	w.pollCC()

	st := led.Get("cc", "boot-old")
	led.Mu().Lock()
	obs, handed := st.ObservedActive, st.HandedOffAt
	led.Mu().Unlock()
	if obs {
		t.Fatal("72h 前的古老文件不得补观察（防全量摆渡风暴）")
	}
	if len(enqueued) != 0 {
		t.Fatalf("古老文件不得入队: %v", enqueued)
	}
	if handed != 0 {
		t.Fatal("未入队不得动 HandedOffAt")
	}
}

func TestMaybeEnqueueDedupesWhenValidHandoffCovers(t *testing.T) {
	// 重启后 HandedOffAt/HandledContentTS 归零，但库中已有覆盖当前内容的有效
	// 交接（与闸门分支5同口径）→ 跳过重摆并补记处置章。
	now := freezeClock(t, 1_800_000_000.0)
	tmp := t.TempDir()
	projects := filepath.Join(tmp, "projects")
	f := bootFixture(t, projects, "boot-1", *now-7200)
	bootChmtime(t, f, *now-7200)

	stt, err := store.New(filepath.Join(tmp, "data"))
	if err != nil {
		t.Fatal(err)
	}
	// 既有交接：covers=内容钟+30s（容差内覆盖）、24h 新鲜窗内、同 (cc, cwd)。
	stt.SaveHandoff("boot-1", "cc", "C:/proj-x", "既有交接",
		isoUTC(*now-7200+30), "fresh", "<<<INJECT>>>x<<</INJECT>>>")

	var enqueued []string
	led := ledger.New()
	w := bootWatcher(t, bootCfg(projects), led, stt, *now, &enqueued)
	w.cxDirs = []string{filepath.Join(tmp, "no-codex")}
	w.pollCC()

	st := led.Get("cc", "boot-1")
	led.Mu().Lock()
	handed, handledTS, lastWrite := st.HandedOffAt, st.HandledContentTS, st.LastWrite
	led.Mu().Unlock()
	if len(enqueued) != 0 {
		t.Fatalf("已有有效交接覆盖 → 不得重摆: %v", enqueued)
	}
	if handed != lastWrite {
		t.Fatalf("去重应补记 HandedOffAt=LastWrite: %v vs %v", handed, lastWrite)
	}
	if d := handledTS - (*now - 7200); d < -1 || d > 1 {
		t.Fatalf("HandledContentTS 应=内容钟: %v vs %v", handledTS, *now-7200)
	}
}

func TestWatcherObserveRecentCodexWindow(t *testing.T) {
	// codex 轨同形：24h 内停笔的存量 rollout 补观察并摆渡。
	// sid 不带连字符：codexSidFromStem 按 rollout 文件名末段取 sid，带杠会被截。
	now := freezeClock(t, 1_800_000_000.0)
	tmp := t.TempDir()
	cx := filepath.Join(tmp, "cx")
	f := writeRollout(t, cx, "codexboot1")
	bootChmtime(t, f, *now-7200)

	stt, err := store.New(filepath.Join(tmp, "data"))
	if err != nil {
		t.Fatal(err)
	}
	var enqueued []string
	led := ledger.New()
	w := bootWatcher(t, bootCfg(filepath.Join(tmp, "no-cc")), led, stt, *now, &enqueued)
	w.ccDir = filepath.Join(tmp, "no-cc")
	w.cxDirs = []string{cx}
	w.pollCodex()

	st := led.Get("codex", "codexboot1")
	if st == nil {
		t.Fatal("rollout 应登记")
	}
	led.Mu().Lock()
	obs := st.ObservedActive
	led.Mu().Unlock()
	if !obs {
		t.Fatal("codex 重启观察窗：存量近活会话应被补观察")
	}
	if len(enqueued) != 1 || enqueued[0] != "codexboot1" {
		t.Fatalf("应入队摆渡 codexboot1: %v", enqueued)
	}
}

// ---- 票06 · 守护重启贫血修复（账本回放重建峰值） ----
//
// 根因与修法见 dsh_boot_replay.go 头注。时间纪律同 dsh_qwatch_test.go：
// clock.Now 真实时钟，账本行 ts 与文件 mtime 都按真实 now 相对偏移——账本
// 月文件（Record 按 ts 落 YYYYMM.jsonl）与回放的「当前月+上一月」同钟。

const dshBootSid = "session-33333333-3333-4333-8333-333333333333"

// dshBootAccRow 造一条 dsh usage/dock 账本行（两科目各自的必填面；sid 或
// lineage 可留空——回放的命中键是二者任一）。
func dshBootAccRow(t *testing.T, acc *accounts.Accounts, kind string, ts float64,
	sid, lineage string, in, cacheRead, cacheCreate int) {
	t.Helper()
	var f accounts.Fields
	switch kind {
	case "usage":
		f = accounts.Fields{
			"agent": "dsh", "session_id": sid, "lineage_id": lineage,
			"model": "glm-5.3", "title": "", "offset": 0, "subagent": "",
			"input_tokens": in, "cache_read_tokens": cacheRead,
			"cache_creation_tokens": cacheCreate, "output_tokens": 10,
		}
	case "dock":
		f = accounts.Fields{
			"agent": "dsh", "session_id": sid, "lineage_id": lineage,
			"mode": "rewrite", "model_in": "glm-5.3", "model_out": "glm-5.3",
			"input_tokens": in, "cache_read_tokens": cacheRead,
			"cache_creation_tokens": cacheCreate, "output_tokens": 10,
			"latency_s": 0.5, "status": 200,
		}
	default:
		t.Fatalf("未知科目 %q", kind)
	}
	if _, err := acc.Record(kind, ts, f); err != nil {
		t.Fatal(err)
	}
}

// dshBootDshWatcher 装配 dsh 轨「刚重启」形态的守望者（led 由调用方持有＝
// 守护重启后台账重建起点；real now 相对偏移的 mtime/账本 ts 与回放同钟）。
func dshBootDshWatcher(t *testing.T, root string, acc *accounts.Accounts,
	stt *store.Store, led *ledger.Ledger, now float64, enq *[]string) *Watcher {
	t.Helper()
	cfg := config.Default()
	cfg.Watch.DshSessionsDir = root
	cfg.Thresholds = config.ThresholdCfg{SummarizeS: 10, BlockS: 1200, MinCtxTokens: 1000}
	return NewWatcher(cfg, led, stt, func(st *ledger.SessionState) bool {
		*enq = append(*enq, st.Agent+"/"+st.SessionID)
		return true
	}, now, acc, nil, nil, nil)
}

func dshBootPeak(t *testing.T, led *ledger.Ledger, sid string) int {
	t.Helper()
	st := led.Get("dsh", sid)
	if st == nil {
		t.Fatal("dsh 主会话应已登记台账")
	}
	led.Mu().Lock()
	defer led.Mu().Unlock()
	return st.PeakCtx
}

func TestDshBootReplayRebuildsPeakFromLedger(t *testing.T) {
	// 重启贫血主例：账本既有历史流量（usage×2 按 session_id 命中 + dock×1 仅
	// 按 lineage_id 命中），重启后台账 peak==0 → 回放重建峰值（三输入列之和
	// 的最大值，dock 行 63000 胜出）。
	now := float64(time.Now().Unix())
	tmp := t.TempDir()
	root := filepath.Join(tmp, "dsh-sessions")
	acc, err := accounts.New(filepath.Join(tmp, "data"))
	if err != nil {
		t.Fatal(err)
	}
	// usage 行按 session_id 命中（lineage 留空＝事件面行形）。
	dshBootAccRow(t, acc, "usage", now-7200, dshBootSid, "", 1000, 20000, 0)
	dshBootAccRow(t, acc, "usage", now-7000, dshBootSid, "", 500, 8000, 5000)
	// 会话文件重启后无新流量（用户侧批，零 usage）。
	gen := writeDshSession(t, root, dshBootSid, dshHeaderFor(dshBootSid),
		dshQwUserSide(2, int64(now*1000)))
	// dock 行仅按 lineage_id 命中（session_id 留空——命中键「二者任一」钉死）。
	dshBootAccRow(t, acc, "dock", now-6900, "", pathsx.NormPath(gen),
		2000, 60000, 1000)

	var enq []string
	led := ledger.New()
	w := dshBootDshWatcher(t, root, acc, nil, led, now, &enq)
	w.pollDsh()

	if peak := dshBootPeak(t, led, dshBootSid); peak != 63000 {
		t.Fatalf("重启后 peak = %d, want 63000（账本回放重建：2000+60000+1000）", peak)
	}
	// 幂等：再轮不重放不改值（每采集态一次）。
	w.pollDsh()
	if peak := dshBootPeak(t, led, dshBootSid); peak != 63000 {
		t.Fatalf("重放后 peak 漂移: %d", peak)
	}
}

func TestDshBootReplayCrossMonthBoundary(t *testing.T) {
	// 跨月边界（A1/F6）：今晨在 9 月文件、重启在 10 月的场景——上一月文件的
	// 条目必须回放得到；「最近两个月」是成本上界，更早的月份不扫。
	now := float64(time.Now().Unix())
	tmp := t.TempDir()
	root := filepath.Join(tmp, "dsh-sessions")
	acc, err := accounts.New(filepath.Join(tmp, "data"))
	if err != nil {
		t.Fatal(err)
	}
	nw := time.Unix(int64(now), 0)
	firstCur := time.Date(nw.Year(), nw.Month(), 1, 0, 0, 0, 0, time.Local)
	tsPrev := float64(firstCur.AddDate(0, -1, 0).Add(24 * time.Hour).Unix()) // 上月 2 日
	tsOld := float64(firstCur.AddDate(0, -2, 0).Add(24 * time.Hour).Unix())  // 两月前 2 日
	dshBootAccRow(t, acc, "usage", tsPrev, dshBootSid, "", 9000, 50000, 0)   // 上月：59000
	dshBootAccRow(t, acc, "usage", now, dshBootSid, "", 1000, 2000, 0)       // 本月：3000
	dshBootAccRow(t, acc, "usage", tsOld, dshBootSid, "", 30000, 30000, 0)   // 两月前：60000（窗外不扫）
	writeDshSession(t, root, dshBootSid, dshHeaderFor(dshBootSid),
		dshQwUserSide(2, int64(now*1000)))

	var enq []string
	led := ledger.New()
	w := dshBootDshWatcher(t, root, acc, nil, led, now, &enq)
	w.pollDsh()

	if peak := dshBootPeak(t, led, dshBootSid); peak != 59000 {
		t.Fatalf("跨月回放 peak = %d, want 59000（上月文件计入、两月前不计）", peak)
	}
}

func TestDshBootReplaySkipsWhenPeakAlreadyPresent(t *testing.T) {
	// 事件面已回写峰值（fed）→ 回放让位：只补 0 值，不覆盖现行面。
	now := float64(time.Now().Unix())
	tmp := t.TempDir()
	root := filepath.Join(tmp, "dsh-sessions")
	acc, err := accounts.New(filepath.Join(tmp, "data"))
	if err != nil {
		t.Fatal(err)
	}
	dshBootAccRow(t, acc, "usage", now-7200, dshBootSid, "", 2000, 60000, 1000)
	writeDshSession(t, root, dshBootSid, dshHeaderFor(dshBootSid),
		dshQwUserSide(2, int64(now*1000)))

	led := ledger.New()
	cfg := config.Default()
	cfg.Watch.DshSessionsDir = root
	cfg.Thresholds = config.ThresholdCfg{SummarizeS: 10, BlockS: 1200, MinCtxTokens: 1000}
	d := NewDaemon(cfg, led, nil, func(*ledger.SessionState) bool { return true }, acc, 0, nil)
	w := NewWatcher(cfg, led, nil, func(*ledger.SessionState) bool { return true },
		now, acc, d, nil, nil)
	d.DshEvent(map[string]any{
		"session_id": dshBootSid, "event": "assistant/message",
		"time": float64(int64(now * 1000)),
		"usage": map[string]any{"input_tokens": 3000, "cache_read_tokens": 2000,
			"cache_creation_tokens": 0, "output_tokens": 10},
	})
	w.pollDsh()

	if peak := dshBootPeak(t, led, dshBootSid); peak != 5000 {
		t.Fatalf("peak = %d, want 5000（事件面已回写，账本历史 63000 不得覆盖）", peak)
	}
}

func TestDshBootReplayTitleFromHandoffDocHead(t *testing.T) {
	// 标题从最近交接文档头行取：[Ferryman 交接 · 会话 X] 形取 X；索引标题空
	//（dsh 产线恒空形）也能从头行拿到。
	now := float64(time.Now().Unix())
	tmp := t.TempDir()
	root := filepath.Join(tmp, "dsh-sessions")
	acc, err := accounts.New(filepath.Join(tmp, "data"))
	if err != nil {
		t.Fatal(err)
	}
	stt, err := store.New(filepath.Join(tmp, "data"))
	if err != nil {
		t.Fatal(err)
	}
	writeDshSession(t, root, dshBootSid, dshHeaderFor(dshBootSid),
		dshQwUserSide(2, int64(now*1000)))
	stt.SaveHandoff(dshBootSid, "dsh", "C:\\proj", "", isoUTC(now-60), "fresh",
		"[Ferryman 交接 · 会话 深夜排错]\n- 生成: 2026-10-06 08:00 · 模型: glm-5.3\n\n正文\n")

	var enq []string
	led := ledger.New()
	w := dshBootDshWatcher(t, root, acc, stt, led, now, &enq)
	w.pollDsh()

	st := led.Get("dsh", dshBootSid)
	if st == nil {
		t.Fatal("dsh 主会话应已登记台账")
	}
	led.Mu().Lock()
	title := st.Title
	led.Mu().Unlock()
	if title != "深夜排错" {
		t.Fatalf("title = %q, want 深夜排错（最近交接文档头行）", title)
	}
}

func TestDshBootReplayTitleAbsentStaysEmpty(t *testing.T) {
	// 无交接：标题留空、不报错（pollDsh 正常走完、台账照常登记）。
	now := float64(time.Now().Unix())
	tmp := t.TempDir()
	root := filepath.Join(tmp, "dsh-sessions")
	acc, err := accounts.New(filepath.Join(tmp, "data"))
	if err != nil {
		t.Fatal(err)
	}
	stt, err := store.New(filepath.Join(tmp, "data"))
	if err != nil {
		t.Fatal(err)
	}
	writeDshSession(t, root, dshBootSid, dshHeaderFor(dshBootSid),
		dshQwUserSide(2, int64(now*1000)))

	var enq []string
	led := ledger.New()
	w := dshBootDshWatcher(t, root, acc, stt, led, now, &enq)
	w.pollDsh()

	st := led.Get("dsh", dshBootSid)
	if st == nil {
		t.Fatal("dsh 主会话应已登记台账")
	}
	led.Mu().Lock()
	title := st.Title
	led.Mu().Unlock()
	if title != "" {
		t.Fatalf("title = %q, want 空串（无交接留空不报错）", title)
	}
}

func TestMaybeEnqueueDshDedupesRestartWithUnconsumedCoveringHandoff(t *testing.T) {
	// 对照组（dsh 形）：重启后台账归零，未消耗交接真覆盖 last_write → 去重挡
	// （HandedOffAt 补记、不重摆）。峰值门槛由回放铺路——回放没接上时本例会
	// 在 peak 门提前空转，故兼作回放→摆渡链路的回归钉。
	now := float64(time.Now().Unix())
	tmp := t.TempDir()
	root := filepath.Join(tmp, "dsh-sessions")
	acc, err := accounts.New(filepath.Join(tmp, "data"))
	if err != nil {
		t.Fatal(err)
	}
	stt, err := store.New(filepath.Join(tmp, "data"))
	if err != nil {
		t.Fatal(err)
	}
	dshBootAccRow(t, acc, "usage", now-7200, dshBootSid, "", 1000, 20000, 0)
	f := writeDshSession(t, root, dshBootSid, dshHeaderFor(dshBootSid),
		dshQwUserSide(2, int64(now*1000)))
	bootChmtime(t, f, now-7200) // 闲置 2h（SummarizeS=10 达标、24h 观察窗内）
	stt.SaveHandoff(dshBootSid, "dsh", "C:\\proj", "t", isoUTC(now-7200+30),
		"fresh", "md")

	var enq []string
	led := ledger.New()
	w := dshBootDshWatcher(t, root, acc, stt, led, now, &enq)
	w.pollDsh()

	if len(enq) != 0 {
		t.Fatalf("未消耗交接覆盖 → 不得重摆: %v", enq)
	}
	st := led.Get("dsh", dshBootSid)
	led.Mu().Lock()
	handed, lastWrite := st.HandedOffAt, st.LastWrite
	led.Mu().Unlock()
	if handed != lastWrite {
		t.Fatalf("去重应补记 HandedOffAt=LastWrite: %v vs %v", handed, lastWrite)
	}
}

func TestMaybeEnqueueDshAfterRestartConsumedHandoffDoesNotBlock(t *testing.T) {
	// 修复组（票01 口径落地钉）：强续已消耗的旧交接不再挡重启后的重摆——
	// bypass 消耗发生在上一次守护运行里，重启后台账归零、台账无章，去重
	// 只认「未消耗交接真覆盖 last_write」→ 已消耗场景放行真入队。
	now := float64(time.Now().Unix())
	tmp := t.TempDir()
	root := filepath.Join(tmp, "dsh-sessions")
	acc, err := accounts.New(filepath.Join(tmp, "data"))
	if err != nil {
		t.Fatal(err)
	}
	stt, err := store.New(filepath.Join(tmp, "data"))
	if err != nil {
		t.Fatal(err)
	}
	dshBootAccRow(t, acc, "usage", now-7200, dshBootSid, "", 1000, 20000, 0)
	f := writeDshSession(t, root, dshBootSid, dshHeaderFor(dshBootSid),
		dshQwUserSide(2, int64(now*1000)))
	bootChmtime(t, f, now-7200)
	stt.SaveHandoff(dshBootSid, "dsh", "C:\\proj", "t", isoUTC(now-7200+30),
		"fresh", "md")
	if n := stt.ConsumeHandoffs("dsh", dshBootSid); n != 1 {
		t.Fatalf("前提：应消耗恰 1 条交接, got %d", n)
	}

	var enq []string
	led := ledger.New()
	w := dshBootDshWatcher(t, root, acc, stt, led, now, &enq)
	w.pollDsh()

	if len(enq) != 1 || enq[0] != "dsh/"+dshBootSid {
		t.Fatalf("已消耗交接不得挡重摆（应真入队）: %v", enq)
	}
}
