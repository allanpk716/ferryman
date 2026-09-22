// 票06 测试：闲置/摆渡事件装载的夹具与断言。
// 夹具经 accounts 包写路径写进 t.TempDir()（与生产行格式逐字同形）；
// 装载器只读。全部 ts 显式给定，不碰真实时钟；断言数字全部来自夹具设计值。
//
// 夹具时间线（分钟自 baseT 起）：
//
//	s1(P1): usage 0 → usage 10 → usage 50 → handoff 80（span 返回10/返回40/摆渡30）
//	s2(P2): usage 2 → handoff 28（span 摆渡 26 分钟，死亡侧）
//	s3(P3): usage 4（单标记 → 截断 span，至数据末端 80 分钟处）
//	s4(P4): 仅 handoff 50（孤儿 handoff，不成 span）
//	s5(空): usage 6 空 project 无候选 → unknown 桶（span 截断）
//	s6(P6): 仅 handoff（baseT−3000000，窗外语义用）
package backtest

import (
	"math"
	"testing"
	"time"

	"ferryman/internal/accounts"
	"ferryman/internal/prices"
)

// recUsageTok 带 token 规模的 usage 行（S = input + cache_read + cache_creation）。
func recUsageTok(t *testing.T, acc *accounts.Accounts, ts float64,
	session, project, subagent string, input, cacheRead, cacheCreation int) {
	t.Helper()
	_, err := acc.Record("usage", ts, accounts.Fields{
		"agent": "cc", "session_id": session, "lineage_id": "L-" + session,
		"project": project, "model": "m", "title": "",
		"input_tokens": input, "cache_read_tokens": cacheRead,
		"cache_creation_tokens": cacheCreation, "output_tokens": 1,
		"offset": 1, "subagent": subagent,
	})
	if err != nil {
		t.Fatal(err)
	}
}

// recHandoff handoff 行（price_ver 钉死 econ@2026-01-01）。
func recHandoff(t *testing.T, acc *accounts.Accounts, ts float64,
	session, project string, prompt, completion int) {
	t.Helper()
	_, err := acc.Record("handoff", ts, accounts.Fields{
		"agent": "cc", "session_id": session, "lineage_id": "L-" + session,
		"project": project, "provider": "third", "model": "flash",
		"price_ver": "econ@2026-01-01", "prompt_tokens": prompt,
		"completion_tokens": completion, "outcome": "ok", "wall_s": 1.5,
	})
	if err != nil {
		t.Fatal(err)
	}
}

// idleBooks 单版本价格表（同 cacheBooks 价：PIn 6.9 / PCache 1.7 / POut 24，
// per=10000）——handoff 行事实定价 = 15×6.9 + 0.1×24 = 105.9（prompt 150000 /
// completion 1000 时）。
func idleBooks() map[string]prices.PriceBook {
	return map[string]prices.PriceBook{"econ": {Key: "econ", Unit: "u", Per: 10000,
		Versions: []prices.PriceVersion{{EffectiveFrom: "2026-01-01",
			PIn: 6.9, PCache: fptr(1.7), POut: 24}}}}
}

// idleFixture 标准六会话夹具（时间线见文件头注释），返回账本目录。
func idleFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	acc := newAcc(t, dir)
	// s1：返回10 / 返回40 / 摆渡30 三段
	recUsageTok(t, acc, baseT, "s1", "P1", "", 100000, 40000, 10000)
	recUsageTok(t, acc, baseT+600, "s1", "P1", "", 100000, 40000, 10000)
	recUsageTok(t, acc, baseT+3000, "s1", "P1", "", 100000, 40000, 10000)
	recHandoff(t, acc, baseT+4800, "s1", "P1", 150000, 1000)
	// s2：摆渡 26 分钟（死亡侧）
	recUsageTok(t, acc, baseT+120, "s2", "P2", "", 150000, 0, 0)
	recHandoff(t, acc, baseT+120+1560, "s2", "P2", 150000, 1000)
	// s3：单标记 → 截断
	recUsageTok(t, acc, baseT+240, "s3", "P3", "", 150000, 0, 0)
	// s4：孤儿 handoff
	recHandoff(t, acc, baseT+3000, "s4", "P4", 150000, 1000)
	// s5：空 project 无候选 → unknown
	recUsageTok(t, acc, baseT+360, "s5", "", "", 150000, 0, 0)
	// s6：窗外孤儿 handoff（默认 30 天窗外）
	recHandoff(t, acc, baseT-3000000, "s6", "P6", 150000, 1000)
	return dir
}

