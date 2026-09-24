package report

// report_test.go — tests/test_report.py 1:1 移植（票19）：族系账单数学、
// unpriced 汇总、策略对比与跳过、--json 结构、--until 含当日、口径披露。
// 另补三例锁定 Python 行为分支：HandoffCost nil 面、savings 对无 p_cache
// 版本跳过 gross、策略表对无 p_cache 价书的整表跳过文案。

import (
	"bytes"
	"encoding/json"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ferryman/internal/accounts"
	"ferryman/internal/config"
	"ferryman/internal/prices"
)

// 测试 TOML 与 Python tests/test_report.py 逐字一致。
const testTOML = `
[prices.glm]
unit = "智谱积分"
per = 10000
[[prices.glm.versions]]
effective_from = "2026-09-01"
p_in = 6.9
p_cache = 1.7
p_out = 24

[heartbeat]
ttl_s = 600
`

// sep30_2300 对应 Python 的 time.mktime(strptime("2026-09-30 23:00:00"))——本地时区。
var sep30_2300 = float64(time.Date(2026, 9, 30, 23, 0, 0, 0, time.Local).Unix())

type envFixture struct {
	acc   *accounts.Accounts
	books map[string]prices.PriceBook
	dir   string
}

// testEnv 对应 pytest 的 env fixture：写配置 + FERRYMAN_CONFIG + 四条族系流水。
func testEnv(t *testing.T) *envFixture {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("FERRYMAN_CONFIG", filepath.Join(dir, "config.toml"))
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(testTOML), 0o644); err != nil {
		t.Fatal(err)
	}
	acc, err := accounts.New(filepath.Join(dir, "data"))
	if err != nil {
		t.Fatal(err)
	}
	rec := func(kind string, f accounts.Fields) {
		t.Helper()
		if _, err := acc.Record(kind, -1, f); err != nil {
			t.Fatal(err)
		}
	}
	// 族系 L1：一次 block(S=150k) + 一次 inject(2200) + 一次 handoff(50k in/2k out)
	rec("block", accounts.Fields{
		"agent": "cc", "session_id": "s1", "lineage_id": "L1",
		"project": "C:/p", "prefix_tokens": 150_000, "idle_s": 2100,
	})
	rec("inject", accounts.Fields{
		"agent": "cc", "session_id": "s2", "lineage_id": "L1",
		"project": "C:/p", "tokens": 2200, "handoff_id": "h1",
	})
	rec("handoff", accounts.Fields{
		"agent": "cc", "session_id": "s1", "lineage_id": "L1",
		"project": "C:/p", "provider": "glm", "model": "glm-5.3",
		"price_ver": "glm@2026-09-01", "prompt_tokens": 50_000,
		"completion_tokens": 2_000, "outcome": "fresh", "wall_s": 9.9,
	})
	rec("bypass", accounts.Fields{
		"agent": "cc", "session_id": "s3", "lineage_id": "L2",
		"project": "C:/p", "prefix_tokens": 150_000,
	})
	return &envFixture{acc: acc,
		books: prices.LoadPrices(filepath.Join(dir, "config.toml")), dir: dir}
}

// glmBookP books["glm"] 的取址形（map 取值副本）。
func glmBookP(books map[string]prices.PriceBook) *prices.PriceBook {
	b := books["glm"]
	return &b
}

// approx 对应 pytest.approx(want)（默认 rel 1e-6）。
func approx(t *testing.T, name string, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > math.Abs(want)*1e-6 {
		t.Fatalf("%s = %v, want %v (rel 1e-6)", name, got, want)
	}
}

// approxAbs 对应 pytest.approx(want, abs=tol)。
func approxAbs(t *testing.T, name string, got, want, tol float64) {
	t.Helper()
	if math.Abs(got-want) > tol {
		t.Fatalf("%s = %v, want %v (abs tol %v)", name, got, want, tol)
	}
}

// stubRun 对应 monkeypatch(report._accounts_for)/monkeypatch(report.load_prices)：
// 账本与价格表都指向 fixture，隔离 ~/ferryman 真实路径。
func stubRun(t *testing.T, env *envFixture) {
	t.Helper()
	oldAcc, oldPrices := accountsFor, loadPrices
	accountsFor = func(*config.Config) (*accounts.Accounts, error) { return env.acc, nil }
	loadPrices = func() map[string]prices.PriceBook { return env.books }
	t.Cleanup(func() { accountsFor, loadPrices = oldAcc, oldPrices })
}

// captureStdout 换掉 os.Stdout 捕获 Run 的打印（Python capsys 等价）。
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	done := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		done <- buf.String()
	}()
	fn()
	w.Close()
	os.Stdout = old
	return <-done
}

// ---- Python: test_savings_v1_math ----

