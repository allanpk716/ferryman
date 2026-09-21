package backtest

// 票01 测试：装载/还原/过滤/切分/计数的夹具与断言。
// 夹具全部经 accounts 包写路径写进 t.TempDir()（自构 window/usage JSONL，
// 与生产行格式逐字同形）；装载器只读。全部 ts 显式给定，不碰真实时钟。
// F4 锚点：断言数字全部来自夹具设计值，无任何规格文本时点快照（118/112 等）。

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"ferryman/internal/accounts"
	"ferryman/internal/prices"
)

// baseT = 2026-09-21 00:00 UTC（落 202609.jsonl）。
const baseT = 1789948800.0

func newAcc(t *testing.T, dir string) *accounts.Accounts {
	t.Helper()
	acc, err := accounts.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	return acc
}

func recWindow(t *testing.T, acc *accounts.Accounts, ts float64,
	session, project, reason string, dur float64, prefix int) {
	t.Helper()
	_, err := acc.Record("window", ts, accounts.Fields{
		"agent": "cc", "session_id": session, "lineage_id": "L-" + session,
		"project": project, "opened_ts": ts - dur, "closed_ts": ts,
		"dur_s": dur, "prefix_tokens": prefix, "close_reason": reason,
	})
	if err != nil {
		t.Fatal(err)
	}
}

func recUsage(t *testing.T, acc *accounts.Accounts, ts float64,
	session, project, subagent string) {
	t.Helper()
	_, err := acc.Record("usage", ts, accounts.Fields{
		"agent": "cc", "session_id": session, "lineage_id": "L-" + session,
		"project": project, "model": "m", "title": "",
		"input_tokens": 1, "cache_read_tokens": 0, "cache_creation_tokens": 0,
		"output_tokens": 1, "offset": 1, "subagent": subagent,
	})
	if err != nil {
		t.Fatal(err)
	}
}

// cacheBooks 单一价格表（p_cache 齐备，2026-01-01 起生效）：默认夹具全部可算。
func cacheBooks() map[string]prices.PriceBook {
	pc := 0.5
	return map[string]prices.PriceBook{"econ": {Key: "econ", Unit: "u", Per: 10000,
		Versions: []prices.PriceVersion{{EffectiveFrom: "2026-01-01",
			PIn: 1, PCache: &pc, POut: 2}}}}
}

func loadOpts(dir string) LoadOptions {
	return LoadOptions{DataDir: dir, Books: cacheBooks(), EconKey: "econ"}
}

func sessionIDs(ws []Window) []string {
	out := make([]string, len(ws))
	for i, w := range ws {
		out[i] = w.SessionID
	}
	return out
}

// ---- 装载计数与还原（验收 1） ----

