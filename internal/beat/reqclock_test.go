package beat

// reqclock_test.go — 票02:判热时钟数据源钉子(ADR-0015 决定一:判热时钟=
// 距该会话最后一次上游请求的时长,含体外心跳重放;台账闲置不含心跳、闸门
// 继续用台账——两钟各司其职,本钟专供判热)。

import (
	"sync"
	"testing"
)

func TestLastRequestClockUnknownSession(t *testing.T) {
	c := NewLastRequestClock()
	if _, ok := c.Last("nope"); ok {
		t.Fatal("无观测会话应 ok=false(调用方据此保守判冷)")
	}
}

func TestLastRequestClockNoteAndMonotonicMax(t *testing.T) {
	c := NewLastRequestClock()
	c.Note("s1", 100)
	if ts, ok := c.Last("s1"); !ok || ts != 100 {
		t.Fatalf("Last = %v,%v want 100,true", ts, ok)
	}
	c.Note("s1", 50) // 乱序旧观测不得回退:usage 行(转录时间戳)与心跳重放(结算
	// 时刻)两源乱序到达,取最近者——判热时钟的单调语义。
	if ts, _ := c.Last("s1"); ts != 100 {
		t.Fatalf("旧观测回退了: %v want 100", ts)
	}
	c.Note("s1", 200)
	if ts, _ := c.Last("s1"); ts != 200 {
		t.Fatalf("新观测未推进: %v want 200", ts)
	}
	if _, ok := c.Last("s2"); ok {
		t.Fatal("会话隔离失败:s2 不应看到 s1 的观测")
	}
}

func TestLastRequestClockEmptySIDNoop(t *testing.T) {
	c := NewLastRequestClock()
	c.Note("", 100)
	if _, ok := c.Last(""); ok {
		t.Fatal("空 sid 不应入钟(无会话身份的观测无处归属)")
	}
}

func TestLastRequestClockConcurrent(t *testing.T) {
	c := NewLastRequestClock()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			c.Note("s", float64(i))
			c.Last("s")
		}(i)
	}
	wg.Wait()
	if ts, ok := c.Last("s"); !ok || ts != 7 {
		t.Fatalf("并发 Note 后 Last = %v,%v want 7,true", ts, ok)
	}
}
