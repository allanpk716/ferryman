package daemon

// 规格：tests/test_gate.py 全部 43 例 1:1（T07 闸门状态机全分支 + T44b/T46/
// 缺口A/T48 票02；T48 票03 的 2 例守望用例票16 Watcher 装配后转绿——见下方实现）。
//
// 时间纪律：Python 版以真实 time.time() + idle_s 相对量驱动；Go 版 clock.Now
// 包级注入冻结为 t0（advance 推进），测试全确定性——idle 判定、pending TTL、
// 停车过期、窗口 dur_s 全部走注入时钟。
//
// 记账失败的 monkeypatch 用例（test_window_wait_never_raises /
// test_note_usage_record_failure_keeps_window）：Python 注入 raise 的
// _record_window；Go 记账无异常源（Acct 打印吞错，windows.go 顶部已声明
// 差异：pop 恒达）——等价故障注入为"拆除账本目录 → Record 落盘必败"，
// 断言转向 Go 契约（主路径不炸 + 窗口按 Go 语义收口）。

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
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
	"ferryman/internal/store"
)

const (
	testSummarizeS = 10.0
	testBlockS     = 30.0
	testMinCtx     = 100
)

// freezeClock 冻结包级时钟并返回可推进的当前时刻指针（用毕 Cleanup 还原）。
func freezeClock(t *testing.T, v float64) *float64 {
	t.Helper()
	orig := clock.Now
	cur := v
	clock.Now = func() float64 { return cur }
	t.Cleanup(func() { clock.Now = orig })
	return &cur
}

// ---- Python env fixture：直构 Daemon（无账本 Accounts，旧测试形态） ----

type gateEnv struct {
	d     *Daemon
	led   *ledger.Ledger
	store *store.Store
	tmp   string
	t0    float64
	now   *float64

	mu       sync.Mutex
	enqueued []string
}

func newGateEnv(t *testing.T) *gateEnv {
	t.Helper()
	e := &gateEnv{tmp: t.TempDir(), t0: 1_800_000_000.0}
	e.now = freezeClock(t, e.t0)
	st, err := store.New(filepath.Join(e.tmp, "data"))
	if err != nil {
		t.Fatal(err)
	}
	e.store = st
	e.led = ledger.New()
	cfg := config.Default()
	// 测试卫生（2026-09-30 生产污染案）：DataDir 必须钉在沙箱——裸 Default 的
	// 空值会解析到 ~/ferryman，gateWarn 的警告行写进生产 gate.log。
	cfg.Server.DataDir = filepath.Join(e.tmp, "data")
	cfg.GateCC = "enforce"
	// Python ThresholdCfg(summarize_s, block_s, min_ctx_tokens)——cache_warn_s
	// 携带 dataclass 默认 720，Go 显式同值。
	cfg.Thresholds = config.ThresholdCfg{
		SummarizeS: testSummarizeS, BlockS: testBlockS, MinCtxTokens: testMinCtx,
		CacheWarnS: 720,
	}
	e.d = NewDaemon(cfg, e.led, st, func(s *ledger.SessionState) bool {
		e.mu.Lock()
		e.enqueued = append(e.enqueued, s.SessionID)
		e.mu.Unlock()
		return true
	}, nil, 0, nil)
	return e
}

func (e *gateEnv) advance(dt float64) { *e.now += dt }

func (e *gateEnv) enqueuedList() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.enqueued...)
}

// reg Python _reg：登记会话，last_write = t0 - idle_s。
func (e *gateEnv) reg(sid, path, cwd string, idleS float64, peak int) {
	e.led.TouchFull("cc", sid, path, e.t0-idleS, 10, cwd, "", peak, 0)
}

func (e *gateEnv) sub(t *testing.T, event, sid string) map[string]any {
	t.Helper()
	r, err := e.d.Subagent(map[string]any{"event": event, "agent": "cc", "session_id": sid})
	if err != nil {
		t.Fatalf("sub %s %s: %v", event, sid, err)
	}
	return r
}

// gateBody Python _body(sid, path, cwd, prompt="继续")。
func gateBody4(sid, path, cwd, prompt string) map[string]any {
	return map[string]any{"agent": "cc", "session_id": sid, "transcript_path": path,
		"cwd": cwd, "prompt": prompt}
}

func gateBody(sid, path, cwd string) map[string]any {
	return gateBody4(sid, path, cwd, "继续")
}

// ---- Python wenv/_daemon_acc fixture：带真 Accounts（窗口流水/记账断言用） ----

type wenvT struct {
	gateEnv
	acc    *accounts.Accounts
	accDir string
}

func newWenv(t *testing.T) *wenvT {
	t.Helper()
	w := &wenvT{gateEnv: gateEnv{tmp: t.TempDir(), t0: 1_800_000_000.0}}
	w.now = freezeClock(t, w.t0)
	acc, err := accounts.New(filepath.Join(w.tmp, "acc"))
	if err != nil {
		t.Fatal(err)
	}
	w.acc = acc
	w.accDir = filepath.Join(w.tmp, "acc")
	st, err := store.New(filepath.Join(w.tmp, "data"))
	if err != nil {
		t.Fatal(err)
	}
	w.store = st
	w.led = ledger.New()
	cfg := config.Default()
	// 测试卫生（2026-09-30 生产污染案）：同 newGateEnv——DataDir 钉沙箱。
	cfg.Server.DataDir = filepath.Join(w.tmp, "data")
	cfg.GateCC = "enforce"
	cfg.Thresholds = config.ThresholdCfg{
		SummarizeS: testSummarizeS, BlockS: testBlockS, MinCtxTokens: testMinCtx,
		CacheWarnS: 720,
	}
	w.d = NewDaemon(cfg, w.led, st, func(*ledger.SessionState) bool { return true },
		acc, 0, nil)
	return w
}

// windowRows Python _window_rows：window 科目流水中该会话的行。
func (w *wenvT) windowRows(sid string) []map[string]any {
	out := []map[string]any{}
	for _, r := range w.acc.Read(accounts.ReadOpts{Kind: "window"}) {
		if r["session_id"] == sid {
			out = append(out, r)
		}
	}
	return out
}

// putUsage Python _usage。
func putUsage(t *testing.T, acc *accounts.Accounts, ts float64, sid string, i, cr, cc int) {
	t.Helper()
	if _, err := acc.Record("usage", ts, accounts.Fields{
		"agent": "cc", "session_id": sid, "lineage_id": "L-" + sid,
		"project": "C:/proj", "model": "glm-5.3", "title": "",
		"input_tokens": i, "cache_read_tokens": cr, "cache_creation_tokens": cc,
		"output_tokens": 10, "offset": 0, "subagent": "",
	}); err != nil {
		t.Fatal(err)
	}
}

// ---- 缺口A 夹具：悬空 / 自愈 jsonl（Python _mk_dangling/_mk_resolved） ----

func mkDangling(t *testing.T, p string) string {
	t.Helper()
	line, _ := json.Marshal(map[string]any{"type": "assistant", "message": map[string]any{
		"content": []any{map[string]any{"type": "tool_use", "id": "t1", "name": "Task",
			"input": map[string]any{}}}}})
	if err := os.WriteFile(p, append(line, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func mkResolved(t *testing.T, p string) string {
	t.Helper()
	tu, _ := json.Marshal(map[string]any{"type": "assistant", "message": map[string]any{
		"content": []any{map[string]any{"type": "tool_use", "id": "t1", "name": "Task",
			"input": map[string]any{}}}}})
	tr, _ := json.Marshal(map[string]any{"type": "user", "message": map[string]any{
		"content": []any{map[string]any{"type": "tool_result", "tool_use_id": "t1",
			"content": "ok"}}}})
	if err := os.WriteFile(p, []byte(string(tu)+"\n"+string(tr)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// ---- T48 夹具：真实转录 jsonl（Python _write_session/_append_blocks/_async/_sync） ----

func asyncBlocks() []map[string]any {
	return []map[string]any{
		{"type": "assistant", "message": map[string]any{"role": "assistant", "content": []any{
			map[string]any{"type": "tool_use", "id": "t1", "name": "Task",
				"input": map[string]any{"prompt": "干活", "run_in_background": true}}}}},
		{"type": "user", "message": map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "tool_result", "tool_use_id": "t1",
				"content": "Async agent launched successfully"}}}},
	}
}

func syncBlocks() []map[string]any {
	return []map[string]any{
		{"type": "assistant", "message": map[string]any{"role": "assistant", "content": []any{
			map[string]any{"type": "tool_use", "id": "t2", "name": "Task",
				"input": map[string]any{"prompt": "短活"}}}}},
		{"type": "user", "message": map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "tool_result", "tool_use_id": "t2", "content": "done"}}}},
	}
}

// writeSession Python _write_session：写转录 jsonl + 登台账（尾判可读的真实文件）。
func (w *wenvT) writeSession(t *testing.T, sid string, blocks []map[string]any, idleS float64) string {
	t.Helper()
	p := filepath.Join(w.tmp, sid+".jsonl")
	var b strings.Builder
	for _, blk := range blocks {
		line, err := json.Marshal(blk)
		if err != nil {
			t.Fatal(err)
		}
		b.Write(line)
		b.WriteString("\n")
	}
	if err := os.WriteFile(p, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	w.led.TouchFull("cc", sid, p, w.t0-idleS, 10, "C:/proj", "", 150000, 0)
	return p
}

// appendBlocks Python _append_blocks：两阶段交错用例——向已有转录追加块。
func (w *wenvT) appendBlocks(t *testing.T, p string, blocks []map[string]any) {
	t.Helper()
	fh, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer fh.Close()
	for _, blk := range blocks {
		line, err := json.Marshal(blk)
		if err != nil {
			t.Fatal(err)
		}
		fh.Write(line)
		fh.WriteString("\n")
	}
}

// isoUTC Python datetime.now(timezone.utc).isoformat().replace("+00:00","Z") 的
// epoch 形（covers_until 用）。
func isoUTC(epoch float64) string {
	sec := math.Floor(epoch)
	return time.Unix(int64(sec), int64((epoch-sec)*1e9)).
		UTC().Format("2006-01-02T15:04:05.999999Z07:00")
}

// indexHandoffs 读 store 的 index.json（branch5 断言 blocked_at 入 index）。
func indexHandoffs(t *testing.T, dataDir string) []map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dataDir, "data", "index.json"))
	if err != nil {
		t.Fatal(err)
	}
	var idx struct {
		Handoffs []map[string]any `json:"handoffs"`
	}
	if err := json.Unmarshal(data, &idx); err != nil {
		t.Fatal(err)
	}
	return idx.Handoffs
}

// assertStr helper：m[k] == want（含存在性）。
func assertStr(t *testing.T, m map[string]any, k, want string) {
	t.Helper()
	if got, _ := m[k].(string); got != want {
		t.Fatalf("%s = %q, want %q", k, got, want)
	}
}

// ---- 前置分支（tests/test_gate.py:44-88） ----

func TestBypassPrefix(t *testing.T) {
	e := newGateEnv(t)
	r := e.d.Gate(gateBody4("s", "p", "c", "!!我知道缓存死了，继续"))
	if r["decision"] != "allow" || r["reason"] != "bypass" {
		t.Fatalf("r = %v, want allow/bypass", r)
	}
	if e.d.Stats.Bypass != 1 {
		t.Fatalf("bypass = %d, want 1", e.d.Stats.Bypass)
	}
}

func TestBypassPrefixQiangxu(t *testing.T) {
	// 「强续」前缀放行——CC 下 !! 不可达（! 首字符即触发 bash 模式），换可达关键词。
	e := newGateEnv(t)
	r := e.d.Gate(gateBody4("s", "p", "c", "强续我知道缓存死了，继续"))
	if r["decision"] != "allow" || r["reason"] != "bypass" {
		t.Fatalf("r = %v, want allow/bypass", r)
	}
	if e.d.Stats.Bypass != 1 {
		t.Fatalf("bypass = %d, want 1", e.d.Stats.Bypass)
	}
}

func TestNoLedger(t *testing.T) {
	e := newGateEnv(t)
	r := e.d.Gate(gateBody("ghost", "C:/nope.jsonl", "C:/any"))
	if r["decision"] != "allow" || r["reason"] != "no-ledger" {
		t.Fatalf("r = %v, want allow/no-ledger", r)
	}
}

func TestModeOff(t *testing.T) {
	e := newGateEnv(t)
	e.d.Cfg.GateCC = "off"
	e.reg("s1", "C:/p1.jsonl", "C:/proj", 9999, 99999)
	assertStr(t, e.d.Gate(gateBody("s1", "C:/p1.jsonl", "C:/proj")), "reason", "mode-off")
}

