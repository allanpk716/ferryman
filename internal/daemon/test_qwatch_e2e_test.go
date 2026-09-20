package daemon

// 规格：tests/test_qwatch_e2e.py（T51 票06 · 端到端集成）的票16 落点 + 票13/14
// 缓交占位转绿（Harness 全链用真守望线程/真监听驱动）。
//
// 归属拆分（316 清点闭环）：
//   - 漏检关联纯函数 2 例（miss_signal 正/反例）——票06 已收编转绿于
//     internal/qwatch/qwatch_test.go（TestMissSignalCountsSurgeThenFullRepay-
//     Revival / TestMissSignalNegativeControls），不重复立目；
//   - 回归挂点 1 例（beat_request_shape_lock）——Q14 段二产物接入后启用，
//     保留 skip 占位（skip reason 逐字搬运）；
//   - 其余本文件 1:1（Harness 全链 2 例 + health miss_signals 1 例 + Harness
//     接线断言（票02 harness_wires_ferry_daemon_for_mutex）+ 票04 HTTP 一键停
//     集成 1 例）。
//
// Harness（Python tests/helpers.py Harness 的 Go 形）：真守望线程（w.Run）+
// 真监听（serveBg）+ fake 摆渡工人（channel→SaveHandoff，队列深度 1 同
// queue.Queue(maxsize=1)——满即 enqueue 返回 false 延迟）。时序全部由真实
// 流转产生，绝不手改台账内部状态变量（与 Python 同纪律）。

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"ferryman/internal/accounts"
	"ferryman/internal/beat"
	"ferryman/internal/clock"
	"ferryman/internal/config"
	"ferryman/internal/ledger"
	"ferryman/internal/store"
)

// ---- Harness（helpers.Harness 的 Go 形） ----

type wHarn struct {
	t        *testing.T
	tmp      string
	projects string
	cfg      *config.Config
	led      *ledger.Ledger
	st       *store.Store
	accts    *accounts.Accounts
	d        *Daemon
	w        *Watcher
	port     int
	token    string

	mu       sync.Mutex
	enqueued []string
	tasks    chan *ledger.SessionState
	cancel   context.CancelFunc
}

