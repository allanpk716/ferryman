package daemon

// dsh_enqueue_test.go — 票03（dsh-gate-ux）P1 接线钉子：
//   - 三条触发路各有测试：gate observe 分支入队（原 :206）、gate 分支7 入队
//    （原 :268）、watcher pollDshSession→maybeEnqueue（原 :248）——dsh 满足
//     入队条件（观察窗内/闲置≥SummarizeS/peak≥MinCtxTokens/交接未覆盖）时
//     Enqueue 被调用；
//   - dsh 跳过 enrich：峰值不被 codextrans 读取器清零、版本章（EnrichedWrite）
//     不盖（enrichImpl 的 else 分支是 codex 读取器，对 dsh 会清零峰值）；
//   - contentClock 对 dsh 恒 fail-open：cctrans.LastTimestamp 对 zstd 代文件
//     必失败 → contentTS=0 → 内容守卫放行＝旧行为（不造 dsh 内容钟）；
//   - 判热豁免道对 dsh 解锁：无钟数据恒判冷（行为零差异）、有钟必活带放行
//    （防将来有钟误拦）；
//   - 端到端（票02 真件）：pollDsh 满足条件 → 入队（serve 闭包同形，
//     covers_at=入队时台账 lastWrite）→ worker.doDsh 渡口快照材料真上模型
//     → fresh 落库 → ValidHandoff 对同时刻 coversBar 命中（cc 路径替身恒败
//     ——误走即露馅）。

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"ferryman/internal/accounts"
	"ferryman/internal/beat"
	"ferryman/internal/config"
	"ferryman/internal/dock"
	"ferryman/internal/ferry"
	"ferryman/internal/ledger"
	"ferryman/internal/store"
)

// dsh 接线测试会话键（session-<uuid> 形；与既有夹具 id 不撞）。
const (
	dshEnqObsID  = "session-aaaaaaaa-0000-4000-8000-000000000001"
	dshEnqBr7ID  = "session-aaaaaaaa-0000-4000-8000-000000000002"
	dshEnqHotID  = "session-aaaaaaaa-0000-4000-8000-000000000003"
	dshEnqSmall  = "session-aaaaaaaa-0000-4000-8000-000000000004"
)

// dshGateBody gateBody 的 dsh 形（agent=dsh；/dsh/gate 桥恒传空转录路径，此处
// 显式给代文件路径——台账按 (dsh, sid) 主键命中，同 TestDshGate 形态）。
func dshGateBody(sid, path, cwd string) map[string]any {
	return map[string]any{"agent": "dsh", "session_id": sid,
		"transcript_path": path, "cwd": cwd, "prompt": "继续"}
}

// dshEnqUsageBatch 带 usage 的 assistant/message 批（计费输入=三列之和）。
func dshEnqUsageBatch(seq int, input, cacheRead, cacheWrite int) string {
	return `{"type":"assistant/message","seq":` + strconv.Itoa(seq) +
		`,"time":1790905227524,"data":{"message":{"source":{"kind":"model","provider":"x","model":"glm-5.3"}},` +
		`"usage":{"inputTokens":` + strconv.Itoa(input) + `,"outputTokens":80,"cacheReadTokens":` + strconv.Itoa(cacheRead) +
		`,"cacheWriteTokens":` + strconv.Itoa(cacheWrite) + `}}}` + "\n"
}

// ---- 触发路一：gate observe 分支入队（gate.go observe 分支） ----

func TestGateDshObserveBranchEnqueues(t *testing.T) {
	e := newGateEnv(t)
	e.d.Cfg.GateCodex = "observe" // dsh 走非 cc 档开关（既有语义）
	proj := filepath.Join(e.tmp, "proj")
	path := filepath.Join(e.tmp, dshEnqObsID, "session.v4.jsonl.zstd")
	e.led.TouchFull("dsh", dshEnqObsID, path, e.t0-(testBlockS+5), 10, proj,
		"", testMinCtx+50, 0)
	r := e.d.Gate(dshGateBody(dshEnqObsID, path, proj))
	if r["decision"] != "allow" {
		t.Fatalf("observe decision = %v", r["decision"])
	}
	if got := e.enqueuedList(); len(got) != 1 || got[0] != dshEnqObsID {
		t.Fatalf("observe 分支应对 dsh 入队摆渡（接线后同机制）: %v", got)
	}
}

// ---- 触发路二：gate 分支7 入队 ----

