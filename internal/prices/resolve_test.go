package prices

import (
	"testing"
)

// resolve_test.go — 票05：价格本定位（BookFor）与现价版本解析（BookVersionAt）
// 的仓库口径单测。口径：key 精确命中 → 取之；仅一本 → 取唯一本；多本无映射
// → nil。早于一切版本 → 回落末版（watcher/report/doctor 同款）。

// TestBookForEmpty 空表 → nil（价格表未装配，调用方拒算/Skip）。
func TestBookForEmpty(t *testing.T) {
	if got := BookFor(nil, "glm"); got != nil {
		t.Fatalf("BookFor(nil) = %v, want nil", got)
	}
	if got := BookFor(map[string]PriceBook{}, "glm"); got != nil {
		t.Fatalf("BookFor(空表) = %v, want nil", got)
	}
}

// TestBookForExactHit key 精确命中（多本之中也直接取）。
func TestBookForExactHit(t *testing.T) {
	all := books(t) // glm + nocache 两本
	if got := BookFor(all, "glm"); got == nil || got.Key != "glm" {
		t.Fatalf("BookFor(glm) = %v, want glm", got)
	}
	if got := BookFor(all, "nocache"); got == nil || got.Key != "nocache" {
		t.Fatalf("BookFor(nocache) = %v, want nocache", got)
	}
}

// TestBookForSingleFallback 未命中 + 仅一本 → 回退该本（仓库既有口径）。
func TestBookForSingleFallback(t *testing.T) {
	only := map[string]PriceBook{"glm": allGlm(t)}
	if got := BookFor(only, "智谱"); got == nil || got.Key != "glm" {
		t.Fatalf("BookFor(仅一本, 智谱) = %v, want glm（单本回退）", got)
	}
	// 空 key 同样只走单本回退（config.bookFor 同款：key 为空不试精确命中）
	if got := BookFor(only, ""); got == nil || got.Key != "glm" {
		t.Fatalf("BookFor(仅一本, 空key) = %v, want glm", got)
	}
}

// TestBookForMultiMiss 未命中 + 多本 → nil（无映射拒算，不猜）。
func TestBookForMultiMiss(t *testing.T) {
	all := books(t)
	if got := BookFor(all, "智谱"); got != nil {
		t.Fatalf("BookFor(两本, 智谱) = %v, want nil（多本无映射）", got)
	}
}

// TestBookVersionAt 现价版本：命中取生效版；早于一切 → 回落末版；无版本 → nil。
func TestBookVersionAt(t *testing.T) {
	glm := books(t)["glm"]
	if got := BookVersionAt(&glm, D16); got == nil || got.EffectiveFrom != "2026-09-01" {
		t.Fatalf("at(D16) = %v, want 2026-09-01", got)
	}
	if got := BookVersionAt(&glm, D17); got == nil || got.EffectiveFrom != "2026-09-17" {
		t.Fatalf("at(D17) = %v, want 2026-09-17", got)
	}
	// 早于一切版本 → 末版兜底（不是 nil）
	if got := BookVersionAt(&glm, 0); got == nil || got.EffectiveFrom != "2026-09-17" {
		t.Fatalf("at(0) = %v, want 末版 2026-09-17（早于一切回落末版）", got)
	}
	// 无版本 → nil（调用方按"无法定价"拒算）
	empty := PriceBook{Key: "empty", Per: 10000}
	if got := BookVersionAt(&empty, D17); got != nil {
		t.Fatalf("at(无版本) = %v, want nil", got)
	}
}

// allGlm 单本 glm 表（单本回退用例）。
func allGlm(t *testing.T) PriceBook {
	t.Helper()
	b, ok := books(t)["glm"]
	if !ok {
		t.Fatal("fixture 缺 glm")
	}
	return b
}
