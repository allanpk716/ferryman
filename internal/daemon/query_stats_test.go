package daemon

// query_stats_test.go — 票04：GET /stats/summary 与 GET /stats/usage 验收钉子。
//
// 夹具风格沿 query_report_test.go（queryEnv 真 Daemon＋临时端口＋冻结时钟；
// 价格表经 queryReportPrices 缝注入——绝不读真用户配置）。金额期望值按
// report.SavingsV1 公式手算钉死（zpBook：per=10，p_in=2、p_cache=1、p_out=4，
// 数值全整除无浮点毛刺）：
//
//	毛   = Σ prefix/per×(p_in−p_cache)
//	注入 = Σ tokens/per×p_in
//	交接 = Σ(prompt×p_in+completion×p_out)/per
//	净   = SavingsV1 内部 Round(毛−注入−交接, 4)——逐字取用,不再包一层公式。
//
// 锚点对拍说明（票面「可选」项）：真实九月数据（五类 10/2/50/778/760、
// requests=76,245）在测试环境不可得（生产账本不入测试依赖），对拍以本文件
// 手算小账本为准；冻结 schema 字段名与 widget/ui/mock/stats/gen_snapshot.py
// （票01 快照,票05 页面直接消费）逐字一致——days/kpi 键集断言即契约钉子。
//
// today（冻结钟 t0=1.8e9 的本地日）记 D0，昨天 D1，前天 D2，大前天 D3。

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"ferryman/internal/accounts"
	"ferryman/internal/prices"
)

// statsGET GET /stats/... 并解成 JSON（断言 200）。
func statsGET(t *testing.T, e *queryEnv, path string) map[string]any {
	t.Helper()
	code, raw := getRaw(t, e.port, path, e.token)
	if code != 200 {
		t.Fatalf("GET %s = %d %q", path, code, raw)
	}
	var resp map[string]any
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("GET %s 响应非 JSON: %v (%q)", path, err, raw)
	}
	return resp
}

// statsDays 时间基：D0（冻结钟本地日）/D1/D2/D3 的本地零点。
func statsDays(t *testing.T, t0 float64) (d0, d1, d2, d3 time.Time) {
	t.Helper()
	now := time.Unix(int64(t0), 0).In(time.Local)
	mid := func(d time.Time) time.Time {
		return time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, time.Local)
	}
	d0 = mid(now)
	return d0, d0.AddDate(0, 0, -1), d0.AddDate(0, 0, -2), d0.AddDate(0, 0, -3)
}

// statsAt 某日本地零点 + sec 的 Unix 秒。
func statsAt(d time.Time, sec float64) float64 { return float64(d.Unix()) + sec }

// statsNum 断言键为数值并返回（JSON 往返后一律 float64）。
func statsNum(t *testing.T, m map[string]any, k string) float64 {
	t.Helper()
	v, ok := m[k].(float64)
	if !ok {
		t.Fatalf("键 %q 应为数值,得到 %#v（容器 %v）", k, m[k], m)
	}
	return v
}

// statsUsageRow 记一行 usage（title 白名单必填——端点绝不回吐,反向断言用）。
func statsUsageRow(t *testing.T, e *queryEnv, ts float64, project, lineage, sid, model string,
	in, cr, cc, out int) {
	t.Helper()
	mustRec(t, e, "usage", ts, project, lineage, sid, accounts.Fields{
		"model": model, "title": "绝密标题", "input_tokens": in,
		"cache_read_tokens": cr, "cache_creation_tokens": cc, "output_tokens": out,
		"offset": 0, "subagent": ""})
}