func TestGateDshBranch7Enqueues(t *testing.T) {
	e := newGateEnv(t)
	e.d.Cfg.GateCodex = "enforce"
	proj := filepath.Join(e.tmp, "proj2")
	path := filepath.Join(e.tmp, dshEnqBr7ID, "session.v4.jsonl.zstd")
	e.led.TouchFull("dsh", dshEnqBr7ID, path, e.t0-(testBlockS+5), 10, proj,
		"", testMinCtx+50, 0)
	r := e.d.Gate(dshGateBody(dshEnqBr7ID, path, proj))
	if r["decision"] != "allow" {
		t.Fatalf("分支7 decision = %v", r["decision"])
	}
	ctx, _ := r["additional_context"].(string)
	if ctx == "" || !strings.Contains(ctx, "新建会话") {
		t.Fatalf("分支7 应带 dsh 警告文案: %q", ctx)
	}
	if _, ok := e.d.Pending.Get([2]string{"dsh", dshEnqBr7ID}); !ok {
		t.Fatal("分支7 应置 pending")
	}
	if got := e.enqueuedList(); len(got) != 1 || got[0] != dshEnqBr7ID {
		t.Fatalf("分支7 应对 dsh 入队摆渡: %v", got)
	}
}

// ---- 判热豁免道解锁：无钟恒判冷（零差异）、有钟必活带放行（不误拦） ----

func TestGateDshHotLaneUnlockedZeroDiffWhenCold(t *testing.T) {
	e := newGateEnv(t)
	e.d.Cfg.GateCodex = "enforce"
	e.d.Cfg.Heartbeat.TTLS = 1800
	e.d.HeatClock = beat.NewLastRequestClock() // 有钟、本会话无观测
	proj := filepath.Join(e.tmp, "projHot")
	path := filepath.Join(e.tmp, dshEnqHotID, "session.v4.jsonl.zstd")
	e.led.TouchFull("dsh", dshEnqHotID, path, e.t0-2400, 10, proj,
		"", testMinCtx+50, 0)
	// 无观测 → 保守判冷（绝不伪造热）→ 分支7：警告+置 pending+入队（零差异）。
	r := e.d.Gate(dshGateBody(dshEnqHotID, path, proj))
	if r["decision"] != "allow" {
		t.Fatalf("无观测应照走分支7 allow, got %v", r["decision"])
	}
	if ctx, _ := r["additional_context"].(string); !strings.Contains(ctx, "将被拦") {
		t.Fatalf("分支7 警告应承诺「将被拦」: %q", ctx)
	}
	if _, ok := e.d.Pending.Get([2]string{"dsh", dshEnqHotID}); !ok {
		t.Fatal("分支7 应置 pending")
	}
	if got := e.enqueuedList(); len(got) != 1 || got[0] != dshEnqHotID {
		t.Fatalf("分支7 应对 dsh 入队摆渡: %v", got)
	}
	// 有观测（dsh 心跳真发喂钟的将来形态）→ 必活带 → 放行不误拦＋pending 清。
	e.d.HeatClock.Note(dshEnqHotID, e.t0-600) // 10min 前真发 → clock_s=600 ≤ 0.8·TTL
	r2 := e.d.Gate(dshGateBody(dshEnqHotID, path, proj))
	if r2["decision"] != "allow" {
		t.Fatalf("必活带应放行: %v", r2)
	}
	if ctx, _ := r2["additional_context"].(string); !strings.Contains(ctx, "仍热") {
		t.Fatalf("热放行应带「仍热」提示: %q", ctx)
	}
	if _, ok := e.d.Pending.Get([2]string{"dsh", dshEnqHotID}); ok {
		t.Fatal("热放行应清 pending（同 cc 语义）")
	}
}

// ---- 触发路三：watcher pollDshSession → maybeEnqueue ----

