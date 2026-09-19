package ledger

// 规格：tests/test_ledger.py 全部 3 例 1:1（T05 ledger）+ test_subagent.py 的
// 台账级子代理计数 3 例 1:1（T32：生命周期/嵌套/泄漏防护）+ 票面补充钉子
// （Touch 覆盖分支、qwatch 清窗、并发读写）。时间纪律：计数时刻取 clock.Now
// （包级可注入）；泄漏防护用例同 Python 一样直改私有 map 伪造最后事件时刻。

import (
	"fmt"
	"sync"
	"testing"

	"ferryman/internal/clock"
)

// ---- tests/test_ledger.py 1:1 ----

func TestLineageSamePathNewSessionID(t *testing.T) {
	led := New()
	st1 := led.TouchFull("cc", "sid-1", "C:\\proj\\a.jsonl", 1000, 10, "C:\\proj", "旧标题", 0, 0)
	if !st1.ObservedActive { // mtime ≥ started
		t.Fatal("st1.observed_active 应为 true")
	}
	st2 := led.Touch("cc", "sid-2", "C:/proj/A.jsonl", 900, 20, 0) // 同路径（大小写/斜杠不同）+ 新 sid
	if st2.SessionID != "sid-2" {
		t.Fatalf("session_id = %q, want sid-2", st2.SessionID)
	}
	if st2.LastWrite != 1000 { // 继承闲置史
		t.Fatalf("last_write = %v, want 1000（继承闲置史）", st2.LastWrite)
	}
	if st2.Cwd != "C:\\proj" || st2.Title != "旧标题" {
		t.Fatalf("lineage 继承 cwd/title = %q/%q, want C:\\proj/旧标题", st2.Cwd, st2.Title)
	}
	if led.GetByPath("c:/PROJ\\a.jsonl") != st2 { // 归一化键指向最新
		t.Fatal("get_by_path 应指向最新状态（指针同一）")
	}
}

func TestLookbackZeroInactiveBeforeStart(t *testing.T) {
	led := New()
	st := led.Touch("cc", "old", "C:\\proj\\old.jsonl", 10, 1, 50)
	if st.ObservedActive { // mtime < started → 不算启动后活动
		t.Fatal("mtime < started 应为 false")
	}
	st2 := led.Touch("cc", "old", "C:\\proj\\old.jsonl", 60, 1, 50)
	if !st2.ObservedActive { // 之后有写入才激活
		t.Fatal("mtime ≥ started 应为 true")
	}
}

func TestLastWriteMonotonicAndLastTranscriptWrite(t *testing.T) {
	led := New()
	led.Touch("cc", "a", "C:\\p\\a.jsonl", 100, 1, 0)
	led.Touch("cc", "a", "C:\\p\\a.jsonl", 50, 1, 0) // 旧 mtime 不回退
	if got := led.Get("cc", "a").LastWrite; got != 100 {
		t.Fatalf("last_write = %v, want 100", got)
	}
	led.Touch("cc", "b", "C:\\p\\b.jsonl", 200, 1, 0)
	if got := led.LastTranscriptWrite(); got != 200 {
		t.Fatalf("last_transcript_write = %v, want 200", got)
	}
}

// ---- tests/test_subagent.py 台账级 3 例 1:1 ----

func TestSubagentCountLifecycle(t *testing.T) {
	led := New()
	if led.SubagentActive("cc", "s1") { // 未知会话不炸
		t.Fatal("未知会话应为 false")
	}
	if got := led.SubagentEvent("cc", "s1", "start"); got != 1 {
		t.Fatalf("start 返回 = %d, want 1", got)
	}
	if !led.SubagentActive("cc", "s1") {
		t.Fatal("start 后应为 true")
	}
	if got := led.SubagentEvent("cc", "s1", "stop"); got != 0 {
		t.Fatalf("stop 返回 = %d, want 0", got)
	}
	if led.SubagentActive("cc", "s1") {
		t.Fatal("stop 后应为 false")
	}
	led.SubagentEvent("cc", "s1", "stop") // 重复 stop（重启丢 start）→ 钳 0
	if led.SubagentActive("cc", "s1") {
		t.Fatal("重复 stop 应钳 0")
	}
}

func TestSubagentNestedTwoLevels(t *testing.T) {
	// 探针实测：嵌套两层各自 Start/Stop，同属主会话 → 纯计数即可，无需 parent 链。
	led := New()
	led.SubagentEvent("cc", "s1", "start") // 外层
	led.SubagentEvent("cc", "s1", "start") // 内层
	if !led.SubagentActive("cc", "s1") {
		t.Fatal("双层 start 应为 true")
	}
	led.SubagentEvent("cc", "s1", "stop") // 内层先停
	if !led.SubagentActive("cc", "s1") {  // 外层仍在
		t.Fatal("还剩外层应为 true")
	}
	led.SubagentEvent("cc", "s1", "stop")
	if led.SubagentActive("cc", "s1") {
		t.Fatal("双层 stop 应为 false")
	}
}