func newWHarn(t *testing.T) *wHarn {
	t.Helper()
	tmp := t.TempDir()
	projects := filepath.Join(tmp, "projects")
	dataDir := filepath.Join(tmp, "data")
	cfg := config.Default()
	cfg.GateCC = "enforce"
	cfg.Thresholds = config.ThresholdCfg{SummarizeS: 1.0, BlockS: 2.0,
		MinCtxTokens: 1000, CacheWarnS: 720}
	cfg.Watch.PollIntervalS = 0.2
	cfg.Watch.CCProjectsDir = projects
	cfg.Watch.CodexSessionsDir = filepath.Join(tmp, "no-codex")
	cfg.Server.DataDir = dataDir
	cfg.FerryProvider = "fake" // T39 去内置默认后，测试密闭：注入假 provider
	token, err := EnsureToken(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	led := ledger.New()
	st, err := store.New(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	accts, err := accounts.New(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	h := &wHarn{t: t, tmp: tmp, projects: projects, cfg: cfg, led: led, st: st,
		accts: accts, token: token,
		tasks: make(chan *ledger.SessionState, 1)} // queue.Queue(maxsize=1) 同形
	qs := beat.NewQWatchStats() // 票04：守望计数器（daemon/watcher 共享）
	h.d = NewDaemon(cfg, led, st, h.enqueue, accts, clock.Now(), qs)
	h.w = NewWatcher(cfg, led, st, h.enqueue, clock.Now(), accts, h.d, nil, qs)
	go func() { // FerryWorker 的 fake 摆渡形：出队即出交接
		for s := range h.tasks {
			h.workerFerry(s)
		}
	}()
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	go h.w.Run(ctx) // 守望线程（真 poll 循环）
	h.port = freePort(t)
	serveBg(t, h.d, h.port, token)
	t.Cleanup(func() { cancel(); close(h.tasks) })
	return h
}

func (h *wHarn) enqueue(s *ledger.SessionState) bool {
	select {
	case h.tasks <- s:
		h.mu.Lock()
		h.enqueued = append(h.enqueued, s.SessionID)
		h.mu.Unlock()
		return true
	default: // 队满 = 延迟（下轮轮询自然重试）
		return false
	}
}

// workerFerry fake ferry_session（default_fake 同位）：交接立即落地。
func (h *wHarn) workerFerry(s *ledger.SessionState) {
	md := "[Ferryman 交接 · 会话 集成测试会话]\n\n<<<INJECT>>>\n注入层：干完了\n<<</INJECT>>\n\n# 全文\n干完了\n"
	h.st.SaveHandoff(s.SessionID, s.Agent, s.Cwd, "集成测试会话",
		time.Now().UTC().Format("2006-01-02T15:04:05Z07:00"), "fresh", md)
}

func (h *wHarn) waitCond(timeout time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

func (h *wHarn) mustWait(cond func() bool, msg string) {
	h.t.Helper()
	if !h.waitCond(15*time.Second, cond) {
		h.t.Fatal(msg)
	}
}

func (h *wHarn) enqueuedHas(sid string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Contains(h.enqueued, sid)
}

func (h *wHarn) handoffExists(sid, cwd string) bool {
	for _, e := range h.st.RestoreCandidates("cc", cwd) {
		if e.SessionID == sid {
			return true
		}
	}
	return false
}

func (h *wHarn) rowsOf(kind, sid string) []map[string]any {
	return h.accts.Read(accounts.ReadOpts{Kind: kind, Session: sid})
}

func (h *wHarn) statsQwatch() map[string]any {
	r := getJSON(h.t, h.port, "/stats", h.token)
	q, _ := r["qwatch"].(map[string]any)
	return q
}

// ledgerState 守望线程并发下的台账一次性快照（共享引用读持锁）。
type ledgerState struct {
	opened    *float64
	plan      []float64
	lastWrite float64
}

func hSt(h *wHarn, sid string) ledgerState {
	st := h.led.Get("cc", sid)
	if st == nil {
		return ledgerState{}
	}
	h.led.Mu().Lock()
	defer h.led.Mu().Unlock()
	return ledgerState{opened: st.QWatchOpenedTS,
		plan: append([]float64(nil), st.QWatchPlan...), lastWrite: st.LastWrite}
}

// nowISO helpers.now_iso 同位。
func nowISO() string {
	return time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
}

// writeE2ESession helpers.write_session 同位：合成普通会话（非提问潮）。
func writeE2ESession(t *testing.T, projects, sid, cwd, text string, usageIn, cacheRead int) string {
	t.Helper()
	dir := filepath.Join(projects, "C--proj")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	f := filepath.Join(dir, sid+".jsonl")
	lines := []map[string]any{
		{"type": "user", "timestamp": nowISO(), "cwd": cwd, "sessionId": sid,
			"message": map[string]any{"role": "user", "content": "做点活"}},
		{"type": "assistant", "timestamp": nowISO(),
			"message": map[string]any{"role": "assistant",
				"content": []any{map[string]any{"type": "text", "text": text}},
				"usage": map[string]any{"input_tokens": usageIn,
					"cache_read_input_tokens":     cacheRead,
					"cache_creation_input_tokens": 0, "output_tokens": 5}}},
		{"type": "ai-title", "aiTitle": "集成测试会话"},
	}
	var b strings.Builder
	for _, ln := range lines {
		raw, err := json.Marshal(ln)
		if err != nil {
			t.Fatal(err)
		}
		b.Write(raw)
		b.WriteString("\n")
	}
	if err := os.WriteFile(f, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := os.Chtimes(f, now, now); err != nil {
		t.Fatal(err)
	}
	return f
}

// ---- Harness 全链（自然闲置，勿手改台账状态） ----

func TestQWatchE2EFullChain(t *testing.T) {
	// 全链：自然开窗→observe 演练跳→用户提交关窗（剩余跳取消）→常规摆渡恢复
	// →/stats 计数与漏检信号→事件序列完整。
	h := newWHarn(t)
	h.cfg.QuestionWatch.Mode = "observe"
	h.cfg.QuestionWatch.BeatIntervalS = 1.2
	h.cfg.QuestionWatch.MaxBeats = 3
	h.cfg.QuestionWatch.FerryDeadlineLeadS = 8.0 // 死线 60−8=52s：测试窗外，窗口期摆渡确被推迟
	h.cfg.Thresholds.SummarizeS = 0.5            // 自然闲置：写入后 0.5s 即达摆渡线
	h.cfg.Thresholds.BlockS = 60.0
	sid := "e2e-surge"
	t0 := time.Now()

	// 提问潮落盘：末条 assistant 纯文本 8 个问题单元（无 tool_use——条件②空集
	// 真空真；也让窗口期的摆渡推迟只能来自窗口本身，不与悬空推迟混淆）。
	surge := strings.Join(surgeLines(8), "\n")
	writeTranscriptLines(t, h.projects, sid, h.tmp, []map[string]any{
		{"type": "user", "timestamp": nowISO(), "cwd": h.tmp, "sessionId": sid,
			"message": map[string]any{"role": "user", "content": "开始"}},
		{"type": "assistant", "timestamp": nowISO(),
			"message": map[string]any{"role": "assistant",
				"content": []any{map[string]any{"type": "text", "text": surge}},
				"usage": map[string]any{"input_tokens": 2000,
					"cache_read_input_tokens":     100,
					"cache_creation_input_tokens": 0,
					"output_tokens":               5}}},
	})

	// ① 守望线程自然开窗（真实 poll 循环流转）
	h.mustWait(func() bool { return hSt(h, sid).opened != nil }, "守望线程自然开窗")
	if rows := h.rowsOf("qwatch_hit", sid); len(rows) != 1 || rows[0]["unit_count"] != float64(8) {
		t.Fatalf("hit 行 = %v, want unit_count 8", rows)
	}
	if rows := h.rowsOf("qwatch_open", sid); len(rows) != 1 || rows[0]["prefix_tokens"] != float64(2100) {
		t.Fatalf("open 行 = %v, want prefix_tokens 2100（真实懒富化：2000+100）", rows)
	}

	// ② observe 演练跳自然到期（开窗瞬间不跳，首跳在 +1.2s；零网络）
	h.mustWait(func() bool { return len(h.rowsOf("beat", sid)) > 0 }, "observe 演练跳应自然到期")
	h.mustWait(func() bool { return clock.Now()-hSt(h, sid).lastWrite >= 1.6 },
		"闲置应过摆渡线")
	if h.enqueuedHas(sid) { // 窗口开着且闲置已过摆渡线：常规摆渡被推迟
		t.Fatal("窗口期不应入队（无悬空——推迟只能来自窗口）")
	}
	if h.handoffExists(sid, h.tmp) {
		t.Fatal("窗口期不应出交接")
	}

	// ③ 用户提交（作答）：answer（user 行）＋assistant 回应（非提问潮）一次落盘。
	//    回应行 timestamp 前移 +1200s——只作用量账本 ts（漏检关联的 ≥TTL 间隙
	//    原料），闲置判定走 mtime 不受影响。回应 usage 缓存零命中＝全量重付。
	respISO := time.Unix(t0.Unix()+1200, 0).UTC().Format("2006-01-02T15:04:05.000Z")
	appendTranscriptLines(t, h.projects, sid, []map[string]any{
		{"type": "user", "timestamp": nowISO(), "cwd": h.tmp, "sessionId": sid,
			"message": map[string]any{"role": "user", "content": "逐条答完了"}},
		{"type": "assistant", "timestamp": respISO,
			"message": map[string]any{"role": "assistant",
				"content": []any{map[string]any{"type": "text", "text": "收到，继续执行。"}},
				"usage": map[string]any{"input_tokens": 2000,
					"cache_read_input_tokens":     0,
					"cache_creation_input_tokens": 0,
					"output_tokens":               5}}},
	})

	// ④ 关窗：剩余跳取消、关窗事件落账（beats_fired=窗口实发数）
	h.mustWait(func() bool { return len(h.rowsOf("qwatch_close", sid)) > 0 }, "关窗事件应落账")
	s := hSt(h, sid)
	if s.opened != nil || len(s.plan) != 0 {
		t.Fatalf("应关窗清计划: %+v", s)
	}
	fired := len(h.rowsOf("beat", sid))
	closeRows := h.rowsOf("qwatch_close", sid)
	if len(closeRows) != 1 {
		t.Fatalf("close 行数 = %d, want 1", len(closeRows))
	}
	if fired < 1 || closeRows[0]["beats_fired"] != float64(fired) {
		t.Fatalf("beats_fired = %v, want 实发 %d", closeRows[0]["beats_fired"], fired)
	}
	if closeRows[0]["close_reason"] != "write" {
		t.Fatalf("close_reason = %v, want write", closeRows[0]["close_reason"])
	}
	h.waitCond(1500*time.Millisecond, func() bool { return false }) // 让守望再跑几轮
	if got := len(h.rowsOf("beat", sid)); got != fired {            // 剩余跳未再发
		t.Fatalf("beat 行数 = %d, want %d", got, fired)
	}
	if hSt(h, sid).opened != nil { // 不重开（末条已非提问潮）
		t.Fatal("不应重开")
	}
	if got := len(h.rowsOf("qwatch_hit", sid)); got != 1 {
		t.Fatalf("qwatch_hit 行数 = %d, want 1", got)
	}

	// ⑤ 恢复常规摆渡调度：闲置过线 → 入队 → 假摆渡出交接
	h.mustWait(func() bool { return h.enqueuedHas(sid) }, "应恢复入队")
	h.mustWait(func() bool { return h.handoffExists(sid, h.tmp) }, "假摆渡应出交接")

	// ⑥ /stats：qwatch 节全链计数＋漏检关联信号（observe 演练不保温 → 计 1）
	q := h.statsQwatch()
	if q["mode"] != "observe" {
		t.Fatalf("mode = %v, want observe", q["mode"])
	}
	if q["windows_opened"] != float64(1) { // HTTP JSON 解码数值恒 float64
		t.Fatalf("windows_opened = %v, want 1", q["windows_opened"])
	}
	if q["beats_fired"] != float64(fired) {
		t.Fatalf("beats_fired = %v, want %d", q["beats_fired"], fired)
	}
	if bo, ok := q["beats_by_outcome"].(map[string]any); !ok || bo["observe"] != float64(fired) {
		t.Fatalf("observe 桶 = %v, want %d", q["beats_by_outcome"], fired)
	}
	if q["miss_signals"] != float64(1) {
		t.Fatalf("miss_signals = %v, want 1", q["miss_signals"])
	}

	// ⑦ 事件完整序列（账本行序＝时序）：hit → open → beat×N → close
	var kinds []string
	for _, r := range h.accts.Read(accounts.ReadOpts{Session: sid}) {
		k, _ := r["kind"].(string)
		if k == "qwatch_hit" || k == "qwatch_open" || k == "beat" || k == "qwatch_close" {
			kinds = append(kinds, k)
		}
	}
	want := []string{"qwatch_hit", "qwatch_open"}
	for i := 0; i < fired; i++ {
		want = append(want, "beat")
	}
	want = append(want, "qwatch_close")
	if !slices.Equal(kinds, want) {
		t.Fatalf("事件序列 = %v, want %v", kinds, want)
	}
}

func TestControlSessionZeroQwatchEvents(t *testing.T) {
	// 对照：非提问潮会话照常被守望摆渡（守望确实在跑），但零问询守望事件——
	// 命中/开窗/跳/关窗四类事件与 beat 全空，/stats 计数全零。
	h := newWHarn(t)
	h.cfg.QuestionWatch.Mode = "observe"
	h.cfg.QuestionWatch.BeatIntervalS = 3600.0
	h.cfg.QuestionWatch.FerryDeadlineLeadS = 1.0
	h.cfg.Thresholds.BlockS = 600.0
	sid := "e2e-calm"
	writeE2ESession(t, h.projects, sid, h.tmp, "干完了", 2000, 100) // 普通会话：无提问潮
	h.mustWait(func() bool { return h.handoffExists(sid, h.tmp) },
		"前提：对照会话照常摆渡（守望在跑）")
	h.waitCond(1*time.Second, func() bool { return false }) // 再让守望跑几轮
	for _, kind := range []string{"qwatch_hit", "qwatch_open", "qwatch_close", "beat"} {
		if rows := h.accts.Read(accounts.ReadOpts{Kind: kind}); len(rows) != 0 {
			t.Fatalf("%s 行 = %v, want 空", kind, rows)
		}
	}
	s := hSt(h, sid)
	if s.opened != nil || len(s.plan) != 0 {
		t.Fatalf("对照会话不应有窗: %+v", s)
	}
	q := h.statsQwatch()
	if q["hits"] != float64(0) || q["windows_opened"] != float64(0) ||
		q["beats_fired"] != float64(0) { // HTTP JSON 解码数值恒 float64
		t.Fatalf("计数应全零: %+v", q)
	}
	if q["miss_signals"] != float64(0) {
		t.Fatalf("miss_signals = %v, want 0", q["miss_signals"])
	}
}

// ---- Harness 接线（票02 harness_wires_ferry_daemon_for_mutex） ----

func TestHarnessWiresFerryDaemonForMutex(t *testing.T) {
	// Harness/serve 接线：守望持有 Daemon 引用（互斥探测用）。
	h := newWHarn(t)
	if h.w.Daemon != h.d {
		t.Fatal("守望应持有 FerryDaemon 引用")
	}
}

// ---- health miss_signals（真实账本行驱动） ----

func TestHealthReportsMissSignalsFromAccounts(t *testing.T) {
	// /stats qwatch 节 miss_signals：真实账本行（Accounts 落盘再读）驱动现算；
	// accounts 未接线（旧调用零改动）→ 0 占位。
	tmp := t.TempDir()
	led := ledger.New()
	accts, err := accounts.New(filepath.Join(tmp, "data"))
	if err != nil {
		t.Fatal(err)
	}
	t0 := clock.Now() - 1200
	common := func() accounts.Fields {
		return accounts.Fields{
			"agent": "cc", "project": "C:/p"}
	}()
	hit := common
	hit["session_id"] = "m-1"
	hit["unit_count"] = 8
	hit["marker_lines"] = 8
	hit["qmark_lines"] = 0
	hit["numbered_lines"] = 0
	hit["transcript_path"] = "C:/x/m-1.jsonl"
	if _, err := accts.Record("qwatch_hit", t0, hit); err != nil {
		t.Fatal(err)
	}
	u1 := accounts.Fields{"agent": "cc", "project": "C:/p",
		"session_id": "m-1"}
	u1["model"] = ""
	u1["title"] = ""
	u1["input_tokens"] = 30000
	u1["cache_read_tokens"] = 100
	u1["cache_creation_tokens"] = 0
	u1["output_tokens"] = 5
	u1["offset"] = 10
	u1["subagent"] = ""
	if _, err := accts.Record("usage", t0, u1); err != nil {
		t.Fatal(err)
	}
	u2 := accounts.Fields{"agent": "cc", "project": "C:/p",
		"session_id": "m-1"}
	u2["model"] = ""
	u2["title"] = ""
	u2["input_tokens"] = 30000
	u2["cache_read_tokens"] = 0
	u2["cache_creation_tokens"] = 0
	u2["output_tokens"] = 5
	u2["offset"] = 20
	u2["subagent"] = ""
	if _, err := accts.Record("usage", t0+1200, u2); err != nil {
		t.Fatal(err)
	}
	d := NewDaemon(mergeCfg(), led, nil, func(*ledger.SessionState) bool { return true }, accts, 0, nil)
	if got := d.Health()["qwatch"].(map[string]any)["miss_signals"]; got != 1 {
		t.Fatalf("miss_signals = %v, want 1", got)
	}
	d2 := NewDaemon(mergeCfg(), led, nil, func(*ledger.SessionState) bool { return true }, nil, 0, nil)
	if got := d2.Health()["qwatch"].(map[string]any)["miss_signals"]; got != 0 {
		t.Fatalf("未接线 miss_signals = %v, want 0", got)
	}
}

// ---- 回归挂点（Q14 段二产物接入后启用） ----

func TestBeatRequestShapeLock(t *testing.T) {
	t.Skip("Q14 段二产物（~/ferryman/captures/ 真实请求捕获模板＋" +
		"BeatBuilder）接入后启用：届时加载捕获模板，断言 beat " +
		"请求被缓存前缀字段（system/tools/messages/cache_control）" +
		"与 CC 真实请求逐字段 diff 全等（spec 决策 4 保形硬要求；" +
		"采样参数不在等价范围——max_tokens=1 封顶不参与比对）")
}

// ---- 票04 HTTP 集成：一键停（test_qwatch_stop_integration_over_http） ----

func TestQWatchStopIntegrationOverHTTP(t *testing.T) {
	// 集成级（真守望线程＋HTTP）：写提问潮 jsonl → 自然开窗 → POST
	// /qwatch_stop → 在飞计划取消、不再开窗、/stats 报 off。
	h := newWHarn(t)
	h.cfg.QuestionWatch.Mode = "observe"
	h.cfg.QuestionWatch.BeatIntervalS = 3600.0
	h.cfg.QuestionWatch.FerryDeadlineLeadS = 1.0
	h.cfg.Thresholds.BlockS = 600.0 // 死线远在测试窗口之外
	sid := "stop-http"
	surge := strings.Join(surgeLines(7), "\n")
	writeTranscriptLines(t, h.projects, sid, h.tmp, []map[string]any{
		{"type": "user", "timestamp": nowISO(), "cwd": h.tmp, "sessionId": sid,
			"message": map[string]any{"role": "user", "content": "开始干"}},
		{"type": "assistant", "timestamp": nowISO(),
			"message": map[string]any{"id": "msg_1", "role": "assistant",
				"content": []any{
					map[string]any{"type": "text", "text": surge},
					map[string]any{"type": "tool_use", "id": "tu_aq",
						"name": "AskUserQuestion", "input": map[string]any{}},
				},
				"usage": map[string]any{"input_tokens": 2000,
					"cache_read_input_tokens":     100,
					"cache_creation_input_tokens": 0,
					"output_tokens":               5}}},
	})
	h.mustWait(func() bool { return hSt(h, sid).opened != nil }, "前提：守望线程自然开窗")
	if len(hSt(h, sid).plan) == 0 { // 在飞计划已排定
		t.Fatal("应已排计划")
	}
	if rows := h.rowsOf("beat", sid); len(rows) != 0 { // 未来跳：一跳未发
		t.Fatalf("beat 行 = %v, want 空", rows)
	}

	out := postJSON(t, h.port, "/qwatch_stop", h.token, map[string]any{})
	if out["ok"] != true || out["mode"] != "off" || out["cancelled"] != float64(1) {
		t.Fatalf("qwatch_stop = %v, want ok/off/1", out)
	}
	s := hSt(h, sid)
	if s.opened != nil || len(s.plan) != 0 {
		t.Fatalf("应清窗清计划: %+v", s)
	}
	closeRows := h.rowsOf("qwatch_close", sid)
	if len(closeRows) != 1 || closeRows[0]["close_reason"] != "stop" { // 一键停关窗也落事件
		t.Fatalf("close 行 = %v, want 1 条 stop", closeRows)
	}
	q := h.statsQwatch()
	if q["mode"] != "off" {
		t.Fatalf("mode = %v, want off", q["mode"])
	}
	if q["windows_opened"] != float64(1) { // HTTP JSON 解码数值恒 float64
		t.Fatalf("windows_opened = %v, want 1", q["windows_opened"])
	}
	if q["beats_fired"] != float64(0) {
		t.Fatalf("beats_fired = %v, want 0", q["beats_fired"])
	}

	// 置 off 后再写入：不开窗、不复发
	appendTranscriptLines(t, h.projects, sid, []map[string]any{
		{"type": "user", "timestamp": nowISO(),
			"message": map[string]any{"role": "user", "content": "继续"}},
	})
	h.waitCond(1500*time.Millisecond, func() bool { return false }) // 让守望跑几轮
	if hSt(h, sid).opened != nil {
		t.Fatal("off 后不应开窗")
	}
	if q := h.statsQwatch(); q["windows_opened"] != float64(1) {
		t.Fatalf("windows_opened = %v, want 1（不复发）", q["windows_opened"])
	}
	if rows := h.rowsOf("beat", sid); len(rows) != 0 {
		t.Fatalf("beat 行 = %v, want 空", rows)
	}
}

// ---- 转录写盘小工具 ----

// surgeLines e2e 语料：i ∈ 1..n 的 ❓ **Qi** 这该如何取舍？（Python f-string 同文）。
func surgeLines(n int) []string {
	ls := make([]string, 0, n)
	for i := 1; i <= n; i++ {
		ls = append(ls, "❓ **Q"+strconv.Itoa(i)+"** 这该如何取舍？")
	}
	return ls
}

// writeTranscriptLines 写整份转录（行集一次落盘 + utime now）。
func writeTranscriptLines(t *testing.T, projects, sid, cwd string, lines []map[string]any) {
	t.Helper()
	dir := filepath.Join(projects, "C--proj")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	f := filepath.Join(dir, sid+".jsonl")
	var b strings.Builder
	for _, ln := range lines {
		raw, err := json.Marshal(ln)
		if err != nil {
			t.Fatal(err)
		}
		b.Write(raw)
		b.WriteString("\n")
	}
	if err := os.WriteFile(f, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := os.Chtimes(f, now, now); err != nil {
		t.Fatal(err)
	}
}

// appendTranscriptLines 向已有转录追加行。
func appendTranscriptLines(t *testing.T, projects, sid string, lines []map[string]any) {
	t.Helper()
	f := filepath.Join(projects, "C--proj", sid+".jsonl")
	fh, err := os.OpenFile(f, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer fh.Close()
	for _, ln := range lines {
		raw, err := json.Marshal(ln)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fh.Write(append(raw, '\n')); err != nil {
			t.Fatal(err)
		}
	}
}