// TestPollDshEnqueuesIdleDshSession 主会话闲置达总结阈值＋peak 达门（harvest
// 维护值）＋交接未覆盖 → 入队；过不了 peak 门的小会话不入队（标记已处理防
// 反复读盘）。时钟冻结远未来：代文件真 mtime（当下）→ 闲置巨大。
func TestPollDshEnqueuesIdleDshSession(t *testing.T) {
	root := t.TempDir()
	accDir := t.TempDir()
	writeDshSession(t, root, dshMainID, dshMainHeader,
		dshEnqUsageBatch(3, 120, 30000, 5000)) // 计费输入 35120 ≥ 默认 MinCtx 20000
	writeDshSession(t, root, dshEnqSmall, dshHeaderFor(dshEnqSmall),
		dshEnqUsageBatch(3, 10, 0, 0))
	w, _, enq, mu := newDshWatcher(t, root, accDir)
	freezeClock(t, 1_800_000_000.0) // 2027：文件真 mtime（2026）→ 闲置 ≫ SummarizeS
	w.pollDsh()
	mu.Lock()
	defer mu.Unlock()
	if !slices.Contains(*enq, "dsh/"+dshMainID) {
		t.Fatalf("闲置大峰值 dsh 会话应入队: %v", *enq)
	}
	if slices.Contains(*enq, "dsh/"+dshEnqSmall) {
		t.Fatalf("过不了 peak 门的小会话不应入队: %v", *enq)
	}
	if st := w.Ledger.Get("dsh", dshMainID); st == nil || st.PeakCtx != 35120 {
		t.Fatalf("peak = %v, want 35120（enrich 跳过后 peak 门读 harvest 维护值）", st)
	}
	if st := w.Ledger.Get("dsh", dshEnqSmall); st == nil || st.HandedOffAt == 0 {
		t.Fatal("小会话应标记已处理（防反复读盘）")
	}
}

// ---- dsh 跳过 enrich ----

// TestDshEnrichSkipPreservesPeak 峰值不被清零、版本章不盖。转录路径不存在
// ——若误走 enrichImpl 的 codextrans else 分支，TokenCountTurns 空转即把
// PeakCtx 写零（红）；dsh 跳过后 peak 原样、EnrichedWrite 保持 -1。
func TestDshEnrichSkipPreservesPeak(t *testing.T) {
	led := ledger.New()
	w := NewWatcher(config.Default(), led, nil,
		func(*ledger.SessionState) bool { return true }, 0, nil, nil, nil, nil)
	st := led.TouchFull("dsh", dshEnqObsID, `C:\no\such\session.v4.jsonl.zstd`,
		1_800_000_000-99, 10, `C:\proj`, "", 0, 0)
	led.Mu().Lock()
	st.PeakCtx = 35120 // harvestDshUsage/事件面回写后的形态
	led.Mu().Unlock()
	w.enrich(st)
	led.Mu().Lock()
	defer led.Mu().Unlock()
	if st.PeakCtx != 35120 {
		t.Fatalf("peak = %d, want 35120（codextrans 读取器不得碰 dsh 峰值）", st.PeakCtx)
	}
	if st.EnrichedWrite != -1 {
		t.Fatalf("EnrichedWrite = %v, want 未盖章（-1）", st.EnrichedWrite)
	}
}

// ---- contentClock 对 dsh 恒 fail-open ----

// TestDshContentClockFailOpen cctrans.LastTimestamp 对 zstd 代文件必失败 →
// contentTS=0 → 内容守卫放行＝旧行为；0 连同版本缓存（ContentStamp=lastWrite，
// 同版本零重复读盘）。不造 dsh 内容钟——钉的就是这个 fail-open 语义。
func TestDshContentClockFailOpen(t *testing.T) {
	root := t.TempDir()
	p := writeDshSession(t, root, dshMainID, dshMainHeader,
		dshEnqUsageBatch(3, 1, 0, 0))
	led := ledger.New()
	w := NewWatcher(config.Default(), led, nil,
		func(*ledger.SessionState) bool { return true }, 0, nil, nil, nil, nil)
	const mt = 1_800_000_000.0
	st := led.TouchFull("dsh", dshMainID, p, mt, 10, `C:\proj`, "", 0, 0)
	if got := w.contentClock(st); got != 0 {
		t.Fatalf("contentClock = %v, want 0（zstd 无 CC 时间戳 → fail-open）", got)
	}
	led.Mu().Lock()
	ts, stamp := st.ContentTS, st.ContentStamp
	led.Mu().Unlock()
	if ts != 0 || stamp != mt {
		t.Fatalf("缓存 = (%v, %v), want (0, lastWrite)——失败值连同版本缓存", ts, stamp)
	}
}

// ---- 端到端：守望入队 → 票02 dsh 材料分支 → ValidHandoff 命中 ----

