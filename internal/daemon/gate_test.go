package daemon

// 规格：tests/test_gate.py 全部 43 例 1:1（T07 闸门状态机全分支 + T44b/T46/
// 缺口A/T48 票02；T48 票03 的 2 例守望用例归票16 占位——子代理测试文件同惯例）。
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
		"output_tokens": 10, "offset": 0,
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
	// A5：分支6 新文案——确定性出路（每次强续都放行）；第4次降级不变。
	// （A3 口径：分支5/7 既有断言不动；分支6 文案断言随本条更新。）
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
		if !strings.Contains(reason, "每次以「强续」开头") { // 确定性出路
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

// ---- T48 票03：守望接线（2 例归票16 占位，子代理测试文件同惯例） ----

func TestParkedWindowDefersFerryAndResumesAfterClose(t *testing.T) {
	t.Skip("e2e→票16：Watcher._maybe_enqueue 停车窗推迟道（20260918 误摆渡案回归锁，守望装配后转绿）；Python: test_gate.py::test_parked_window_defers_ferry_and_resumes_after_close")
}

func TestHarvestUsageFeedsNoteUsageMaxTS(t *testing.T) {
	t.Skip("e2e→票16：_harvest_usage 落账后把新行最大 ts 喂 note_usage（守望装配后转绿）；Python: test_gate.py::test_harvest_usage_feeds_note_usage_max_ts")
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
