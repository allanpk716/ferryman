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
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"ferryman/internal/beat"
	"ferryman/internal/config"
	"ferryman/internal/dock"
	"ferryman/internal/ferry"
	"ferryman/internal/ledger"
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