// TestDshEnqueueEndToEndFreshHandoff 全链（替身只替上游与渡口捕获）：pollDsh
// 满足条件 → 入队（serve 闭包同形，covers_at=入队时台账 lastWrite）→
// worker.doDsh 取渡口主快照上模型 → fresh 落库 → ValidHandoff 对同时刻
// coversBar 命中。Ferry 替身恒败——dsh 误走 cc 提取路径即露馅。
func TestDshEnqueueEndToEndFreshHandoff(t *testing.T) {
	root := t.TempDir()
	env := newFerryWenv(t)
	ok := newChainFakeUpstream(t)
	env.providers["fake"] = ferry.Provider{Name: "fake", BaseURL: ok.srv.URL, Model: "m"}
	snapStore := dock.NewSnapshotStore()
	snapStore.Capture(dshMainID, dshSnapBody(t), nil) // 渡口捕获替身（主快照=唯一体）
	led := ledger.New()
	worker := NewWorker(env.cfg, env.st, nil, env.providers, explodingFerry)
	worker.DockSnap = snapStore
	worker.Ledger = led
	runWorkerCtx(t, worker)
	gen := writeDshSession(t, root, dshMainID, dshMainHeader,
		dshEnqUsageBatch(3, 120, 30000, 5000))
	// 代文件 mtime 回拨 33 分钟：守望闲置判定（≥SummarizeS 1500s）走文件钟，
	// 而 ValidHandoff 的新鲜窗（24h）与覆盖判定（covers==coversBar）用真钟
	// ——回拨量须同时满足两侧（不能用冻结远未来钟，那会让 covers 显得超龄）。
	past := time.Now().Add(-2000 * time.Second)
	if err := os.Chtimes(gen, past, past); err != nil {
		t.Fatal(err)
	}
	cfg := env.cfg
	cfg.Watch.DshSessionsDir = root
	enqueue := func(s *ledger.SessionState) bool { // serve.go 装配闭包同形
		led.Mu().Lock()
		cwd, lastWrite := s.Cwd, s.LastWrite
		led.Mu().Unlock()
		return worker.Enqueue(map[string]any{
			"transcript_path": s.TranscriptPath,
			"agent":           s.Agent,
			"session_id":      s.SessionID,
			"cwd":             cwd,
			"covers_at":       lastWrite,
		})
	}
	w := NewWatcher(cfg, led, env.st, enqueue, 0, env.acc, nil, nil, nil)
	w.pollDsh()
	st := w.Ledger.Get("dsh", dshMainID)
	if st == nil {
		t.Fatal("dsh 主会话未登记")
	}
	led.Mu().Lock()
	coversAt := st.LastWrite
	led.Mu().Unlock()
	waitForCond(t, 10*time.Second, func() bool {
		h := env.st.ValidHandoff("dsh", `C:\proj`, coversAt)
		return h != nil && h.SessionID == dshMainID && h.Status == "fresh"
	})
	if n := atomic.LoadInt32(&ok.count); n != 1 {
		t.Fatalf("provider 请求数 = %d, want 1（dsh 材料真上模型）", n)
	}
	// 材料分支旁证已足：Ferry 替身恒败（cc 提取路径误走即落 skeleton/失败），
	// 而此处 status=fresh＋上游恰被调 1 次——dsh 项走的是 worker.doDsh 渡口
	// 快照材料路（票02 真件）。
}

// ---- 票04：dsh 强续后重铸触发（dsh_regen.go） ----
//
// 规则（用户已确认措辞；F12 空集方向 2026-10-06 协调者裁定修正）：仅 dsh 生效；
// 触发=强续交换完成（assistant 回复落账）后该会话下一次渡口请求（dock 记账）
// 时入队重铸，强续那条请求本身不是触发点（F9）；待重铸谓词=存在 bypass 账痕
// B（取最新）∧ 不存在未消耗交接其 covers ≥ B——空集（交接全被强续消耗／从未
// 有交接）即待重铸，票面原句「无未消耗交接则不成立」方向写反系规格笔误（按
// 字面实现则主流程永不触发）；全持久面推导、重启按同推导重算；入队走
// maybeEnqueue 通道（peak 门/材料/覆盖截止口径全复用），入队后新交接 covers
// 越过 bypass 时刻谓词熄火。
//
// 时间纪律同 watcher_boot_test.go：clock.Now 真实时钟，账本行 ts 与文件 mtime
// 都按真实 now 相对偏移（账本月文件与 ReadMonths 的「当前月+上一月」同钟）。

const dshRegenID = "session-aaaaaaaa-0000-4000-8000-000000000005"

// dshRegenFixture 票04 触发测试装配：会话文件（peak 21000 过 peak 门）＋账本
// ＋可重开的 store 目录。会话文件 mtime=当下 → 台账 lastWrite≈now → 常规
// maybeEnqueue 线被闲置门（SummarizeS）挡死——enq 里出现的入队只能来自重铸线。
type dshRegenFixture struct {
	root    string
	acc     *accounts.Accounts
	dataDir string
	now     float64
}

