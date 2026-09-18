package policy

import (
	"math"
	"strings"
	"testing"
)

// assertF 浮点黄金数断言：绝对误差 1e-6。
func assertF(t *testing.T, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1e-6 {
		t.Fatalf("got %v want %v", got, want)
	}
}

// TestGoldenGLM 锚定 GLM 价格制下的全部中间量，手算链：
// 150000/10000=15 块；15×1.7=25.5；25.5+300/10000×24=26.22；15×6.9=103.5；
// 480×(103.5−25.5)/26.22≈1428.0
func TestGoldenGLM(t *testing.T) {
	p := Params{PIn: 6.9, PCache: 1.7, POut: 24, Per: 10000,
		PrefixTokens: 150000, TTLS: 600, Safety: 0.8, BeatOutTokens: 300}
	r, err := Derive(p)
	if err != nil {
		t.Fatal(err)
	}
	assertF(t, r.TauS, 480)
	assertF(t, r.CacheRead, 25.5)
	assertF(t, r.PerBeat, 26.22)
	assertF(t, r.Expire, 103.5)
	if r.CapS < 1427 || r.CapS > 1429 {
		t.Fatalf("cap=%v", r.CapS)
	}
}

// TestManualCapOnlyLowers 手动上限只能往下收，不能放大。
func TestManualCapOnlyLowers(t *testing.T) {
	base := Params{PIn: 6.9, PCache: 1.7, POut: 24, Per: 10000,
		PrefixTokens: 150000, TTLS: 600, Safety: 0.8, BeatOutTokens: 300}

	low := base
	low.MaxWaitS = 900
	r, err := Derive(low)
	if err != nil {
		t.Fatal(err)
	}
	assertF(t, r.CapS, 900)

	high := base
	high.MaxWaitS = 99999
	r, err = Derive(high)
	if err != nil {
		t.Fatal(err)
	}
	if r.CapS < 1427 || r.CapS > 1429 {
		t.Fatalf("cap=%v", r.CapS)
	}
}

// TestNoCachePriceRefuses 无缓存单价时拒绝推导，不造数。
func TestNoCachePriceRefuses(t *testing.T) {
	_, err := Derive(Params{PIn: 6.9, PCache: 0, POut: 24, Per: 10000,
		PrefixTokens: 150000, TTLS: 600})
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if !strings.Contains(err.Error(), "拒绝推导") {
		t.Fatalf("err=%v, want contains 拒绝推导", err)
	}
}

// TestRefusesBadTTL TTL 零值拒绝推导——堵 SimulateBeats 的 t += 0 死循环。
func TestRefusesBadTTL(t *testing.T) {
	_, err := Derive(Params{PIn: 6.9, PCache: 1.7, POut: 24, Per: 10000,
		PrefixTokens: 150000, TTLS: 0})
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if !strings.Contains(err.Error(), "拒绝推导") {
		t.Fatalf("err=%v, want contains 拒绝推导", err)
	}
}

// TestSimulateAndDoNothing 排跳与不作为成本：
// τ=480, cap≈1428：t0=0, windowEnd=1500 → 跳在 480、960（第三跳 1440 > cap 1428 被截）
// t0=0, windowEnd=700 → 只有 480；dur=700>600 → DoNothing=Expire=103.5；dur=500≤600 → 0
func TestSimulateAndDoNothing(t *testing.T) {
	p := Params{PIn: 6.9, PCache: 1.7, POut: 24, Per: 10000,
		PrefixTokens: 150000, TTLS: 600, Safety: 0.8, BeatOutTokens: 300}
	r, err := Derive(p)
	if err != nil {
		t.Fatal(err)
	}

	beats := SimulateBeats(0, 1500, r)
	if len(beats) != 2 {
		t.Fatalf("len(beats)=%v, want 2: %v", len(beats), beats)
	}
	assertF(t, beats[0], 480)
	assertF(t, beats[1], 960)

	beats = SimulateBeats(0, 700, r)
	if len(beats) != 1 {
		t.Fatalf("len(beats)=%v, want 1: %v", len(beats), beats)
	}
	assertF(t, beats[0], 480)

	if got := DoNothingCost(700, 600, r); got != r.Expire {
		t.Fatalf("DoNothing(700)=%v, want %v", got, r.Expire)
	}
	if got := DoNothingCost(500, 600, r); got != 0 {
		t.Fatalf("DoNothing(500)=%v, want 0", got)
	}
}
