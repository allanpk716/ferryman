package beat

// reqclock_test.go — 票02:判热时钟数据源钉子(ADR-0015 决定一:判热时钟=
// 距该会话最后一次上游请求的时长,含体外心跳重放;台账闲置不含心跳、闸门
// 继续用台账——两钟各司其职,本钟专供判热)。
// 票03 追加:快照持久化钉子(重启回种,修同模型摆渡重启失忆;D6/F9)。

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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

// ---- 票03:快照持久化(重启回种,修同模型摆渡重启失忆;D6) ----

// TestPersistentClockSnapshotRoundTrip 红绿钉一:快照含 sid→ts;新构造读快照
// 后 Last 返回该值(旧码红:纯内存恒无观测);回种值与后续 Note 取 max(单调
// 语义不变);空 sid 不入不变。
func TestPersistentClockSnapshotRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "reqclock.json")
	c := NewPersistentLastRequestClock(path)
	c.Note("s1", 1_800_000_000)
	// 快照落盘:{"sessions":{"<sid>":<ts>}}(临时文件+替换写,读者只见完整文件)
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("Note 推进应落快照: %v", err)
	}
	var snap struct {
		Sessions map[string]float64 `json:"sessions"`
	}
	if err := json.Unmarshal(b, &snap); err != nil {
		t.Fatalf(`快照应为 {"sessions":{...}} JSON: %v`, err)
	}
	if snap.Sessions["s1"] != 1_800_000_000 {
		t.Fatalf("快照 sid→ts = %v, want s1=1.8e9", snap.Sessions)
	}
	// 重启形态:新构造读快照回种
	c2 := NewPersistentLastRequestClock(path)
	if ts, ok := c2.Last("s1"); !ok || ts != 1_800_000_000 {
		t.Fatalf("回种失败: Last = %v,%v want 1.8e9,true", ts, ok)
	}
	// 回种值与后续 Note 取 max:旧观测不回退、新观测照常推进
	c2.Note("s1", 100)
	if ts, _ := c2.Last("s1"); ts != 1_800_000_000 {
		t.Fatalf("回种值被旧观测回退: %v", ts)
	}
	c2.Note("s1", 1_800_000_100)
	if ts, _ := c2.Last("s1"); ts != 1_800_000_100 {
		t.Fatalf("回种后新观测未推进: %v", ts)
	}
	// 二次重启:回种到推进后的值(推进也落了盘)
	c2b := NewPersistentLastRequestClock(path)
	if ts, ok := c2b.Last("s1"); !ok || ts != 1_800_000_100 {
		t.Fatalf("二次回种应取到推进后的值: %v,%v", ts, ok)
	}
	// 空 sid 不入不变(快照形态同款)
	c.Note("", 1_800_000_200)
	if _, ok := c.Last(""); ok {
		t.Fatal("空 sid 不应入钟(无会话身份的观测无处归属)")
	}
}

// TestPersistentClockFailsafeOnMissingOrCorrupt 缺失/损坏快照 = 空钟起步
// (fail-safe,现状语义,绝不伪造热);写失败静默不影响 Note 主路径。
func TestPersistentClockFailsafeOnMissingOrCorrupt(t *testing.T) {
	dir := t.TempDir()
	// 缺失
	if _, ok := NewPersistentLastRequestClock(filepath.Join(dir, "absent.json")).Last("s"); ok {
		t.Fatal("缺失快照应空钟起步(重启后无观测→保守判冷,现状语义)")
	}
	// 损坏
	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte(`{"sessions":{"s1":not-json`), 0o644); err != nil {
		t.Fatal(err)
	}
	c := NewPersistentLastRequestClock(bad)
	if _, ok := c.Last("s1"); ok {
		t.Fatal("损坏快照应空钟起步(fail-safe)")
	}
	c.Note("s2", 100) // 回种失败不传染主路径
	if ts, ok := c.Last("s2"); !ok || ts != 100 {
		t.Fatalf("损坏快照后 Note 应照常工作: %v,%v", ts, ok)
	}
	// 写失败静默:落盘路径所在目录不存在 → 写盘失败被吞,Note 照常推进
	c3 := NewPersistentLastRequestClock(filepath.Join(dir, "no", "such", "dir", "reqclock.json"))
	c3.Note("s", 100)
	if ts, ok := c3.Last("s"); !ok || ts != 100 {
		t.Fatalf("写失败不得影响 Note 主路径: %v,%v", ts, ok)
	}
}

// TestPersistentClockCapacityBounded sid 键有界(F9):内存与快照恒 ≤ 容量上限,
// 超限按 ts 降序截断(保留最近,淘汰最旧)。
func TestPersistentClockCapacityBounded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "reqclock.json")
	c := NewPersistentLastRequestClock(path)
	n := reqClockSnapshotMaxSids + 64
	for i := 0; i < n; i++ {
		c.Note(fmt.Sprintf("s%d", i), float64(1000+i))
	}
	if len(c.last) != reqClockSnapshotMaxSids {
		t.Fatalf("内存键数 = %d, want 有界 %d", len(c.last), reqClockSnapshotMaxSids)
	}
	if _, ok := c.Last("s0"); ok {
		t.Fatal("超限后最旧 sid 应按 ts 淘汰")
	}
	if ts, ok := c.Last(fmt.Sprintf("s%d", n-1)); !ok || ts != float64(1000+n-1) {
		t.Fatalf("最新 sid 应保留: %v,%v", ts, ok)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var snap struct {
		Sessions map[string]float64 `json:"sessions"`
	}
	if err := json.Unmarshal(b, &snap); err != nil {
		t.Fatal(err)
	}
	if len(snap.Sessions) != reqClockSnapshotMaxSids {
		t.Fatalf("快照键数 = %d, want 有界 %d", len(snap.Sessions), reqClockSnapshotMaxSids)
	}
}