func TestNotInWindowAllowsPlainly(t *testing.T) {
	e := newGateEnv(t)
	e.reg("s1", "C:/p1.jsonl", "C:/proj", testBlockS-10, 99999)
	r := e.d.Gate(gateBody("s1", "C:/p1.jsonl", "C:/proj"))
	if r["decision"] != "allow" {
		t.Fatalf("decision = %v", r["decision"])
	}
	if _, ok := r["additional_context"]; ok {
		t.Fatalf("未到死线不得带 additional_context: %v", r)
	}
}

func TestObserveModeWarnsNotBlocks(t *testing.T) {
	e := newGateEnv(t)
	e.d.Cfg.GateCC = "observe"
	proj := filepath.Join(e.tmp, "proj")
	e.reg("s1", "C:/p1.jsonl", proj, testBlockS+5, 99999)
	r := e.d.Gate(gateBody("s1", "C:/p1.jsonl", proj))
	if r["decision"] != "allow" {
		t.Fatalf("decision = %v", r["decision"])
	}
	ctx, _ := r["additional_context"].(string)
	if ctx == "" {
		t.Fatal("observe 应带警告 additional_context")
	}
	// 2026-09-18 文案修复：observe 永不拦，不得再发"将被拦"空头支票
	if !strings.Contains(ctx, "只提醒不拦") {
		t.Fatalf("文案缺「只提醒不拦」: %s", ctx)
	}
	if strings.Contains(ctx, "将被拦") {
		t.Fatalf("observe 不得发「将被拦」空头支票: %s", ctx)
	}
	if got := e.enqueuedList(); len(got) != 1 || got[0] != "s1" {
		t.Fatalf("enqueued = %v, want [s1]（observe 也触发摆渡补交接）", got)
	}
}

// ---- enforce：分支 5（有效交接 → block） ----

func TestBranch5BlockWithValidHandoff(t *testing.T) {
	e := newGateEnv(t)
	covers := isoUTC(e.t0)
	proj := filepath.Join(e.tmp, "proj")
	e.reg("s1", "C:/p1.jsonl", proj, testBlockS+5, 99999)
	en := e.store.SaveHandoff("s1", "cc", proj, "t", covers, "fresh", "md")
	r := e.d.Gate(gateBody4("s1", "C:/p1.jsonl", proj, "被拦的原话"))
	if r["decision"] != "block" {
		t.Fatalf("decision = %v, want block", r["decision"])
	}
	if r["suppressOriginalPrompt"] != true {
		t.Fatalf("suppressOriginalPrompt = %v", r["suppressOriginalPrompt"])
	}
	assertStr(t, r, "handoff_path", en.Path)
	if !strings.Contains(r["reason"].(string), "交接") {
		t.Fatalf("reason 缺交接指引: %v", r["reason"])
	}
	if got := e.store.PopPendingPrompt("s1", ""); got != "被拦的原话" {
		t.Fatalf("待续 prompt 保管 = %q", got)
	}
	handoffs := indexHandoffs(t, e.tmp)
	if len(handoffs) == 0 || handoffs[0]["blocked_at"] == nil {
		t.Fatalf("block 事件应入 index: %v", handoffs)
	}
	if got := e.enqueuedList(); len(got) != 0 {
		t.Fatalf("分支5 不再入队, enqueued = %v", got)
	}
}

// ---- 「热缓存不拦」（2026-10-04 用户拍板：拦窗内先问判热钟） ----

// TestGateHotCacheAllowsInBlockWindow 拦窗内但判热必活带（τ=0.8·TTL）→
// 放行+死线提示、不置 pending（连发不进分支6跑步机）；缓存死线过后 →
// 原状态机完整回归（分支7警告置 pending → 分支6真拦），保护不丢。
func TestGateHotCacheAllowsInBlockWindow(t *testing.T) {
	e := newGateEnv(t)
	e.d.Cfg.Heartbeat.TTLS = 1800 // 必活带 = 0.8·1800 = 1440s
	e.d.HeatClock = beat.NewLastRequestClock()
	proj := filepath.Join(e.tmp, "proj")
	e.reg("hot1", "C:/hot1.jsonl", proj, 2400, 99999) // 闲置 40min ≥ BlockS=30s 拦窗内
	e.d.HeatClock.Note("hot1", e.t0-600)              // 10min 前真发（同模型重放/心跳）→ 热

	r := e.d.Gate(gateBody("hot1", "C:/hot1.jsonl", proj))
	if r["decision"] != "allow" || !strings.Contains(r["additional_context"].(string), "仍热") {
		t.Fatalf("热缓存放行+提示: %v", r)
	}
	if _, pok := e.d.Pending.Get([2]string{"cc", "hot1"}); pok {
		t.Fatal("热放行不得置 pending")
	}
	r = e.d.Gate(gateBody("hot1", "C:/hot1.jsonl", proj))
	if r["decision"] != "allow" {
		t.Fatalf("热窗内连发应放行（不进跑步机）: %v", r)
	}

	e.advance(3000) // clock_s = 3600 > 1440 判冷；idle 5400 仍拦窗
	r = e.d.Gate(gateBody("hot1", "C:/hot1.jsonl", proj))
	if r["decision"] != "allow" || !strings.Contains(r["additional_context"].(string), "交接生成中") {
		t.Fatalf("冷后首条走分支7（警告+置 pending）: %v", r)
	}
	if _, pok := e.d.Pending.Get([2]string{"cc", "hot1"}); !pok {
		t.Fatal("分支7应置 pending")
	}
	r = e.d.Gate(gateBody("hot1", "C:/hot1.jsonl", proj))
	if r["decision"] != "block" {
		t.Fatalf("冷后第二条应真拦（保护回归）: %v", r)
	}
}

// TestGateHotCacheBeatsBranch5Handoff 分支5 前置判热：交接已在库（拦得住）
// 但缓存仍热 → 放行优先，不存待续原话（本道正是 35~50min 保温覆盖带——
// 2026-10-04 空键事故修复后此带会被「正确地拦」，拍板改为不拦便宜请求）。
func TestGateHotCacheBeatsBranch5Handoff(t *testing.T) {
	e := newGateEnv(t)
	e.d.Cfg.Heartbeat.TTLS = 1800
	e.d.HeatClock = beat.NewLastRequestClock()
	proj := filepath.Join(e.tmp, "proj")
	e.reg("hot5", "C:/p5.jsonl", proj, testBlockS+5, 99999)
	e.store.SaveHandoff("hot5", "cc", proj, "t", isoUTC(e.t0), "fresh", "md")
	e.d.HeatClock.Note("hot5", e.t0-300) // 5min 前真发 → 热
	r := e.d.Gate(gateBody4("hot5", "C:/p5.jsonl", proj, "原话照发"))
	if r["decision"] != "allow" || !strings.Contains(r["additional_context"].(string), "仍热") {
		t.Fatalf("交接在库但缓存热 → 放行优先: %v", r)
	}
	if got := e.store.PopPendingPrompt("hot5", ""); got != "" {
		t.Fatalf("放行不得存待续原话: %q", got)
	}
}

// TestGateHotCacheConservativeWithoutObservation 保守沿：有钟但本会话无观测
// （重启丢钟/从未真发）→ 绝不伪造热，分支5 照拦。未接线（HeatClock nil）
// 形态由全部既有闸门测试钉死（夹具不接钟即旧行为）。
func TestGateHotCacheConservativeWithoutObservation(t *testing.T) {
	e := newGateEnv(t)
	e.d.Cfg.Heartbeat.TTLS = 1800
	e.d.HeatClock = beat.NewLastRequestClock() // 有钟、本 sid 无观测
	proj := filepath.Join(e.tmp, "proj")
	e.reg("nc1", "C:/nc.jsonl", proj, testBlockS+5, 99999)
	e.store.SaveHandoff("nc1", "cc", proj, "t", isoUTC(e.t0), "fresh", "md")
	r := e.d.Gate(gateBody("nc1", "C:/nc.jsonl", proj))
	if r["decision"] != "block" {
		t.Fatalf("无观测应照拦（不伪造热）: %v", r)
	}
}

func TestBranch5CopyGuidesPostClear(t *testing.T) {
	// block 文案须自带三步指引（/clear 会抹掉文案，空屏后用户无任何提示）。
	e := newGateEnv(t)
	proj := filepath.Join(e.tmp, "proj")
	e.reg("s1", "C:/p1.jsonl", proj, testBlockS+5, 99999)
	e.store.SaveHandoff("s1", "cc", proj, "t", isoUTC(e.t0), "fresh", "md")
	r := e.d.Gate(gateBody4("s1", "C:/p1.jsonl", proj, "被拦"))
	if r["decision"] != "block" {
		t.Fatalf("decision = %v", r["decision"])
	}
	reason := r["reason"].(string)
	if !strings.Contains(reason, "随便发一个字") { // /clear 后怎么续（关键痛点）
		t.Fatalf("文案缺「随便发一个字」: %s", reason)
	}
	if !strings.Contains(reason, "强续") || strings.Contains(reason, "!!") { // 可达的逃生关键词
		t.Fatalf("逃生关键词应可达（强续有、!!无）: %s", reason)
	}
}

// ---- enforce：分支 7 → 6 → 降级 ----

func TestBranch7WarnThenBranch6BlocksThenDegrade(t *testing.T) {
	e := newGateEnv(t)
	proj2 := filepath.Join(e.tmp, "proj2") // 无交接的 cwd
	e.reg("s2", "C:/p2.jsonl", proj2, testBlockS+5, 99999)
	key := [2]string{"cc", "s2"}

	r1 := e.d.Gate(gateBody("s2", "C:/p2.jsonl", proj2)) // 分支7：警告一次
	if r1["decision"] != "allow" {
		t.Fatalf("decision = %v", r1["decision"])
	}
	if ctx, _ := r1["additional_context"].(string); ctx == "" {
		t.Fatal("分支7 应带警告")
	}
	if _, ok := e.d.Pending.Get(key); !ok {
		t.Fatal("分支7 应置 pending")
	}
	if got := e.enqueuedList(); len(got) != 1 || got[0] != "s2" {
		t.Fatalf("enqueued = %v, want [s2]", got)
	}

	for i := 1; i <= 3; i++ { // 分支6：连续 3 次 block
		r := e.d.Gate(gateBody("s2", "C:/p2.jsonl", proj2))
		if r["decision"] != "block" {
			t.Fatalf("第 %d 次 decision = %v", i, r["decision"])
		}
		reason := r["reason"].(string)
		if !strings.Contains(reason, fmt.Sprintf("第 %d 次", i)) {
			t.Fatalf("reason 缺「第 %d 次」: %s", i, reason)
		}
		if !strings.Contains(reason, "强续") { // 逃生关键词可达
			t.Fatalf("reason 缺「强续」: %s", reason)
		}
	}
	r4 := e.d.Gate(gateBody("s2", "C:/p2.jsonl", proj2)) // 第 4 次：降级放行
	if r4["decision"] != "allow" {
		t.Fatalf("第 4 次应降级 allow, got %v", r4["decision"])
	}
	if ctx, _ := r4["additional_context"].(string); ctx == "" {
		t.Fatal("降级放行应带警告")
	}
	if _, ok := e.d.Pending.Get(key); ok { // pending 清除
		t.Fatal("降级应清除 pending")
	}
}

