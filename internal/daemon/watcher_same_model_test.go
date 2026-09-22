package daemon

// watcher_same_model_test.go — 票02:同模型触发点守望集成钉子(ADR-0015 决定一/
// 决定二;review_blocks F1 冷分支静默交还、F3 判热时钟口径、F6 跳过原因编码)。
// 票03 追加:执行档钉子(追加重放派发/失败链/零重试/lane 记账;决定一/三/六)。
//
// 验收对照:
//   - 判冷/白名单未中/未启用三路径:零模型调用、静默交还(25 分钟档原样),
//     跳过事件各记一次且原因独立编码,同写入版本去重;
//   - 判热时钟数据源钉死:主转录 usage 行时间戳(真实上游流量)+ 真发成功的
//     心跳重放(问询/等待两泳道)都计入;子代理 usage、ERROR、observe 不计;
//   - same_model off(默认)零行为差异;台账闲置(闸门语义)不动;
//   - 票03:判热通过派发追加重放(单发、计划形状=指令模板+max_tokens 封顶),
//     成功落 fresh 产物(叙事+骨架合成)并记 lane=same_model 行;失败
//     (tool_use/格式不符)记 failed 行、清章交还既有 25 分钟档、零重试,
//     第三方再败落骨架(既有 worker 链);无发送器=降级静默交还。

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
	"ferryman/internal/ferry"
	"ferryman/internal/ledger"
	"ferryman/internal/store"
)

// smBaseT 冻结时钟基点(任意 epoch 秒;闲置判定全程确定)。
const smBaseT = 1_800_000_000.0

// smGateCfg 判热门用配置:白名单含 zhipu(条目键=[dock.upstreams] 键)、
// 阈值 0.1min=6s(低于 mergeCfg 的 SummarizeS=10s——跳过带内不触碰总结档)、
// 实测 TTL 30min(τ=24min)。
func smGateCfg() *config.Config {
	cfg := mergeCfg()
	cfg.Dock = &config.DockCfg{Listen: "127.0.0.1:15722", Active: "zhipu",
		Upstreams: map[string]config.DockUpstream{
			"zhipu": {BaseURL: "https://open.bigmodel.cn"},
		}}
	cfg.SameModel = config.SameModelCfg{Enabled: true, Upstreams: []string{"zhipu"},
		ThresholdMin: 0.1, CeilingMin: map[string]float64{}}
	cfg.Heartbeat.TTLS = 1800
	// 终局修复后生效值拒算回落冷启动种子(不再 CeilingFor),本文件门序测试
	// 需要亚分钟触发带 → 改走 manual 档(配置值即生效值,0.1min=6s 照读);
	// 生效值来源本身由 watcher_same_model_effective_test.go 分列钉死。
	cfg.Tuning.Mode = "manual"
	return cfg
}

// skipSink 跳过遥测缝的捕获替身:sid → 原因序列与字段。
type skipSink struct {
	mu     sync.Mutex
	reason map[string][]string
	fields map[string][]accounts.Fields
}

func newSkipSink() *skipSink {
	return &skipSink{reason: map[string][]string{}, fields: map[string][]accounts.Fields{}}
}

func (s *skipSink) record(st *ledger.SessionState, reason string, ff accounts.Fields) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reason[st.SessionID] = append(s.reason[st.SessionID], reason)
	s.fields[st.SessionID] = append(s.fields[st.SessionID], ff)
}

func (s *skipSink) count(sid string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.reason[sid])
}

func (s *skipSink) last(sid string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.reason[sid]
	if len(r) == 0 {
		return ""
	}
	return r[len(r)-1]
}

// ---- 三条跳过路径(F6 独立编码;F1 静默交还) ----

