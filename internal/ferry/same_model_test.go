package ferry

// same_model_test.go — 票02:判热预测器与跳过原因编码钉子。
//
// 口径(ADR-0015 决定一/决定二;F5 保守 bootstrap、F6 跳过原因编码):
//   - 判据 = 判热时钟 + 该上游 TTL 观测的闭式预判;观测不足(无实测 TTL)
//     保守判冷——宁可落回第三方/骨架,不赌全价前缀重付;
//   - 热当口 = 时钟 ≤ safety·TTL(与 policy 的 τ=safety·T 同一条闭式——
//     「缓存必活带」,常量单源取 policy.DefaultSafety,本包不出现第二份);
//   - 跳过三原因独立编码:cold / whitelist_miss / not_enabled,互异、稳定
//     字面量,防误统计(F6)。

import (
	"testing"

	"ferryman/internal/policy"
)

func TestSameModelSkipCodesDistinct(t *testing.T) {
	codes := []string{SameModelSkipCold, SameModelSkipWhitelistMiss, SameModelSkipNotEnabled}
	seen := map[string]bool{}
	for _, c := range codes {
		if c == "" || seen[c] {
			t.Fatalf("跳过原因编码须非空且互异, got %q", codes)
		}
		seen[c] = true
	}
}

func TestPredictHotConservativeDefault(t *testing.T) {
	hot := 1800.0 * policy.DefaultSafety // τ = safety·T:缓存必活带右沿(T=30min)
	cases := []struct {
		name   string
		clockS float64
		ttlS   float64
		want   bool
	}{
		{"无观测判冷_T零值", 0, 0, false},
		{"无观测判冷_T负值", 10, -1, false},
		{"无观测判冷_时钟再小也冷", 0.001, 0, false},
		{"时钟零点热", 0, 1800, true},
		{"τ内热", 600, 1800, true},
		{"τ恰好热_含等号", hot, 1800, true},
		{"τ外保守判冷", hot + 0.5, 1800, false},
		{"T恰好_已出τ判冷", 1800, 1800, false},
		{"超T判冷", 3600, 1800, false},
	}
	for _, tc := range cases {
		if got := PredictHot(tc.clockS, TTLObs{TTLS: tc.ttlS}); got != tc.want {
			t.Errorf("%s: PredictHot(%.3f, T=%.1f) = %v, want %v",
				tc.name, tc.clockS, tc.ttlS, got, tc.want)
		}
	}
}