// 2026-09-30 06703fbd 案回归：强续（bypass）必须清 pending——inWindow 含
// `|| pok`，pending 不清则活跃会话被永久卡在拦截窗口（实案：强续放行后
// 58.6s 的下一条消息仍被分支6第3次拦截，用户被逼弃会话）。清除后拦截计数
// 归零、保护不丢：再次长闲置从分支7警告重新起圈。
func TestBypassClearsPendingEscapesTreadmill(t *testing.T) {
	e := newGateEnv(t)
	proj := filepath.Join(e.tmp, "projT")
	e.reg("t1", "C:/t1.jsonl", proj, testBlockS+5, 99999)
	key := [2]string{"cc", "t1"}

	if r := e.d.Gate(gateBody("t1", "C:/t1.jsonl", proj)); r["decision"] != "allow" {
		t.Fatalf("分支7 decision = %v", r["decision"]) // 警告+置 pending
	}
	if r := e.d.Gate(gateBody("t1", "C:/t1.jsonl", proj)); r["decision"] != "block" {
		t.Fatalf("分支6 第1次 decision = %v", r["decision"])
	}

	// 会话恢复活跃：助手已回复（lastWrite 推进到 t0-1），闲置仅 1s。
	e.reg("t1", "C:/t1.jsonl", proj, 1, 99999)

	rb := e.d.Gate(gateBody4("t1", "C:/t1.jsonl", proj, "强续原话自己带上"))
	if rb["decision"] != "allow" || rb["reason"] != "bypass" {
		t.Fatalf("bypass = %v", rb)
	}
	if _, ok := e.d.Pending.Get(key); ok {
		t.Fatal("强续应清 pending")
	}

	// 强续后的下一条正常消息（无前缀、闲置 1s）——实案在此被拦第3次。
	if r := e.d.Gate(gateBody("t1", "C:/t1.jsonl", proj)); r["decision"] != "allow" {
		t.Fatalf("强续后活跃会话仍被拦: %v", r)
	}

	// 保护不丢：再次长闲置 → 分支7 重新警告（allow），下一条从「第 1 次」
	// 重新计数（bypass 清除连计数一起归零，不残留降级进度）。
	e.advance(testBlockS + 10)
	if r := e.d.Gate(gateBody("t1", "C:/t1.jsonl", proj)); r["decision"] != "allow" {
		t.Fatalf("新周期应分支7警告放行, got %v", r["decision"])
	}
	r2 := e.d.Gate(gateBody("t1", "C:/t1.jsonl", proj))
	if r2["decision"] != "block" ||
		!strings.Contains(r2["reason"].(string), "第 1 次") {
		t.Fatalf("新周期应从第1次重新拦: %v", r2)
	}
}

func TestBranch7NoEnqueueBelowMinCtx(t *testing.T) {
	e := newGateEnv(t)
	proj := filepath.Join(e.tmp, "small")
	e.reg("s3", "C:/p3.jsonl", proj, testBlockS+5, testMinCtx-1)
	r := e.d.Gate(gateBody("s3", "C:/p3.jsonl", proj))
	if r["decision"] != "allow" {
		t.Fatalf("decision = %v", r["decision"])
	}
	ctx, _ := r["additional_context"].(string)
	if ctx == "" { // 仍警告+置 pending
		t.Fatal("分支7 应带警告")
	}
	if !strings.Contains(ctx, "将被拦") { // enforce 下承诺为真
		t.Fatalf("enforce 警告应承诺「将被拦」: %s", ctx)
	}
	if got := e.enqueuedList(); len(got) != 0 { // 但小会话不入队
		t.Fatalf("enqueued = %v, want 空", got)
	}
}

// ---- pending 生命周期 ----

func TestPendingClearedOnNewIdleCycle(t *testing.T) {
	e := newGateEnv(t)
	proj := "C:/projX"
	e.reg("s4", "C:/p4.jsonl", proj, testBlockS+5, 99999)
	key := [2]string{"cc", "s4"}
	e.d.Gate(gateBody("s4", "C:/p4.jsonl", proj)) // 分支7 → pending
	if _, ok := e.d.Pending.Get(key); !ok {
		t.Fatal("分支7 应置 pending")
	}
	// 模拟用户回来又离开 summarize 时长（pending 早于新 last_write）
	// （Get 自取台账锁；字段写入再短暂持锁——sync.Mutex 不可重入。）
	st := e.led.Get("cc", "s4")
	e.led.Mu().Lock()
	lw := *e.now - (testSummarizeS + 5)
	st.LastWrite = lw
	e.led.Mu().Unlock()
	e.d.Pending.mu.Lock()
	e.d.Pending.t[key] = PendingRec{SetAt: lw - 10}
	e.d.Pending.mu.Unlock()
	r := e.d.Gate(gateBody("s4", "C:/p4.jsonl", proj))
	if r["decision"] != "allow" { // 未到 block，普通放行
		t.Fatalf("decision = %v, want allow", r["decision"])
	}
	if _, ok := e.d.Pending.Get(key); ok { // 新周期已清 pending
		t.Fatal("新闲置周期应清 pending")
	}
}

func TestPendingTTL24h(t *testing.T) {
	e := newGateEnv(t)
	key := [2]string{"cc", "s9"}
	e.d.Pending.Set(key)
	e.d.Pending.mu.Lock()
	e.d.Pending.t[key] = PendingRec{SetAt: *e.now - 25*3600}
	e.d.Pending.mu.Unlock()
	if _, ok := e.d.Pending.Get(key); ok {
		t.Fatal("超 24h TTL 应视为过期")
	}
}

// ---- T44b：缓存死线纯提醒（12min 信息条；不拦、不摆渡、0=关） ----

func TestCacheInfoAt15min(t *testing.T) {
	// 闲置 12–35min 区间：纯提醒信息条——含"全价计费/无需操作"，非"交接生成中"。
	e := newGateEnv(t)
	e.d.Cfg.Thresholds = config.Default().Thresholds // 真实默认：warn 720s / block 2100s
	e.reg("s5", "C:/p5.jsonl", "C:/proj", 900, 99999)
	r := e.d.Gate(gateBody("s5", "C:/p5.jsonl", "C:/proj"))
	if r["decision"] != "allow" {
		t.Fatalf("decision = %v", r["decision"])
	}
	ctx, _ := r["additional_context"].(string)
	if !strings.Contains(ctx, "全价计费") || !strings.Contains(ctx, "无需操作") {
		t.Fatalf("信息条缺关键文案: %s", ctx)
	}
	if strings.Contains(ctx, "交接生成中") {
		t.Fatalf("信息条不得含「交接生成中」: %s", ctx)
	}
	if got := e.enqueuedList(); len(got) != 0 { // 纯提醒：不触发摆渡
		t.Fatalf("enqueued = %v, want 空", got)
	}
	e.d.Cfg.GateCC = "observe" // observe 放行路径同样带信息条
	r2 := e.d.Gate(gateBody("s5", "C:/p5.jsonl", "C:/proj"))
	if r2["decision"] != "allow" {
		t.Fatalf("observe decision = %v", r2["decision"])
	}
	ctx2, _ := r2["additional_context"].(string)
	if !strings.Contains(ctx2, "全价计费") {
		t.Fatalf("observe 信息条缺「全价计费」: %s", ctx2)
	}
}

func TestNoInfoBelow12min(t *testing.T) {
	e := newGateEnv(t)
	e.d.Cfg.Thresholds = config.Default().Thresholds
	e.reg("s6", "C:/p6.jsonl", "C:/proj", 400, 99999)
	r := e.d.Gate(gateBody("s6", "C:/p6.jsonl", "C:/proj"))
	if len(r) != 1 || r["decision"] != "allow" { // 未到死线：无 additional_context
		t.Fatalf("r = %v, want 仅 {decision: allow}", r)
	}
}

func TestNoInfoAtBlockWindow(t *testing.T) {
	// ≥block 窗口走既有警告而非信息条——"建议 /clear" vs "无需操作"互斥可辨。
	e := newGateEnv(t)
	e.d.Cfg.GateCC = "observe"
	e.reg("s7", "C:/p7.jsonl", "C:/proj", testBlockS+5, 99999)
	r := e.d.Gate(gateBody("s7", "C:/p7.jsonl", "C:/proj"))
	if r["decision"] != "allow" {
		t.Fatalf("decision = %v", r["decision"])
	}
	ctx, _ := r["additional_context"].(string)
	if ctx == "" {
		t.Fatal("应带既有警告")
	}
	if !strings.Contains(ctx, "闲置") || !strings.Contains(ctx, "建议 /clear") {
		t.Fatalf("应走既有警告文案: %s", ctx)
	}
	if strings.Contains(ctx, "无需操作") { // 非信息条专属文案
		t.Fatalf("block 窗口不得出「无需操作」: %s", ctx)
	}
}

func TestCacheInfoDisabled(t *testing.T) {
	e := newGateEnv(t)
	th := config.Default().Thresholds
	th.CacheWarnS = 0
	e.d.Cfg.Thresholds = th
	e.reg("s8", "C:/p8.jsonl", "C:/proj", 900, 99999)
	r := e.d.Gate(gateBody("s8", "C:/p8.jsonl", "C:/proj"))
	if len(r) != 1 || r["decision"] != "allow" { // 0=关：无 additional_context
		t.Fatalf("r = %v, want 仅 {decision: allow}", r)
	}
}

// ---- T46：窗口前缀懒富化（peak_ctx 缺位时回落账本 usage 实报值） ----

func TestWindowPrefixPrefersPeakCtx(t *testing.T) {
	// peak_ctx 非零 → 照旧直接用，不回落账本。
	w := newWenv(t)
	w.led.TouchFull("cc", "w1", "C:/p1.jsonl", w.t0, 10, "C:/proj", "", 5000, 0)
	putUsage(t, w.acc, w.t0-60, "w1", 999, 149000, 0)
	w.sub(t, "start", "w1")
	w.sub(t, "stop", "w1")
	rows := w.windowRows("w1")
	if len(rows) != 1 {
		t.Fatalf("window 行数 = %d", len(rows))
	}
	if got := rows[0]["prefix_tokens"].(float64); got != 5000 {
		t.Fatalf("prefix_tokens = %v, want 5000", got)
	}
}

func TestWindowPrefixFallsBackToUsage(t *testing.T) {
	// peak_ctx=0 → 回落开窗前最后一条 usage 的 input+cache_read+cache_creation。
	w := newWenv(t)
	w.led.TouchFull("cc", "w2", "C:/p2.jsonl", w.t0, 10, "C:/proj", "", 0, 0)
	putUsage(t, w.acc, w.t0-300, "w2", 100, 120000, 300)
	putUsage(t, w.acc, w.t0-200, "w2", 200, 130000, 400)
	putUsage(t, w.acc, w.t0-100, "w2", 800, 140000, 9200) // 开窗前最后一条 → 150000
	w.sub(t, "start", "w2")
	w.sub(t, "stop", "w2")
	rows := w.windowRows("w2")
	if len(rows) != 1 {
		t.Fatalf("window 行数 = %d", len(rows))
	}
	if got := rows[0]["prefix_tokens"].(float64); got != 150000 {
		t.Fatalf("prefix_tokens = %v, want 150000", got)
	}
}

func TestWindowPrefixIgnoresRowsAfterOpen(t *testing.T) {
	// 开窗后（ts > opened_ts）写入的更大行不采纳——仍取 ≤opened_ts 的最后一条。
	w := newWenv(t)
	w.led.TouchFull("cc", "w3", "C:/p3.jsonl", w.t0, 10, "C:/proj", "", 0, 0)
	putUsage(t, w.acc, w.t0-100, "w3", 800, 140000, 9200) // 开窗前 → 150000
	w.sub(t, "start", "w3")
	w.advance(1)                                  // 避开 round(ts,3) 与开窗时刻同毫秒（时钟注入版 time.sleep）
	putUsage(t, w.acc, w.t0+1, "w3", 50000, 0, 0) // 窗口期间别处写入的更大行
	w.sub(t, "stop", "w3")
	rows := w.windowRows("w3")
	if len(rows) != 1 {
		t.Fatalf("window 行数 = %d", len(rows))
	}
	if got := rows[0]["prefix_tokens"].(float64); got != 150000 {
		t.Fatalf("prefix_tokens = %v, want 150000", got)
	}
}

func TestWindowPrefixClockSkewTakesLatestAny(t *testing.T) {
	// 无 ≤opened_ts 行（时钟毛刺）→ 退取该会话任意最后一条 usage。
	w := newWenv(t)
	w.led.TouchFull("cc", "w4", "C:/p4.jsonl", w.t0, 10, "C:/proj", "", 0, 0)
	w.sub(t, "start", "w4")
	putUsage(t, w.acc, w.t0+5, "w4", 800, 140000, 9200) // 毛刺：ts 落在开窗后
	w.sub(t, "stop", "w4")
	rows := w.windowRows("w4")
	if len(rows) != 1 {
		t.Fatalf("window 行数 = %d", len(rows))
	}
	if got := rows[0]["prefix_tokens"].(float64); got != 150000 {
		t.Fatalf("prefix_tokens = %v, want 150000", got)
	}
}

func TestWindowPrefixZeroWhenNoUsage(t *testing.T) {
	// 账本里全无该会话 usage → 0（窗口行仍要落，不能丢）。
	w := newWenv(t)
	w.led.TouchFull("cc", "w5", "C:/p5.jsonl", w.t0, 10, "C:/proj", "", 0, 0)
	w.sub(t, "start", "w5")
	w.sub(t, "stop", "w5")
	rows := w.windowRows("w5")
	if len(rows) != 1 {
		t.Fatalf("window 行数 = %d", len(rows))
	}
	if got := rows[0]["prefix_tokens"].(float64); got != 0 {
		t.Fatalf("prefix_tokens = %v, want 0", got)
	}
}

