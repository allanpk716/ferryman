package demo

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"ferryman/internal/policy"
	"ferryman/internal/viewer/ledger"
)

// testBase 锚在具体某天 09:00：剧本只相对 base 推演，取哪个日子/时区不影响结构断言。
func testBase() time.Time {
	return time.Date(2026, 9, 18, 9, 0, 0, 0, time.Local)
}

// linOf 过滤出一个族系的全部行。
func linOf(es []ledger.Entry, lin string) []ledger.Entry {
	var out []ledger.Entry
	for _, e := range es {
		if e.LineageID == lin {
			out = append(out, e)
		}
	}
	return out
}

// kindCount 统计某 kind 行数。
func kindCount(es []ledger.Entry, kind string) int {
	n := 0
	for _, e := range es {
		if e.Kind == kind {
			n++
		}
	}
	return n
}

// TestEntriesCounts 总行数写死为剧本常数（25+14+10+7+11）；Summarize 后五族系的
// window/beat/handoff/block 计数是剧本骨架——改剧本必须先改这里。
func TestEntriesCounts(t *testing.T) {
	es := Entries(testBase())
	const wantTotal = 67
	if len(es) != wantTotal {
		t.Fatalf("总行数 = %d, want %d", len(es), wantTotal)
	}
	sums := ledger.Summarize(es)
	if len(sums) != 5 {
		t.Fatalf("族系数 = %d, want 5", len(sums))
	}
	cases := []struct {
		lin                              string
		beats, windows, handoffs, blocks int
	}{
		{"lin-a", 2, 1, 1, 1},
		{"lin-b", 2, 1, 0, 0},
		{"lin-c", 1, 1, 0, 0},
		{"lin-d", 0, 1, 0, 0},
		{"lin-e", 0, 0, 1, 0},
	}
	for _, tc := range cases {
		idx := slices.IndexFunc(sums, func(s ledger.SessionSummary) bool { return s.LineageID == tc.lin })
		if idx < 0 {
			t.Fatalf("缺族系 %s 的汇总", tc.lin)
		}
		s := sums[idx]
		if s.Beats != tc.beats || s.Windows != tc.windows || s.Handoffs != tc.handoffs || s.Blocks != tc.blocks {
			t.Fatalf("%s 计数 = (beat %d, win %d, handoff %d, block %d), want (%d, %d, %d, %d)",
				tc.lin, s.Beats, s.Windows, s.Handoffs, s.Blocks, tc.beats, tc.windows, tc.handoffs, tc.blocks)
		}
	}
}

