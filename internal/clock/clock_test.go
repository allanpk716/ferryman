package clock

import (
	"math"
	"testing"
	"time"
)

// TestNowIsEpochSeconds 默认实现返回 float64 epoch 秒（UnixNano/1e9），
// 与 time.Now 折算值相差不超过 1 秒。
func TestNowIsEpochSeconds(t *testing.T) {
	want := float64(time.Now().UnixNano()) / 1e9
	if got := Now(); math.Abs(got-want) > 1 {
		t.Errorf("Now() = %v, 与 epoch 秒 %v 相差超过 1s", got, want)
	}
}

// TestNowInjectable 替换包级 Now 后，读时钟的函数读到注入值；
// 恢复原值后回到真实时间——保证不污染同包其他测试。
func TestNowInjectable(t *testing.T) {
	orig := Now
	t.Cleanup(func() { Now = orig })

	const injected = 1234.5678
	Now = func() float64 { return injected }

	// reader 代表业务侧读时钟的函数，须透过包级变量取值。
	if got := reader(); got != injected {
		t.Fatalf("reader() = %v, want 注入值 %v", got, injected)
	}

	// 恢复原值后不再返回注入值，且与真实时间一致。
	Now = orig
	want := float64(time.Now().UnixNano()) / 1e9
	if got := Now(); got == injected || math.Abs(got-want) > 1 {
		t.Fatalf("恢复后 Now() = %v, 应回到真实时间（约 %v）而非注入值 %v", got, want, injected)
	}
}

// reader 代表业务侧读时钟的函数。
func reader() float64 { return Now() }