func TestWindowNoneAccountsNoCrash(t *testing.T) {
	// accounts=None（旧测试形态）→ 闭窗整条跳过，全程不炸。
	e := newGateEnv(t)
	e.led.TouchFull("cc", "w6", "C:/p6.jsonl", e.t0, 10, "C:/proj", "", 0, 0)
	e.sub(t, "start", "w6")
	e.sub(t, "stop", "w6")
	if len(e.d.windows) != 0 { // 窗已 pop，无行可记也不炸
		t.Fatalf("窗应已清空, 残留 %d", len(e.d.windows))
	}
}

// ---- 缺口A：机器等机器豁免（rev2 规格，验收 A1-A10 除 A6 人工项） ----

func TestA1SubagentActiveExempts(t *testing.T) {
	// A1：计数>0 → 放行 + 说明（含卡死恢复出路）+ 不置 pending + 不入队 + 计数不变。
	e := newGateEnv(t)
	proj := filepath.Join(e.tmp, "projA1")
	e.reg("a1", "C:/no-such-a1.jsonl", proj, testBlockS+5, 99999)
	e.led.SubagentEvent("cc", "a1", "start")
	r := e.d.Gate(gateBody("a1", "C:/no-such-a1.jsonl", proj))
	if r["decision"] != "allow" || r["reason"] != "machine-waiting" {
		t.Fatalf("r = %v, want allow/machine-waiting", r)
	}
	ctx := r["additional_context"].(string)
	if !strings.Contains(ctx, "放行") || !strings.Contains(ctx, "Esc") {
		t.Fatalf("说明应含「放行/Esc」: %s", ctx)
	}
	if _, ok := e.d.Pending.Get([2]string{"cc", "a1"}); ok { // 不置 pending
		t.Fatal("豁免不得置 pending")
	}
	if got := e.enqueuedList(); len(got) != 0 { // 不入队摆渡（分支7不可达）
		t.Fatalf("enqueued = %v", got)
	}
	if !e.led.SubagentActive("cc", "a1") { // 计数不受影响
		t.Fatal("子代理计数应不变")
	}
}

func TestA2DanglingOnlyExempts(t *testing.T) {
	// A2：仅悬空（计数=0）→ 同样放行（两道 OR 的直接验证）。
	e := newGateEnv(t)
	f := mkDangling(t, filepath.Join(e.tmp, "a2.jsonl"))
	proj := filepath.Join(e.tmp, "projA2")
	e.reg("a2", f, proj, testBlockS+5, 99999)
	r := e.d.Gate(gateBody("a2", f, proj))
	if r["decision"] != "allow" || r["reason"] != "machine-waiting" {
		t.Fatalf("r = %v, want allow/machine-waiting", r)
	}
	if _, ok := e.d.Pending.Get([2]string{"cc", "a2"}); ok {
		t.Fatal("豁免不得置 pending")
	}
	if got := e.enqueuedList(); len(got) != 0 {
		t.Fatalf("enqueued = %v", got)
	}
}

func TestA4ExemptionBeatsObserveWarnAndEnqueue(t *testing.T) {
	// A4：observe 模式下豁免同样生效（不警告不入队，mid-wait 摆渡堵死）；
	// 对照组证明非豁免路径零回归。
	e := newGateEnv(t)
	e.d.Cfg.GateCC = "observe"
	proj := filepath.Join(e.tmp, "projA4")
	e.reg("a4", "C:/no-such-a4.jsonl", proj, testBlockS+5, 99999)
	e.led.SubagentEvent("cc", "a4", "start")
	r := e.d.Gate(gateBody("a4", "C:/no-such-a4.jsonl", proj))
	if r["decision"] != "allow" || r["reason"] != "machine-waiting" {
		t.Fatalf("r = %v, want allow/machine-waiting", r)
	}
	if got := e.enqueuedList(); len(got) != 0 {
		t.Fatalf("enqueued = %v", got)
	}
	e.reg("a4b", "C:/no-such-a4b.jsonl", proj, testBlockS+5, 99999)
	r2 := e.d.Gate(gateBody("a4b", "C:/no-such-a4b.jsonl", proj))
	if r2["decision"] != "allow" {
		t.Fatalf("对照组 decision = %v", r2["decision"])
	}
	if ctx, _ := r2["additional_context"].(string); ctx == "" {
		t.Fatal("对照组应带警告")
	}
	if got := e.enqueuedList(); len(got) != 1 || got[0] != "a4b" { // 既有 observe 行为零回归
		t.Fatalf("enqueued = %v, want [a4b]", got)
	}
}

func TestA7CountLeakRecoversGate(t *testing.T) {
	// A7：计数泄漏期过后恢复拦截能力（前置：tool_result 已落盘=无悬空）。
	e := newGateEnv(t)
	f := mkResolved(t, filepath.Join(e.tmp, "a7.jsonl"))
	proj := filepath.Join(e.tmp, "projA7")
	e.reg("a7", f, proj, testBlockS+5, 99999)
	e.led.SubagentEvent("cc", "a7", "start") // Stop 丢失：计数停 1（last=t0）
	e.advance(4000)                          // 泄漏期已过（SubagentEventLeakS=3600）
	r := e.d.Gate(gateBody("a7", f, proj))
	if r["decision"] != "allow" { // 正常路径（分支7 警告）
		t.Fatalf("decision = %v", r["decision"])
	}
	if r["reason"] == "machine-waiting" {
		t.Fatal("泄漏期已过不得再豁免")
	}
	if got := e.enqueuedList(); len(got) != 1 || got[0] != "a7" { // 入队恢复=拦截能力恢复
		t.Fatalf("enqueued = %v, want [a7]", got)
	}
}

func TestA8PendingPreservedAcrossExemption(t *testing.T) {
	// A8：先置 pending → 子代理在飞提交=放行且 pending 原样（不清、不 bump
	// blocks 计数）→ 子代理结束后下一次提交按 pending 状态机走（分支6）。
	e := newGateEnv(t)
	f := mkResolved(t, filepath.Join(e.tmp, "a8.jsonl"))
	proj := filepath.Join(e.tmp, "projA8")
	e.reg("a8", f, proj, testBlockS+5, 99999)
	e.d.Gate(gateBody("a8", f, proj)) // 分支7：置 pending
	if _, ok := e.d.Pending.Get([2]string{"cc", "a8"}); !ok {
		t.Fatal("分支7 应置 pending")
	}
	e.led.SubagentEvent("cc", "a8", "start")
	r1 := e.d.Gate(gateBody("a8", f, proj)) // 豁免放行
	if r1["reason"] != "machine-waiting" {
		t.Fatalf("r1 reason = %v", r1["reason"])
	}
	rec, ok := e.d.Pending.Get([2]string{"cc", "a8"})
	if !ok || rec.Blocks != 0 { // 计数不变（未 bump）
		t.Fatalf("pending 应原样且 blocks=0: %+v ok=%v", rec, ok)
	}
	e.led.SubagentEvent("cc", "a8", "stop") // 计数归零
	r2 := e.d.Gate(gateBody("a8", f, proj))
	if r2["decision"] != "block" || !strings.Contains(r2["reason"].(string), "强续") { // pending 状态机照走
		t.Fatalf("r2 = %v", r2)
	}
}

func TestA9DanglingPersistentIsDesign(t *testing.T) {
	// A9：计数=0 且悬空=真 → 连续多次提交均放行（宁可不拦方向的显式验收）。
	e := newGateEnv(t)
	f := mkDangling(t, filepath.Join(e.tmp, "a9.jsonl"))
	proj := filepath.Join(e.tmp, "projA9")
	e.reg("a9", f, proj, testBlockS+5, 99999)
	for i := 0; i < 3; i++ {
		r := e.d.Gate(gateBody("a9", f, proj))
		if r["decision"] != "allow" || r["reason"] != "machine-waiting" {
			t.Fatalf("第 %d 次 r = %v", i, r)
		}
	}
}

func TestA10DanglingSelfheals(t *testing.T) {
	// A10：悬空期放行 → tool_result 落盘 → 下一次提交恢复正常闸门路径。
	e := newGateEnv(t)
	f := mkDangling(t, filepath.Join(e.tmp, "a10.jsonl"))
	proj := filepath.Join(e.tmp, "projA10")
	e.reg("a10", f, proj, testBlockS+5, 99999)
	if r := e.d.Gate(gateBody("a10", f, proj)); r["reason"] != "machine-waiting" {
		t.Fatalf("悬空期应豁免: %v", r)
	}
	mkResolved(t, f) // tool_result 落盘
	r := e.d.Gate(gateBody("a10", f, proj))
	if r["decision"] != "allow" || r["reason"] == "machine-waiting" {
		t.Fatalf("自愈后应走正常路径: %v", r)
	}
	if got := e.enqueuedList(); len(got) != 1 || got[0] != "a10" { // 分支7 正常入队
		t.Fatalf("enqueued = %v, want [a10]", got)
	}
}

func TestA5Branch6CopyDeterministicEscape(t *testing.T) {
	// A5：分支6 新文案——确定性出路（强续一次解除本轮拦截）；第4次降级不变。
	// （A3 口径：分支5/7 既有断言不动；分支6 文案断言随本条更新。）
	// 2026-09-30 06703fbd 案后语义升级：原「每一条都要强续」跑步机改为强续即
	// 清 pending（见 TestBypassClearsPendingEscapesTreadmill），文案随之改口径。
	e := newGateEnv(t)
	proj := filepath.Join(e.tmp, "projA5")
	e.reg("a5", "C:/no-such-a5.jsonl", proj, testBlockS+5, 99999)
	r0 := e.d.Gate(gateBody("a5", "C:/no-such-a5.jsonl", proj)) // 分支7：警告+置 pending
	if r0["decision"] != "allow" {
		t.Fatalf("decision = %v", r0["decision"])
	}
	if ctx, _ := r0["additional_context"].(string); ctx == "" {
		t.Fatal("分支7 应带警告")
	}
	for i := 1; i <= 3; i++ {
		r := e.d.Gate(gateBody("a5", "C:/no-such-a5.jsonl", proj))
		if r["decision"] != "block" {
			t.Fatalf("第 %d 次 decision = %v", i, r["decision"])
		}
		reason := r["reason"].(string)
		if !strings.Contains(reason, fmt.Sprintf("第 %d 次", i)) {
			t.Fatalf("reason 缺「第 %d 次」: %s", i, reason)
		}
		if !strings.Contains(reason, "以「强续」开头") || // 确定性出路
			!strings.Contains(reason, "解除本轮拦截") { // 强续一次即解，不再每条都要带
			t.Fatalf("reason 缺确定性出路文案: %s", reason)
		}
		if !strings.Contains(reason, "已保存") { // 原话下落明确
			t.Fatalf("reason 缺「已保存」: %s", reason)
		}
	}
	r4 := e.d.Gate(gateBody("a5", "C:/no-such-a5.jsonl", proj))
	if r4["decision"] != "allow" { // 第4次降级（行为不变）
		t.Fatalf("第 4 次应降级 allow, got %v", r4["decision"])
	}
}

// ---- T48 票02：等待窗口停表停车状态机（async 停车/latch/宽限/过期/豁免） ----

func TestAsyncStopParksUntilMainResumes(t *testing.T) {
	// 核心案：async 派发 stop 后不闭窗；主会话恢复调用才闭（main_resumed）。
	w := newWenv(t)
	sid := "as1"
	p := w.writeSession(t, sid, asyncBlocks(), 0)
	w.sub(t, "start", sid)
	w.advance(1) // dur_s > 0 可断言（时钟注入版 time.sleep(0.05)）
	w.sub(t, "stop", sid)
	if got := w.windowRows(sid); len(got) != 0 { // 不再秒闭（旧 bug：dur 0.6min 废数据）
		t.Fatalf("停车不得产生 window 行: %v", got)
	}
	if !w.d.WindowWait("cc", sid) { // 机器等待在停
		t.Fatal("window_wait 应为 true")
	}
	w.d.NoteUsage("cc", sid, *w.now+200) // 恢复调用（晚于停表+90s 宽限）
	rows := w.windowRows(sid)
	if len(rows) != 1 {
		t.Fatalf("rows = %v, want 1", rows)
	}
	if rows[0]["close_reason"] != "main_resumed" {
		t.Fatalf("close_reason = %v", rows[0]["close_reason"])
	}
	if rows[0]["dur_s"].(float64) <= 0 {
		t.Fatalf("dur_s = %v, want > 0", rows[0]["dur_s"])
	}
	if w.d.WindowWait("cc", sid) {
		t.Fatal("闭窗后 window_wait 应为 false")
	}
	_ = p
}

