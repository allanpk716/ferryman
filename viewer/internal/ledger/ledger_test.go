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
	// len：18 行基础结构 + 1 行 inject（审查 Important 防回潮补入）
	if len(entries) != 19 {
		t.Fatalf("Load(sample) 条数 = %d, want 19", len(entries))
	}

	// kind 分布：3 个 lineage 各含 usage×3/2/1 + window/handoff/beat/block×1，另 inject×1
	wantDist := map[string]int{"usage": 6, "window": 3, "handoff": 3, "beat": 3, "block": 3, "inject": 1}
	gotDist := map[string]int{}
	for _, e := range entries {
		gotDist[e.Kind]++
	}
	if !mapEqual(gotDist, wantDist) {
		t.Fatalf("kind 分布 = %v, want %v", gotDist, wantDist)
	}

	// handoff/beat/inject 特有键必须能读出——防 json tag 再漏键静默丢数据
	wantCacheRead := map[string]int64{"lin-alpha": 410, "lin-beta": 2510, "lin-gamma": 96}
	for _, e := range entries {
		switch e.Kind {
		case "handoff":
			if e.PriceVer != "v2026-09" {
				t.Errorf("%s handoff PriceVer = %q, want v2026-09", e.LineageID, e.PriceVer)
			}
		case "beat":
			if e.PriceVer != "v2026-09" {
				t.Errorf("%s beat PriceVer = %q, want v2026-09", e.LineageID, e.PriceVer)
			}
			if e.CacheRead != wantCacheRead[e.LineageID] {
				t.Errorf("%s beat CacheRead = %d, want %d", e.LineageID, e.CacheRead, wantCacheRead[e.LineageID])
			}
		case "inject":
			if e.HandoffID != "ho-alpha-1" || e.Tokens != 999 {
				t.Errorf("inject 行 HandoffID/Tokens = (%q, %d), want (ho-alpha-1, 999)", e.HandoffID, e.Tokens)
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
	}{
		{"lin-gamma", "Gamma 会话", "cc", 1780002000.1, 1780002000.5, 10, 1, 0, 5, 1},
		{"lin-beta", "Beta 会话", "cc,codex", 1780001000.1, 1780001000.6, 3000, 300, 150, 600, 2},
		{"lin-alpha", "Alpha 会话", "cc", 1780000000.1, 1780000000.7, 600, 60, 30, 180, 3},
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
		if s.Windows != 1 || s.Beats != 1 || s.Handoffs != 1 || s.Blocks != 1 {
			t.Errorf("%s 计数 = (win %d, beat %d, handoff %d, block %d), want 全 1",
				tc.lin, s.Windows, s.Beats, s.Handoffs, s.Blocks)
		}
		if s.Project != "Ferryman" {
			t.Errorf("%s Project = %q, want Ferryman", tc.lin, s.Project)
		}
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