// statsFixture 三日夹具（可算态）：D2=usage+block+handoff、D1=usage+block、
// D0=usage+block+inject+bypass+window+beat（beat 不入五类）+子代理 usage 零列行。
// 手算期望（zpBook per=10 / p_in=2 / p_cache=1 / p_out=4）：
//
//	KPI：requests=4；四列 350/35/15/80；hit=35 base=385；毛 10+2+5=17、注入 8、
//	交接 (30×2+20×4)/10=14、净 Round(17−8−14,4)=−5；counts 3/1/1/1/1。
//	D2：gross 10 / hc 14 / net −4；D1：gross 2 / net 2；D0：gross 5 / inject 8 / net −3。
func statsFixture(t *testing.T, e *queryEnv) (d0, d1, d2, d3 time.Time) {
	t.Helper()
	pc := 1.0
	withPrices(t, zpBook(&pc))
	e.d.Cfg.FerryProvider = "zp"
	d0, d1, d2, d3 = statsDays(t, e.t0)

	statsUsageRow(t, e, statsAt(d2, 60), "C:/proj", "L1", "s-d2", "glm-5.3", 200, 20, 10, 40)
	mustRec(t, e, "block", statsAt(d2, 120), "C:/proj", "L1", "s-d2",
		accounts.Fields{"prefix_tokens": 100, "idle_s": 30}) // 毛 100/10×(2−1)=10
	mustRec(t, e, "handoff", statsAt(d2, 180), "C:/proj", "L1", "s-d2",
		accounts.Fields{"provider": "zp", "model": "m", "price_ver": "zp@2026-01-01",
			"prompt_tokens": 30, "completion_tokens": 20, "outcome": "ok", "wall_s": 1.0})
	statsUsageRow(t, e, statsAt(d1, 60), "C:/proj", "L1", "s-d1", "glm-5.3", 50, 5, 0, 10)
	mustRec(t, e, "block", statsAt(d1, 120), "C:/proj", "L1", "s-d1",
		accounts.Fields{"prefix_tokens": 20, "idle_s": 30}) // 毛 2
	statsUsageRow(t, e, statsAt(d0, 60), "C:/proj", "L1", "s-d0", "glm-5.3", 100, 10, 5, 30)
	mustRec(t, e, "block", statsAt(d0, 120), "C:/proj", "L1", "s-d0",
		accounts.Fields{"prefix_tokens": 50, "idle_s": 30}) // 毛 5
	mustRec(t, e, "inject", statsAt(d0, 130), "C:/proj", "L1", "s-d0",
		accounts.Fields{"tokens": 40, "handoff_id": "h1"}) // 注入 40/10×2=8
	mustRec(t, e, "bypass", statsAt(d0, 140), "C:/proj", "L1", "s-d0",
		accounts.Fields{"prefix_tokens": 70})
	mustRec(t, e, "window", statsAt(d0, 150), "C:/proj", "L1", "s-d0",
		accounts.Fields{"opened_ts": statsAt(d0, 100), "closed_ts": statsAt(d0, 150),
			"dur_s": 50, "prefix_tokens": 900, "close_reason": "resumed"})
	mustRec(t, e, "beat", statsAt(d0, 160), "C:/proj", "L1", "s-d0",
		accounts.Fields{"provider": "zp", "model": "m", "price_ver": nil,
			"prefix_tokens": 900, "cache_read": 800, "outcome": "hit",
			"cost_pred": 0.1, "cost_actual": 0.2, "lane": "qwatch"})
	// 子代理 usage 行也计 requests（与 /report 同源）；四列全零不搅动手算期望。
	statsUsageRow(t, e, statsAt(d0, 170), "C:/proj", "L2", "s-sub", "glm-5.3", 0, 0, 0, 0)
	return
}

// ---- 聚合端点：KPI＋逐日序列＋冻结 schema 键集 ----

