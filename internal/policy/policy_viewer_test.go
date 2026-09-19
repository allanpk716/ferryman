package policy

import (
	"math"
	"strings"
	"testing"
)

// 本文件原为 internal/viewer/policy/policy_test.go，随公式单源收口并入本包；
// 全部函数加 Viewer 前缀消歧（TestNoCachePriceRefuses 与 tests/test_policy.py
// 移植版重名，见票 03）。

// viewerAssertF 浮点黄金数断言：绝对误差 1e-6。
func viewerAssertF(t *testing.T, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1e-6 {
		t.Fatalf("got %v want %v", got, want)
	}
}

// TestViewerGoldenGLM 锚定 GLM 价格制下的全部中间量，手算链：
// 150000/10000=15 块；15×1.7=25.5；25.5+300/10000×24=26.22；15×6.9=103.5；
// 480×(103.5−25.5)/26.22≈1428.0
func TestViewerGoldenGLM(t *testing.T) {
	p := Params{PIn: 6.9, PCache: 1.7, POut: 24, Per: 10000,
		PrefixTokens: 150000, TTLS: 600, Safety: 0.8, BeatOutTokens: 300}
	r, err := Derive(p)
	if err != nil {
		t.Fatal(err)
	}
	viewerAssertF(t, r.TauS, 480)
	viewerAssertF(t, r.CacheRead, 25.5)
	viewerAssertF(t, r.PerBeat, 26.22)
	viewerAssertF(t, r.Expire, 103.5)
	if r.CapS < 1427 || r.CapS > 1429 {
		t.Fatalf("cap=%v", r.CapS)
	}
}

// TestViewerManualCapOnlyLowers 手动上限只能往下收，不能放大。
func TestViewerManualCapOnlyLowers(t *testing.T) {
	base := Params{PIn: 6.9, PCache: 1.7, POut: 24, Per: 10000,
		PrefixTokens: 150000, TTLS: 600, Safety: 0.8, BeatOutTokens: 300}

	low := base
	low.MaxWaitS = 900
	r, err := Derive(low)
	if err != nil {
		t.Fatal(err)
	}
	viewerAssertF(t, r.CapS, 900)

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

// TestViewerNoCachePriceRefuses 无缓存单价时拒绝推导，不造数。
func TestViewerNoCachePriceRefuses(t *testing.T) {
	_, err := Derive(Params{PIn: 6.9, PCache: 0, POut: 24, Per: 10000,
		PrefixTokens: 150000, TTLS: 600})
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if !strings.Contains(err.Error(), "拒绝推导") {
		t.Fatalf("err=%v, want contains 拒绝推导", err)
	}
}

// TestViewerRefusesBadTTL TTL 零值拒绝推导——堵 SimulateBeats 的 t += 0 死循环。
func TestViewerRefusesBadTTL(t *testing.T) {
	_, err := Derive(Params{PIn: 6.9, PCache: 1.7, POut: 24, Per: 10000,
		PrefixTokens: 150000, TTLS: 0})
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if !strings.Contains(err.Error(), "拒绝推导") {
		t.Fatalf("err=%v, want contains 拒绝推导", err)
	}
}

// TestViewerSimulateAndDoNothing 排跳与不作为成本：
// τ=480, cap≈1428：t0=0, windowEnd=1500 → 跳在 480、960（第三跳 1440 > cap 1428 被截）
// t0=0, windowEnd=700 → 只有 480；dur=700>600 → DoNothing=Expire=103.5；dur=500≤600 → 0
func TestViewerSimulateAndDoNothing(t *testing.T) {
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
	viewerAssertF(t, beats[0], 480)
	viewerAssertF(t, beats[1], 960)

	beats = SimulateBeats(0, 700, r)
	if len(beats) != 1 {
		t.Fatalf("len(beats)=%v, want 1: %v", len(beats), beats)
	}
	viewerAssertF(t, beats[0], 480)

	if got := DoNothingCost(700, 600, r); got != r.Expire {
		t.Fatalf("DoNothing(700)=%v, want %v", got, r.Expire)
	}
	if got := DoNothingCost(500, 600, r); got != 0 {
		t.Fatalf("DoNothing(500)=%v, want 0", got)
	}
}