func TestSubagentLeakGuard(t *testing.T) {
	// Stop 丢失（CC 崩溃/强杀会话）→ 计数不得永久卡死摆渡：1h 无新事件视为 0。
	led := New()
	led.SubagentEvent("cc", "s1", "start")
	led.subagents[[2]string{"cc", "s1"}] = subEnt{count: 1, last: clock.Now() - 3601} // 伪造最后事件在 1h 前
	if led.SubagentActive("cc", "s1") {
		t.Fatal("1h 无新事件应视为 0")
	}
	if _, ok := led.subagents[[2]string{"cc", "s1"}]; ok { // 判定即清理
		t.Fatal("判死后条目应删除")
	}
}

func TestSubagentsActiveCountSumAndLeakCutoff(t *testing.T) {
	// 求和与泄漏同口径：过期条目不计；嵌套计数累加。
	led := New()
	led.SubagentEvent("cc", "s1", "start")
	led.SubagentEvent("cc", "s1", "start") // 嵌套：count=2
	led.SubagentEvent("codex", "s2", "start")
	led.subagents[[2]string{"cc", "stale"}] = subEnt{count: 5, last: clock.Now() - 3601}
	if got := led.SubagentsActiveCount(); got != 3 {
		t.Fatalf("subagents_active = %d, want 3（过期条目不计）", got)
	}
}

// ---- 票面补充钉子：Touch 覆盖分支 ----

func TestTouchOverwriteBranches(t *testing.T) {
	led := New()
	st := led.TouchFull("cc", "s1", "C:\\p\\a.jsonl", 100, 10, "C:\\p", "标题", 500, 2000)
	if st.EnrichedWrite != -1 {
		t.Fatalf("enriched_write 初值 = %v, want -1", st.EnrichedWrite)
	}
	if st.ObservedActive { // mtime(100) < started(2000)
		t.Fatal("mtime < started 不激活")
	}
	// size=0 / 空 cwd / 空 title / peak_ctx=0 一律不覆盖（Python size or st.size 等）
	led.TouchFull("cc", "s1", "C:\\p\\a.jsonl", 100, 0, "", "", 0, 0)
	if st.Size != 10 || st.Cwd != "C:\\p" || st.Title != "标题" || st.PeakCtx != 500 {
		t.Fatalf("零值不得覆盖: %+v", st)
	}
	// mtime == last_write：严格大于才推进 → 不置 observed_active
	led.Touch("cc", "s1", "C:\\p\\a.jsonl", 100, 10, 0)
	if st.ObservedActive {
		t.Fatal("mtime 未推进不得置 observed_active")
	}
	// 非零值照常覆盖 + mtime 推进置位
	led.TouchFull("cc", "s1", "C:\\p\\a.jsonl", 110, 20, "C:\\p2", "标题2", 600, 0)
	if st.LastWrite != 110 || st.Size != 20 || st.Cwd != "C:\\p2" ||
		st.Title != "标题2" || st.PeakCtx != 600 || !st.ObservedActive {
		t.Fatalf("非零值应覆盖且推进置位: %+v", st)
	}
}

// ---- 票面补充钉子：qwatch 清窗 ----

func TestTouchNewWriteClearsQWatchWindow(t *testing.T) {
	led := New()
	st := led.Touch("cc", "s1", "C:\\p\\a.jsonl", 100, 10, 0)
	// 开窗（生产者=daemon 开窗临界区持锁直改；测试同法造窗）
	ts := 50.0
	st.QWatchOpenedTS = &ts
	st.QWatchBeatsFired = 2
	st.QWatchPlan = []float64{60, 70}
	st.QWatchSnapshot = &QSnap{MTime: 100, Size: 10}

	// 旧 mtime 不推进 → 不关窗（四件全保留）
	led.Touch("cc", "s1", "C:\\p\\a.jsonl", 90, 10, 0)
	if st.QWatchOpenedTS == nil || st.QWatchBeatsFired != 2 ||
		len(st.QWatchPlan) != 2 || st.QWatchSnapshot == nil {
		t.Fatalf("mtime 未推进不得清窗: %+v", st)
	}
	// 新写入（mtime > last_write）→ 四字段清空（任何新写入=用户已作答）
	led.Touch("cc", "s1", "C:\\p\\a.jsonl", 120, 10, 0)
	if st.QWatchOpenedTS != nil || st.QWatchBeatsFired != 0 ||
		len(st.QWatchPlan) != 0 || st.QWatchSnapshot != nil {
		t.Fatalf("新写入应清窗: %+v", st)
	}
}

// ---- 票面验收：并发读写（goroutine + WaitGroup；-race 下验证共享引用纪律） ----

func TestConcurrentTouchGetNoRace(t *testing.T) {
	led := New()
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				n := g*200 + i
				sid := fmt.Sprintf("s%d", n%40)
				path := fmt.Sprintf("C:\\p\\%d.jsonl", n%40)
				st := led.Touch("cc", sid, path, float64(1000+n), n, 0)
				_ = led.Get("cc", sid)
				_ = led.GetByPath(path)
				switch n % 5 {
				case 0:
					led.SubagentEvent("cc", sid, "start")
				case 1:
					led.SubagentEvent("cc", sid, "stop")
				case 2:
					led.MarkHandedOff(st) // 共享引用写：走自带锁
				case 3:
					_ = led.SubagentActive("cc", sid)
					_ = led.SubagentsActiveCount()
				case 4:
					for _, s := range led.AllSessions() { // 元素为共享引用：不读字段外泄
						_ = s.SessionID
					}
					_ = led.LastTranscriptWrite()
				}
			}
		}(g)
	}
	wg.Wait()
}