func TestSavingsV1Math(t *testing.T) {
	env := testEnv(t)
	s := SavingsV1(env.acc.Read(accounts.ReadOpts{}), env.books, glmBookP(env.books))
	var row map[string]any
	for _, r := range s["lineages"].([]map[string]any) {
		if r["lineage_id"] == "L1" {
			row = r
		}
	}
	if row == nil {
		t.Fatal("L1 行缺失")
	}
	if intOf(row["blocks"]) != 1 || intOf(row["injects"]) != 1 || intOf(row["handoffs"]) != 1 {
		t.Fatalf("counts = %v", row)
	}
	approx(t, "gross", anyNum(row["gross"]), 78.0)               // 15×(6.9−1.7)
	approx(t, "inject_cost", anyNum(row["inject_cost"]), 1.518)  // 0.22×6.9
	approx(t, "handoff_cost", anyNum(row["handoff_cost"]), 39.3) // 5×6.9 + 0.2×24
	approx(t, "net", anyNum(row["net"]), 78.0-1.518-39.3)
	if intOf(s["totals"].(map[string]any)["bypass"]) != 1 { // 对照组单列
		t.Fatalf("totals.bypass = %v", s["totals"])
	}
	if s["formula"] != "v1" {
		t.Fatalf("formula = %v", s["formula"])
	}
}

// ---- Python: test_unpriced_provider_listed ----

func TestUnpricedProviderListed(t *testing.T) {
	env := testEnv(t)
	if _, err := env.acc.Record("handoff", -1, accounts.Fields{
		"agent": "cc", "session_id": "s9", "lineage_id": "L9",
		"project": "C:/p", "provider": "whoever", "model": "m",
		"price_ver": nil, "prompt_tokens": 1, "completion_tokens": 1,
		"outcome": "fresh", "wall_s": 0.1,
	}); err != nil {
		t.Fatal(err)
	}
	s := SavingsV1(env.acc.Read(accounts.ReadOpts{}), env.books, glmBookP(env.books))
	found := false
	for _, p := range s["unpriced"].([]string) {
		if p == "whoever" {
			found = true
		}
	}
	if !found {
		t.Fatalf("unpriced = %v, want whoever", s["unpriced"])
	}
}

// ---- Python: test_strategy_table ----

func TestStrategyTable(t *testing.T) {
	env := testEnv(t)
	now := float64(time.Now().UnixNano()) / 1e9 // Python time.time()
	if _, err := env.acc.Record("window", -1, accounts.Fields{
		"agent": "cc", "session_id": "s1", "lineage_id": "L1",
		"project": "C:/p", "opened_ts": now - 900, "closed_ts": now,
		"dur_s": 900.0, "prefix_tokens": 150_000, "close_reason": "subagents_done",
	}); err != nil {
		t.Fatal(err)
	}
	tt := StrategyTable(env.acc.Read(accounts.ReadOpts{Kind: "window"}),
		env.books, 600, "glm")
	rows := tt["rows"].([]map[string]any)
	if len(rows) != 1 {
		t.Fatalf("rows = %d", len(rows))
	}
	r := rows[0]
	approx(t, "none", anyNum(r["none"]), 103.5) // 900s > TTL
	approxAbs(t, "beat", anyNum(r["beat"]), 2*26.2, 0.2)
	approx(t, "expire_compact", anyNum(r["expire_compact"]), 25.875)
	if strOr(r, "best") != "expire_compact" {
		t.Fatalf("best = %v", r["best"])
	}
}

// ---- Python: test_strategy_table_skips_without_ttl_or_cache ----

func TestStrategyTableSkipsWithoutTTLorCache(t *testing.T) {
	env := testEnv(t)
	tt := StrategyTable(env.acc.Read(accounts.ReadOpts{Kind: "window"}),
		env.books, 0, "glm")
	if len(tt["rows"].([]map[string]any)) != 0 || len(tt["skipped"].([]string)) == 0 {
		t.Fatalf("rows/skipped = %v/%v", tt["rows"], tt["skipped"])
	}
}

// ---- Python: test_run_json_output ----