func newDshRegenFixture(t *testing.T) *dshRegenFixture {
	t.Helper()
	tmp := t.TempDir()
	acc, err := accounts.New(filepath.Join(tmp, "acc"))
	if err != nil {
		t.Fatal(err)
	}
	f := &dshRegenFixture{
		root:    filepath.Join(tmp, "dsh-sessions"),
		acc:     acc,
		dataDir: filepath.Join(tmp, "data"),
		now:     float64(time.Now().Unix()),
	}
	writeDshSession(t, f.root, dshRegenID, dshHeaderFor(dshRegenID),
		dshQwAsstUsage(3, nowMS())) // 计费输入 21000 ≥ MinCtxTokens 1000
	return f
}

func (f *dshRegenFixture) openStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.New(f.dataDir)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// seedHandoff 未消耗交接：covers=1h 前（24h 新鲜窗内；对当下 coversBar 判
// 「未覆盖」→ 去重不挡）。
func (f *dshRegenFixture) seedHandoff(t *testing.T, st *store.Store) {
	t.Helper()
	st.SaveHandoff(dshRegenID, "dsh", `C:\proj`, "t", isoUTC(f.now-3600), "fresh", "md")
}

// seedConsumedHandoff 主流程铺垫：先有交接，再按票02 闸门 bypass 分支的持久
// 效果消耗之（dsh 强续即消耗该会话全部 fresh 交接）——此后该会话无未消耗
// 交接，正是 A4① 的主场景。
func (f *dshRegenFixture) seedConsumedHandoff(t *testing.T, st *store.Store) {
	t.Helper()
	f.seedHandoff(t, st)
	if n := st.ConsumeHandoffs("dsh", dshRegenID); n != 1 {
		t.Fatalf("前提：应消耗恰 1 条交接, got %d", n)
	}
}

// dshRegenWatcher dshBootDshWatcher 同形装配，MinCtxTokens 可调（peak 门钉用）。
func dshRegenWatcher(t *testing.T, root string, acc *accounts.Accounts,
	st *store.Store, led *ledger.Ledger, now float64, enq *[]string, minCtx int) *Watcher {
	t.Helper()
	cfg := config.Default()
	cfg.Watch.DshSessionsDir = root
	cfg.Thresholds = config.ThresholdCfg{SummarizeS: 10, BlockS: 1200, MinCtxTokens: minCtx}
	return NewWatcher(cfg, led, st, func(s *ledger.SessionState) bool {
		*enq = append(*enq, s.Agent+"/"+s.SessionID)
		return true
	}, now, acc, nil, nil, nil)
}

// dshRegenBypass 强续账痕（gate bypass 分支 Acct 同科目；dt 相对 now 秒）。
func (f *dshRegenFixture) dshRegenBypass(t *testing.T, dt float64) {
	t.Helper()
	if _, err := f.acc.Record("bypass", f.now+dt, accounts.Fields{
		"agent": "dsh", "session_id": dshRegenID, "prefix_tokens": 21000,
	}); err != nil {
		t.Fatal(err)
	}
}

// dshRegenDock 渡口流水行（billed=输入三列之和；0=计费零的 count_tokens 形
// ——该路无 SSE usage 可扫，四列恒 0）。
func (f *dshRegenFixture) dshRegenDock(t *testing.T, dt float64, billed int) {
	t.Helper()
	dshBootAccRow(t, f.acc, "dock", f.now+dt, dshRegenID, "", billed, 0, 0)
}

// dshRegenReply assistant 回复落账行（事件面 DshEvent / 文件面 harvest 同科目
// 的主会话 usage 行——subagent 空串）。
func (f *dshRegenFixture) dshRegenReply(t *testing.T, dt float64) {
	t.Helper()
	dshBootAccRow(t, f.acc, "usage", f.now+dt, dshRegenID, "", 100, 0, 0)
}

// dshRegenStrongSeq 强续完整时序（bypass → dock(强续请求) → 回复落账 →
// dock(下一请求)）。
func (f *dshRegenFixture) dshRegenStrongSeq(t *testing.T) {
	t.Helper()
	f.dshRegenBypass(t, -600)
	f.dshRegenDock(t, -590, 30000) // 强续请求本身（计费非零）
	f.dshRegenReply(t, -580)       // assistant 回复落账
	f.dshRegenDock(t, -570, 31000) // 下一次渡口请求
}