func TestLoadCountsFixture(t *testing.T) {
	dir := t.TempDir()
	acc := newAcc(t, dir)
	recWindow(t, acc, baseT, "s1", "C:/work/alpha", "subagents_done", 600, 100000)
	recWindow(t, acc, baseT+100, "s2", "C:/work/beta", "prompt", 300, 80000)
	recWindow(t, acc, baseT+200, "s3", "", "subagents_done", 900, 120000) // 空 project → 经 usage 还原
	recWindow(t, acc, baseT+300, "s4", "", "expired", 1200, 40000)        // 空 project 无候选 → unknown
	recUsage(t, acc, baseT-50, "s3", "C:/work/gamma", "")                 // s3 主会话行
	recUsage(t, acc, baseT+10, "s5", "C:/elsewhere", "agent-a1")          // 无窗 usage：不进任何计数

	ds, err := Load(loadOpts(dir))
	if err != nil {
		t.Fatal(err)
	}
	c := ds.Counts
	if c.TotalWindows != 4 {
		t.Fatalf("TotalWindows = %d, want 4", c.TotalWindows)
	}
	if c.UnresolvedWindows != 1 {
		t.Fatalf("UnresolvedWindows = %d, want 1", c.UnresolvedWindows)
	}
	if c.MultiMatchSessions != 0 {
		t.Fatalf("MultiMatchSessions = %d, want 0", c.MultiMatchSessions)
	}
	if c.AfterFilterWindows != 4 {
		t.Fatalf("AfterFilterWindows = %d, want 4（默认全量，unknown 留存）", c.AfterFilterWindows)
	}
	if c.ReplayWindows != 3 || len(ds.Windows) != 3 {
		t.Fatalf("ReplayWindows = %d / len(Windows) = %d, want 3/3", c.ReplayWindows, len(ds.Windows))
	}
	if c.UnknownCount != 1 || len(ds.Unknown) != 1 {
		t.Fatalf("UnknownCount = %d / len(Unknown) = %d, want 1/1", c.UnknownCount, len(ds.Unknown))
	}
	if ds.Unknown[0].SessionID != "s4" || ds.Unknown[0].Project != UnknownProject {
		t.Fatalf("unknown 窗 = %+v, want s4/unknown", ds.Unknown[0])
	}
	if ds.Unknown[0].ProjectResolved {
		t.Fatal("unknown 窗 ProjectResolved = true, want false")
	}
	if c.UncomputableCount != 0 || len(ds.Uncomputable) != 0 {
		t.Fatalf("UncomputableCount = %d / len = %d, want 0/0", c.UncomputableCount, len(ds.Uncomputable))
	}
	wantBefore := map[string]int{"subagents_done": 2, "prompt": 1, "expired": 1}
	if !reflect.DeepEqual(c.CloseReasonBefore, wantBefore) {
		t.Fatalf("CloseReasonBefore = %v, want %v", c.CloseReasonBefore, wantBefore)
	}
	if !reflect.DeepEqual(c.CloseReasonAfter, wantBefore) {
		t.Fatalf("CloseReasonAfter = %v, want %v（默认全量两口径相同）", c.CloseReasonAfter, wantBefore)
	}
	if c.LoadedAt <= 0 || c.LoadedAtISO == "" {
		t.Fatalf("时点戳缺失: LoadedAt=%v LoadedAtISO=%q", c.LoadedAt, c.LoadedAtISO)
	}
	if _, err := time.Parse("2006-01-02T15:04:05-0700", c.LoadedAtISO); err != nil {
		t.Fatalf("LoadedAtISO 格式 = %q, %v", c.LoadedAtISO, err)
	}
	// 还原落位：s3 → gamma；窗按 OpenedTS 升序（s3 开窗最早 baseT-700）。
	if ds.Windows[0].SessionID != "s3" || ds.Windows[0].Project != "C:/work/gamma" {
		t.Fatalf("Windows[0] = %+v, want s3 还原 C:/work/gamma", ds.Windows[0])
	}
	if !ds.Windows[0].ProjectResolved {
		t.Fatal("还原窗 ProjectResolved = false, want true")
	}
	wantOrder := []string{"s3", "s1", "s2"}
	if !reflect.DeepEqual(sessionIDs(ds.Windows), wantOrder) {
		t.Fatalf("排序 = %v, want %v（OpenedTS 升序）", sessionIDs(ds.Windows), wantOrder)
	}
}

// ---- project 还原确定性三组（验收 2） ----