func TestSameModelSkipWhitelistMissPath(t *testing.T) {
	now := freezeClock(t, smBaseT)
	led := ledger.New()
	tmp := t.TempDir()
	cfg := smGateCfg()
	cfg.Dock.Active = "other" // 活动上游不在白名单(白名单只有 zhipu)
	cfg.Dock.Upstreams["other"] = config.DockUpstream{BaseURL: "https://example.invalid"}
	sink := newSkipSink()
	w := newTestWatcherW(cfg, led, nil, nil, nil, nil)
	w.bookSameModelSkipFn = sink.record
	st, _ := bareSession(t, led, tmp, "sm-wm1")
	setLastWrite(led, st, *now-7) // 闲置 7s ≥ 阈值 6s,< SummarizeS 10s
	w.maybeSameModel(st)
	if got := sink.count("sm-wm1"); got != 1 {
		t.Fatalf("白名单未中应记 1 次跳过事件, got %d", got)
	}
	if r := sink.last("sm-wm1"); r != ferry.SameModelSkipWhitelistMiss {
		t.Fatalf("原因 = %q, want %q", r, ferry.SameModelSkipWhitelistMiss)
	}
	w.maybeSameModel(st) // 同写入版本只判一次(防逐轮刷屏)
	if got := sink.count("sm-wm1"); got != 1 {
		t.Fatalf("同版本应去重, got %d", got)
	}
	if qwRead(led, st).handedOff != 0 {
		t.Fatal("跳过带内不得入队(静默交还既有调度)")
	}
	// 新写入=新版本:重新武装,再判再记(F6 防误统计:每版本一次)
	setLastWrite(led, st, *now-8)
	w.maybeSameModel(st)
	if got := sink.count("sm-wm1"); got != 2 {
		t.Fatalf("新版本应重判, got %d", got)
	}
}

func TestSameModelSkipNotEnabledPath(t *testing.T) {
	now := freezeClock(t, smBaseT)
	led := ledger.New()
	tmp := t.TempDir()
	sink := newSkipSink()
	w := newTestWatcherW(smGateCfg(), led, nil, nil, nil, nil) // 白名单=活动上游 zhipu
	w.bookSameModelSkipFn = sink.record
	variants := []struct {
		name string
		arm  config.ArmVerdictResolver
	}{
		{"缝未装配缺省nil", nil},
		{"无结论", func(string) (bool, bool) { return false, false }},
		{"有结论未启用", func(string) (bool, bool) { return true, false }},
	}
	for i, v := range variants {
		sid := fmt.Sprintf("sm-ne-%d", i)
		st, _ := bareSession(t, led, tmp, sid)
		setLastWrite(led, st, *now-7)
		w.ArmVerdict = v.arm
		w.maybeSameModel(st)
		if r := sink.last(sid); r != ferry.SameModelSkipNotEnabled {
			t.Fatalf("%s: 原因 = %q, want %q", v.name, r, ferry.SameModelSkipNotEnabled)
		}
		if qwRead(led, st).handedOff != 0 {
			t.Fatalf("%s: 未启用分支不得入队", v.name)
		}
	}
}

func TestSameModelSkipColdPath(t *testing.T) {
	now := freezeClock(t, smBaseT)
	led := ledger.New()
	tmp := t.TempDir()
	sink := newSkipSink()
	w := newTestWatcherW(smGateCfg(), led, nil, nil, nil, nil)
	w.bookSameModelSkipFn = sink.record
	w.ArmVerdict = func(string) (bool, bool) { return true, true } // 白名单+已启用,只看热
	// 无最后请求观测(如 daemon 重启后)→ 保守判冷
	st, _ := bareSession(t, led, tmp, "sm-cold1")
	setLastWrite(led, st, *now-7)
	w.maybeSameModel(st)
	if r := sink.last("sm-cold1"); r != ferry.SameModelSkipCold {
		t.Fatalf("无观测应保守判冷, got %q", r)
	}
	// 有观测但时钟超 τ(2000s > 0.8×1800=1440)→ 冷
	st2, _ := bareSession(t, led, tmp, "sm-cold2")
	setLastWrite(led, st2, *now-7)
	w.ReqClock.Note("sm-cold2", *now-2000)
	w.maybeSameModel(st2)
	if r := sink.last("sm-cold2"); r != ferry.SameModelSkipCold {
		t.Fatalf("时钟超 τ 应判冷, got %q", r)
	}
	for _, s := range []*ledger.SessionState{st, st2} {
		if qwRead(led, s).handedOff != 0 {
			t.Fatal("判冷分支不得入队(零模型调用,静默交还)")
		}
	}
}

