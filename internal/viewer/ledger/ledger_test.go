package ledger

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// loadOne 把 testdata 下单个账本文件拷进独立临时目录后 Load——
// Load(dir) 读目录下全部 *.jsonl，拷贝隔离保证各用例互不串扰。
func loadOne(t *testing.T, name string) ([]Entry, error) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("读 testdata/%s: %v", name, err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, name), raw, 0o644); err != nil {
		t.Fatalf("拷贝 %s 到临时目录: %v", name, err)
	}
	return Load(dir)
}

func TestLoadSample(t *testing.T) {
	entries, err := loadOne(t, "sample.jsonl")
	if err != nil {
		t.Fatalf("Load(sample): %v", err)
	}
	// len：18 行基础结构 + 1 行 inject（审查 Important 防回潮补入）+ 1 行 beat
	// observe 样例（T51 票04 补注：outcome 形态样例行）
	if len(entries) != 20 {
		t.Fatalf("Load(sample) 条数 = %d, want 20", len(entries))
	}

	// kind 分布：3 个 lineage 各含 usage×3/2/1 + window/handoff/beat/block×1，
	// 另 inject×1、beat observe 样例×1
	wantDist := map[string]int{"usage": 6, "window": 3, "handoff": 3, "beat": 4, "block": 3, "inject": 1}
	gotDist := map[string]int{}
	for _, e := range entries {
		gotDist[e.Kind]++
	}
	if !mapEqual(gotDist, wantDist) {
		t.Fatalf("kind 分布 = %v, want %v", gotDist, wantDist)
	}

	// handoff/beat/inject 特有键必须能读出——防 json tag 再漏键静默丢数据。
	// beat 行按 ts 序给 outcome/cache_read 期望（T51 票04：hit 布尔改 outcome
	// 三态+observe，四种形态在夹具各现一例）。
	wantOutcome := map[string][]string{
		"lin-alpha": {"hit"},
		"lin-beta":  {"miss"},
		"lin-gamma": {"error", "observe"},
	}
	wantCacheRead := map[string][]int64{
		"lin-alpha": {410},
		"lin-beta":  {2510},
		"lin-gamma": {96, 0},
	}
	for _, e := range entries {
		switch e.Kind {
		case "handoff":
			if e.PriceVer != "v2026-09" {
				t.Errorf("%s handoff PriceVer = %q, want v2026-09", e.LineageID, e.PriceVer)
			}
		case "beat":
			if e.PriceVer != "v2026-09" && e.Outcome != "observe" {
				t.Errorf("%s beat PriceVer = %q, want v2026-09", e.LineageID, e.PriceVer)
			}
		case "inject":
			if e.HandoffID != "ho-alpha-1" || e.Tokens != 999 {
				t.Errorf("inject 行 HandoffID/Tokens = (%q, %d), want (ho-alpha-1, 999)", e.HandoffID, e.Tokens)
			}
		}
	}
	// beat outcome/cache_read 逐行核对（文件序=ts 序，按 lineage 聚集后比对）
	gotBeat := map[string][]Entry{}
	for _, e := range entries {
		if e.Kind == "beat" {
			gotBeat[e.LineageID] = append(gotBeat[e.LineageID], e)
		}
	}
	for lin, want := range wantOutcome {
		got := gotBeat[lin]
		if len(got) != len(want) {
			t.Fatalf("%s beat 行数 = %d, want %d", lin, len(got), len(want))
		}
		for i, e := range got {
			if e.Outcome != want[i] {
				t.Errorf("%s beat[%d] Outcome = %q, want %q", lin, i, e.Outcome, want[i])
			}
			if e.CacheRead != wantCacheRead[lin][i] {
				t.Errorf("%s beat[%d] CacheRead = %d, want %d", lin, i, e.CacheRead, wantCacheRead[lin][i])
			}
		}
	}

	// 抽查一行：字段逐键落位（json tag 与 accounts.py 一致）
	var win *Entry
	for i := range entries {
		if entries[i].Kind == "window" && entries[i].LineageID == "lin-alpha" {
			win = &entries[i]
		}
	}
	if win == nil {
		t.Fatal("未找到 lin-alpha 的 window 行")
	}
	want := Entry{
		V: 1, Kind: "window", Ts: 1780000000.4, TsISO: "2026-09-18T10:00:03+0800",
		Agent: "cc", SessionID: "sess-alpha", LineageID: "lin-alpha", Project: "Ferryman",
		OpenedTS: 1780000000, ClosedTS: 1780000060, DurS: 60,
		PrefixTokens: 500, CloseReason: "idle",
	}
	if *win != want {
		t.Fatalf("window 行解码不符:\n got %+v\nwant %+v", *win, want)
	}
}