func TestRestorePriority(t *testing.T) {
	cases := []struct {
		name  string
		build func(t *testing.T, acc *accounts.Accounts, base float64)
		want  string
		multi int // MultiMatchSessions 期望
	}{
		{
			name: "主行优先于更晚的子代理行",
			build: func(t *testing.T, acc *accounts.Accounts, base float64) {
				recWindow(t, acc, base+200, "sx", "", "subagents_done", 100, 50000)
				recUsage(t, acc, base+100, "sx", "C:/x/sub", "agent-a1") // 子代理行更晚
				recUsage(t, acc, base, "sx", "C:/x/main", "")            // 主会话行更早
			},
			want:  "C:/x/main",
			multi: 1,
		},
		{
			name: "同级取时间最新",
			build: func(t *testing.T, acc *accounts.Accounts, base float64) {
				recWindow(t, acc, base+200, "sx", "", "subagents_done", 100, 50000)
				recUsage(t, acc, base, "sx", "C:/x/early", "agent-a1")
				recUsage(t, acc, base+100, "sx", "C:/x/late", "agent-a2")
			},
			want:  "C:/x/late",
			multi: 1,
		},
		{
			name: "平局字典序取最小",
			build: func(t *testing.T, acc *accounts.Accounts, base float64) {
				recWindow(t, acc, base+200, "sx", "", "subagents_done", 100, 50000)
				recUsage(t, acc, base, "sx", "C:/x/bbb", "agent-a1")
				recUsage(t, acc, base, "sx", "C:/x/aaa", "agent-a2")
			},
			want:  "C:/x/aaa",
			multi: 1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			acc := newAcc(t, dir)
			tc.build(t, acc, baseT)
			ds, err := Load(loadOpts(dir))
			if err != nil {
				t.Fatal(err)
			}
			if len(ds.Windows) != 1 {
				t.Fatalf("len(Windows) = %d, want 1", len(ds.Windows))
			}
			if got := ds.Windows[0].Project; got != tc.want {
				t.Fatalf("Project = %q, want %q", got, tc.want)
			}
			if ds.Counts.MultiMatchSessions != tc.multi {
				t.Fatalf("MultiMatchSessions = %d, want %d", ds.Counts.MultiMatchSessions, tc.multi)
			}
		})
	}
}

// ---- unknown 桶与过滤交互（验收 3） ----

func TestUnknownBucketFiltering(t *testing.T) {
	dir := t.TempDir()
	acc := newAcc(t, dir)
	recWindow(t, acc, baseT, "s1", "C:/work/alpha", "subagents_done", 100, 50000)
	recWindow(t, acc, baseT+100, "s2", "", "prompt", 100, 50000) // no-match

	// 默认全量：unknown 照登留存。
	ds, err := Load(loadOpts(dir))
	if err != nil {
		t.Fatal(err)
	}
	if ds.Counts.AfterFilterWindows != 2 || ds.Counts.UnknownCount != 1 {
		t.Fatalf("默认全量: after=%d unknown=%d, want 2/1",
			ds.Counts.AfterFilterWindows, ds.Counts.UnknownCount)
	}

	// --projects 正集：unknown 不被包含（剔除），未还原数（过滤前口径）不变。
	o := loadOpts(dir)
	o.Projects = []string{"C:/work/*"}
	ds, err = Load(o)
	if err != nil {
		t.Fatal(err)
	}
	if ds.Counts.AfterFilterWindows != 1 || ds.Counts.UnknownCount != 0 {
		t.Fatalf("--projects: after=%d unknown=%d, want 1/0",
			ds.Counts.AfterFilterWindows, ds.Counts.UnknownCount)
	}
	if ds.Counts.UnresolvedWindows != 1 {
		t.Fatalf("UnresolvedWindows = %d, want 1（过滤前口径）", ds.Counts.UnresolvedWindows)
	}
	if len(ds.Windows) != 1 || ds.Windows[0].SessionID != "s1" {
		t.Fatalf("Windows = %v, want 仅 s1", sessionIDs(ds.Windows))
	}

	// --exclude 命中 unknown 串：剔除。
	o = loadOpts(dir)
	o.Exclude = []string{"unknown"}
	ds, err = Load(o)
	if err != nil {
		t.Fatal(err)
	}
	if ds.Counts.AfterFilterWindows != 1 || ds.Counts.UnknownCount != 0 {
		t.Fatalf("--exclude unknown: after=%d unknown=%d, want 1/0",
			ds.Counts.AfterFilterWindows, ds.Counts.UnknownCount)
	}

	// --exclude 只排真实项目：unknown 仍在（排除集不波及）。
	o = loadOpts(dir)
	o.Exclude = []string{"C:/work/*"}
	ds, err = Load(o)
	if err != nil {
		t.Fatal(err)
	}
	if ds.Counts.AfterFilterWindows != 1 || ds.Counts.UnknownCount != 1 {
		t.Fatalf("--exclude 项目: after=%d unknown=%d, want 1/1",
			ds.Counts.AfterFilterWindows, ds.Counts.UnknownCount)
	}
	if len(ds.Windows) != 0 {
		t.Fatalf("Windows = %v, want 空", sessionIDs(ds.Windows))
	}
}

