package daemon

// watcher_same_model_test.go — 票02:同模型触发点守望集成钉子(ADR-0015 决定一/
// 决定二;review_blocks F1 冷分支静默交还、F3 判热时钟口径、F6 跳过原因编码)。
//
// 验收对照:
//   - 判冷/白名单未中/未启用三路径:零模型调用、静默交还(25 分钟档原样),
//     跳过事件各记一次且原因独立编码,同写入版本去重;
//   - 判热时钟数据源钉死:主转录 usage 行时间戳(真实上游流量)+ 真发成功的
//     心跳重放(问询/等待两泳道)都计入;子代理 usage、ERROR、observe 不计;
//   - same_model off(默认)零行为差异;台账闲置(闸门语义)不动。

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"ferryman/internal/accounts"
	"ferryman/internal/beat"
	"ferryman/internal/clock"
	"ferryman/internal/config"
	"ferryman/internal/ferry"
	"ferryman/internal/ledger"
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

// ---- 热路径:静默交还 + 25 分钟档原样接管 ----

func TestSameModelHotHandsBackSilently(t *testing.T) {
	now := freezeClock(t, smBaseT)
	led := ledger.New()
	tmp := t.TempDir()
	sink := newSkipSink()
	w := newTestWatcherW(smGateCfg(), led, nil, nil, nil, nil)
	w.bookSameModelSkipFn = sink.record
	w.ArmVerdict = func(string) (bool, bool) { return true, true }
	w.ReqClock.Note("sm-hot1", *now-100) // 心跳 100s 前刚保温;时钟 ≤ τ → 热
	st, _ := bareSession(t, led, tmp, "sm-hot1")
	setLastWrite(led, st, *now-7)
	w.maybeSameModel(st)
	if got := sink.count("sm-hot1"); got != 0 {
		t.Fatalf("判热不得记跳过事件, got %d", got)
	}
	if qwRead(led, st).handedOff != 0 {
		t.Fatal("热路径本票无执行体(票03):不得入队、不得有模型调用")
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
			"model": "glm-5.3",
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