func TestLoadCorrupt(t *testing.T) {
	entries, err := loadOne(t, "corrupt.jsonl")
	if err != nil {
		t.Fatalf("Load(corrupt) 不应报错: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("Load(corrupt) 条数 = %d, want 2（损坏行+空行跳过）", len(entries))
	}
	if entries[0].LineageID != "lin-delta" || entries[1].LineageID != "lin-delta" {
		t.Fatalf("合法行 lineage = %q, %q, want lin-delta", entries[0].LineageID, entries[1].LineageID)
	}
}

func TestSummarize(t *testing.T) {
	entries, err := loadOne(t, "sample.jsonl")
	if err != nil {
		t.Fatalf("Load(sample): %v", err)
	}
	sums := Summarize(entries)
	if len(sums) != 3 {
		t.Fatalf("Summarize 行数 = %d, want 3", len(sums))
	}

	// LastTS 倒序：lin-gamma → lin-beta → lin-alpha
	wantOrder := []string{"lin-gamma", "lin-beta", "lin-alpha"}
	for i, want := range wantOrder {
		if sums[i].LineageID != want {
			t.Fatalf("sums[%d].LineageID = %q, want %q", i, sums[i].LineageID, want)
		}
	}

	// mainTok/subTok 列（票02）：主/子桶口径=input+cache_read+creation（不含
	// output，与列表页 tokens 列一致）。sample 无 subagent 行 → 子桶恒 0、
	// 主桶=既有四列里三列之和（既有字段零回归锚点）。
	cases := []struct {
		lin      string
		title    string
		agents   string
		firstTS  float64
		lastTS   float64
		input    int64
		cacheRd  int64
		creation int64
		output   int64
		requests int
		mainTok  int64
		subTok   int64
	}{
		{"lin-gamma", "Gamma 会话", "cc", 1780002000.1, 1780002000.5, 10, 1, 0, 5, 1, 11, 0},
		{"lin-beta", "Beta 会话", "cc,codex", 1780001000.1, 1780001000.6, 3000, 300, 150, 600, 2, 3450, 0},
		{"lin-alpha", "Alpha 会话", "cc", 1780000000.1, 1780000000.7, 600, 60, 30, 180, 3, 690, 0},
	}
	for _, tc := range cases {
		var s *SessionSummary
		for i := range sums {
			if sums[i].LineageID == tc.lin {
				s = &sums[i]
			}
		}
		if s == nil {
			t.Fatalf("缺 lineage %s 的汇总", tc.lin)
		}
		if s.Title != tc.title {
			t.Errorf("%s Title = %q, want %q（最后一个非空）", tc.lin, s.Title, tc.title)
		}
		if s.Agents != tc.agents {
			t.Errorf("%s Agents = %q, want %q（去重逗号连缀）", tc.lin, s.Agents, tc.agents)
		}
		if s.FirstTS != tc.firstTS || s.LastTS != tc.lastTS {
			t.Errorf("%s TS 区间 = (%v, %v), want (%v, %v)", tc.lin, s.FirstTS, s.LastTS, tc.firstTS, tc.lastTS)
		}
		if s.Input != tc.input || s.CacheRead != tc.cacheRd || s.Creation != tc.creation || s.Output != tc.output {
			t.Errorf("%s token 汇总 = (in %d, rd %d, cr %d, out %d), want (in %d, rd %d, cr %d, out %d)",
				tc.lin, s.Input, s.CacheRead, s.Creation, s.Output, tc.input, tc.cacheRd, tc.creation, tc.output)
		}
		if s.Requests != tc.requests {
			t.Errorf("%s Requests = %d, want %d", tc.lin, s.Requests, tc.requests)
		}
		if s.MainTokens != tc.mainTok || s.SubTokens != tc.subTok {
			t.Errorf("%s 主/子桶 = (main %d, sub %d), want (%d, %d)",
				tc.lin, s.MainTokens, s.SubTokens, tc.mainTok, tc.subTok)
		}
		// gamma 多一行 beat observe 样例（T51 票04）→ 2 跳
		wantBeats := map[string]int{"lin-alpha": 1, "lin-beta": 1, "lin-gamma": 2}[tc.lin]
		if s.Windows != 1 || s.Beats != wantBeats || s.Handoffs != 1 || s.Blocks != 1 {
			t.Errorf("%s 计数 = (win %d, beat %d, handoff %d, block %d), want (1, %d, 1, 1)",
				tc.lin, s.Windows, s.Beats, s.Handoffs, s.Blocks, wantBeats)
		}
		if s.Project != "Ferryman" {
			t.Errorf("%s Project = %q, want Ferryman", tc.lin, s.Project)
		}
	}
}

// TestSummarizeSubagentBuckets 票02：usage 行的 subagent 标记（票01 字段，值=
// 子代理文件 stem，主行空串）→ Summarize 拆主/子两个 token 桶（口径
// input+cache_read+creation）。手算期望见 testdata/subagent.jsonl：
//   - lin-sub：主 (100+10+5)+(200+20+10)=345；子 agent-aaa 两行
//     (1000+100+50)+(2000+200+100)=3450；LastTS=次日子行（跨日族系月日锚点）。
//   - 尾段路径 lineage 的 inject-only 行（codex case）：无 usage → 双桶皆 0、
//     Agents/LastTS 仍由 inject 行供给（列表兜底链的数据面）。
// 同时锚 Entry 对 subagent 键的解码：显式空串、缺键（旧账本）、stem 三形态。
func TestSummarizeSubagentBuckets(t *testing.T) {
	entries, err := loadOne(t, "subagent.jsonl")
	if err != nil {
		t.Fatalf("Load(subagent): %v", err)
	}
	if len(entries) != 5 {
		t.Fatalf("Load(subagent) 条数 = %d, want 5", len(entries))
	}
	// Entry 解码三形态：显式空串 / 缺键（旧账本行）/ stem
	if entries[0].Subagent != "" {
		t.Errorf("主行(显式空串) Subagent = %q, want \"\"", entries[0].Subagent)
	}
	if entries[1].Subagent != "" {
		t.Errorf("主行(旧账本缺键) Subagent = %q, want \"\"", entries[1].Subagent)
	}
	if entries[2].Subagent != "agent-aaa" || entries[3].Subagent != "agent-aaa" {
		t.Errorf("子行 Subagent = %q, %q, want agent-aaa, agent-aaa",
			entries[2].Subagent, entries[3].Subagent)
	}
	if entries[4].Kind != "inject" || entries[4].Subagent != "" {
		t.Errorf("inject 行 = (%s, subagent %q), want (inject, \"\")", entries[4].Kind, entries[4].Subagent)
	}

	sums := Summarize(entries)
	if len(sums) != 2 {
		t.Fatalf("Summarize 行数 = %d, want 2", len(sums))
	}
	// fixture 的 inject-only 族系键是路径形态（codex case）——与 lin-sub 区分取行
	injLin := "C:/users/allan716/.codex/sessions/2026/09/18/rollout-2026-09-18T09-00-00-abcdef12.jsonl"
	// LastTS 倒序：inj(1780090000.1) > lin-sub(1780086500.4)
	if sums[0].LineageID != injLin {
		byLin := map[string]*SessionSummary{}
		for i := range sums {
			byLin[sums[i].LineageID] = &sums[i]
		}
		sums = []SessionSummary{*byLin[injLin], *byLin["lin-sub"]}
	}
	inj, sub := &sums[0], &sums[1]
	if !strings.HasSuffix(inj.LineageID, ".jsonl") || !strings.Contains(inj.LineageID, "/") {
		t.Fatalf("inject lineage 应为路径形态（列表兜底链的 uuid 尾段取自此）: %q", inj.LineageID)
	}
	if inj.Agents != "codex" || inj.Project != "" || inj.Title != "" || inj.Requests != 0 ||
		inj.MainTokens != 0 || inj.SubTokens != 0 || inj.LastTS != 1780090000.1 {
		t.Errorf("inject-only 族系汇总不符: %+v", *inj)
	}
	if sub.Agents != "cc" || sub.Title != "主会话标题" || sub.Requests != 4 ||
		sub.FirstTS != 1780000000.1 || sub.LastTS != 1780086500.4 {
		t.Errorf("lin-sub 基本汇总不符: %+v", *sub)
	}
	// 主/子桶手算（口径 input+cache_read+creation，不含 output）
	if sub.MainTokens != 345 {
		t.Errorf("lin-sub MainTokens = %d, want 345 (115+230)", sub.MainTokens)
	}
	if sub.SubTokens != 3450 {
		t.Errorf("lin-sub SubTokens = %d, want 3450 (1150+2300)", sub.SubTokens)
	}
	// 既有合计字段含子行（族系总账语义不变）：input=3300 cache_read=330 creation=165
	if sub.Input != 3300 || sub.CacheRead != 330 || sub.Creation != 165 || sub.Output != 1410 {
		t.Errorf("lin-sub 既有合计 = (in %d, rd %d, cr %d, out %d), want (3300, 330, 165, 1410)",
			sub.Input, sub.CacheRead, sub.Creation, sub.Output)
	}
}


func TestLoadOverlongLine(t *testing.T) {
	dir := t.TempDir()
	var b strings.Builder
	b.WriteString(`{"v":1,"kind":"block","ts":1,"lineage_id":"lin-ok"}` + "\n")
	b.WriteString(`{"v":1,"kind":"block","ts":2,"lineage_id":"lin-fat","pad":"`) // 未闭合的超长行
	b.WriteString(strings.Repeat("x", 2<<20))
	b.WriteString(`"}` + "\n")
	b.WriteString(`{"v":1,"kind":"block","ts":3,"lineage_id":"lin-ok2"}` + "\n")
	if err := os.WriteFile(filepath.Join(dir, "fat.jsonl"), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	entries, err := Load(dir)
	if err != nil {
		t.Fatalf("Load 不应因超长行报错: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("条数 = %d, want 2（超长行跳过、前后正常行保留）", len(entries))
	}
	if entries[0].LineageID != "lin-ok" || entries[1].LineageID != "lin-ok2" {
		t.Fatalf("lineage = %q, %q, want lin-ok, lin-ok2", entries[0].LineageID, entries[1].LineageID)
	}
}

func mapEqual(a, b map[string]int) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}