func TestRunJSONOutput(t *testing.T) {
	env := testEnv(t)
	// 遗留②回归钉(2026-09-25):project 含 Windows 反斜杠路径的行进 --json,
	// 输出必须是合法 JSON(历史缺陷形态:裸 \ 转义,json.loads 炸;实测出自
	// v0.1.3-2-g5af93ea 来路存疑构建,现行 json.Encoder 路径不复发——本钉防
	// 未来任何手拼 JSON 发射器回归)。
	if _, err := env.acc.Record("bypass", -1, accounts.Fields{
		"agent": "cc", "session_id": "s9", "lineage_id": "L9",
		"project": `C:\Users\allan716\proj`, "prefix_tokens": 1,
	}); err != nil {
		t.Fatal(err)
	}
	stubRun(t, env)
	out := captureStdout(t, func() {
		if Run(Args{JSON: true}) != 0 {
			t.Fatal("run != 0")
		}
	})
	if !json.Valid([]byte(out)) {
		t.Fatalf("--json 输出不是合法 JSON(裸反斜杠转义?): %.120s", out)
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if _, ok := parsed["savings"]; !ok {
		t.Fatal("输出缺 savings 键")
	}
}

// ---- 补例：HandoffCost 的 nil 面（无价/无版本 → nil，不造数） ----

func TestHandoffCostNilCases(t *testing.T) {
	env := testEnv(t)
	books := env.books
	for name, e := range map[string]map[string]any{
		"无price_ver": {"kind": "handoff", "prompt_tokens": 1.0, "completion_tokens": 1.0},
		"无@":         {"kind": "handoff", "price_ver": "glm", "prompt_tokens": 1.0},
		"未知价格表":      {"kind": "handoff", "price_ver": "who@2026-09-01", "prompt_tokens": 1.0},
		"已知表未知版本":    {"kind": "handoff", "price_ver": "glm@2020-01-01", "prompt_tokens": 1.0},
	} {
		if got := HandoffCost(e, books); got != nil {
			t.Fatalf("%s: got %v, want nil", name, *got)
		}
	}
	e := map[string]any{"kind": "handoff", "price_ver": "glm@2026-09-01",
		"prompt_tokens": 50_000.0, "completion_tokens": 2_000.0}
	got := HandoffCost(e, books)
	if got == nil {
		t.Fatal("命中版本应为可算")
	}
	approx(t, "handoff_cost", *got, 39.3) // 5×6.9 + 0.2×24
}

// ---- 补例：savings 对无 p_cache 版本跳过 gross（宁可不算，不造数） ----

func TestSavingsV1SkipsBlockWithoutPCache(t *testing.T) {
	env := testEnv(t)
	// 无 p_cache 价书（形态同 tests/test_prices.py 的 nocache）
	nocache := prices.PriceBook{Key: "nocache", Unit: "元", Per: 1000000,
		Versions: []prices.PriceVersion{
			{EffectiveFrom: "2026-09-01", PIn: 1.0, PCache: nil, POut: 2.0},
		}}
	d17 := float64(time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC).Unix())
	entries := []map[string]any{
		{"kind": "block", "lineage_id": "L1", "project": "C:/p",
			"ts": d17, "prefix_tokens": 150_000.0},
		{"kind": "inject", "lineage_id": "L1", "ts": d17, "tokens": 10_000.0},
	}
	s := SavingsV1(entries, env.books, &nocache)
	row := s["lineages"].([]map[string]any)[0]
	if anyNum(row["gross"]) != 0 {
		t.Fatalf("gross = %v, want 0（无 p_cache 跳过）", row["gross"])
	}
	approx(t, "inject_cost", anyNum(row["inject_cost"]), 0.01) // 10k/1e6×1.0
}

// ---- 补例：价书最新版本无 p_cache → 整表跳过（Q16 文案） ----

func TestStrategyTableSkipsBookWithoutPCache(t *testing.T) {
	nb := prices.PriceBook{Key: "nocache", Unit: "元", Per: 1000000,
		Versions: []prices.PriceVersion{
			{EffectiveFrom: "2026-09-01", PIn: 1.0, PCache: nil, POut: 2.0},
		}}
	tt := StrategyTable(nil, map[string]prices.PriceBook{"nocache": nb}, 600, "nocache")
	if rows := tt["rows"].([]map[string]any); len(rows) != 0 {
		t.Fatalf("rows = %v", rows)
	}
	skipped := tt["skipped"].([]string)
	if len(skipped) != 1 || skipped[0] != "nocache 无 p_cache：节省额不可算（Q16）" {
		t.Fatalf("skipped = %v", skipped)
	}
}

// ---- 终审修复：--until 含当日 ----

// ---- Python: test_until_day_inclusive_read ----

func TestUntilDayInclusiveRead(t *testing.T) {
	// 终审：--until 2026-09-30 必须含 9-30 当天 23:00 的流水（月账主形态）。
	env := testEnv(t)
	if _, err := env.acc.Record("block", sep30_2300, accounts.Fields{
		"agent": "cc", "session_id": "sE", "lineage_id": "LE",
		"project": "C:/p", "prefix_tokens": 150_000, "idle_s": 60,
	}); err != nil {
		t.Fatal(err)
	}
	u, err := parseDate("2026-09-30", true)
	if err != nil {
		t.Fatal(err)
	}
	hasLE := func(rows []map[string]any) bool {
		for _, r := range rows {
			if r["lineage_id"] == "LE" {
				return true
			}
		}
		return false
	}
	if !hasLE(env.acc.Read(accounts.ReadOpts{Until: u})) {
		t.Fatal("--until 含当日口径漏掉了 LE")
	}
	uMid, err := parseDate("2026-09-30", false)
	if err != nil {
		t.Fatal(err)
	}
	// 旧口径（午夜截断）确实会把当日排掉——锁定行为差异真实存在
	if hasLE(env.acc.Read(accounts.ReadOpts{Until: uMid})) {
		t.Fatal("午夜口径不应包含当日 23:00")
	}
}