// TestDshRegenFiresAtNextDockAfterReply 触发时点钉（主流程：交接先在、强续
// 即消耗——票02）：恰在「回复落账后的下一次 dock」入队；强续那条请求本身不
// 触发（F9）；count_tokens 形（计费零）的 dock 行不顶替「下一次请求」。
func TestDshRegenFiresAtNextDockAfterReply(t *testing.T) {
	f := newDshRegenFixture(t)
	st := f.openStore(t)
	f.seedConsumedHandoff(t, st)
	f.dshRegenBypass(t, -600)
	f.dshRegenDock(t, -590, 30000) // 强续请求本身
	var enq []string
	w := dshBootDshWatcher(t, f.root, f.acc, st, ledger.New(), f.now, &enq)
	w.pollDsh()
	if len(enq) != 0 {
		t.Fatalf("强续请求本身不是触发点（F9），不得入队: %v", enq)
	}
	f.dshRegenDock(t, -585, 0) // count_tokens 形（计费零）——不计数
	w.pollDsh()
	if len(enq) != 0 {
		t.Fatalf("计费零的 dock 行（count_tokens）不得顶替下一次请求: %v", enq)
	}
	f.dshRegenReply(t, -580)       // 回复落账
	f.dshRegenDock(t, -570, 31000) // 下一次渡口请求
	w.pollDsh()
	if len(enq) != 1 || enq[0] != "dsh/"+dshRegenID {
		t.Fatalf("恰在第二次 dock 后入队一次: %v", enq)
	}
}

// dshRegenUpstream 记录型假上游：回链形合格回复（DshFerrySession 走 fresh），
// 记收到的请求体（材料断言用）。
type dshRegenUpstream struct {
	srv    *httptest.Server
	mu     sync.Mutex
	count  int
	bodies [][]byte
}

func newDshRegenUpstream(t *testing.T) *dshRegenUpstream {
	t.Helper()
	u := &dshRegenUpstream{}
	u.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		u.mu.Lock()
		u.count++
		u.bodies = append(u.bodies, body)
		u.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{"content": chainOKReplyMD}}},
			"usage":   map[string]any{"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15},
		})
	}))
	t.Cleanup(u.srv.Close)
	return u
}

func (u *dshRegenUpstream) n() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.count
}

func (u *dshRegenUpstream) lastBody() []byte {
	u.mu.Lock()
	defer u.mu.Unlock()
	if len(u.bodies) == 0 {
		return nil
	}
	return u.bodies[len(u.bodies)-1]
}