func TestStatsSummaryKPIDaysFrozenSchema(t *testing.T) {
	e := newQueryEnv(t)
	d0, d1, d2, _ := statsFixture(t, e)

	resp := statsGET(t, e, "/stats/summary")

	// 顶层与 kpi 键集 = gen_snapshot.py 冻结 schema（票05 直接消费,逐字一致）。
	if !keySetEqual(keys(resp), "generated_at", "kpi", "days") {
		t.Fatalf("顶层键集 = %v", keys(resp))
	}
	kpi := resp["kpi"].(map[string]any)
	if !keySetEqual(keys(kpi), "requests", "tokens", "cache_hit", "cost", "savings") {
		t.Fatalf("kpi 键集 = %v", keys(kpi))
	}
	if statsNum(t, kpi, "requests") != 4 {
		t.Fatalf("requests = %v, want 4（含子代理行）", kpi["requests"])
	}
	tok := kpi["tokens"].(map[string]any)
	if !keySetEqual(keys(tok), "input", "cache_read", "cache_creation", "output") {
		t.Fatalf("tokens 键集 = %v", keys(tok))
	}
	if statsNum(t, tok, "input") != 350 || statsNum(t, tok, "cache_read") != 35 ||
		statsNum(t, tok, "cache_creation") != 15 || statsNum(t, tok, "output") != 80 {
		t.Fatalf("KPI 四列 = %v, want 350/35/15/80", tok)
	}
	ch := kpi["cache_hit"].(map[string]any)
	if !keySetEqual(keys(ch), "hit", "base", "rate") {
		t.Fatalf("cache_hit 键集 = %v", keys(ch))
	}
	if statsNum(t, ch, "hit") != 35 || statsNum(t, ch, "base") != 385 {
		t.Fatalf("命中率两原始数 = %v, want hit 35 base 385", ch)
	}
	if got, want := statsNum(t, ch, "rate"), 35.0/385.0; got != want {
		t.Fatalf("rate = %v, want %v", got, want)
	}
	cost := kpi["cost"].(map[string]any)
	if !keySetEqual(keys(cost), "computable", "note", "unit", "value") {
		t.Fatalf("cost 键集 = %v", keys(cost))
	}
	if cost["computable"] != true || cost["note"] != "" || cost["unit"] != "分" {
		t.Fatalf("cost 可算性结构 = %v", cost)
	}
	if cost["value"] != nil {
		t.Fatalf("cost.value 应为 null（金额面由前端用可算性结构处理,端点不硬造）: %v", cost["value"])
	}
	sv := kpi["savings"].(map[string]any)
	if !keySetEqual(keys(sv), "computable", "note", "unit", "gross", "inject_cost",
		"handoff_cost", "net", "counts", "unpriced") {
		t.Fatalf("kpi.savings 键集 = %v", keys(sv))
	}
	if sv["computable"] != true || sv["unit"] != "分" {
		t.Fatalf("savings 可算性 = %v", sv)
	}
	if statsNum(t, sv, "gross") != 17 || statsNum(t, sv, "inject_cost") != 8 ||
		statsNum(t, sv, "handoff_cost") != 14 || statsNum(t, sv, "net") != -5 {
		t.Fatalf("KPI 成效账 = %v, want 毛17/注入8/交接14/净−5", sv)
	}
	cnt := sv["counts"].(map[string]any)
	if !keySetEqual(keys(cnt), "block", "bypass", "inject", "handoff", "window") {
		t.Fatalf("counts 键集 = %v", keys(cnt))
	}
	if statsNum(t, cnt, "block") != 3 || statsNum(t, cnt, "bypass") != 1 ||
		statsNum(t, cnt, "inject") != 1 || statsNum(t, cnt, "handoff") != 1 ||
		statsNum(t, cnt, "window") != 1 {
		t.Fatalf("五类计数 = %v, want 3/1/1/1/1", cnt)
	}
	if ups, ok := sv["unpriced"].([]any); !ok || len(ups) != 0 {
		t.Fatalf("unpriced = %#v, want []（handoff 已按 price_ver 计价）", sv["unpriced"])
	}

	// 逐日序列：D2→D0 三天连续,首尾与分桶边界逐日核对。
	days := resp["days"].([]any)
	if len(days) != 3 {
		t.Fatalf("days 长度 = %d, want 3（自账本最早日 D2 至今天 D0）", len(days))
	}
	type dayWant struct {
		date                                 string
		inp, cr, cc, out, req                float64
		block, bypass, inject, hand, window_ float64
		gross, injectCost, hc, net           float64
		neg                                  bool
	}
	for i, w := range []dayWant{
		{date: d2.Format("2006-01-02"), inp: 200, cr: 20, cc: 10, out: 40, req: 1,
			block: 1, hand: 1, gross: 10, hc: 14, net: -4, neg: true},
		{date: d1.Format("2006-01-02"), inp: 50, cr: 5, cc: 0, out: 10, req: 1,
			block: 1, gross: 2, net: 2},
		{date: d0.Format("2006-01-02"), inp: 100, cr: 10, cc: 5, out: 30, req: 2,
			block: 1, bypass: 1, inject: 1, window_: 1, gross: 5, injectCost: 8, net: -3, neg: true},
	} {
		day := days[i].(map[string]any)
		if !keySetEqual(keys(day), "date", "input", "cache_read", "cache_creation",
			"output", "requests", "events", "savings", "neg") {
			t.Fatalf("day[%d] 键集 = %v（冻结 schema 逐字）", i, keys(day))
		}
		if day["date"] != w.date {
			t.Fatalf("day[%d].date = %v, want %v", i, day["date"], w.date)
		}
		if statsNum(t, day, "input") != w.inp || statsNum(t, day, "cache_read") != w.cr ||
			statsNum(t, day, "cache_creation") != w.cc || statsNum(t, day, "output") != w.out ||
			statsNum(t, day, "requests") != w.req {
			t.Fatalf("day[%d] 四列/请求 = %v", i, day)
		}
		ev := day["events"].(map[string]any)
		if !keySetEqual(keys(ev), "block", "bypass", "inject", "handoff", "window") {
			t.Fatalf("day[%d].events 键集 = %v", i, keys(ev))
		}
		if statsNum(t, ev, "block") != w.block || statsNum(t, ev, "bypass") != w.bypass ||
			statsNum(t, ev, "inject") != w.inject || statsNum(t, ev, "handoff") != w.hand ||
			statsNum(t, ev, "window") != w.window_ {
			t.Fatalf("day[%d] 五类事件 = %v（beat 不得混入）", i, ev)
		}
		ds := day["savings"].(map[string]any)
		if !keySetEqual(keys(ds), "computable", "gross", "inject_cost", "handoff_cost", "net") {
			t.Fatalf("day[%d].savings 键集 = %v", i, keys(ds))
		}
		if ds["computable"] != true {
			t.Fatalf("day[%d].savings.computable = %v", i, ds["computable"])
		}
		if statsNum(t, ds, "gross") != w.gross || statsNum(t, ds, "inject_cost") != w.injectCost ||
			statsNum(t, ds, "handoff_cost") != w.hc || statsNum(t, ds, "net") != w.net {
			t.Fatalf("day[%d] 逐日节省 = %v, want 毛%v/注%v/交%v/净%v", i, ds,
				w.gross, w.injectCost, w.hc, w.net)
		}
		if day["neg"] != w.neg {
			t.Fatalf("day[%d].neg = %v, want %v", i, day["neg"], w.neg)
		}
	}
	// days 求和 == KPI（票01 对拍同款抽查：四列/请求）。
	var sumIn, sumReq float64
	for _, d := range days {
		day := d.(map[string]any)
		sumIn += statsNum(t, day, "input")
		sumReq += statsNum(t, day, "requests")
	}
	if sumIn != 350 || sumReq != 4 {
		t.Fatalf("days 求和 input=%v requests=%v, want 350/4", sumIn, sumReq)
	}
	// 红线：响应不出现 title。
	var all []string
	walkKeys(resp, &all)
	for _, k := range all {
		if k == "title" {
			t.Fatalf("响应出现 title（隐私红线）")
		}
	}
}