// ---- glob 过滤（验收 4） ----

func TestGlobFilters(t *testing.T) {
	dir := t.TempDir()
	acc := newAcc(t, dir)
	recWindow(t, acc, baseT, "s1", "C:/work/alpha", "subagents_done", 100, 50000)
	recWindow(t, acc, baseT+100, "s2", "C:/work/beta", "subagents_done", 100, 50000)
	recWindow(t, acc, baseT+200, "s3", "D:/other/gamma", "subagents_done", 100, 50000)

	cases := []struct {
		name     string
		projects []string
		exclude  []string
		want     []string
	}{
		{name: "正集跨段星号", projects: []string{"C:/work/*"}, want: []string{"s1", "s2"}},
		{name: "正集前缀星号", projects: []string{"C:/work/a*"}, want: []string{"s1"}},
		{name: "fnmatch 星号跨路径段", projects: []string{"*/work/*"}, want: []string{"s1", "s2"}},
		{name: "排除集压过正集", projects: []string{"C:/work/*"},
			exclude: []string{"*/beta"}, want: []string{"s1"}},
		{name: "仅排除集默认全量", exclude: []string{"D:/other/*"}, want: []string{"s1", "s2"}},
		{name: "正集大小写不敏感", projects: []string{"c:/WORK/ALPHA"}, want: []string{"s1"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o := loadOpts(dir)
			o.Projects, o.Exclude = tc.projects, tc.exclude
			ds, err := Load(o)
			if err != nil {
				t.Fatal(err)
			}
			if got := sessionIDs(ds.Windows); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("Windows = %v, want %v", got, tc.want)
			}
			if ds.Counts.AfterFilterWindows != len(tc.want) {
				t.Fatalf("AfterFilterWindows = %d, want %d",
					ds.Counts.AfterFilterWindows, len(tc.want))
			}
		})
	}
	// 过滤前后口径并存：正集一档 → before 3 / after 2。
	o := loadOpts(dir)
	o.Projects = []string{"C:/work/*"}
	ds, err := Load(o)
	if err != nil {
		t.Fatal(err)
	}
	if ds.Counts.TotalWindows != 3 || ds.Counts.AfterFilterWindows != 2 {
		t.Fatalf("before=%d after=%d, want 3/2",
			ds.Counts.TotalWindows, ds.Counts.AfterFilterWindows)
	}
	if ds.Counts.CloseReasonBefore["subagents_done"] != 3 ||
		ds.Counts.CloseReasonAfter["subagents_done"] != 2 {
		t.Fatalf("close_reason 双口径 = %v / %v, want 3 / 2",
			ds.Counts.CloseReasonBefore, ds.Counts.CloseReasonAfter)
	}
}

// ---- 时间对半切留出集（验收 5） ----