// TestBeatGoldenFromPolicy 演示 beat 的成本必须与 policy.Derive 现算值严格相等
// （同一公式同参 → 逐位相等；谁手抄数字这里立刻红）。另以黄金数锚定演示口径常量
// 未被误改（PerBeat 26.22 / Expire 103.5 对应 S=150k 的 GLM 价目）。
func TestBeatGoldenFromPolicy(t *testing.T) {
	derive := func(prefix int64) policy.Result {
		r, err := policy.Derive(policy.Params{
			PIn: PIn, PCache: PCache, POut: POut, Per: Per,
			PrefixTokens: float64(prefix), TTLS: TTLS, Safety: 0.8, BeatOutTokens: 300,
		})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	es := Entries(testBase())

	r15 := derive(150000)
	if r15.PerBeat != 26.22 || r15.Expire != 103.5 {
		t.Fatalf("演示口径黄金数漂移：PerBeat=%v Expire=%v, want 26.22/103.5", r15.PerBeat, r15.Expire)
	}
	var hits []ledger.Entry
	for _, e := range linOf(es, "lin-a") {
		if e.Kind == "beat" {
			hits = append(hits, e)
		}
	}
	if len(hits) != 2 {
		t.Fatalf("lin-a beat 数 = %d, want 2", len(hits))
	}
	for i, b := range hits {
		if b.Outcome != "hit" || b.CacheRead != 150000 {
			t.Fatalf("lin-a beat[%d] outcome/cache_read = (%q, %d), want (hit, 150000)", i, b.Outcome, b.CacheRead)
		}
		if b.CostPred != r15.PerBeat || b.CostActual != r15.PerBeat {
			t.Fatalf("lin-a beat[%d] 成本 = (%v, %v), want 均为 PerBeat %v", i, b.CostPred, b.CostActual, r15.PerBeat)
		}
	}

	// lin-c 首跳 miss：实际成本=过期全价款，预测仍是单跳价，cache_read=0
	r90 := derive(90000)
	var miss []ledger.Entry
	for _, e := range linOf(es, "lin-c") {
		if e.Kind == "beat" {
			miss = append(miss, e)
		}
	}
	if len(miss) != 1 {
		t.Fatalf("lin-c beat 数 = %d, want 1（miss 后停跳）", len(miss))
	}
	m := miss[0]
	if m.Outcome != "miss" || m.CacheRead != 0 {
		t.Fatalf("lin-c beat outcome/cache_read = (%q, %d), want (miss, 0)", m.Outcome, m.CacheRead)
	}
	if m.CostActual != r90.Expire {
		t.Fatalf("miss cost_actual = %v, want Expire %v（miss 本身=一次全价重付）", m.CostActual, r90.Expire)
	}
	if m.CostPred != r90.PerBeat {
		t.Fatalf("miss cost_pred = %v, want PerBeat %v", m.CostPred, r90.PerBeat)
	}
}

// TestEntriesDeterministic 同 base 两次调用必须逐字段相等——确定性是演示模式的根，
// 一旦有人往剧本里掺时钟（time.Now/随机数）这里立刻红。
func TestEntriesDeterministic(t *testing.T) {
	a := Entries(testBase())
	b := Entries(testBase())
	if !reflect.DeepEqual(a, b) {
		t.Fatal("同 base 两次 Entries 输出不一致——确定性被破坏（多半混入了时钟或随机数）")
	}
}

// TestEntriesTsInOrder 全部 ts 落在 [base, base+3h]：base 由 main 锚在最近一次 09:00，
// 3h 上限保证"时间戳永远在过去"的承诺不被剧本越界打破。
func TestEntriesTsInOrder(t *testing.T) {
	base := testBase()
	lo := float64(base.Unix())
	hi := float64(base.Add(3 * time.Hour).Unix())
	for i, e := range Entries(base) {
		if e.Ts < lo || e.Ts > hi {
			t.Fatalf("行 %d（%s %s）ts=%v 越界 [%v, %v]", i, e.LineageID, e.Kind, e.Ts, lo, hi)
		}
	}
}

// TestWriteRoundTrip 写出的 JSONL 必须能被真实读路径（ledger.Load）原样解析回内存视图：
// 行数一致、内容 DeepEqual、Summarize 五族系且 LastTS 倒序（lin-a 排第一——列表页门面）。
func TestWriteRoundTrip(t *testing.T) {
	base := testBase()
	dir := t.TempDir()
	path, err := Write(dir, base)
	if err != nil {
		t.Fatal(err)
	}
	if want := "demo-" + base.Format("200601") + ".jsonl"; filepath.Base(path) != want {
		t.Fatalf("文件名 = %q, want %q", filepath.Base(path), want)
	}
	loaded, err := ledger.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 67 {
		t.Fatalf("Load 回读行数 = %d, want 67", len(loaded))
	}
	if !reflect.DeepEqual(loaded, Entries(base)) {
		t.Fatal("Load 回读与 Entries 直出不一致——落盘视图与内存视图漂移")
	}
	sums := ledger.Summarize(loaded)
	got := make([]string, 0, len(sums))
	for _, s := range sums {
		got = append(got, s.LineageID)
	}
	// LastTS 倒序由剧本时间轴决定：a(+101min) > e(+75min) > b(+64min) > c(+32min) > d(+18min)
	want := []string{"lin-a", "lin-e", "lin-b", "lin-c", "lin-d"}
	if !slices.Equal(got, want) {
		t.Fatalf("族系序 = %v, want %v（LastTS 倒序，lin-a 列表页第一行）", got, want)
	}
}

// accounts.py 的字段白名单（_COMMON + _KIND_FIELDS）硬编码为期望集——演示行逐 kind
// 全等：多一个字段真实账本会拒收（演示数据不能撒谎），少一个则丢必填。
var whitelist = map[string][]string{
	"handoff": {"provider", "model", "price_ver", "prompt_tokens", "completion_tokens", "outcome", "wall_s"},
	"beat":    {"provider", "model", "price_ver", "prefix_tokens", "cache_read", "outcome", "cost_pred", "cost_actual"},
	"block":   {"prefix_tokens", "idle_s"},
	"inject":  {"tokens", "handoff_id"},
	"bypass":  {"prefix_tokens"},
	"window":  {"opened_ts", "closed_ts", "dur_s", "prefix_tokens", "close_reason"},
	"usage":   {"model", "title", "input_tokens", "cache_read_tokens", "cache_creation_tokens", "output_tokens", "offset"},
}
var commonFields = []string{"ts", "ts_iso", "kind", "v", "agent", "session_id", "lineage_id", "project"}

// readLines 逐行读回 Write 落盘的 JSONL（解码为 map 保键集合信息）。
func readLines(t *testing.T, path string) []map[string]any {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []map[string]any
	sc := bufio.NewScanner(f)
	ln := 0
	for sc.Scan() {
		ln++
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var row map[string]any
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			t.Fatalf("第 %d 行不是合法 JSON: %v", ln, err)
		}
		out = append(out, row)
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// TestSchemaFieldWhitelist 落盘每行的键集合与白名单逐 kind 全等（比"⊆"更严：
// 多余字段立刻红，缺失必填也立刻红）。
func TestSchemaFieldWhitelist(t *testing.T) {
	dir := t.TempDir()
	path, err := Write(dir, testBase())
	if err != nil {
		t.Fatal(err)
	}
	for i, row := range readLines(t, path) {
		kind, _ := row["kind"].(string)
		extra, ok := whitelist[kind]
		if !ok {
			t.Fatalf("行 %d 出现白名单外 kind %q", i+1, kind)
		}
		want := map[string]bool{}
		for _, k := range commonFields {
			want[k] = true
		}
		for _, k := range extra {
			want[k] = true
		}
		if len(row) != len(want) {
			t.Fatalf("行 %d（%s）键数 = %d, want %d（有多余或缺失字段）", i+1, kind, len(row), len(want))
		}
		for k := range row {
			if !want[k] {
				t.Fatalf("行 %d（%s）出现白名单外字段 %q", i+1, kind, k)
			}
		}
	}
}

// tsISORe 对齐 accounts.py 的 strftime("%Y-%m-%dT%H:%M:%S%z")。
var tsISORe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}[+-]\d{4}$`)

// TestTsISOFormat 全量校验 ts_iso 形状（简报说"抽几行"，全量扫描代价可忽略、更严）。
func TestTsISOFormat(t *testing.T) {
	dir := t.TempDir()
	path, err := Write(dir, testBase())
	if err != nil {
		t.Fatal(err)
	}
	for i, row := range readLines(t, path) {
		got, _ := row["ts_iso"].(string)
		if !tsISORe.MatchString(got) {
			t.Fatalf("行 %d ts_iso = %q, 不匹配 %%Y-%%m-%%dT%%H:%%M:%%S%%z", i+1, got)
		}
	}
}