// ---- 聚合端点：since/until 窗口 ----

func TestStatsSummarySinceUntilWindow(t *testing.T) {
	e := newQueryEnv(t)
	d0, d1, _, _ := statsFixture(t, e)
	d1s := d1.Format("2006-01-02")

	// 恰一天窗：days 只此一日,KPI 也只聚合窗内行（D1：1 请求、毛 2、净 2）。
	resp := statsGET(t, e, "/stats/summary?since="+d1s+"&until="+d1s)
	days := resp["days"].([]any)
	if len(days) != 1 || days[0].(map[string]any)["date"] != d1s {
		t.Fatalf("单日窗 days = %v, want [%s]", days, d1s)
	}
	kpi := resp["kpi"].(map[string]any)
	if statsNum(t, kpi, "requests") != 1 {
		t.Fatalf("单日窗 requests = %v, want 1", kpi["requests"])
	}
	if statsNum(t, kpi["tokens"].(map[string]any), "input") != 50 {
		t.Fatalf("单日窗 input = %v, want 50", kpi["tokens"])
	}
	sv := kpi["savings"].(map[string]any)
	if statsNum(t, sv, "gross") != 2 || statsNum(t, sv, "net") != 2 ||
		statsNum(t, sv["counts"].(map[string]any), "block") != 1 {
		t.Fatalf("单日窗成效账 = %v, want 毛2/净2/block1", sv)
	}

	// since 早于账本最早日：首日补零占位,终日仍为今天（缺省 until）。
	d2past := d0.AddDate(0, 0, -3).Format("2006-01-02") // D3
	resp = statsGET(t, e, "/stats/summary?since="+d2past)
	days = resp["days"].([]any)
	if len(days) != 4 || days[0].(map[string]any)["date"] != d2past {
		t.Fatalf("前移窗 days = %v, want 4 天自 %s", days, d2past)
	}
	first := days[0].(map[string]any)
	if statsNum(t, first, "requests") != 0 || statsNum(t, first["savings"].(map[string]any), "net") != 0 {
		t.Fatalf("占位零日 = %v", first)
	}

	// 格式坏 → 400 JSON。
	for _, q := range []string{"?since=2026/09/01", "?until=not-a-date", "?since=2026-13-01"} {
		code, raw := getRaw(t, e.port, "/stats/summary"+q, e.token)
		if code != 400 {
			t.Fatalf("GET /stats/summary%s = %d %q, want 400", q, code, raw)
		}
	}
}