func TestAckTurnWithinGraceDoesNotClose(t *testing.T) {
	// ★ 宽限：stop 后 ack 确认回合（90s 内的 usage 行）不闭窗。
	// 当天实测 ack 均落在 stop 前（钩子时序），时序反转时靠宽限兜底
	// （round 0 评审 #10/#11 的廉价保险）。
	w := newWenv(t)
	sid := "as1b"
	w.writeSession(t, sid, asyncBlocks(), 0)
	w.sub(t, "start", sid)
	w.advance(1)
	w.sub(t, "stop", sid)
	w.d.NoteUsage("cc", sid, *w.now+5) // ack 行：stop 后 5s（宽限内）
	if got := w.windowRows(sid); len(got) != 0 {
		t.Fatalf("宽限内不闭窗, rows = %v", got)
	}
	if !w.d.WindowWait("cc", sid) {
		t.Fatal("窗应仍在停")
	}
	w.d.NoteUsage("cc", sid, *w.now+200) // 真恢复
	rows := w.windowRows(sid)
	if len(rows) != 1 || rows[0]["close_reason"] != "main_resumed" {
		t.Fatalf("rows = %v", rows)
	}
}

func TestSyncStopClosesImmediately(t *testing.T) {
	// 同步派发语义不变：stop 即闭（既有行为回归锁）。
	w := newWenv(t)
	sid := "sy1"
	w.writeSession(t, sid, syncBlocks(), 0)
	w.sub(t, "start", sid)
	w.sub(t, "stop", sid)
	rows := w.windowRows(sid)
	if len(rows) != 1 || rows[0]["close_reason"] != "subagents_done" {
		t.Fatalf("rows = %v, want 1 条 subagents_done", rows)
	}
	if w.d.WindowWait("cc", sid) {
		t.Fatal("闭窗后应 false")
	}
}

func TestInterleaveSyncAfterAsyncKeepsPark(t *testing.T) {
	// ★ latch（附录#1 两阶段写文件）：async 停车后再派 sync，sync 的 stop 不再
	// 走即闭——交错派发不丢 async 等待（round 0 e2 实验：一次性预写会使 stop 时
	// 尾判读到 sync、latch 永不触发，故先 async 停车、再追加 sync 块）。
	w := newWenv(t)
	sid := "ix1"
	p := w.writeSession(t, sid, asyncBlocks(), 0)
	w.sub(t, "start", sid)
	w.advance(1)
	w.sub(t, "stop", sid) // 第一段 async 的 stop → 停车
	if got := w.windowRows(sid); len(got) != 0 {
		t.Fatalf("rows = %v", got)
	}
	w.appendBlocks(t, p, syncBlocks())           // 再派 sync：追加转录块
	w.sub(t, "start", sid)                       // 续窗
	w.sub(t, "stop", sid)                        // sync 的 stop
	if got := w.windowRows(sid); len(got) != 0 { // 仍不闭（latch 生效）
		t.Fatalf("rows = %v", got)
	}
	if !w.d.WindowWait("cc", sid) {
		t.Fatal("窗应仍在停")
	}
	w.d.NoteUsage("cc", sid, *w.now+200)
	rows := w.windowRows(sid)
	if len(rows) != 1 || rows[0]["close_reason"] != "main_resumed" {
		t.Fatalf("rows = %v", rows)
	}
}

func TestOverlapSeqParksEvenWhenSyncStopSeenLast(t *testing.T) {
	// ★ 重叠序（附录#7）：async start→sync start→async stop→sync stop。
	// 停车判定只在计数归零时跑，届时尾部最后派发已是 sync——须靠 start 事件
	// 处理时的尾判置 latch，否则 sync 的 stop 会误闭 async 等待。
	w := newWenv(t)
	sid := "ov1"
	p := w.writeSession(t, sid, asyncBlocks(), 0)
	w.sub(t, "start", sid) // async start
	w.appendBlocks(t, p, syncBlocks())
	w.sub(t, "start", sid)                       // sync start（计数=2）
	w.sub(t, "stop", sid)                        // async 早到 stop（计数=1，无判定）
	w.sub(t, "stop", sid)                        // sync stop（归零）
	if got := w.windowRows(sid); len(got) != 0 { // 最终仍停车（不误闭）
		t.Fatalf("rows = %v", got)
	}
	if !w.d.WindowWait("cc", sid) {
		t.Fatal("窗应仍在停")
	}
}

func TestParkedWindowExemptsGateAndPromptKeepsPark(t *testing.T) {
	// 停车窗期间用户输消息 → 缺口 A 放行（machine-waiting），且窗口不因
	// prompt 闭——async 真身还在跑，等待没结束（今天会误拦/误闭的场景）。
	w := newWenv(t)
	sid := "as4"
	p := w.writeSession(t, sid, asyncBlocks(), 9999) // 闲置远超 block 线
	w.sub(t, "start", sid)
	w.advance(1)
	w.sub(t, "stop", sid)
	r := w.d.Gate(gateBody4(sid, p, "C:/proj", "继续"))
	if r["decision"] != "allow" || r["reason"] != "machine-waiting" {
		t.Fatalf("r = %v", r)
	}
	if !w.d.WindowWait("cc", sid) {
		t.Fatal("窗应仍在停")
	}
	if got := w.windowRows(sid); len(got) != 0 { // 停车窗不因 prompt 闭
		t.Fatalf("rows = %v", got)
	}
}

func TestParkedWindowExpires(t *testing.T) {
	// 停表超 PARK_EXPIRE_S → 懒过期闭窗记 expired，豁免随之失效。
	w := newWenv(t)
	sid := "as5"
	w.writeSession(t, sid, asyncBlocks(), 0)
	w.sub(t, "start", sid)
	w.advance(1)
	w.sub(t, "stop", sid)
	expired := *w.now - (ParkExpireS + 400)
	w.d.windows[[2]string{"cc", sid}].StopTS = &expired
	if w.d.WindowWait("cc", sid) {
		t.Fatal("过期应闭窗返回 false")
	}
	rows := w.windowRows(sid)
	if len(rows) == 0 || rows[len(rows)-1]["close_reason"] != "expired" {
		t.Fatalf("rows = %v, want expired", rows)
	}
}

func TestWindowWaitNeverRaises(t *testing.T) {
	// ★ 异常边界（附录#5）：记账路径炸了，谓词只返回 false 不外抛；闸门主路径
	// 不受影响照常决策。Python monkeypatch _record_window→raise；Go 记账无异常
	// 源（Acct 打印吞错）——注入"账本目录拆除→落盘必败"的等价故障。
	// 差异声明（windows.go closeWindowLocked）：Python 记账炸→窗保留待重试；
	// Go Acct 吞错故 pop 恒达——本例按 Go 契约断言（窗已摘、主路径不炸）。
	w := newWenv(t)
	sid := "as6"
	p := w.writeSession(t, sid, asyncBlocks(), 9999)
	w.sub(t, "start", sid)
	w.advance(1)
	w.sub(t, "stop", sid)
	expired := *w.now - (ParkExpireS + 400)
	w.d.windows[[2]string{"cc", sid}].StopTS = &expired

	if err := os.RemoveAll(w.accDir); err != nil { // 记账落盘必败
		t.Fatal(err)
	}
	if w.d.WindowWait("cc", sid) { // 不抛、按 false
		t.Fatal("过期应闭窗返回 false")
	}
	if _, ok := w.d.windows[[2]string{"cc", sid}]; ok { // Go 形：pop 恒达
		t.Fatal("Go 契约：记账失败不保留窗（差异已声明）")
	}
	r := w.d.Gate(gateBody4(sid, p, "C:/proj", "继续")) // 闸门主路径不炸、照常出决策
	if r["decision"] != "allow" && r["decision"] != "block" {
		t.Fatalf("决策异常: %v", r)
	}
}

func TestNoteUsageRecordFailureKeepsWindow(t *testing.T) {
	// ★ 附录#10：note_usage 记账炸 → 不抛。Python 断言窗保留（先记后 pop）；
	// Go Acct 打印吞错故闭窗恒达（windows.go 差异声明）——本例钉 Go 契约：
	// 记账失败绝不炸主路径，窗口语义照常收口。
	w := newWenv(t)
	sid := "as7"
	w.writeSession(t, sid, asyncBlocks(), 0)
	w.sub(t, "start", sid)
	w.advance(1)
	w.sub(t, "stop", sid)

	if err := os.RemoveAll(w.accDir); err != nil { // 记账落盘必败
		t.Fatal(err)
	}
	w.d.NoteUsage("cc", sid, *w.now+200) // 不炸
	if w.d.WindowWait("cc", sid) {       // Go 契约：闭窗恒达（差异已声明）
		t.Fatal("Go 契约：记账失败不保留窗（pop 恒达）")
	}
}

func TestReanchorExpiredParkRecordsExpiredNoFutureTS(t *testing.T) {
	// 重锚旧停车窗（附录#3/#13）：按距 stop_ts 超 PARK_EXPIRE_S 判过期（非
	// opened_ts）；closed_ts=min(stop+PARK_EXPIRE_S, now)——绝不出现未来时刻。
	w := newWenv(t)
	sid := "ix2"
	w.writeSession(t, sid, asyncBlocks(), 0)
	w.sub(t, "start", sid)
	w.advance(1)
	w.sub(t, "stop", sid)
	stop := *w.now - (ParkExpireS + 400)
	w.d.windows[[2]string{"cc", sid}].StopTS = &stop
	w.d.windows[[2]string{"cc", sid}].OpenedTS = stop - 300 // opened 更早：超 LEAK → 下个 start 走重锚
	w.sub(t, "start", sid)
	rows := w.windowRows(sid)
	if len(rows) != 1 || rows[0]["close_reason"] != "expired" {
		t.Fatalf("rows = %v, want 1 条 expired", rows)
	}
	if rows[0]["closed_ts"].(float64) > *w.now+1 { // 无未来时刻
		t.Fatalf("closed_ts = %v 出现未来时刻", rows[0]["closed_ts"])
	}
	if math.Abs(rows[0]["closed_ts"].(float64)-(stop+ParkExpireS)) >= 5 {
		t.Fatalf("closed_ts = %v, want ≈ stop+PARK_EXPIRE_S", rows[0]["closed_ts"])
	}
	if w.d.windows[[2]string{"cc", sid}].StopTS != nil { // 新窗在跑
		t.Fatal("重锚后新窗 stop_ts 应为空")
	}
}

func TestPromptStillClosesOpenWindow(t *testing.T) {
	// 未停车的窗口遇 prompt 照旧闭（R1 既有语义回归锁）。
	w := newWenv(t)
	sid := "sy6"
	p := w.writeSession(t, sid, syncBlocks(), 0)
	w.sub(t, "start", sid)
	w.d.Gate(gateBody4(sid, p, "C:/proj", "继续"))
	rows := w.windowRows(sid)
	if len(rows) != 1 || rows[0]["close_reason"] != "prompt" {
		t.Fatalf("rows = %v, want 1 条 prompt", rows)
	}
}

// ---- T48 票03：守望接线（2 例，票16 Watcher 装配后转绿） ----