func TestHoldoutSplit(t *testing.T) {
	// 5 窗乱序写入：切分须按 OpenedTS 升序，前半 ceil(5/2)=3。
	dir := t.TempDir()
	acc := newAcc(t, dir)
	order := []float64{3, 0, 4, 1, 2} // 写入序故意乱
	for _, k := range order {
		ts := baseT + k*1000
		recWindow(t, acc, ts, fmt.Sprintf("s%d", int(k)), "C:/work/alpha",
			"subagents_done", 100, 50000)
	}
	ds, err := Load(loadOpts(dir))
	if err != nil {
		t.Fatal(err)
	}
	if n, m := len(ds.SelectHalf), len(ds.HoldoutHalf); n != 3 || m != 2 {
		t.Fatalf("halves = %d/%d, want 3/2", n, m)
	}
	if diff := len(ds.SelectHalf) - len(ds.HoldoutHalf); diff > 1 || diff < -1 {
		t.Fatalf("两半差 = %d, want ≤1", diff)
	}
	if got := sessionIDs(ds.SelectHalf); !reflect.DeepEqual(got, []string{"s0", "s1", "s2"}) {
		t.Fatalf("SelectHalf = %v, want 前半 s0,s1,s2", got)
	}
	if got := sessionIDs(ds.HoldoutHalf); !reflect.DeepEqual(got, []string{"s3", "s4"}) {
		t.Fatalf("HoldoutHalf = %v, want 后半 s3,s4", got)
	}

	// 偶数 4 窗：2/2。
	dir2 := t.TempDir()
	acc2 := newAcc(t, dir2)
	for k := 0; k < 4; k++ {
		recWindow(t, acc2, baseT+float64(k)*1000, fmt.Sprintf("s%d", k),
			"C:/work/alpha", "subagents_done", 100, 50000)
	}
	ds2, err := Load(loadOpts(dir2))
	if err != nil {
		t.Fatal(err)
	}
	if len(ds2.SelectHalf) != 2 || len(ds2.HoldoutHalf) != 2 {
		t.Fatalf("偶数 halves = %d/%d, want 2/2",
			len(ds2.SelectHalf), len(ds2.HoldoutHalf))
	}

	// 同输入两次装载切分逐元素一致（确定性锚点）。
	ds3, err := Load(loadOpts(dir))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(sessionIDs(ds.SelectHalf), sessionIDs(ds3.SelectHalf)) ||
		!reflect.DeepEqual(sessionIDs(ds.HoldoutHalf), sessionIDs(ds3.HoldoutHalf)) {
		t.Fatal("两次装载切分不一致")
	}
}

// ---- P_cache 缺省 → 不可算桶（验收 6） ----