// ---- 聚合端点：可算性两态（无 p_cache / 无价格表）——金额 null、计数照发 ----

func TestStatsSummaryNotComputableNulls(t *testing.T) {
	// 态一：书在但末版缺 p_cache——gross/inject_cost/net 全 null,handoff 仍按
	// price_ver 计价（SavingsV1 单源行为）,counts/unpriced 照发。
	e := newQueryEnv(t)
	withPrices(t, zpBook(nil)) // p_cache 缺
	e.d.Cfg.FerryProvider = "zp"
	_, _, d2, _ := statsDays(t, e.t0)
	mustRec(t, e, "handoff", statsAt(d2, 60), "C:/proj", "L1", "s-n",
		accounts.Fields{"provider": "zp", "model": "m", "price_ver": "zp@2026-01-01",
			"prompt_tokens": 30, "completion_tokens": 20, "outcome": "ok", "wall_s": 1.0})
	mustRec(t, e, "block", statsAt(d2, 70), "C:/proj", "L1", "s-n",
		accounts.Fields{"prefix_tokens": 100, "idle_s": 30})

	resp := statsGET(t, e, "/stats/summary")
	sv := resp["kpi"].(map[string]any)["savings"].(map[string]any)
	if sv["computable"] != false {
		t.Fatalf("缺 p_cache 应不可算: %v", sv["computable"])
	}
	if note, _ := sv["note"].(string); note == "" ||
		(!containsStr(note, "节省额不可算") || !containsStr(note, "p_cache")) {
		t.Fatalf("note = %q, want 缺 p_cache 文案", note)
	}
	if sv["gross"] != nil || sv["inject_cost"] != nil || sv["net"] != nil {
		t.Fatalf("不可算金额应 null: %v", sv)
	}
	if statsNum(t, sv, "handoff_cost") != 14 {
		t.Fatalf("handoff_cost = %v, want 14（独立于 econ 可算性）", sv["handoff_cost"])
	}
	if statsNum(t, sv["counts"].(map[string]any), "block") != 1 {
		t.Fatalf("计数不受可算性影响: %v", sv["counts"])
	}
	day := resp["days"].([]any)[0].(map[string]any)
	ds := day["savings"].(map[string]any)
	if ds["gross"] != nil || ds["net"] != nil {
		t.Fatalf("日 savings 不可算金额应 null: %v", ds)
	}
	if statsNum(t, ds, "handoff_cost") != 14 {
		t.Fatalf("日 handoff_cost = %v, want 14", ds["handoff_cost"])
	}
	if day["neg"] != false {
		t.Fatalf("不可算日 neg = %v, want false", day["neg"])
	}
	cost := resp["kpi"].(map[string]any)["cost"].(map[string]any)
	if cost["computable"] != false || cost["value"] != nil || cost["unit"] != "分" {
		t.Fatalf("cost 结构 = %v", cost)
	}

	// 态二：无任何价格表——unit 落缺省「智谱积分」,handoff 不可价入 unpriced。
	e2 := newQueryEnv(t)
	withPrices(t, map[string]prices.PriceBook{})
	e2.d.Cfg.FerryProvider = "local"
	_, _, d22, _ := statsDays(t, e2.t0)
	mustRec(t, e2, "handoff", statsAt(d22, 60), "C:/proj", "L1", "s-n2",
		accounts.Fields{"provider": "local", "model": "m", "price_ver": "local@2026-01-01",
			"prompt_tokens": 30, "completion_tokens": 20, "outcome": "ok", "wall_s": 1.0})
	resp2 := statsGET(t, e2, "/stats/summary")
	sv2 := resp2["kpi"].(map[string]any)["savings"].(map[string]any)
	if sv2["computable"] != false || sv2["unit"] != "智谱积分" {
		t.Fatalf("无表态 = %v, want computable=false unit=智谱积分", sv2)
	}
	if note, _ := sv2["note"].(string); !containsStr(note, "无可用品价格表") {
		t.Fatalf("无表 note = %q", note)
	}
	if ups, ok := sv2["unpriced"].([]any); !ok || len(ups) != 1 || ups[0] != "local" {
		t.Fatalf("unpriced = %#v, want [local]", sv2["unpriced"])
	}
}