// ---- 热路径:票03 执行体已落地——无发送器形态=降级静默交还 + 25 分钟档原样接管 ----

func TestSameModelHotNoSenderDegradesSilently(t *testing.T) {
	now := freezeClock(t, smBaseT)
	led := ledger.New()
	tmp := t.TempDir()
	sink := newSkipSink()
	w := newTestWatcherW(smGateCfg(), led, nil, nil, nil, nil) // 无 BeatSender/AppendSender
	w.bookSameModelSkipFn = sink.record
	w.smSyncExec = true // 同步直调形态:断言免竞态等待(生产恒异步)
	w.ArmVerdict = func(string) (bool, bool) { return true, true }
	w.ReqClock.Note("sm-hot1", *now-100) // 心跳 100s 前刚保温;时钟 ≤ τ → 热
	st, _ := bareSession(t, led, tmp, "sm-hot1")
	setLastWrite(led, st, *now-7)
	w.maybeSameModel(st)
	if got := sink.count("sm-hot1"); got != 0 {
		t.Fatalf("判热不得记跳过事件, got %d", got)
	}
	// 无发送器=降级:派发章被失败路径清零,静默交还既有调度。
	if qwRead(led, st).handedOff != 0 {
		t.Fatal("无发送器降级应清派发章(零模型调用,静默交还)")
	}
	// 版本章已盖:换成未启用结论再判不得出新事件(证明热路径也只判一次)
	w.ArmVerdict = func(string) (bool, bool) { return true, false }
	w.maybeSameModel(st)
	if got := sink.count("sm-hot1"); got != 0 {
		t.Fatalf("热路径应随版本去重, got %d", got)
	}
	// 静默交还的兑现:闲置越过总结阈值(12s ≥ 10s)后,25 分钟档原样入队
	setLastWrite(led, st, *now-12)
	w.maybeEnqueue(st)
	if qwRead(led, st).handedOff == 0 {
		t.Fatal("总结阈值档应照常入队(冷/热分支都只是让路,一行不改)")
	}
}

// ---- off(默认)零行为差异 ----

func TestSameModelOffZeroBehavior(t *testing.T) {
	now := freezeClock(t, smBaseT)
	led := ledger.New()
	tmp := t.TempDir()
	cfg := smGateCfg()
	cfg.SameModel.Enabled = false // 默认 off:所有新路径必须零行为
	sink := newSkipSink()
	w := newTestWatcherW(cfg, led, nil, nil, nil, nil)
	w.bookSameModelSkipFn = sink.record
	st, _ := bareSession(t, led, tmp, "sm-off1")
	setLastWrite(led, st, *now-7)
	w.maybeSameModel(st)
	setLastWrite(led, st, *now-12)
	w.maybeSameModel(st)
	if got := sink.count("sm-off1"); got != 0 {
		t.Fatalf("off 时零跳过事件, got %d", got)
	}
	w.maybeEnqueue(st)
	if qwRead(led, st).handedOff == 0 {
		t.Fatal("off 时既有 25 分钟档行为不得受影响")
	}
}

// ---- 瞬态让路:等答复窗开着不判不章 ----

func TestSameModelQwatchWindowDefersWithoutStamp(t *testing.T) {
	now := freezeClock(t, smBaseT)
	led := ledger.New()
	tmp := t.TempDir()
	sink := newSkipSink()
	w := newTestWatcherW(smGateCfg(), led, nil, nil, nil, nil)
	w.bookSameModelSkipFn = sink.record
	st, _ := bareSession(t, led, tmp, "sm-qw1")
	setLastWrite(led, st, *now-7)
	setOpened(led, st, *now-1) // 等答复窗开着:时机归窗口机制管
	w.maybeSameModel(st)
	if got := sink.count("sm-qw1"); got != 0 {
		t.Fatalf("窗口开着不得判/不得记事件, got %d", got)
	}
	led.Mu().Lock() // 关窗(写入关窗语义的手工形)
	st.QWatchOpenedTS = nil
	led.Mu().Unlock()
	w.maybeSameModel(st) // 未盖章 ⇒ 关窗后同版本仍可判
	if got := sink.count("sm-qw1"); got != 1 {
		t.Fatalf("关窗后应判一次, got %d", got)
	}
}