func approxF(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

// ---- 装载：span 构造 / 结局分类 / 计数 ----

func TestLoadIdleSpans(t *testing.T) {
	dir := idleFixture(t)
	ds, err := LoadIdle(IdleLoadOptions{DataDir: dir, Books: idleBooks(),
		Now: func() float64 { return baseT + 9000 }})
	if err != nil {
		t.Fatal(err)
	}
	c := ds.Counts
	if c.TotalHandoffRows != 4 {
		t.Fatalf("TotalHandoffRows = %d, want 4", c.TotalHandoffRows)
	}
	if c.TotalSpans != 6 {
		t.Fatalf("TotalSpans = %d, want 6（s1×3 + s2 + s3 + s5；s4/s6 孤儿不成 span）", c.TotalSpans)
	}
	if c.ReturnedSpans != 2 || c.FerrySpans != 2 || c.CensoredSpans != 2 {
		t.Fatalf("结局计数 = 返回%d/摆渡%d/截断%d, want 2/2/2（截断含 unknown 桶 s5）",
			c.ReturnedSpans, c.FerrySpans, c.CensoredSpans)
	}
	if c.OrphanHandoffRows != 2 {
		t.Fatalf("OrphanHandoffRows = %d, want 2（s4 + s6）", c.OrphanHandoffRows)
	}
	if c.Sessions != 6 {
		t.Fatalf("Sessions = %d, want 6", c.Sessions)
	}
	// 末端 = 全部行最大 ts = baseT+4800（s6 在更早处）
	if !approxF(c.HorizonTS, baseT+4800) {
		t.Fatalf("HorizonTS = %v, want %v", c.HorizonTS, baseT+4800)
	}
	// 滚动窗摆渡事件：默认 30 天窗 = [horizon−2592000, horizon]，s6 窗外 → 3
	if c.WindowDays != 30 {
		t.Fatalf("WindowDays 缺省 = %d, want 30", c.WindowDays)
	}
	if c.FerryEventsInWindow != 3 {
		t.Fatalf("FerryEventsInWindow = %d, want 3（s6 窗外不计）", c.FerryEventsInWindow)
	}
	// unknown 桶：s5
	if len(ds.Unknown) != 1 || ds.Unknown[0].SessionID != "s5" {
		t.Fatalf("Unknown = %+v, want [s5]", ds.Unknown)
	}
	if len(ds.Events) != 5 {
		t.Fatalf("Events = %d 条, want 5", len(ds.Events))
	}
	// 排序：StartTS 升序。首条 = s1 @0（返回 10 分钟，前缀取闭窗标记 150000）。
	first := ds.Events[0]
	if first.SessionID != "s1" || first.Outcome != IdleReturned || !approxF(first.IdleMin, 10) {
		t.Fatalf("Events[0] = %+v, want s1/returned/10min", first)
	}
	if first.PrefixTokens != 150000 {
		t.Fatalf("Events[0].PrefixTokens = %d, want 150000（input+cache_read+cache_creation）", first.PrefixTokens)
	}
	// 摆渡 span：s2 d=26（死亡侧），前缀取 handoff prompt_tokens，事实价可算。
	var s2ev *IdleEvent
	for i := range ds.Events {
		if ds.Events[i].SessionID == "s2" {
			s2ev = &ds.Events[i]
		}
	}
	if s2ev == nil || s2ev.Outcome != IdleFerry || !approxF(s2ev.IdleMin, 26) {
		t.Fatalf("s2 事件 = %+v, want ferry/26min", s2ev)
	}
	if s2ev.PrefixTokens != 150000 || !s2ev.ActualPriced {
		t.Fatalf("s2 前缀/定价 = %d/%v, want 150000/true", s2ev.PrefixTokens, s2ev.ActualPriced)
	}
	if !approxF(s2ev.ActualFerryCost, 105.9) {
		t.Fatalf("s2 事实摆渡价 = %v, want 105.9（15×6.9 + 0.1×24）", s2ev.ActualFerryCost)
	}
	// s1 尾段摆渡 span：50→80 分钟，d=30。
	var s1tail *IdleEvent
	for i := range ds.Events {
		e := &ds.Events[i]
		if e.SessionID == "s1" && e.Outcome == IdleFerry {
			s1tail = e
		}
	}
	if s1tail == nil || !approxF(s1tail.IdleMin, 30) {
		t.Fatalf("s1 尾段 = %+v, want ferry/30min", s1tail)
	}
	// 截断 span：s3 至末端 76 分钟，不入评分总体但在 Events 留存。
	var s3ev *IdleEvent
	for i := range ds.Events {
		if ds.Events[i].SessionID == "s3" {
			s3ev = &ds.Events[i]
		}
	}
	if s3ev == nil || s3ev.Outcome != IdleCensored || !approxF(s3ev.IdleMin, 76) {
		t.Fatalf("s3 事件 = %+v, want censored/76min", s3ev)
	}
	// 装载时点戳注入生效。
	if !approxF(c.LoadedAt, baseT+9000) || c.LoadedAtISO == "" {
		t.Fatalf("时点戳 = %v/%q, want 注入值", c.LoadedAt, c.LoadedAtISO)
	}
	if _, err := time.Parse("2006-01-02T15:04:05-0700", c.LoadedAtISO); err != nil {
		t.Fatalf("LoadedAtISO 格式 = %q, %v", c.LoadedAtISO, err)
	}
}

// ---- 装载：项目 glob 过滤与窗口宽度 ----

func TestLoadIdleFilterAndWindow(t *testing.T) {
	dir := idleFixture(t)
	// 正集 P1：只留 s1 的 3 段；窗内摆渡事件也按过滤后口径 = 1。
	ds, err := LoadIdle(IdleLoadOptions{DataDir: dir, Books: idleBooks(),
		Projects: []string{"P1"},
		Now:      func() float64 { return baseT + 9000 }})
	if err != nil {
		t.Fatal(err)
	}
	if len(ds.Events) != 3 {
		t.Fatalf("P1 过滤后 Events = %d, want 3", len(ds.Events))
	}
	if len(ds.Unknown) != 0 {
		t.Fatalf("正集模式下 unknown 不留存, got %d", len(ds.Unknown))
	}
	if ds.Counts.FerryEventsInWindow != 1 {
		t.Fatalf("过滤后窗内摆渡事件 = %d, want 1", ds.Counts.FerryEventsInWindow)
	}

	// 显式 40 天窗：s6 入窗 → 4。
	ds40, err := LoadIdle(IdleLoadOptions{DataDir: dir, Books: idleBooks(),
		WindowDays: 40, Now: func() float64 { return baseT + 9000 }})
	if err != nil {
		t.Fatal(err)
	}
	if ds40.Counts.FerryEventsInWindow != 4 {
		t.Fatalf("40 天窗摆渡事件 = %d, want 4", ds40.Counts.FerryEventsInWindow)
	}
	if ds40.Counts.WindowDays != 40 {
		t.Fatalf("WindowDays = %d, want 40", ds40.Counts.WindowDays)
	}
}

// ---- 装载：无 handoff 科目的账本（gate=0 不炸）与目录缺失硬错 ----

func TestLoadIdleEmptyAndMissing(t *testing.T) {
	dir := t.TempDir()
	acc := newAcc(t, dir)
	recUsageTok(t, acc, baseT, "s1", "P1", "", 100000, 0, 0)
	recUsageTok(t, acc, baseT+300, "s2", "P1", "", 100000, 0, 0) // 抬高数据末端
	ds, err := LoadIdle(IdleLoadOptions{DataDir: dir, Books: idleBooks(),
		Now: func() float64 { return baseT + 60 }})
	if err != nil {
		t.Fatal(err)
	}
	if ds.Counts.FerryEventsInWindow != 0 || ds.Counts.FerrySpans != 0 {
		t.Fatalf("无 handoff 应 gate=0/摆渡 span=0, got %+v", ds.Counts)
	}
	// s1 截断 span [0, 5min]；s2 末端零长截断 = 退化，不入 Events。
	if len(ds.Events) != 1 || ds.Events[0].Outcome != IdleCensored ||
		!approxF(ds.Events[0].IdleMin, 5) {
		t.Fatalf("Events = %+v, want 1 条截断 5min", ds.Events)
	}
	if ds.Counts.DegenerateSpans != 1 {
		t.Fatalf("DegenerateSpans = %d, want 1（s2 末端零长）", ds.Counts.DegenerateSpans)
	}
	if _, err := LoadIdle(IdleLoadOptions{DataDir: t.TempDir() + "/-absent"}); err == nil {
		t.Fatal("目录缺失应硬错")
	}
}

// ---- 装载：确定性（同输入两次装载逐元素一致） ----

func TestLoadIdleDeterministic(t *testing.T) {
	dir := idleFixture(t)
	opts := IdleLoadOptions{DataDir: dir, Books: idleBooks(),
		Now: func() float64 { return baseT + 9000 }}
	a, err := LoadIdle(opts)
	if err != nil {
		t.Fatal(err)
	}
	b, err := LoadIdle(opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Events) != len(b.Events) || len(a.Unknown) != len(b.Unknown) {
		t.Fatal("两次装载条数不一致")
	}
	for i := range a.Events {
		if a.Events[i] != b.Events[i] {
			t.Fatalf("Events[%d] 不一致: %+v vs %+v", i, a.Events[i], b.Events[i])
		}
	}
}