// ---- 聚合端点：空账本——今日单日零值,命中率 rate=null ----

func TestStatsSummaryEmptyLedgerTodayOnly(t *testing.T) {
	e := newQueryEnv(t)
	withPrices(t, map[string]prices.PriceBook{})
	_, _, _, _ = statsDays(t, e.t0)

	resp := statsGET(t, e, "/stats/summary")
	days := resp["days"].([]any)
	if len(days) != 1 {
		t.Fatalf("空账本 days = %v, want 仅今天一日", days)
	}
	kpi := resp["kpi"].(map[string]any)
	if statsNum(t, kpi, "requests") != 0 {
		t.Fatalf("空账本 requests = %v", kpi["requests"])
	}
	if kpi["cache_hit"].(map[string]any)["rate"] != nil {
		t.Fatalf("base=0 时 rate 应 null: %v", kpi["cache_hit"])
	}
	if ga, _ := resp["generated_at"].(string); ga == "" {
		t.Fatalf("generated_at 缺失")
	}
}

// ---- 明细端点：过滤（时间/项目/模型）+倒序+分页+行键集（无 title/无 cost）----

func TestStatsUsageFilterSortAndPagination(t *testing.T) {
	e := newQueryEnv(t)
	withPrices(t, map[string]prices.PriceBook{})
	d0, _, _, _ := statsDays(t, e.t0)
	statsUsageRow(t, e, statsAt(d0, 10), "C:/A", "L1", "s1", "GLM-5.3", 1, 2, 3, 4)
	statsUsageRow(t, e, statsAt(d0, 20), "C:/B", "L1", "s2", "GLM-5.3-Air", 5, 6, 7, 8)
	statsUsageRow(t, e, statsAt(d0, 30), "C:/B", "L1", "s3", "Kimi-K3", 9, 10, 11, 12)
	statsUsageRow(t, e, statsAt(d0, 40), "C:/A", "L1", "s4", "GLM-5.3", 13, 14, 15, 16)
	statsUsageRow(t, e, statsAt(d0, 50), "C:/C", "L1", "s5", "glm-5.3x", 17, 18, 19, 20)
	mustRec(t, e, "beat", statsAt(d0, 45), "C:/A", "L1", "s1",
		accounts.Fields{"provider": "zp", "model": "m", "price_ver": nil,
			"prefix_tokens": 1, "cache_read": 1, "outcome": "hit",
			"cost_pred": 0, "cost_actual": 0, "lane": "qwatch"})

	sids := func(m map[string]any) []string {
		out := []string{}
		for _, r := range m["rows"].([]any) {
			out = append(out, r.(map[string]any)["session_id"].(string))
		}
		return out
	}
	eq := func(got []string, want ...string) bool {
		if len(got) != len(want) {
			return false
		}
		for i := range got {
			if got[i] != want[i] {
				return false
			}
		}
		return true
	}

	// 缺省：时间倒序、limit 50、offset 0;beat 行不进明细。
	resp := statsGET(t, e, "/stats/usage")
	if statsNum(t, resp, "total") != 5 || statsNum(t, resp, "limit") != 50 ||
		statsNum(t, resp, "offset") != 0 {
		t.Fatalf("缺省回显 = %v", resp)
	}
	if rows := sids(resp); !eq(rows, "s5", "s4", "s3", "s2", "s1") {
		t.Fatalf("缺省序 = %v, want s5..s1 倒序", rows)
	}
	row := resp["rows"].([]any)[0].(map[string]any)
	if !keySetEqual(keys(row), "ts_iso", "project", "session_id", "model",
		"input_tokens", "cache_read_tokens", "cache_creation_tokens", "output_tokens") {
		t.Fatalf("行键集 = %v（无 title、无 cost）", keys(row))
	}
	if statsNum(t, row, "input_tokens") != 17 || statsNum(t, row, "output_tokens") != 20 {
		t.Fatalf("行四列 = %v", row)
	}

	// model：包含匹配、大小写不敏感。
	resp = statsGET(t, e, "/stats/usage?model=glm-5.3")
	if statsNum(t, resp, "total") != 4 {
		t.Fatalf("model=glm-5.3 total = %v, want 4", resp["total"])
	}
	if rows := sids(resp); !eq(rows, "s5", "s4", "s2", "s1") {
		t.Fatalf("model 过滤序 = %v", rows)
	}
	resp = statsGET(t, e, "/stats/usage?model=KIMI")
	if statsNum(t, resp, "total") != 1 || sids(resp)[0] != "s3" {
		t.Fatalf("model=KIMI = %v", resp)
	}

	// project 与 model 叠加。
	resp = statsGET(t, e, "/stats/usage?project=C:/B&model=AIR")
	if statsNum(t, resp, "total") != 1 || sids(resp)[0] != "s2" {
		t.Fatalf("project+model = %v", resp)
	}

	// 分页。
	resp = statsGET(t, e, "/stats/usage?limit=2")
	if rows := sids(resp); !eq(rows, "s5", "s4") || statsNum(t, resp, "total") != 5 {
		t.Fatalf("limit=2 = %v", resp)
	}
	resp = statsGET(t, e, "/stats/usage?limit=2&offset=2")
	if rows := sids(resp); !eq(rows, "s3", "s2") {
		t.Fatalf("offset=2 = %v", resp)
	}
	resp = statsGET(t, e, "/stats/usage?offset=10")
	if l := len(resp["rows"].([]any)); l != 0 || statsNum(t, resp, "total") != 5 {
		t.Fatalf("offset 越界应空页: %v", resp)
	}

	// 时间窗：今天全收;昨日窗零行。
	resp = statsGET(t, e, "/stats/usage?since="+d0.Format("2006-01-02")+
		"&until="+d0.Format("2006-01-02"))
	if statsNum(t, resp, "total") != 5 {
		t.Fatalf("今日窗 total = %v, want 5", resp["total"])
	}
	y := d0.AddDate(0, 0, -1).Format("2006-01-02")
	resp = statsGET(t, e, "/stats/usage?since="+y+"&until="+y)
	if statsNum(t, resp, "total") != 0 {
		t.Fatalf("昨日窗 total = %v, want 0", resp["total"])
	}

	// 红线：title / cost 金额绝不出现。
	var all []string
	walkKeys(resp, &all)
	for _, k := range all {
		if k == "title" || k == "cost" {
			t.Fatalf("明细响应出现 %q（票面禁字段）", k)
		}
	}
}