func TestParkedWindowDefersFerryAndResumesAfterClose(t *testing.T) {
	// 回归锁（20260918 12:20/14:15 误摆渡案）：停车窗期间 maybe_enqueue 不入队
	// （推迟且不置 handed_off）；恢复调用闭窗后同一会话恢复入队。
	// test_gate.py::test_parked_window_defers_ferry_and_resumes_after_close 1:1。
	w := newWenv(t)
	sid := "wf1"
	w.writeSession(t, sid, asyncBlocks(), 0)
	cfg := config.Default()
	cfg.Thresholds = config.ThresholdCfg{SummarizeS: 10, BlockS: 30,
		MinCtxTokens: 100, CacheWarnS: 720}
	var mu sync.Mutex
	var calls []string
	// Python accounts=None：watcher 不采集不记账（旧测试形态）
	wtr := NewWatcher(cfg, w.led, nil, func(st *ledger.SessionState) bool {
		mu.Lock()
		calls = append(calls, st.SessionID)
		mu.Unlock()
		return true
	}, 0, nil, w.d, nil, nil)
	st := w.led.Get("cc", sid)
	w.led.Mu().Lock()
	st.ObservedActive = true
	st.LastWrite = *w.now - 999     // 闲置远超 summarize 线
	st.EnrichedWrite = st.LastWrite // 预置已富化：_enrich 会用转录重算
	st.PeakCtx = 150000             //   peak（无 usage 的转录算 0），与本用例无关
	w.led.Mu().Unlock()
	w.sub(t, "start", sid) // 开窗 + 停车
	w.sub(t, "stop", sid)
	wtr.maybeEnqueue(st)
	mu.Lock()
	if len(calls) != 0 { // 停车窗 → 推迟摆渡
		t.Fatalf("停车窗应推迟摆渡, calls = %v", calls)
	}
	mu.Unlock()
	if got := w.led.Get("cc", sid).HandedOffAt; got != 0 { // 推迟不置 handed_off（下轮重查）
		t.Fatalf("handed_off = %v, want 0", got)
	}
	w.d.NoteUsage("cc", sid, *w.now+200) // 恢复调用 → 窗闭
	wtr.maybeEnqueue(st)
	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 1 || calls[0] != sid { // 窗闭后照常摆渡
		t.Fatalf("calls = %v, want [wf1]", calls)
	}
}

func TestHarvestUsageFeedsNoteUsageMaxTS(t *testing.T) {
	// 票03③：_harvest_usage 落完 usage 行后把新行最大 ts 喂 note_usage
	// （agent 取会话自身，不硬编码）——停车窗靠守望采集感知主会话恢复调用。
	// test_gate.py::test_harvest_usage_feeds_note_usage_max_ts 的 Go 形：
	// Daemon 为具体类型无 RecDaemon 替身可注——以停车窗闭窗行为钉喂入值：
	// stop = t2 − ACK_GRACE − 0.5，喂最大 ts（t2）→ main_resumed 闭窗；
	// 若误喂首行 ts（t1 = t2−1）→ t1 < stop+GRACE 不闭（可判别）。
	w := newWenv(t)
	sid := "hu1"
	blocks := []map[string]any{
		{"type": "assistant", "timestamp": "2026-09-18T12:00:01Z", "cwd": "C:/proj",
			"message": map[string]any{"role": "assistant",
				"content": []any{map[string]any{"type": "text", "text": "一"}},
				"usage": map[string]any{"input_tokens": 100,
					"cache_read_input_tokens":     5000,
					"cache_creation_input_tokens": 0, "output_tokens": 1}}},
		{"type": "assistant", "timestamp": "2026-09-18T12:00:02Z",
			"message": map[string]any{"role": "assistant",
				"content": []any{map[string]any{"type": "text", "text": "二"}},
				"usage": map[string]any{"input_tokens": 200,
					"cache_read_input_tokens":     6000,
					"cache_creation_input_tokens": 0, "output_tokens": 1}}},
	}
	var b strings.Builder
	for _, blk := range blocks {
		line, err := json.Marshal(blk)
		if err != nil {
			t.Fatal(err)
		}
		b.Write(line)
		b.WriteString("\n")
	}
	p := filepath.Join(w.tmp, sid+".jsonl")
	if err := os.WriteFile(p, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	w.led.TouchFull("cc", sid, p, w.t0, 10, "C:/proj", "", 150000, 0)

	cfg := config.Default()
	cfg.Thresholds = config.ThresholdCfg{SummarizeS: 10, BlockS: 30,
		MinCtxTokens: 100, CacheWarnS: 720}
	// accounts 接线（harvest 随之建立——NewWatcher 同条件）
	wtr := NewWatcher(cfg, w.led, nil, func(*ledger.SessionState) bool { return true },
		0, w.acc, w.d, nil, nil)
	if wtr.harvest == nil {
		t.Fatal("前提：harvest 应随 accounts+HarvestUsage 建立")
	}
	t2 := float64(time.Date(2026, 9, 18, 12, 0, 2, 0, time.UTC).Unix())
	stop := t2 - AckGraceS - 0.5
	w.d.windows[[2]string{"cc", sid}] = &waitWindow{OpenedTS: stop - 100,
		StopTS: &stop, SawAsync: true}

	wtr.harvestUsage(p, info.Size(), w.led.Get("cc", sid))

	rows := w.acc.Read(accounts.ReadOpts{Kind: "usage", Session: sid})
	if len(rows) != 2 {
		t.Fatalf("usage 行数 = %d, want 2", len(rows))
	}
	windowRows := w.windowRows(sid)
	if len(windowRows) != 1 || windowRows[0]["close_reason"] != "main_resumed" {
		// 喂的是最大 ts（第二行），非首行——首行不越 ack 线，窗不会闭
		t.Fatalf("window 行 = %v, want 1 条 main_resumed（喂入=最大 ts）", windowRows)
	}
}

// ---- 骑手（票13 评审 Minor C）：QWatchStop 写 mode × Health/读侧并发护栏 ----

func TestQWatchStopHealthConcurrentModeAccess(t *testing.T) {
	// 一键停写 mode × /stats 读 mode（含 GetQWatchMode 直读）交错：
	// -race 下无数据竞争；无 -race 以逻辑断言为主——全程无 panic、终态 mode=off、
	// 返回口径一致。读写统一走 Daemon.GetQWatchMode/SetQWatchModeOff（cfgMu）。
	e := newGateEnv(t)
	d := e.d
	var wg sync.WaitGroup
	stop := make(chan struct{})
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					_ = d.GetQWatchMode()
				}
			}
		}()
	}
	for g := 0; g < 2; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					_ = d.Health()
				}
			}
		}()
	}
	for i := 0; i < 50; i++ {
		r := d.QWatchStop()
		if r["mode"] != "off" || r["ok"] != true {
			t.Fatalf("一键停返回 = %v", r)
		}
	}
	close(stop)
	wg.Wait()
	if d.GetQWatchMode() != "off" {
		t.Fatalf("一键停后 mode = %s, want off", d.GetQWatchMode())
	}
	if h := d.Health(); h["qwatch"].(map[string]any)["mode"] != "off" {
		t.Fatalf("health qwatch mode 应读活值: %v", h["qwatch"])
	}
}

// 2026-09-30 603fef0f 案回归：真闲置 78 分钟被无时间戳状态块（mode/snapshot/
// lastPrompt 幻影写）把文件时钟顶新成 17 分钟——闸门只提醒不拦。闲置锚点改
// 内容时钟后：mtime 再新，内容时钟停在最后带时间戳记录 → 照进拦窗；真实新
// 内容落地 → 内容时钟前进 → 放行。文件取不到时间戳（本套件其余用例的假路径
// 形态）回落文件时钟＝旧行为，天然回归。
func TestGateIdleAnchoredToContentClock(t *testing.T) {
	e := newGateEnv(t)
	proj := filepath.Join(e.tmp, "proj")
	path := filepath.Join(e.tmp, "cc.jsonl")
	tsFmt := "2006-01-02T15:04:05.000Z"
	old := time.Unix(int64(e.t0-(testBlockS+600)), 0).UTC().Format(tsFmt)
	// 内容：最后带时间戳记录停在 t0-41min；尾部混一条幻影写（无 timestamp 字段）
	os.WriteFile(path, []byte(fmt.Sprintf(
		`{"type":"user","timestamp":%q,"message":{"content":"早"}}`+"\n"+
			`{"type":"mode","mode":"default"}`+"\n", old)), 0o644)
	// 文件时钟被幻影写顶新到现在（mtime=t0）
	e.led.TouchFull("cc", "pc1", path, e.t0, 10, proj, "", 99999, 0)

	r1 := e.d.Gate(gateBody4("pc1", path, proj, "继续")) // 内容闲置 41min → 分支7 警告
	if r1["decision"] != "allow" {
		t.Fatalf("分支7 decision = %v", r1["decision"])
	}
	if ctx, _ := r1["additional_context"].(string); ctx == "" {
		t.Fatal("分支7 应带警告")
	}
	r2 := e.d.Gate(gateBody4("pc1", path, proj, "继续")) // 分支6 拦（pending 置位后）
	if r2["decision"] != "block" {
		t.Fatalf("幻影写不得稀释拦窗, decision = %v", r2["decision"])
	}

	// 真实新内容（带时间戳）落地 + mtime 前进 → 内容时钟前进 → 强续清 pending 后正常放行
	e.advance(200)
	fresh := time.Unix(int64(e.t0+190), 0).UTC().Format(tsFmt)
	os.WriteFile(path, []byte(fmt.Sprintf(
		`{"type":"user","timestamp":%q,"message":{"content":"回来了"}}`+"\n", fresh)), 0o644)
	e.led.TouchFull("cc", "pc1", path, e.t0+190, 20, proj, "", 99999, 0)
	if rb := e.d.Gate(gateBody4("pc1", path, proj, "强续清场")); rb["reason"] != "bypass" {
		t.Fatalf("强续 = %v", rb)
	}
	r4 := e.d.Gate(gateBody4("pc1", path, proj, "继续"))
	if r4["decision"] != "allow" {
		t.Fatalf("内容时钟前进后应放行, decision = %v", r4["decision"])
	}
	if ctx, has := r4["additional_context"]; has && ctx != "" {
		t.Fatalf("新内容后不应再警告: %v", ctx)
	}
}

// ---- 票01（D2/D3）：文案按 agent 分支——dsh 不再教 /clear ----
//
// dsh（DeepSeek Harness 桌面端）没有 /clear 命令：四处用户可见文案
// （分支5 block、分支6 block、warnCtx、restore 锚定归还·无交接）对 dsh
// 换「新建会话」引导（restore 为中性表述）；cc 逐字零变化——cc 侧四处
// 输出整串钉死，动一个字即红；强续段落两 agent 同文，均在整串断言内。