// ---- Python: test_run_json_until_includes_last_day ----

func TestRunJSONUntilIncludesLastDay(t *testing.T) {
	// 终审：report.run 的 --until 走含当日口径（JSON 端到端）。
	env := testEnv(t)
	if _, err := env.acc.Record("bypass", sep30_2300, accounts.Fields{
		"agent": "cc", "session_id": "sE", "lineage_id": "LE",
		"project": "C:/p", "prefix_tokens": 150_000,
	}); err != nil {
		t.Fatal(err)
	}
	stubRun(t, env)
	out := captureStdout(t, func() {
		if Run(Args{Since: "2026-09-01", Until: "2026-09-30", JSON: true}) != 0 {
			t.Fatal("run != 0")
		}
	})
	var payload map[string]any
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("JSON 解析失败: %v\n%s", err, out)
	}
	lineages := payload["savings"].(map[string]any)["lineages"].([]any)
	found := false
	for _, l := range lineages {
		if l.(map[string]any)["lineage_id"] == "LE" {
			found = true
		}
	}
	if !found {
		t.Fatalf("--until 末日 23:00 的流水被排除: %s", out)
	}
}

// ---- 终审修复：策略口径披露 ----

func addWindow(t *testing.T, acc *accounts.Accounts) {
	t.Helper()
	now := float64(time.Now().UnixNano()) / 1e9
	if _, err := acc.Record("window", -1, accounts.Fields{
		"agent": "cc", "session_id": "s1", "lineage_id": "L1",
		"project": "C:/p", "opened_ts": now - 900, "closed_ts": now,
		"dur_s": 900.0, "prefix_tokens": 150_000, "close_reason": "subagents_done",
	}); err != nil {
		t.Fatal(err)
	}
}

// ---- Python: test_strategy_table_stamps_formula_and_ratio ----

func TestStrategyTableStampsFormulaAndRatio(t *testing.T) {
	env := testEnv(t)
	addWindow(t, env.acc)
	tt := StrategyTable(env.acc.Read(accounts.ReadOpts{Kind: "window"}),
		env.books, 600, "glm")
	if tt["formula"] != StrategyFormula || StrategyFormula != "v1" {
		t.Fatalf("formula = %v", tt["formula"])
	}
	if anyNum(tt["compact_ratio"]) != CompactRatio || CompactRatio != 0.25 {
		t.Fatalf("compact_ratio = %v", tt["compact_ratio"])
	}
}

// ---- Python: test_strategy_caveats_in_json_and_text ----

func TestStrategyCaveatsInJSONAndText(t *testing.T) {
	// 终审：口径披露两行进入 --json payload 与文本报表；公式在 render 头部盖章。
	env := testEnv(t)
	addWindow(t, env.acc)
	stubRun(t, env)
	out := captureStdout(t, func() {
		if Run(Args{JSON: true}) != 0 {
			t.Fatal("run != 0")
		}
	})
	var payload map[string]any
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("JSON 解析失败: %v\n%s", err, out)
	}
	st := payload["strategy"].(map[string]any)
	if st["formula"] != "v1" {
		t.Fatalf("strategy.formula = %v", st["formula"])
	}
	if anyNum(st["compact_ratio"]) != 0.25 {
		t.Fatalf("strategy.compact_ratio = %v", st["compact_ratio"])
	}
	caveats := asStrings(payload["strategy_caveats"])
	if len(caveats) != 2 {
		t.Fatalf("strategy_caveats = %d 段", len(caveats))
	}
	anyContains := func(sub string) bool {
		for _, c := range caveats {
			if strings.Contains(c, sub) {
				return true
			}
		}
		return false
	}
	if !anyContains("口径披露") || !anyContains("不得作为心跳授权依据") {
		t.Fatalf("strategy_caveats 缺口径披露: %v", caveats)
	}
	s := SavingsV1(env.acc.Read(accounts.ReadOpts{}), env.books, glmBookP(env.books))
	text := RenderText(s, st, env.books, "glm", map[string]string{})
	if !strings.Contains(text, "口径披露") || !strings.Contains(text, "不得作为心跳授权依据") {
		t.Fatal("文本报表缺口径披露")
	}
	if !strings.Contains(text, "策略公式："+StrategyFormula) {
		t.Fatal("文本报表头部缺公式章")
	}
}