// dshRegenBody 渡口请求体替身：末条 user=强续原话；reply 非空时在强续消息后
// 追加 assistant 回复半边（=第二次捕获的「下一请求体」形状，已含完整交换）。
func dshRegenBody(t *testing.T, strongPrompt, reply string) []byte {
	t.Helper()
	msgs := []any{
		map[string]any{"role": "user", "content": strongPrompt},
	}
	if reply != "" {
		msgs = append(msgs, map[string]any{"role": "assistant", "content": reply})
	}
	b, err := json.Marshal(map[string]any{"messages": msgs})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestDshRegenMaterialContainsReplyHalf 材料钉（F9 验收断言，主流程：交接先
// 在、强续即消耗）：重铸材料=快照 Main(sid)=触发时点的请求体，含强续交换的
// assistant 回复半边。
func TestDshRegenMaterialContainsReplyHalf(t *testing.T) {
	f := newDshRegenFixture(t)
	st := f.openStore(t)
	f.seedConsumedHandoff(t, st)
	env := newFerryWenv(t)
	up := newDshRegenUpstream(t)
	env.providers["fake"] = ferry.Provider{Name: "fake", BaseURL: up.srv.URL, Model: "m"}
	snapStore := dock.NewSnapshotStore()
	led := ledger.New()
	worker := NewWorker(env.cfg, st, nil, env.providers, explodingFerry)
	worker.DockSnap = snapStore
	worker.Ledger = led
	runWorkerCtx(t, worker)
	// 渡口捕获替身①：强续请求体（不含回复）。
	snapStore.Capture(dshRegenID, dshRegenBody(t, "强续 修一下登录页", ""), nil)
	f.dshRegenBypass(t, -600)
	f.dshRegenDock(t, -590, 30000)
	cfg := env.cfg
	cfg.Watch.DshSessionsDir = f.root
	enqueue := func(s *ledger.SessionState) bool { // serve.go 装配闭包同形
		led.Mu().Lock()
		cwd, lastWrite := s.Cwd, s.LastWrite
		led.Mu().Unlock()
		return worker.Enqueue(map[string]any{
			"transcript_path": s.TranscriptPath,
			"agent":           s.Agent,
			"session_id":      s.SessionID,
			"cwd":             cwd,
			"covers_at":       lastWrite,
		})
	}
	w := NewWatcher(cfg, led, st, enqueue, f.now, f.acc, nil, nil, nil)
	w.pollDsh()
	if n := up.n(); n != 0 {
		t.Fatalf("第一次 dock（强续请求本身）不得触发摆渡: %d", n)
	}
	// 回复落账 + 第二次 dock ＋ 渡口捕获②（下一请求体：含完整强续交换）。
	snapStore.Capture(dshRegenID,
		dshRegenBody(t, "强续 修一下登录页", "REPLY-MARKER-回复半边"), nil)
	f.dshRegenReply(t, -580)
	f.dshRegenDock(t, -570, 31000)
	w.pollDsh()
	waitForCond(t, 10*time.Second, func() bool { return up.n() > 0 })
	if n := up.n(); n != 1 {
		t.Fatalf("重铸恰上模型一次, got %d", n)
	}
	if !bytes.Contains(up.lastBody(), []byte("REPLY-MARKER-回复半边")) {
		t.Fatalf("重铸材料应含强续交换的 assistant 回复（F9）: %q", up.lastBody())
	}
}

// TestDshRegenPredicateRestartStable 推导谓词重启一致钉（主流程形态）：触发
// 条件全持久面（账本＋store 索引），重启（台账重建＋store 重开）后按同推导
// 重算结论一致。
func TestDshRegenPredicateRestartStable(t *testing.T) {
	f := newDshRegenFixture(t)
	st := f.openStore(t)
	f.seedConsumedHandoff(t, st)
	f.dshRegenStrongSeq(t)
	var enq1 []string
	w1 := dshBootDshWatcher(t, f.root, f.acc, st, ledger.New(), f.now, &enq1)
	w1.pollDsh()
	if len(enq1) != 1 || enq1[0] != "dsh/"+dshRegenID {
		t.Fatalf("首跑应触发: %v", enq1)
	}
	// 重启：入队替身不落库（重铸交接未真正落 store），谓词仍成立——重开 store、
	// 重建台账后同推导得同结论。
	st2 := f.openStore(t)
	var enq2 []string
	w2 := dshBootDshWatcher(t, f.root, f.acc, st2, ledger.New(), f.now, &enq2)
	w2.pollDsh()
	if len(enq2) != 1 || enq2[0] != "dsh/"+dshRegenID {
		t.Fatalf("重启后按同推导重算应得同结论: %v", enq2)
	}
}

// TestDshRegenFiresEvenWithoutAnyHandoff 空集可达性第二形（F12 修正方向）：
// 从未有交接＋bypass 账痕 → 谓词真、正常触发入队。
func TestDshRegenFiresEvenWithoutAnyHandoff(t *testing.T) {
	f := newDshRegenFixture(t)
	st := f.openStore(t) // 空库：从未有交接
	f.dshRegenStrongSeq(t)
	if _, ok := dshRegenPending(st, f.acc, "dsh", dshRegenID, `C:\proj`); !ok {
		t.Fatal("空集（从未有交接）＋bypass 账痕应待重铸（F12 修正方向）")
	}
	var enq []string
	w := dshBootDshWatcher(t, f.root, f.acc, st, ledger.New(), f.now, &enq)
	w.pollDsh()
	if len(enq) != 1 || enq[0] != "dsh/"+dshRegenID {
		t.Fatalf("空集＋完整强续时序应触发重铸: %v", enq)
	}
}

// TestDshRegenEmptySetPendingButPeakGateStillApplies 空集两形谓词皆真，但入
// 队仍走 maybeEnqueue 的 peak 门不绕——MinCtx 高于该会话峰值（账本 dock 行
// 经票06 回放重建 31000）即挡，小会话不放大。
func TestDshRegenEmptySetPendingButPeakGateStillApplies(t *testing.T) {
	f := newDshRegenFixture(t)
	st := f.openStore(t)
	f.dshRegenStrongSeq(t)
	// 形一：从未有交接 → 谓词真。
	if _, ok := dshRegenPending(st, f.acc, "dsh", dshRegenID, `C:\proj`); !ok {
		t.Fatal("形一（从未有交接）：谓词应真")
	}
	var enq []string
	w := dshRegenWatcher(t, f.root, f.acc, st, ledger.New(), f.now, &enq, 50000)
	w.pollDsh()
	if len(enq) != 0 {
		t.Fatalf("入队不绕 peak 门，应被挡: %v", enq)
	}
	// 形二：交接被强续消耗致空集 → 谓词同样真；peak 门同样挡。
	f.seedConsumedHandoff(t, st)
	if _, ok := dshRegenPending(st, f.acc, "dsh", dshRegenID, `C:\proj`); !ok {
		t.Fatal("形二（被消耗致空集）：谓词应真")
	}
	w.pollDsh()
	if len(enq) != 0 {
		t.Fatalf("形二 peak 门应挡住: %v", enq)
	}
}

// TestDshRegenPredicateQuenchedByCoveringHandoff 竞态流（交接晚于 bypass 落库
// 且 covers ≥ B——如 branch5 摆渡在飞时用户强续、摆渡完成后落库的形态）：
// 强续交换已被可供出的交接捕获 → 谓词假、不入队。
func TestDshRegenPredicateQuenchedByCoveringHandoff(t *testing.T) {
	f := newDshRegenFixture(t)
	st := f.openStore(t)
	f.dshRegenStrongSeq(t) // bypass 在先（B=now-600）
	// 交接后落库：未消耗、covers=now-100 ≥ B。
	st.SaveHandoff(dshRegenID, "dsh", `C:\proj`, "t", isoUTC(f.now-100), "fresh", "md")
	if _, ok := dshRegenPending(st, f.acc, "dsh", dshRegenID, `C:\proj`); ok {
		t.Fatal("交接 covers ≥ B 已捕获强续交换，谓词应假")
	}
	var enq []string
	w := dshBootDshWatcher(t, f.root, f.acc, st, ledger.New(), f.now, &enq)
	w.pollDsh()
	if len(enq) != 0 {
		t.Fatalf("已捕获形态不得重铸: %v", enq)
	}
}

// TestDshRegenQuenchesAfterRecast 熄火钉（主流程形态）：入队后同轮防重
//（HandedOffAt 章＋每 bypass 防重闸，maybeEnqueue 同款）；重铸交接落库（covers
// 越过 bypass 时刻、未消耗）后谓词翻转——mtime 顶过 HandedOffAt、直断言谓词
// 假，钉的是「新交接捕获强续交换→熄火」本身。
func TestDshRegenQuenchesAfterRecast(t *testing.T) {
	f := newDshRegenFixture(t)
	st := f.openStore(t)
	f.seedConsumedHandoff(t, st)
	f.dshRegenStrongSeq(t)
	var enq []string
	w := dshBootDshWatcher(t, f.root, f.acc, st, ledger.New(), f.now, &enq)
	w.pollDsh()
	if len(enq) != 1 {
		t.Fatalf("应恰触发一次: %v", enq)
	}
	w.pollDsh()
	if len(enq) != 1 {
		t.Fatalf("同轮重复轮不得重复入队（入队即记 HandedOffAt）: %v", enq)
	}
	// 重铸落库：同 (agent,sid) 覆盖——新条目未消耗、covers=now-100 > bypass。
	st.SaveHandoff(dshRegenID, "dsh", `C:\proj`, "t", isoUTC(f.now-100), "fresh", "md2")
	if _, ok := dshRegenPending(st, f.acc, "dsh", dshRegenID, `C:\proj`); ok {
		t.Fatal("新交接 covers 越过 bypass 后推导谓词应熄火")
	}
	gen := filepath.Join(f.root, "--C-proj--", dshRegenID, "session.v4.jsonl.zstd")
	fut := time.Unix(int64(f.now+120), 0)
	if err := os.Chtimes(gen, fut, fut); err != nil { // lastWrite 顶过 HandedOffAt
		t.Fatal(err)
	}
	w.pollDsh()
	if len(enq) != 1 {
		t.Fatalf("新交接 covers 越过 bypass 时刻后谓词应熄火不重复重铸: %v", enq)
	}
}

// TestDshRegenPredicateScopedToDsh 作用域钉（F10/D8④）：推导谓词仅 dsh 成立
// ——cc/codex 恒 false（无 agent 限定时该谓词对 cc 在任一 bypass 后恒真）。
func TestDshRegenPredicateScopedToDsh(t *testing.T) {
	f := newDshRegenFixture(t)
	st := f.openStore(t)
	f.seedConsumedHandoff(t, st)
	f.dshRegenStrongSeq(t)
	if _, ok := dshRegenPending(st, f.acc, "dsh", dshRegenID, `C:\proj`); !ok {
		t.Fatal("前提：dsh 形态谓词应成立")
	}
	for _, ag := range []string{"cc", "codex"} {
		if _, ok := dshRegenPending(st, f.acc, ag, dshRegenID, `C:\proj`); ok {
			t.Fatalf("谓词对 %s 必须不成立（作用域限定）", ag)
		}
	}
}