// ---- 判热时钟喂入(数据源钉死:F3) ----

func TestHeatClockFeedsFromSettledBeats(t *testing.T) {
	led := ledger.New()
	tmp := t.TempDir()
	w := newTestWatcherW(mergeCfg(), led, nil, nil, nil, nil)
	st, _ := bareSession(t, led, tmp, "sm-bc1")
	w.settleBeat(st, beat.BeatResult{Sent: true, OK: true}) // 真发拿到结论(含 miss:上游已处理全前缀)
	first, ok := w.ReqClock.Last("sm-bc1")
	if !ok || first <= 0 {
		t.Fatalf("真发成功的心跳重放应计入判热时钟, got %v,%v", first, ok)
	}
	w.settleBeat(st, beat.BeatResult{Sent: true, OK: false, Err: "conn_error"}) // ERROR:缓存状态未知
	afterErr, _ := w.ReqClock.Last("sm-bc1")
	if afterErr != first {
		t.Fatalf("ERROR 不得推进判热时钟: before=%v after=%v", first, afterErr)
	}
	w.settleBeat(st, beat.BeatResult{Sent: false}) // observe 演练未真发
	afterObserve, _ := w.ReqClock.Last("sm-bc1")
	if afterObserve != first {
		t.Fatalf("observe 演练不得计入判热时钟: before=%v after=%v", first, afterObserve)
	}
}

func TestHeatClockFeedsFromWaitLaneReplay(t *testing.T) {
	led := ledger.New()
	tmp := t.TempDir()
	rec := &recordingSender{}
	d, w := newWaitDaemonAndWatcher(waitCfg("enforce", 0), led, rec, nil)
	st, _ := bareSession(t, led, tmp, "sm-wl1")
	setLastWrite(led, st, clock.Now()-150)
	openActiveWindow(d, "sm-wl1")
	engage(w, st)
	if got := len(rec.plansList()); got != 1 {
		t.Fatalf("活跃窗到点应排 1 跳, got %d", got)
	}
	if _, ok := w.ReqClock.Last("sm-wl1"); !ok {
		t.Fatal("等待窗真发重放应计入判热时钟(含体外心跳重放口径)")
	}
}