// ---- 明细端点：上限钳制与坏参 400 ----

func TestStatsUsageClampAndBadParams(t *testing.T) {
	e := newQueryEnv(t)
	withPrices(t, map[string]prices.PriceBook{})
	d0, _, _, _ := statsDays(t, e.t0)
	statsUsageRow(t, e, statsAt(d0, 10), "C:/A", "L1", "s1", "GLM-5.3", 1, 0, 0, 0)
	statsUsageRow(t, e, statsAt(d0, 20), "C:/A", "L1", "s2", "GLM-5.3", 1, 0, 0, 0)

	// limit>500 → 钳到 500 回显。
	resp := statsGET(t, e, "/stats/usage?limit=999")
	if statsNum(t, resp, "limit") != 500 {
		t.Fatalf("limit 钳制回显 = %v, want 500", resp["limit"])
	}
	if l := len(resp["rows"].([]any)); l != 2 {
		t.Fatalf("钳制后行数 = %d, want 2", l)
	}

	for _, q := range []string{"?limit=abc", "?limit=0", "?limit=-1",
		"?offset=-1", "?offset=xyz", "?since=bad"} {
		code, raw := getRaw(t, e.port, "/stats/usage"+q, e.token)
		if code != 400 {
			t.Fatalf("GET /stats/usage%s = %d %q, want 400", q, code, raw)
		}
		var body map[string]any
		if err := json.Unmarshal(raw, &body); err != nil || body["error"] == nil {
			t.Fatalf("GET /stats/usage%s 400 应为 JSON error: %q", q, raw)
		}
	}
}