func TestUncomputableBucket(t *testing.T) {
	dir := t.TempDir()
	acc := newAcc(t, dir)
	// 2026-01-02（无 p_cache 版本期）与 2026-02-01 后（有 p_cache 版本期）。
	janT := 1767225600.0 + 86400 // 2026-01-02 00:00 UTC
	febT := 1769904000.0 + 86400 // 2026-02-02 00:00 UTC
	recWindow(t, acc, janT, "sj", "C:/work/alpha", "subagents_done", 100, 50000)
	recWindow(t, acc, febT, "sf", "C:/work/alpha", "subagents_done", 100, 50000)

	pc := 0.5
	books := map[string]prices.PriceBook{"econ": {Key: "econ", Unit: "u", Per: 10000,
		Versions: []prices.PriceVersion{
			{EffectiveFrom: "2026-01-01", PIn: 1, POut: 2},              // PCache nil
			{EffectiveFrom: "2026-02-01", PIn: 1, PCache: &pc, POut: 2}, // PCache 齐
		}}}

	o := LoadOptions{DataDir: dir, Books: books, EconKey: "econ"}
	ds, err := Load(o)
	if err != nil {
		t.Fatal(err)
	}
	if ds.Counts.UncomputableCount != 1 || len(ds.Uncomputable) != 1 {
		t.Fatalf("UncomputableCount = %d / len = %d, want 1/1",
			ds.Counts.UncomputableCount, len(ds.Uncomputable))
	}
	if ds.Uncomputable[0].SessionID != "sj" {
		t.Fatalf("不可算窗 = %s, want sj（1 月无 p_cache 版本）", ds.Uncomputable[0].SessionID)
	}
	if ds.Counts.ReplayWindows != 1 || ds.Windows[0].SessionID != "sf" {
		t.Fatalf("可重放 = %v, want sf", sessionIDs(ds.Windows))
	}
	if ds.Counts.AfterFilterWindows != 2 {
		t.Fatalf("AfterFilterWindows = %d, want 2（不可算是价格侧桶，不是过滤）",
			ds.Counts.AfterFilterWindows)
	}

	// key 不命中且多本价格表 → 无可用品价格表 → 全部不可算（宁可不算，不造数）。
	o = LoadOptions{DataDir: dir, Books: map[string]prices.PriceBook{
		"econ": books["econ"], "other": cacheBooks()["econ"],
	}, EconKey: "nope"}
	ds, err = Load(o)
	if err != nil {
		t.Fatal(err)
	}
	if ds.Counts.UncomputableCount != 2 || ds.Counts.ReplayWindows != 0 {
		t.Fatalf("无书: uncomputable=%d replay=%d, want 2/0",
			ds.Counts.UncomputableCount, ds.Counts.ReplayWindows)
	}

	// 单本兜底（EconKey 空 → 唯一本）：与首例同判。
	o = LoadOptions{DataDir: dir, Books: books, EconKey: ""}
	ds, err = Load(o)
	if err != nil {
		t.Fatal(err)
	}
	if ds.Counts.UncomputableCount != 1 || ds.Counts.ReplayWindows != 1 {
		t.Fatalf("单本兜底: uncomputable=%d replay=%d, want 1/1",
			ds.Counts.UncomputableCount, ds.Counts.ReplayWindows)
	}
}

// ---- 脏行与缺失目录（装载鲁棒性） ----

func TestBadLineSkippedAndMissingDir(t *testing.T) {
	dir := t.TempDir()
	acc := newAcc(t, dir)
	recWindow(t, acc, baseT, "s1", "C:/work/alpha", "subagents_done", 100, 50000)
	// 追加一行坏 JSON（截断行），装载须跳过不炸、计数不受污染。
	f := filepath.Join(dir, "accounts", "202609.jsonl")
	fh, err := os.OpenFile(f, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fh.WriteString("{\"v\":1,\"kind\":\"window\",\"ts\":0\n"); err != nil {
		t.Fatal(err)
	}
	fh.Close()

	ds, err := Load(loadOpts(dir))
	if err != nil {
		t.Fatal(err)
	}
	if ds.Counts.TotalWindows != 1 {
		t.Fatalf("TotalWindows = %d, want 1（坏行跳过）", ds.Counts.TotalWindows)
	}

	if _, err := Load(LoadOptions{DataDir: filepath.Join(dir, "nope")}); err == nil {
		t.Fatal("缺失账本目录应报错")
	}
}

// ---- 网格点 tie-break 契约（spec：并列按 τ→首跳→cap 字典序取最小） ----

func TestCompareGridPoint(t *testing.T) {
	inf := math.Inf(1)
	small := GridPoint{TauS: 10, FirstBeatS: 10, CapS: 100}
	bigTau := GridPoint{TauS: 20, FirstBeatS: 0, CapS: 0}
	if CompareGridPoint(small, bigTau) >= 0 {
		t.Fatal("τ 小者应排前")
	}
	lateBeat := GridPoint{TauS: 10, FirstBeatS: 20, CapS: 0}
	if CompareGridPoint(small, lateBeat) >= 0 {
		t.Fatal("首跳早者应排前")
	}
	bigCap := GridPoint{TauS: 10, FirstBeatS: 10, CapS: inf}
	if CompareGridPoint(small, bigCap) >= 0 {
		t.Fatal("cap 小者应排前（∞ 排最后）")
	}
	if CompareGridPoint(small, small) != 0 {
		t.Fatal("同参数组应判 0")
	}
}