func TestHeatClockFeedsFromMainTranscriptUsage(t *testing.T) {
	tmp := t.TempDir()
	projects := filepath.Join(tmp, "projects")
	acc, err := accounts.New(filepath.Join(tmp, "acc"))
	if err != nil {
		t.Fatal(err)
	}
	led := ledger.New()
	cfg := config.Default()
	cfg.Watch.CCProjectsDir = projects
	w := NewWatcher(cfg, led, nil, func(*ledger.SessionState) bool { return true }, 0, acc, nil, nil, nil)
	if w.ReqClock == nil {
		t.Fatal("NewWatcher 应装配判热时钟")
	}
	// 主转录 usage 行:真实上游流量的最后请求时刻=转录时间戳(非墙钟)
	row, _ := json.Marshal(map[string]any{
		"type": "user", "timestamp": "2026-09-18T12:00:00.000Z",
		"sessionId": "sm-us1", "cwd": "C:/proj",
		"message": map[string]any{"role": "user", "content": "提问原话(不入账)"},
	})
	asst, _ := json.Marshal(map[string]any{
		"type": "assistant", "timestamp": "2026-09-18T12:00:05.000Z",
		"sessionId": "sm-us1", "cwd": "C:/proj",
		"message": map[string]any{"role": "assistant", "id": "msg_sm_us1",
			"model":   "glm-5.3",
			"content": []any{map[string]any{"type": "text", "text": "回答原话(不入账)"}},
			"usage": map[string]any{"input_tokens": 100,
				"cache_read_input_tokens": 4000, "cache_creation_input_tokens": 0,
				"output_tokens": 50}},
	})
	f := filepath.Join(projects, "C--proj", "sm-us1.jsonl")
	if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f, []byte(string(row)+"\n"+string(asst)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(f)
	if err != nil {
		t.Fatal(err)
	}
	st := led.Touch("cc", "sm-us1", f, statMTime(info), int(info.Size()), 0)
	w.harvestUsage(f, info.Size(), st)
	want := float64(time.Date(2026, 9, 18, 12, 0, 5, 0, time.UTC).Unix())
	if ts, ok := w.ReqClock.Last("sm-us1"); !ok || math.Abs(ts-want) > 1 {
		t.Fatalf("主转录 usage 行应入钟: got %v,%v want ≈%v", ts, ok, want)
	}
	// 子代理 usage 不入钟:子代理前缀 ≠ 主会话前缀,不刷主会话缓存
	sub := writeSubagentTranscript(t, projects, "sm-us1", "agent-x1", 10, 5)
	si, err := os.Stat(sub)
	if err != nil {
		t.Fatal(err)
	}
	w.harvestSubagentUsage(sub, si.Size())
	if ts, _ := w.ReqClock.Last("sm-us1"); math.Abs(ts-want) > 1 {
		t.Fatalf("子代理 usage 不得改写主会话判热时钟: got %v want ≈%v", ts, want)
	}
}

// ---- 票03:执行档(追加重放派发/失败链/零重试/lane 记账) ----

// smNarrative 合格叙事夹具:两层标记+非空层(严格解析通过)。
const smNarrative = ferry.InjectOpen + "\n注入层:目标过半,下一步做B\n" +
	ferry.InjectClose + "\n# 目标\n做完A\n# 续接第一句话\n接B"

// smFakeSender 追加重放发送替身:记录计划、按脚本吐结果(默认合格叙事)。
type smFakeSender struct {
	mu      sync.Mutex
	plans   []beat.AppendReplayPlan
	results []beat.AppendReplayResult
	idx     int
}

func (f *smFakeSender) SendAppendReplay(p beat.AppendReplayPlan) beat.AppendReplayResult {
	f.mu.Lock()
	f.plans = append(f.plans, p)
	f.mu.Unlock()
	if f.idx < len(f.results) {
		r := f.results[f.idx]
		f.idx++
		return r
	}
	return beat.AppendReplayResult{Sent: true, OK: true, StopReason: "end_turn",
		Text: smNarrative, InputTokens: 100, CacheReadTokens: 1900,
		OutputTokens: 220, Model: "glm-5.3"}
}

func (f *smFakeSender) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.plans)
}

func (f *smFakeSender) plan(i int) beat.AppendReplayPlan {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.plans[i]
}

// smHandoffRow 读该会话的 handoff 科目行(lane 断言用)。
func smHandoffRow(t *testing.T, acc *accounts.Accounts, sid string) map[string]any {
	t.Helper()
	rows := acc.Read(accounts.ReadOpts{Kind: "handoff", Session: sid})
	if len(rows) != 1 {
		t.Fatalf("handoff 行数 = %d, want 1", len(rows))
	}
	return rows[0]
}

// smEntryWithStatus 该会话指定状态的交接条目(无=nil)。
func smEntryWithStatus(t *testing.T, stt *store.Store, cwd, status string) *store.Entry {
	t.Helper()
	for _, e := range stt.RestoreCandidates("cc", cwd) {
		if e.Status == status {
			cp := e
			return &cp
		}
	}
	return nil
}