// ---- CORS：Tauri 壳 webview 轮询两统计端点的预检与回声（票05 接线前提）----

func TestStatsCORSPreflightAndEcho(t *testing.T) {
	e := newQueryEnv(t)
	withPrices(t, map[string]prices.PriceBook{})
	base := fmt.Sprintf("http://127.0.0.1:%d", e.port)

	// 预检（OPTIONS 集中面）：白名单源 204。
	req, _ := http.NewRequest(http.MethodOptions, base+"/stats/summary", nil)
	req.Header.Set("Origin", "http://tauri.localhost")
	req.Header.Set("Access-Control-Request-Method", http.MethodGet)
	cresp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	cresp.Body.Close()
	if cresp.StatusCode != http.StatusNoContent ||
		cresp.Header.Get("Access-Control-Allow-Origin") != "http://tauri.localhost" {
		t.Fatalf("预检 = %d ACAO=%q", cresp.StatusCode,
			cresp.Header.Get("Access-Control-Allow-Origin"))
	}

	// 实际 GET：带 Bearer + Origin → 回声 ACAO;不带 Origin → 不设头。
	get := func(origin string) *http.Response {
		r, _ := http.NewRequest(http.MethodGet, base+"/stats/usage", nil)
		r.Header.Set("Authorization", "Bearer "+e.token)
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(r)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp
	}
	if r := get("http://tauri.localhost"); r.Header.Get("Access-Control-Allow-Origin") !=
		"http://tauri.localhost" {
		t.Fatalf("GET 回声 ACAO 缺失: %q", r.Header.Get("Access-Control-Allow-Origin"))
	}
	if r := get(""); r.Header.Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("无 Origin 不应设 ACAO: %q", r.Header.Get("Access-Control-Allow-Origin"))
	}
}

// containsStr 包含判定（测试内使用,strings.Contains 的直通）。
func containsStr(s, sub string) bool { return strings.Contains(s, sub) }