// TestGateDshCopyBranchesByAgent 两 agent × 四位点表驱动。
func TestGateDshCopyBranchesByAgent(t *testing.T) {
	type copyOut struct {
		b5, b6, warn, restore, b5Path string
	}
	// drive 同一 env 内驱动四个位点（闲置统一 3600s=60min，整分钟无舍入歧义）：
	//   b5 = 分支5 block reason（有效交接在库）
	//   warn = 分支7 的 additional_context（warnCtx，nil 交接）
	//   b6  = 随后第一次分支6 block reason
	//   restore = 锚定归还·无交接（锚无候选）context 首段
	drive := func(t *testing.T, agent string) copyOut {
		t.Helper()
		e := newGateEnv(t)
		if agent != "cc" {
			e.d.Cfg.GateCodex = "enforce" // 非 cc 走 codex 道，dsh 需显式开
		}
		out := copyOut{}
		body := func(cwd, sid, prompt string) map[string]any {
			return map[string]any{"agent": agent, "session_id": sid,
				"transcript_path": filepath.Join(e.tmp, sid+".jsonl"),
				"cwd":             cwd, "prompt": prompt}
		}
		touch := func(cwd, sid string) {
			e.led.TouchFull(agent, sid, filepath.Join(e.tmp, sid+".jsonl"),
				e.t0-3600, 10, cwd, "", 99999, 0)
		}
		// 位点1 分支5：有效交接 → block
		proj := filepath.Join(e.tmp, "proj")
		touch(proj, "b5")
		en := e.store.SaveHandoff("b5", agent, proj, "t", isoUTC(e.t0), "fresh", "md")
		out.b5Path = en.Path
		r5 := e.d.Gate(body(proj, "b5", "被拦原话B5"))
		if r5["decision"] != "block" {
			t.Fatalf("%s 分支5 decision = %v", agent, r5["decision"])
		}
		out.b5 = r5["reason"].(string)
		// 位点3+2：分支7 警告（warnCtx）→ 分支6 首拦；无交接项目
		proj2 := filepath.Join(e.tmp, "proj2")
		touch(proj2, "b67")
		r7 := e.d.Gate(body(proj2, "b67", "继续"))
		if r7["decision"] != "allow" {
			t.Fatalf("%s 分支7 decision = %v", agent, r7["decision"])
		}
		out.warn = r7["additional_context"].(string)
		r6 := e.d.Gate(body(proj2, "b67", "继续"))
		if r6["decision"] != "block" {
			t.Fatalf("%s 分支6 decision = %v", agent, r6["decision"])
		}
		out.b6 = r6["reason"].(string)
		// 位点4 锚定归还·无交接：锚在、候选无 → 只带原话
		e.store.SavePendingPromptFor(agent, proj2, "nohandoff-src", "原话R")
		rr := e.d.Restore(agent, proj2, "fresh")
		out.restore, _ = rr["context"].(string)
		return out
	}

	for _, agent := range []string{"cc", "dsh"} {
		out := drive(t, agent)
		switch agent {
		case "cc": // 逐字钉死（改动前口径；动一字即红）
			wantB5 := "此会话已闲置 60 分钟（缓存已失效）。" +
				"你刚输入的内容没有发出去，原话已保存：被拦原话B5\n" +
				"\n【推荐】/clear 换新会话（约 10 秒，进度和原话自动带过去）：\n" +
				"  1. 输入 /clear\n" +
				"  2. 随便发一个字（如「继续」）\n" +
				"  新会话开场自动收到：本会话的进度交接 + 你这条原话，接着原话继续干。\n" +
				"\n【不想换会话】以「强续」开头重发你的内容（例：「强续 被拦原话B5」），" +
				"解除本轮拦截、留在本会话继续。\n" +
				"交接文档: " + out.b5Path
			if out.b5 != wantB5 {
				t.Fatalf("cc 分支5 逐字不符:\n got %q\nwant %q", out.b5, wantB5)
			}
			wantB6 := "此会话闲置超时被拦（第 1 次）。" +
				"你刚输入的内容没有发出去，原话已保存：继续\n" +
				"\n【现在就能继续】以「强续」开头重发你的内容（例：「强续 继续」），" +
				"解除本轮拦截、留在本会话。\n" +
				"\n【或 /clear 换新会话】开场发一个字即可；本会话的交接若已生成会" +
				"一并带给新会话，此刻还没好则新会话只会带回你这条原话（之前的进度" +
				"需要自己简述两句）。"
			if out.b6 != wantB6 {
				t.Fatalf("cc 分支6 逐字不符:\n got %q\nwant %q", out.b6, wantB6)
			}
			wantWarn := "[Ferryman] 本会话已闲置 60 分钟，缓存大概率已失效，" +
				"继续使用将全量重付 input。交接生成中，下次提交将被拦。" +
				"建议 /clear 后开新会话（自动注入交接）。"
			if out.warn != wantWarn {
				t.Fatalf("cc warnCtx 逐字不符:\n got %q\nwant %q", out.warn, wantWarn)
			}
			wantRestore := "[Ferryman] 你 /clear 前被拦的那条消息没有丢，" +
				"原话如下，接着它继续即可：\n「原话R」\n"
			if !strings.HasPrefix(out.restore, wantRestore) {
				t.Fatalf("cc restore 首段逐字不符:\n got %q\nwant %q", out.restore, wantRestore)
			}
		case "dsh": // 评审钉死口径：无 /clear + 新建会话引导（restore 中性）
			wantB5 := "此会话已闲置 60 分钟（缓存已失效）。" +
				"你刚输入的内容没有发出去，原话已保存：被拦原话B5\n" +
				"\n【推荐】新建会话（桌面端点新建 / web 端 new session），" +
				"开场自动收到：本会话的进度交接+你这条原话。\n" +
				"\n【不想换会话】以「强续」开头重发你的内容（例：「强续 被拦原话B5」），" +
				"解除本轮拦截、留在本会话继续。\n" +
				"交接文档: " + out.b5Path
			if out.b5 != wantB5 {
				t.Fatalf("dsh 分支5 不符:\n got %q\nwant %q", out.b5, wantB5)
			}
			wantB6 := "此会话闲置超时被拦（第 1 次）。" +
				"你刚输入的内容没有发出去，原话已保存：继续\n" +
				"\n【现在就能继续】以「强续」开头重发你的内容（例：「强续 继续」），" +
				"解除本轮拦截、留在本会话。\n" +
				"\n【或新建会话】（桌面端点新建 / web 端 new session）开场发一个字即可；" +
				"本会话的交接若已生成会一并带给新会话，此刻还没好则新会话只会带回" +
				"你这条原话（之前的进度需要自己简述两句）。"
			if out.b6 != wantB6 {
				t.Fatalf("dsh 分支6 不符:\n got %q\nwant %q", out.b6, wantB6)
			}
			wantWarn := "[Ferryman] 本会话已闲置 60 分钟，缓存大概率已失效，" +
				"继续使用将全量重付 input。交接生成中，下次提交将被拦。" +
				"建议新建会话（桌面端点新建 / web 端 new session），开场自动注入交接。"
			if out.warn != wantWarn {
				t.Fatalf("dsh warnCtx 不符:\n got %q\nwant %q", out.warn, wantWarn)
			}
			wantRestore := "[Ferryman] 你被拦时输入的那条消息没有丢，" +
				"原话如下，接着它继续即可：\n「原话R」\n"
			if !strings.HasPrefix(out.restore, wantRestore) {
				t.Fatalf("dsh restore 首段不符:\n got %q\nwant %q", out.restore, wantRestore)
			}
			// 四位点整体无 /clear（含 restore 尾段等未逐字钉死的部分）
			for name, s := range map[string]string{
				"分支5": out.b5, "分支6": out.b6, "warnCtx": out.warn, "restore": out.restore,
			} {
				if strings.Contains(s, "/clear") {
					t.Fatalf("dsh %s 不得出现 /clear: %s", name, s)
				}
			}
		}
	}
}

// TestGateDshModeIndependentKey gate.dsh_mode 独立档（dsh-gate-ux P3 前置）：
// dsh=enforce 而 codex 仍 observe——同条件下 dsh 真拦（分支5）、codex 只警告
// 不拦；dsh 升档不连坐 codex。
func TestGateDshModeIndependentKey(t *testing.T) {
	e := newGateEnv(t)
	e.d.Cfg.GateCodex = "observe"
	e.d.Cfg.GateDsh = "enforce"

	body := func(agent, cwd, sid string) map[string]any {
		return map[string]any{"agent": agent, "session_id": sid,
			"transcript_path": filepath.Join(e.tmp, sid+".jsonl"),
			"cwd":             cwd, "prompt": "继续"}
	}
	touch := func(agent, cwd, sid string) {
		e.led.TouchFull(agent, sid, filepath.Join(e.tmp, sid+".jsonl"),
			e.t0-3600, 10, cwd, "", 99999, 0)
	}

	// dsh：分支5 条件（闲置 60min+交接在库+峰值过门）→ 真拦
	projD := filepath.Join(e.tmp, "projD")
	touch("dsh", projD, "sd1")
	e.store.SaveHandoff("sd1", "dsh", projD, "t", isoUTC(e.t0), "fresh", "md")
	rd := e.d.Gate(body("dsh", projD, "sd1"))
	if rd["decision"] != "block" {
		t.Fatalf("dsh enforce decision = %v, want block（独立档生效）", rd["decision"])
	}
	if !strings.Contains(rd["reason"].(string), "新建会话") {
		t.Fatalf("dsh block 文案应含新建会话引导: %v", rd["reason"])
	}

	// codex 同条件：observe → 只警告不拦（不连坐）
	projC := filepath.Join(e.tmp, "projC")
	touch("codex", projC, "sc1")
	e.store.SaveHandoff("sc1", "codex", projC, "t", isoUTC(e.t0), "fresh", "md")
	rc := e.d.Gate(body("codex", projC, "sc1"))
	if rc["decision"] != "allow" {
		t.Fatalf("codex observe decision = %v, want allow（不连坐）", rc["decision"])
	}
	// 交接在库时 observe 警告带"交接文档:"（willBlock 尾句被 doc 替换），断言警告在场即可。
	if ctx, _ := rc["additional_context"].(string); !strings.Contains(ctx, "闲置") || !strings.Contains(ctx, "交接文档") {
		t.Fatalf("codex 应走 observe 警告（闲置+交接文档）: %v", rc["additional_context"])
	}
}

// ---- 票02（D2 消耗语义/D4 保守面/A5① idle 口径） ----
//
// dsh 强续（bypass）即消耗该会话现行交接——之后新会话要么拿到含最新进展的
// 新交接、要么明说没有，绝不端旧快照（用户原话案：强续后新会话收到 11 小时
// 前旧快照）。cc/codex 永不消耗，行为逐字零变化（本节负例 + 既有断言双护栏）。

// gateBodyAgent 自由 agent 的 gate body（gateBody4 钉死 cc，dsh/codex 用例用此）。
func gateBodyAgent(agent, sid, path, cwd, prompt string) map[string]any {
	return map[string]any{"agent": agent, "session_id": sid, "transcript_path": path,
		"cwd": cwd, "prompt": prompt}
}

// findHandoff 从 index.json 取 (agent,sid) 的交接行（消耗断言读持久化面）。
func findHandoff(t *testing.T, dataDir, agent, sid string) map[string]any {
	t.Helper()
	for _, h := range indexHandoffs(t, dataDir) {
		if h["agent"] == agent && h["session_id"] == sid {
			return h
		}
	}
	t.Fatalf("index 缺 (%s,%s) 交接", agent, sid)
	return nil
}

// TestGateDshBypassConsumesHandoff dsh 强续放行的同一刻，其现行交接被标记
// 已消耗（持久化面 consumed_at 落库 + 供给面 ValidHandoff 随即不认）。
func TestGateDshBypassConsumesHandoff(t *testing.T) {
	e := newGateEnv(t)
	proj := filepath.Join(e.tmp, "proj")
	path := filepath.Join(e.tmp, "sd1.jsonl")
	e.led.TouchFull("dsh", "sd1", path, e.t0-3600, 10, proj, "", 99999, 0)
	e.store.SaveHandoff("sd1", "dsh", proj, "t", isoUTC(e.t0), "fresh", "md")

	rb := e.d.Gate(gateBodyAgent("dsh", "sd1", path, proj, "强续 我就要留在这"))
	if rb["decision"] != "allow" || rb["reason"] != "bypass" {
		t.Fatalf("bypass = %v", rb)
	}
	if got := findHandoff(t, e.tmp, "dsh", "sd1")["consumed_at"]; got == nil {
		t.Fatalf("dsh 强续后交接应已消耗（consumed_at 落库）: %v", got)
	}
	if h := e.store.ValidHandoff("dsh", proj, e.t0); h != nil {
		t.Fatalf("消耗后 ValidHandoff 应不供给: %+v", h)
	}
}

// TestGateCCCodexBypassKeepsHandoff cc/codex 强续不消耗交接（D4 保守面：
// 消耗调用只挂 dsh 道；供给面原样＝行为零变化的可观测锚）。
func TestGateCCCodexBypassKeepsHandoff(t *testing.T) {
	for _, agent := range []string{"cc", "codex"} {
		t.Run(agent, func(t *testing.T) {
			e := newGateEnv(t)
			proj := filepath.Join(e.tmp, "proj")
			path := filepath.Join(e.tmp, "sx.jsonl")
			e.led.TouchFull(agent, "sx", path, e.t0-3600, 10, proj, "", 99999, 0)
			e.store.SaveHandoff("sx", agent, proj, "t", isoUTC(e.t0), "fresh", "md")
			rb := e.d.Gate(gateBodyAgent(agent, "sx", path, proj, "强续 留下"))
			if rb["decision"] != "allow" || rb["reason"] != "bypass" {
				t.Fatalf("%s bypass = %v", agent, rb)
			}
			if got := findHandoff(t, e.tmp, agent, "sx")["consumed_at"]; got != nil {
				t.Fatalf("%s 强续不得消耗交接: %v", agent, got)
			}
			if h := e.store.ValidHandoff(agent, proj, e.t0); h == nil {
				t.Fatalf("%s 交接供给不得受 bypass 影响", agent)
			}
		})
	}
}