// writeSmTranscript 冻结时钟对齐的转录夹具:时间戳=smBaseT-60s(新鲜窗内),
// 使 facts.LastTS→covers_until 可过 RestoreCandidates 的新鲜度过滤。
func writeSmTranscript(t *testing.T, dir, sid, cwd string) string {
	t.Helper()
	d := filepath.Join(dir, "C--smproj")
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	ts := time.Unix(int64(smBaseT-60), 0).UTC().Format("2006-01-02T15:04:05.000Z")
	f := filepath.Join(d, sid+".jsonl")
	lines := []string{
		fmt.Sprintf(`{"type":"user","timestamp":%q,"cwd":%q,"sessionId":%q,`+
			`"message":{"role":"user","content":"做点活"}}`, ts, cwd, sid),
		fmt.Sprintf(`{"type":"assistant","timestamp":%q,"message":{"role":"assistant",`+
			`"content":[{"type":"text","text":"干完了"}],"usage":{"input_tokens":2000,`+
			`"cache_read_input_tokens":100,"cache_creation_input_tokens":0,`+
			`"output_tokens":5}}}`, ts),
	}
	if err := os.WriteFile(f, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return f
}

// smSession 登记+闲置到触发点的会话(7s ≥ 阈值 6s);返回(状态, 转录路径)。
func smSession(t *testing.T, led *ledger.Ledger, tmp, sid, cwd string, now float64) (*ledger.SessionState, string) {
	t.Helper()
	path := writeSmTranscript(t, filepath.Join(tmp, "projects"), sid, cwd)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	st := led.Touch("cc", sid, path, statMTime(info), int(info.Size()), 0)
	led.Mu().Lock()
	st.Cwd = cwd // 台账 cwd(富化替身不填,手工对齐生产形态)
	led.Mu().Unlock()
	setLastWrite(led, st, now-7)
	return st, path
}

func TestSameModelHotExecutesAndSavesHandoff(t *testing.T) {
	now := freezeClock(t, smBaseT)
	led := ledger.New()
	tmp := t.TempDir()
	stt, err := store.New(filepath.Join(tmp, "data"))
	if err != nil {
		t.Fatal(err)
	}
	acc, err := accounts.New(tmp)
	if err != nil {
		t.Fatal(err)
	}
	sink := newSkipSink()
	w := newTestWatcherW(smGateCfg(), led, acc, nil, nil, nil)
	w.Store = stt
	w.smSyncExec = true
	w.bookSameModelSkipFn = sink.record
	w.ArmVerdict = func(string) (bool, bool) { return true, true }
	w.ReqClock.Note("sm-exec1", *now-100)
	fake := &smFakeSender{}
	w.sameModelSendFn = fake.SendAppendReplay
	// 冻结时钟对齐真转录:covers_until 有效(交接条目可经 RestoreCandidates 断言)。
	st, _ := smSession(t, led, tmp, "sm-exec1", "C:/smproj", *now)
	w.maybeSameModel(st)

	if got := sink.count("sm-exec1"); got != 0 {
		t.Fatalf("判热进档不得记跳过事件, got %d", got)
	}
	if fake.count() != 1 {
		t.Fatalf("追加重放应单发, got %d", fake.count())
	}
	p := fake.plan(0)
	if p.SessionID != "sm-exec1" || p.Instruction != ferry.SameModelInstruction {
		t.Fatalf("计划形状: sid=%q 指令模板匹配=%v", p.SessionID, p.Instruction == ferry.SameModelInstruction)
	}
	if p.MaxTokens != ferry.SameModelMaxTokens || p.MaxTokens <= 0 {
		t.Fatalf("max_tokens 封顶 = %d, want %d(可配缝)", p.MaxTokens, ferry.SameModelMaxTokens)
	}
	// 产物:叙事+骨架合成的 fresh 交接(两层结构不变)。
	e := smEntryWithStatus(t, stt, "C:/smproj", "fresh")
	if e == nil {
		t.Fatal("成功路径应落 fresh 交接")
	}
	md, err := os.ReadFile(e.Path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{ferry.InjectOpen, "做完A",
		"确定性骨架（程序化抽取，未经模型）", "same_model"} {
		if !strings.Contains(string(md), want) {
			t.Fatalf("产物缺 %q:\n%s", want, md)
		}
	}
	// 记账:lane=same_model、usage 口径(prompt=未命中前缀+缓存读)。
	row := smHandoffRow(t, acc, "sm-exec1")
	if row["lane"] != ferry.HandoffLaneSameModel || row["outcome"] != "fresh" {
		t.Fatalf("行 = %v", row)
	}
	if row["provider"] != "zhipu" || row["prompt_tokens"] != float64(2000) ||
		row["completion_tokens"] != float64(220) {
		t.Fatalf("记账口径: %v", row)
	}
	// 派发章保留(执行期间 25 分钟档不重复入队);判热时钟被喂入。
	if qwRead(led, st).handedOff == 0 {
		t.Fatal("成功应保留派发章")
	}
	if ts, ok := w.ReqClock.Last("sm-exec1"); !ok || ts != *now {
		t.Fatalf("追加重放应喂判热时钟: %v,%v want %v", ts, ok, *now)
	}
}

func TestSameModelToolUseZeroRetryFallsToExistingChainThenSkeleton(t *testing.T) {
	now := freezeClock(t, smBaseT)
	led := ledger.New()
	tmp := t.TempDir()
	stt, err := store.New(filepath.Join(tmp, "data"))
	if err != nil {
		t.Fatal(err)
	}
	acc, err := accounts.New(tmp)
	if err != nil {
		t.Fatal(err)
	}
	var enqueued []*ledger.SessionState
	w := NewWatcher(smGateCfg(), led, stt, func(s *ledger.SessionState) bool {
		enqueued = append(enqueued, s)
		return true
	}, 0, acc, nil, nil, nil)
	w.enrich = func(st *ledger.SessionState) { // newTestWatcherW 同款富化替身
		led.Mu().Lock()
		st.PeakCtx = 50000
		led.Mu().Unlock()
	}
	w.harvest = nil
	w.smSyncExec = true
	w.ArmVerdict = func(string) (bool, bool) { return true, true }
	w.ReqClock.Note("sm-tu1", *now-100)
	fake := &smFakeSender{results: []beat.AppendReplayResult{{
		Sent: true, OK: true, StopReason: "tool_use", Text: "调了工具",
		InputTokens: 50, CacheReadTokens: 1950, OutputTokens: 5, Model: "glm-5.3",
	}}}
	w.sameModelSendFn = fake.SendAppendReplay
	st, path := smSession(t, led, tmp, "sm-tu1", "C:/smfail", *now)
	w.maybeSameModel(st)
	w.maybeSameModel(st) // 同版本再判:零重试(smSeen 已章)

	if fake.count() != 1 {
		t.Fatalf("同模型档零重试, got %d 发", fake.count())
	}
	if got := len(enqueued); got != 0 {
		t.Fatalf("失败当场不得入队(交还既有调度,不提前第三方时机), got %d", got)
	}
	// 失败行:lane=same_model、outcome=failed。
	row := smHandoffRow(t, acc, "sm-tu1")
	if row["lane"] != ferry.HandoffLaneSameModel || row["outcome"] != "failed" {
		t.Fatalf("失败行 = %v", row)
	}
	// 无 fresh 产物;派发章被清(既有 25 分钟档重武装)。
	if e := smEntryWithStatus(t, stt, "C:/smfail", "fresh"); e != nil {
		t.Fatal("tool_use 失败不得落 fresh 产物")
	}
	if qwRead(led, st).handedOff != 0 {
		t.Fatal("失败应清派发章(25 分钟档重武装)")
	}
	// 交还兑现:闲置越过总结阈值 → 既有链入队(第三方档接管)。
	setLastWrite(led, st, *now-12)
	w.maybeEnqueue(st)
	if got := len(enqueued); got != 1 {
		t.Fatalf("25 分钟档应照常入队, got %d", got)
	}
	if qwRead(led, st).handedOff == 0 {
		t.Fatal("入队即记(既有语义)")
	}
	// 再败落骨架(既有 worker 链,一行不改):第三方执行器恒败 → 骨架交接。
	cfg := smGateCfg()
	cfg.FerryProvider = "fake"
	worker := NewWorker(cfg, stt, acc, map[string]ferry.Provider{"fake": {
		Name: "fake", BaseURL: "http://127.0.0.1:9/v1", Model: "fake"}}, explodingFerry)
	worker.do(map[string]any{"transcript_path": path, "agent": "cc",
		"session_id": "sm-tu1", "cwd": "C:/smfail"})
	if e := smEntryWithStatus(t, stt, "C:/smfail", "skeleton"); e == nil {
		t.Fatal("第三方再败应落骨架(既有链)")
	}
}

func TestSameModelBadFormatFallsBack(t *testing.T) {
	now := freezeClock(t, smBaseT)
	led := ledger.New()
	tmp := t.TempDir()
	stt, err := store.New(filepath.Join(tmp, "data"))
	if err != nil {
		t.Fatal(err)
	}
	acc, err := accounts.New(tmp)
	if err != nil {
		t.Fatal(err)
	}
	sink := newSkipSink()
	w := newTestWatcherW(smGateCfg(), led, acc, nil, nil, nil)
	w.Store = stt
	w.smSyncExec = true
	w.bookSameModelSkipFn = sink.record
	w.ArmVerdict = func(string) (bool, bool) { return true, true }
	w.ReqClock.Note("sm-bf1", *now-100)
	fake := &smFakeSender{results: []beat.AppendReplayResult{{
		Sent: true, OK: true, StopReason: "end_turn",
		Text:  "没有标记的散文输出", // 不合交接 MD 结构
		Model: "glm-5.3", InputTokens: 10, CacheReadTokens: 90, OutputTokens: 7,
	}}}
	w.sameModelSendFn = fake.SendAppendReplay
	st, _ := smSession(t, led, tmp, "sm-bf1", "C:/smbf", *now)
	w.maybeSameModel(st)

	if fake.count() != 1 {
		t.Fatalf("单发, got %d", fake.count())
	}
	row := smHandoffRow(t, acc, "sm-bf1")
	if row["lane"] != ferry.HandoffLaneSameModel || row["outcome"] != "failed" {
		t.Fatalf("格式不符应记 failed 行: %v", row)
	}
	if e := smEntryWithStatus(t, stt, "C:/smbf", "fresh"); e != nil {
		t.Fatal("格式不符不得落 fresh 产物")
	}
	if qwRead(led, st).handedOff != 0 {
		t.Fatal("格式不符应清章交还既有调度")
	}
}

func TestSameModelInFlightGuardSkipsDispatch(t *testing.T) {
	now := freezeClock(t, smBaseT)
	led := ledger.New()
	tmp := t.TempDir()
	sink := newSkipSink()
	w := newTestWatcherW(smGateCfg(), led, nil, nil, nil, nil)
	w.smSyncExec = true
	w.bookSameModelSkipFn = sink.record
	w.ArmVerdict = func(string) (bool, bool) { return true, true }
	w.ReqClock.Note("sm-if1", *now-100)
	fake := &smFakeSender{}
	w.sameModelSendFn = fake.SendAppendReplay
	st, _ := bareSession(t, led, tmp, "sm-if1")
	setLastWrite(led, st, *now-7)
	w.sameModelInFlight.Store(true) // 已有追加重放在途
	w.maybeSameModel(st)
	if fake.count() != 0 {
		t.Fatalf("在途占用期不得再派发, got %d", fake.count())
	}
	if qwRead(led, st).handedOff != 0 {
		t.Fatal("占用期跳过不得盖派发章")
	}
}

func TestSameModelSkipBooksTelemetryRow(t *testing.T) {
	now := freezeClock(t, smBaseT)
	led := ledger.New()
	tmp := t.TempDir()
	acc, err := accounts.New(tmp)
	if err != nil {
		t.Fatal(err)
	}
	// 真实账本走默认通道(票02 遗留:same_model_skip 科目白名单在票03 落地)。
	w := newTestWatcherW(smGateCfg(), led, acc, nil, nil, nil)
	w.smSyncExec = true
	w.ArmVerdict = func(string) (bool, bool) { return true, true }
	st, _ := bareSession(t, led, tmp, "sm-tr1")
	setLastWrite(led, st, *now-7)
	w.maybeSameModel(st) // 无判热观测 → cold
	rows := acc.Read(accounts.ReadOpts{Kind: "same_model_skip", Session: "sm-tr1"})
	if len(rows) != 1 {
		t.Fatalf("跳过遥测应落一行, got %d", len(rows))
	}
	if rows[0]["reason"] != ferry.SameModelSkipCold || rows[0]["upstream"] != "zhipu" {
		t.Fatalf("行 = %v", rows[0])
	}
}
