package policy

import (
	"errors"
	"math"
	"strings"
	"testing"

	"ferryman/internal/prices"
)

// glmBook 对应 Python 测试常量 BOOK：GLM 实测口径（150k 前缀、2026-09-17 价）。
func glmBook() prices.PriceBook {
	return prices.PriceBook{Key: "glm", Unit: "智谱积分", Per: 10000,
		Versions: []prices.PriceVersion{glmVer("2026-09-17", 6.9, 1.7, 24)}}
}

// glmVer 构造带 p_cache 的版本（测试辅助；指针指向独立副本，互不共享）。
func glmVer(eff string, pin, pcache, pout float64) prices.PriceVersion {
	return prices.PriceVersion{EffectiveFrom: eff, PIn: pin, PCache: &pcache, POut: pout}
}

// approxAbs 对应 pytest.approx(want, abs=tol)。
func approxAbs(t *testing.T, name string, got, want, tol float64) {
	t.Helper()
	if math.Abs(got-want) > tol {
		t.Fatalf("%s = %v, want %v (abs tol %v)", name, got, want, tol)
	}
}

// approxRel 对应 pytest.approx(want, rel=tol)。
func approxRel(t *testing.T, name string, got, want, tol float64) {
	t.Helper()
	if math.Abs(got-want) > math.Abs(want)*tol {
		t.Fatalf("%s = %v, want %v (rel tol %v)", name, got, want, tol)
	}
}

// TestGLM150kReportNumbers 锚定实验报告 §4 的 GLM 实测数字。
func TestGLM150kReportNumbers(t *testing.T) {
	b := glmBook()
	pol, err := Compute(b, b.Versions[0], 600, 150000,
		DefaultBeatOutTokens, DefaultSafety, DefaultGraceS, DefaultMinPrefixTokens)
	if err != nil {
		t.Fatal(err)
	}
	approxRel(t, "tau_s", pol.TauS, 480, 1e-6)                       // 0.8×10min
	approxAbs(t, "per_beat_cost", pol.PerBeatCost, 26.2, 0.1)        // 25.5 + 0.72
	approxRel(t, "expire_cost", pol.ExpireCost, 103.5, 1e-6)         // 15×6.9
	approxRel(t, "worthwhile_cap_s", pol.WorthwhileCapS, 1428, 0.03) // ~24min（报告口径 25–28 带）
	if pol.GraceS != 300 || pol.MinPrefixTokens != 30000 {
		t.Fatalf("grace/min_prefix = (%v, %d), want (300, 30000)", pol.GraceS, pol.MinPrefixTokens)
	}
}

// TestSmallPrefixShrinksCap 300×P_out 占比变大 → cap 变小。
func TestSmallPrefixShrinksCap(t *testing.T) {
	b := glmBook()
	pv := b.Versions[0]
	big, err := Compute(b, pv, 600, 150000,
		DefaultBeatOutTokens, DefaultSafety, DefaultGraceS, DefaultMinPrefixTokens)
	if err != nil {
		t.Fatal(err)
	}
	small, err := Compute(b, pv, 600, 30000,
		DefaultBeatOutTokens, DefaultSafety, DefaultGraceS, DefaultMinPrefixTokens)
	if err != nil {
		t.Fatal(err)
	}
	if !(small.WorthwhileCapS < big.WorthwhileCapS) {
		t.Fatalf("small cap %v 不小于 big cap %v", small.WorthwhileCapS, big.WorthwhileCapS)
	}
}

// TestNoCachePriceRefuses 无缓存单价时拒绝推导，不造数。
func TestNoCachePriceRefuses(t *testing.T) {
	nb := prices.PriceBook{Key: "x", Unit: "元", Per: 1000000,
		Versions: []prices.PriceVersion{
			{EffectiveFrom: "2026-09-01", PIn: 1.0, PCache: nil, POut: 2.0},
		}}
	_, err := Compute(nb, nb.Versions[0], 600, 100000,
		DefaultBeatOutTokens, DefaultSafety, DefaultGraceS, DefaultMinPrefixTokens)
	var ncp NoCachePriceError
	if !errors.As(err, &ncp) {
		t.Fatalf("err = %v, want NoCachePriceError", err)
	}
}

// TestBadTTL TTL 零值拒绝推导，文案含 "ttl_s"。
func TestBadTTL(t *testing.T) {
	b := glmBook()
	_, err := Compute(b, b.Versions[0], 0, 100000,
		DefaultBeatOutTokens, DefaultSafety, DefaultGraceS, DefaultMinPrefixTokens)
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if !errors.Is(err, ErrTTLUnset) || !strings.Contains(err.Error(), "ttl_s") {
		t.Fatalf("err = %v, want ErrTTLUnset 且含 ttl_s", err)
	}
}

// TestTiers 分档：≤τ 缓存必活；τ..cap 划算；>cap 放任过期。
func TestTiers(t *testing.T) {
	b := glmBook()
	pol, err := Compute(b, b.Versions[0], 600, 150000,
		DefaultBeatOutTokens, DefaultSafety, DefaultGraceS, DefaultMinPrefixTokens)
	if err != nil {
		t.Fatal(err)
	}
	if got := TierFor(pol, 400); got != "none" {
		t.Fatalf("tier(400) = %q, want none", got)
	}
	if got := TierFor(pol, 900); got != "beat" {
		t.Fatalf("tier(900) = %q, want beat", got)
	}
	if got := TierFor(pol, 10000); got != "expire" {
		t.Fatalf("tier(10000) = %q, want expire", got)
	}
}

// TestStrategyCosts 三策略 + compact 变体。
func TestStrategyCosts(t *testing.T) {
	b := glmBook()
	pv := b.Versions[0]
	sc, err := StrategyCosts(b, pv, 600, 900, 150000, DefaultBeatOutTokens, DefaultCompactRatio)
	if err != nil {
		t.Fatal(err)
	}
	approxRel(t, "none", sc["none"], 103.5, 1e-6) // 900s > TTL 600s → 全量重付
	approxAbs(t, "beat", sc["beat"], 2*26.2, 0.2) // ⌈900/480⌉ = 2 跳
	approxRel(t, "expire", sc["expire"], 103.5, 1e-6)
	approxRel(t, "expire_compact", sc["expire_compact"], 0.25*103.5, 1e-6)
	sc2, err := StrategyCosts(b, pv, 600, 300, 150000, DefaultBeatOutTokens, DefaultCompactRatio)
	if err != nil {
		t.Fatal(err)
	}
	if sc2["none"] != 0.0 { // 300s ≤ TTL：白等
		t.Fatalf("none = %v, want 0", sc2["none"])
	}
	approxAbs(t, "beat", sc2["beat"], 1*26.2, 0.1)
}