// TestGateBlockAcctIdleAnchoredToDecisionAnchor 票02/A5① 回归钉：block 记账
// 的 idle_s 与闸门判定锚同源（内容时钟），不被顶新的台账 last_write 稀释。
// 场景＝ADR-0013 幻影写形态（2026-10-06 08:12:51 账本 16.5s vs 判定 8.8h 的
// 构造版）：last_write 被顶到当下、内容时钟停在 630s 前——记账若取台账
// last_write 口径会记 ≈0，取判定锚口径才记 ≈630。
func TestGateBlockAcctIdleAnchoredToDecisionAnchor(t *testing.T) {
	w := newWenv(t)
	proj := filepath.Join(w.tmp, "proj")
	path := filepath.Join(w.tmp, "ac1.jsonl")
	tsFmt := "2006-01-02T15:04:05.000Z"
	old := time.Unix(int64(w.t0-(testBlockS+600)), 0).UTC().Format(tsFmt)
	// 内容：最后带时间戳记录停在 t0-630；尾部混一条幻影写（无 timestamp 字段）
	if err := os.WriteFile(path, []byte(fmt.Sprintf(
		`{"type":"user","timestamp":%q,"message":{"content":"早"}}`+"\n"+
			`{"type":"mode","mode":"default"}`+"\n", old)), 0o644); err != nil {
		t.Fatal(err)
	}
	// 文件时钟被幻影写顶新到现在（last_write=t0 → 台账口径 idle≈0）
	w.led.TouchFull("cc", "ac1", path, w.t0, 10, proj, "", 99999, 0)
	w.store.SaveHandoff("ac1", "cc", proj, "t", isoUTC(w.t0), "fresh", "md")

	if r := w.d.Gate(gateBody4("ac1", path, proj, "原话")); r["decision"] != "block" {
		t.Fatalf("decision = %v, want block（判定走内容时钟）", r["decision"])
	}
	rows := w.acc.Read(accounts.ReadOpts{Kind: "block", Session: "ac1"})
	if len(rows) != 1 {
		t.Fatalf("block 行数 = %d, want 1", len(rows))
	}
	idle, _ := rows[0]["idle_s"].(float64)
	if math.Abs(idle-(testBlockS+600)) >= 1 {
		t.Fatalf("idle_s = %v, want ≈ %v（判定锚口径，非被顶新的台账 last_write≈0）",
			idle, testBlockS+600)
	}
}

// ---- 票04（dsh-hot-compaction）gate 联动：已压缩短前缀不拦 ----
//
// 命中拦截条件（拦窗内）先查 compressed 标记（判定单源票02 DshCompressedActive，
// gate 只消费不重复实现）：标记有效 ∧ 当前前缀 < dshCompactPassLine（v0.9.3 票1
// 解耦：max(pass_floor_tokens=12000, pass_ratio=0.5×压前峰值)，不再与
// min_peak_tokens 共用——2026-10-07 首单事故实锚：60562 压到 20562 撞 20000
// 线照样拦）→ 放行（reason="compacted-short-prefix"，gate.log 落 mode=
// compacted-short-prefix 可 grep 行，pending 同 hot-allow 款清掉）；标记过期 /
// 被流量作废 / 无标记 / 前缀不短 → 照旧拦（分支5，不放宽任何其他判定）。
// 夹具复用票02 compactEnv（真 Accounts＋TTL 可配），gate 档显式钉 enforce
//（compactEnv 沿 Default：dsh 空→回落 GateCodex=off，非本票面）。

// TestGateDshAutoContinue v0.9.3 票2：dsh 拦截改自动强续无卡直续（[gate].
// dsh_auto_continue，缺省关）。开=allow reason=auto-strong-continue＋横幅报
// 冷重付价＋pending 清＋交接消耗（D2）＋bypass 记账（reason 可区分手动）＋
// gate.log auto-continue 痕；关=照旧拦（缺省行为零变化）；cc 永不受影响。
func TestGateDshAutoContinue(t *testing.T) {
	newEnv := func(t *testing.T) *compactEnv {
		e := newCompactEnv(t, 100)
		e.d.Cfg.GateDsh = "enforce"
		e.d.Cfg.Thresholds = config.ThresholdCfg{
			SummarizeS: testSummarizeS, BlockS: testBlockS, MinCtxTokens: testMinCtx,
			CacheWarnS: 720,
		}
		return e
	}
	proj := "C:/proj"

	t.Run("开=无卡直续", func(t *testing.T) {
		e := newEnv(t)
		path := filepath.Join(e.tmp, compactSID+".jsonl.zstd")
		e.d.Cfg.GateDshAutoContinue = true
		e.regCompact(compactSID, testBlockS+3600, 50000)
		e.store.SaveHandoff(compactSID, "dsh", proj, "t", isoUTC(e.t0+30), "fresh", "md")
		if h := e.d.Store.ValidHandoff("dsh", proj, e.t0+40); h == nil {
			t.Fatal("前置：交接应在库")
		}
		r := e.d.Gate(gateBodyAgent("dsh", compactSID, path, proj, "继续干活"))
		if r["decision"] != "allow" || r["reason"] != "auto-strong-continue" {
			t.Fatalf("decision/reason = %v/%v, want allow/auto-strong-continue",
				r["decision"], r["reason"])
		}
		if ctx, _ := r["additional_context"].(string); ctx == "" ||
			!strings.Contains(ctx, "冷重付") {
			t.Fatalf("横幅应报冷重付价: %q", ctx)
		}
		if _, pok := e.d.Pending.Get([2]string{"dsh", compactSID}); pok {
			t.Fatal("自动强续不得留 pending（06703fbd 跑步机同款）")
		}
		if got := e.store.PopPendingPrompt(compactSID, ""); got != "" {
			t.Fatalf("不得存被拦原话（本道无被拦）: %q", got)
		}
		if h := e.d.Store.ValidHandoff("dsh", proj, e.t0+40); h != nil {
			t.Fatal("现行交接应被消耗（D2：绝不端旧快照）")
		}
		var row map[string]any
		for _, r := range e.acc.Read(accounts.ReadOpts{Kind: "bypass"}) {
			if r["session_id"] == compactSID {
				row = r
			}
		}
		if row == nil || row["prefix_tokens"] != 50000.0 {
			t.Fatalf("bypass 行 = %v（want peak=50000；reason/idle_s 不落账——隐私不变量，自动/手动区分在 gate.log）", row)
		}
		data, err := os.ReadFile(filepath.Join(e.tmp, "data", "gate.log"))
		if err != nil || !strings.Contains(string(data), "mode=auto-continue") {
			t.Fatalf("gate.log 缺 auto-continue 痕: %q err=%v", string(data), err)
		}
	})
	t.Run("关=照旧拦（缺省）", func(t *testing.T) {
		e := newEnv(t) // 缺省 false
		path := filepath.Join(e.tmp, compactSID+".jsonl.zstd")
		e.regCompact(compactSID, testBlockS+3600, 50000)
		e.store.SaveHandoff(compactSID, "dsh", proj, "t", isoUTC(e.t0+30), "fresh", "md")
		r := e.d.Gate(gateBodyAgent("dsh", compactSID, path, proj, "继续干活"))
		if r["decision"] != "block" {
			t.Fatalf("缺省关应照旧拦: %v", r["decision"])
		}
	})
	t.Run("cc 零变化（开着也拦）", func(t *testing.T) {
		e := newEnv(t)
		e.d.Cfg.GateCC = "enforce"
		e.d.Cfg.GateDshAutoContinue = true
		cc := "cc-sid-autocontinue"
		e.reg(cc, filepath.Join(e.tmp, "cc.jsonl"), proj, testBlockS+3600, 50000)
		e.store.SaveHandoff(cc, "cc", proj, "t", isoUTC(e.t0+30), "fresh", "md")
		r := e.d.Gate(gateBodyAgent("cc", cc, filepath.Join(e.tmp, "cc.jsonl"), proj, "继续"))
		if r["decision"] != "block" {
			t.Fatalf("cc 永不走自动强续: %v", r["decision"])
		}
	})
}

// e0Tmp 已并入各子测试内取 e.tmp。

func TestGateCompactedShortPrefix(t *testing.T) {
	cases := []struct {
		name         string
		compact      bool    // 经 /dsh/compacted 置标记（ok=true＋prefix 覆盖）
		prefix       int     // 上报 prefix_tokens（=标记后 PeakCtx 现值）
		minPeak      int     // >0 时改配 min_peak_tokens（已解耦：不再影响放行）
		markPrePeak  *int    // 非 nil 时改写标记 PrePeak（模拟压前峰值不可得）
		advanceS     float64 // 置标记后推进秒数（标记过期态）
		voidByWriter bool    // 置标记后 Touch 推进 LastWrite（流量作废态）
		wantDecision string
		wantReason   string
	}{
		{"有效标记+短前缀→放行", true, 1234, 0, nil, 0, false, "allow", "compacted-short-prefix"},
		{"标记过期→照拦", true, 1234, 0, nil, 201, false, "block", ""},
		{"标记被流量作废→照拦", true, 1234, 0, nil, testBlockS + 600, true, "block", ""},
		{"无标记→照拦", false, 0, 0, nil, 0, false, "block", ""},
		{"标记有效但前缀压线(=0.5×PrePeak)→照拦", true, 25000, 0, nil, 0, false, "block", ""},
		{"首单事故实锚:60562压到20562→放行", true, 20562, 0, nil, 0, false, "allow", "compacted-short-prefix"},
		{"min_peak已解耦:同配1000→照放", true, 1234, 1000, nil, 0, false, "allow", "compacted-short-prefix"},
		{"PrePeak不可得:过地板→照拦", true, 30000, 0, &[]int{0}[0], 0, false, "block", ""},
		{"PrePeak不可得:地板内→放行", true, 11999, 0, &[]int{0}[0], 0, false, "allow", "compacted-short-prefix"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newCompactEnv(t, 100) // 标记有效期 2.0×100=200s
			e.d.Cfg.GateDsh = "enforce"
			// 阈值钉 gate 套件常量（compactEnv 沿 Default：block=2100s，会淹掉
			// 流量作废态的 620s 闲置——不进拦窗就走不到分支5）。
			e.d.Cfg.Thresholds = config.ThresholdCfg{
				SummarizeS: testSummarizeS, BlockS: testBlockS, MinCtxTokens: testMinCtx,
				CacheWarnS: 720,
			}
			if tc.minPeak > 0 {
				e.d.Cfg.DshCompact.MinPeakTokens = tc.minPeak
			}
			proj := filepath.Join(e.tmp, "proj")
			path := filepath.Join(e.tmp, compactSID+".jsonl.zstd")
			e.regCompact(compactSID, testBlockS+3600, 50000) // 闲置 61min 拦窗内
			if tc.compact {
				e.d.DshCompacted(map[string]any{"session_id": compactSID,
					"ok": true, "prefix_tokens": tc.prefix})
			}
			if tc.markPrePeak != nil { // 压前峰值不可得态（历史标记/未 harvest）
				e.d.Ledger.Mu().Lock()
				if st := e.d.Ledger.GetLocked("dsh", compactSID); st != nil && st.DshCompressed != nil {
					st.DshCompressed.PrePeak = *tc.markPrePeak
				}
				e.d.Ledger.Mu().Unlock()
			}
			if tc.voidByWriter { // 标记后新流量：LastWrite 越过标记时刻即作废
				e.led.TouchFull("dsh", compactSID, path, e.t0+10, 10, proj, "", 50001, 0)
			}
			if tc.advanceS > 0 {
				e.advance(tc.advanceS)
			}
			// 交接在库（"照拦"的分支5 基座）；covers 统一取 t0+30——各态
			// coversBar（t0-3630，流量作废态 t0+10）都落在容差内，分支5 恒可达。
			e.store.SaveHandoff(compactSID, "dsh", proj, "t", isoUTC(e.t0+30), "fresh", "md")

			r := e.d.Gate(gateBodyAgent("dsh", compactSID, path, proj, "继续"))
			if r["decision"] != tc.wantDecision {
				t.Fatalf("decision = %v, want %v", r["decision"], tc.wantDecision)
			}
			if tc.wantReason == "" { // 照拦四态：decision 即全部断言面
				return
			}
			if r["reason"] != tc.wantReason {
				t.Fatalf("reason = %v, want %q", r["reason"], tc.wantReason)
			}
			if ctx, _ := r["additional_context"].(string); ctx == "" {
				t.Fatal("放行应带 additional_context 说明（为何不拦＋红利边界）")
			}
			if _, pok := e.d.Pending.Get([2]string{"dsh", compactSID}); pok {
				t.Fatal("压缩放行不得置 pending（hot-allow 同款清除）")
			}
			if got := e.store.PopPendingPrompt(compactSID, ""); got != "" {
				t.Fatalf("放行不得存待续原话: %q", got)
			}
			// 红利期内连发不再被拦（无分支6跑步机）
			if r2 := e.d.Gate(gateBodyAgent("dsh", compactSID, path, proj, "再发一条")); r2["decision"] != "allow" {
				t.Fatalf("红利期内连发应放行: %v", r2)
			}
			// gate.log 落 mode=compacted-short-prefix 行（allow 痕 reason 可 grep）
			data, err := os.ReadFile(filepath.Join(e.tmp, "data", "gate.log"))
			if err != nil || !strings.Contains(string(data), "mode=compacted-short-prefix") {
				t.Fatalf("gate.log 缺 compacted-short-prefix 痕: %q err=%v", string(data), err)
			}
		})
	}
}
