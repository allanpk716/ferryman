package beat

// 规格：beat 纯逻辑用例移植——tests/test_qwatch_scheduler.py 纯逻辑段
// （test_classify_three_states / test_breaker_unit_transparent_error_and_reset /
// test_beat_sender_protocol_shape）+ tests/test_qwatch_events.py 的
// QWatchStats 纯计数段（计数与 Round6）。全部无 I/O、无消息内容
// （隐私不变量：字段只有元数据与金额）。
// HttpBeatSender 不实现（Q14 未授权；接口位保留）。

import (
	"reflect"
	"testing"

	"ferryman/internal/mathx"
)

func TestClassifyThreeStates(t *testing.T) {
	cases := []struct {
		name string
		r    BeatResult
		want string
	}{
		{"未真发=observe", BeatResult{Sent: false}, OutObserve},
		{"重试后仍败=error", BeatResult{Sent: true, OK: false, Err: "429"}, OutError},
		{"0.9=hit", BeatResult{Sent: true, OK: true, InputTokens: 100, CacheReadTokens: 900}, OutHit},
		{"0.5边界=hit", BeatResult{Sent: true, OK: true, InputTokens: 100, CacheReadTokens: 100}, OutHit},
		{"≈0全miss", BeatResult{Sent: true, OK: true, InputTokens: 2000, CacheReadTokens: 0}, OutMiss},
		{"全零ratio=0.0=miss", BeatResult{Sent: true, OK: true}, OutMiss},
		{"0.4<0.5=miss", BeatResult{Sent: true, OK: true, InputTokens: 600, CacheReadTokens: 400}, OutMiss},
	}
	for _, c := range cases {
		if got := Classify(c.r); got != c.want {
			t.Errorf("%s: Classify = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestBreakerUnitTransparentErrorAndReset(t *testing.T) {
	// 计数器纯逻辑：ERROR 对 MISS 连击透明；HIT/MISS 清 ERROR 连击；
	// observe 演练不动任何连击。
	b := &Breaker{}
	if got := b.Record(OutMiss); got != "" || b.MissStreak != 1 {
		t.Errorf("record(miss) = %q, MissStreak = %d; want \"\", 1", got, b.MissStreak)
	}
	if got := b.Record(OutError); got != "" || b.MissStreak != 1 {
		t.Errorf("record(error) = %q, MissStreak = %d; want \"\"（不计入）, 1", got, b.MissStreak)
	}
	if got := b.Record(OutMiss); got != "demote" || b.MissStreak != 0 {
		t.Errorf("record(miss) = %q, MissStreak = %d; want \"demote\", 0", got, b.MissStreak)
	}

	b2 := &Breaker{}
	got := []string{b2.Record(OutError), b2.Record(OutError), b2.Record(OutError)}
	if want := []string{"", "", "pause"}; !reflect.DeepEqual(got, want) {
		t.Errorf("record(error)×3 = %v, want %v", got, want)
	}

	b3 := &Breaker{}
	b3.Record(OutError)
	b3.Record(OutHit)
	if b3.ErrorStreak != 0 {
		t.Errorf("ErrorStreak = %d, want 0（成功清 ERROR 连击）", b3.ErrorStreak)
	}

	b4 := &Breaker{}
	b4.Record(OutMiss)
	b4.Record(OutObserve)
	if b4.MissStreak != 1 {
		t.Errorf("MissStreak = %d, want 1（演练不动连击）", b4.MissStreak)
	}
}

func TestNoopSenderObserveDrillContract(t *testing.T) {
	// BeatSender 接口位与 NoopSender 契约：恒返回未真发结果（入账标 observe）。
	var _ Sender = NoopSender{}        // 接口形状（Python hasattr(BeatSender, "send") 的 Go 形）
	r := NoopSender{}.Send(BeatPlan{}) // 零网络：不需要真计划
	if r.Sent || r.OK {
		t.Errorf("NoopSender.Send = %+v, want Sent=false OK=false", r)
	}
	if got := Classify(r); got != OutObserve {
		t.Errorf("Classify(NoopSender.Send) = %q, want %q", got, OutObserve)
	}
}

func TestQWatchStatsCountersByOutcome(t *testing.T) {
	// hit/miss 两跳 → 跳数、四道 outcome 计数、累计实收。
	s := NewQWatchStats()
	s.RecordBeat(OutHit, 0.01)
	s.RecordBeat(OutMiss, 0)
	snap := s.Snapshot()
	if snap["beats_fired"] != 2 {
		t.Errorf("beats_fired = %v, want 2", snap["beats_fired"])
	}
	want := map[string]int{"hit": 1, "miss": 1, "error": 0, "observe": 0}
	if got, ok := snap["beats_by_outcome"].(map[string]int); !ok || !reflect.DeepEqual(got, want) {
		t.Errorf("beats_by_outcome = %v, want %v", snap["beats_by_outcome"], want)
	}
	if got := snap["cost_actual"].(float64); mathx.Round(got-0.01, 9) != 0 {
		t.Errorf("cost_actual = %v, want 0.01", got)
	}
}

func TestQWatchStatsHitsWindowsAndRound6(t *testing.T) {
	// 命中数/开窗数计数；cost_actual 以 mathx.Round 6 位收口（CPython round
	// half-even 同语义）——0.1+0.2 浮点累加尾巴被 Round6 抹平。
	s := NewQWatchStats()
	s.RecordHit()
	s.RecordBeat(OutMiss, 0.5)
	snap := s.Snapshot()
	if snap["hits"] != 1 {
		t.Errorf("hits = %v, want 1", snap["hits"])
	}
	if snap["windows_opened"] != 0 {
		t.Errorf("windows_opened = %v, want 0", snap["windows_opened"])
	}
	if snap["beats_fired"] != 1 {
		t.Errorf("beats_fired = %v, want 1", snap["beats_fired"])
	}
	if snap["cost_actual"] != 0.5 {
		t.Errorf("cost_actual = %v, want 0.5", snap["cost_actual"])
	}

	s.RecordWindowOpened()
	if snap = s.Snapshot(); snap["windows_opened"] != 1 {
		t.Errorf("windows_opened = %v, want 1", snap["windows_opened"])
	}

	s2 := NewQWatchStats()
	s2.RecordBeat(OutHit, 0.1)
	s2.RecordBeat(OutHit, 0.2)
	if got := s2.Snapshot()["cost_actual"].(float64); got != 0.3 {
		t.Errorf("cost_actual = %v, want 0.3（round(0.1+0.2, 6) 的 CPython 实测值）", got)
	}
}

func TestQWatchStatsObserveDrillNotBilled(t *testing.T) {
	// observe 演练跳进 observe 桶、实收花费不计（test_qwatch_events.py 纯计数部分）。
	s := NewQWatchStats()
	s.RecordBeat(OutObserve, 0)
	snap := s.Snapshot()
	if snap["beats_fired"] != 1 {
		t.Errorf("beats_fired = %v, want 1", snap["beats_fired"])
	}
	if bo, ok := snap["beats_by_outcome"].(map[string]int); !ok || bo[OutObserve] != 1 {
		t.Errorf("beats_by_outcome[observe] = %v, want 1", snap["beats_by_outcome"])
	}
	if snap["cost_actual"] != 0.0 {
		t.Errorf("cost_actual = %v, want 0.0（演练跳零花费）", snap["cost_actual"])
	}
}

func TestQWatchStatsUnknownOutcomeBucketed(t *testing.T) {
	// Python get(outcome, 0) + 1：未知 outcome 动态建桶（record_beat 逐字语义）。
	s := NewQWatchStats()
	s.RecordBeat("weird", 0)
	if bo := s.Snapshot()["beats_by_outcome"].(map[string]int); bo["weird"] != 1 {
		t.Errorf("beats_by_outcome[weird] = %d, want 1", bo["weird"])
	}
}
