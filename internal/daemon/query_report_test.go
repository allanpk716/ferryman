package daemon

// query_report_test.go — 票03：GET /report 验收钉子。
//
// 夹具风格沿 query_sessions_test.go 的 queryEnv（真 Daemon＋临时端口＋冻结时钟）；
// 价格表经 queryReportPrices 缝注入（report.loadPrices 同款接缝——绝不读真用户
// 配置，测试确定性）。成效账期望值按 report.SavingsV1 公式手算钉死：
// 毛 = Σ prefix/per×(p_in−p_cache)，注入 = Σ tokens/per×p_in，
// 交接 = Σ(prompt×p_in+completion×p_out)/per，净 = 毛−注入−交接。

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"ferryman/internal/accounts"
	"ferryman/internal/prices"
)

// withPrices 注入测试价格表（用毕还原）。
func withPrices(t *testing.T, books map[string]prices.PriceBook) {
	t.Helper()
	orig := queryReportPrices
	queryReportPrices = func() map[string]prices.PriceBook { return books }
	t.Cleanup(func() { queryReportPrices = orig })
}

// zpBook 标准夹具价格本：per=10，p_in=2、p_cache=1、p_out=4（数值全整除，
// 手算期望值无浮点毛刺）。
func zpBook(pCache *float64) map[string]prices.PriceBook {
	return map[string]prices.PriceBook{"zp": {Key: "zp", Unit: "分", Per: 10,
		Versions: []prices.PriceVersion{{EffectiveFrom: "2026-01-01",
			PIn: 2, PCache: pCache, POut: 4}}}}
}

// mustRec 记一条账本流水（公共章＋科目字段）。
func mustRec(t *testing.T, e *queryEnv, kind string, ts float64, project, lineage, sid string, f accounts.Fields) {
	t.Helper()
	f["agent"] = "cc"
	f["session_id"] = sid
	f["lineage_id"] = lineage
	f["project"] = project
	if _, err := e.acc.Record(kind, ts, f); err != nil {
		t.Fatalf("Record %s: %v", kind, err)
	}
}

// reportGET GET /report 并解成 JSON（断言 200）。
func reportGET(t *testing.T, e *queryEnv, query string) map[string]any {
	t.Helper()
	code, raw := getRaw(t, e.port, "/report"+query, e.token)
	if code != 200 {
		t.Fatalf("GET /report%s = %d %q", query, code, raw)
	}
	var resp map[string]any
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("响应非 JSON: %v (%q)", err, raw)
	}
	return resp
}

// ---- 项目 scope：四列聚合＋成效账金额＋bypass/无效保温单列 ----

func TestQueryReportProjectScopeAggregates(t *testing.T) {
	e := newQueryEnv(t)
	pc := 1.0
	withPrices(t, zpBook(&pc))
	e.d.Cfg.FerryProvider = "zp"

	// 本项目两笔 block（毛节省 10+5=15）、一笔 bypass（对照组单列，不计节省）、
	// 一笔 inject（8）、一笔 handoff（(30×2+20×4)/10=14）、
	// 两行 usage（四列 300/30/15/70）、
	// 一行无效保温（useless_warm=true，cost 0.5）＋一行有效保温（false，9.9）。
	mustRec(t, e, "block", e.t0-100, "C:/proj", "L1", "r-s1",
		accounts.Fields{"prefix_tokens": 100, "idle_s": 30})
	mustRec(t, e, "block", e.t0-90, "C:/proj", "L1", "r-s1",
		accounts.Fields{"prefix_tokens": 50, "idle_s": 30})
	mustRec(t, e, "bypass", e.t0-80, "C:/proj", "L1", "r-s1",
		accounts.Fields{"prefix_tokens": 70})
	mustRec(t, e, "inject", e.t0-70, "C:/proj", "L1", "r-s1",
		accounts.Fields{"tokens": 40, "handoff_id": "h1"})
	mustRec(t, e, "handoff", e.t0-60, "C:/proj", "L1", "r-s1",
		accounts.Fields{"provider": "zp", "model": "m", "price_ver": "zp@2026-01-01",
			"prompt_tokens": 30, "completion_tokens": 20, "outcome": "ok", "wall_s": 1.0})
	mustRec(t, e, "wait_close", e.t0-50, "C:/proj", "L1", "r-s1",
		accounts.Fields{"lane": "wait", "opened_ts": e.t0 - 100, "closed_ts": e.t0 - 50,
			"dur_s": 50, "beats_fired": 3, "cost_actual": 0.5, "main_resumed": false,
			"useless_warm": true, "close_reason": "window_closed"})
	mustRec(t, e, "wait_close", e.t0-40, "C:/proj", "L1", "r-s1",
		accounts.Fields{"lane": "wait", "opened_ts": e.t0 - 90, "closed_ts": e.t0 - 40,
			"dur_s": 50, "beats_fired": 2, "cost_actual": 9.9, "main_resumed": true,
			"useless_warm": false, "close_reason": "window_closed"})
	mustRec(t, e, "usage", e.t0-30, "C:/proj", "L1", "r-s1",
		accounts.Fields{"model": "glm-5.3", "title": "t", "input_tokens": 100,
			"cache_read_tokens": 10, "cache_creation_tokens": 5, "output_tokens": 30, "offset": 0})
	mustRec(t, e, "usage", e.t0-20, "C:/proj", "L1", "r-s2",
		accounts.Fields{"model": "glm-5.3", "title": "t", "input_tokens": 200,
			"cache_read_tokens": 20, "cache_creation_tokens": 10, "output_tokens": 40, "offset": 0})
	// 他项目流水不得串入。
	mustRec(t, e, "block", e.t0-100, "C:/other", "L9", "r-s3",
		accounts.Fields{"prefix_tokens": 999, "idle_s": 30})
	mustRec(t, e, "usage", e.t0-30, "C:/other", "L9", "r-s3",
		accounts.Fields{"model": "glm-5.3", "title": "t", "input_tokens": 99999,
			"cache_read_tokens": 0, "cache_creation_tokens": 0, "output_tokens": 0, "offset": 0})

	resp := reportGET(t, e, "?scope=project&key=C:/proj")
	if resp["scope"] != "project" || resp["key"] != "C:/proj" {
		t.Fatalf("scope/key 回显: %v / %v", resp["scope"], resp["key"])
	}
	tk := resp["tokens"].(map[string]any)
	if tk["input_tokens"] != 300.0 || tk["cache_read_tokens"] != 30.0 ||
		tk["cache_creation_tokens"] != 15.0 || tk["output_tokens"] != 70.0 ||
		tk["requests"] != 2.0 {
		t.Fatalf("四列聚合: %v", tk)
	}
	sv := resp["savings"].(map[string]any)
	if sv["formula"] != "v1" {
		t.Fatalf("成效公式章 = %v, want v1（单源 report.SavingsV1）", sv["formula"])
	}
	tot := sv["totals"].(map[string]any)
	// 毛 15 · 注入 8 · 交接 14 · 净 −7（bypass 单列对照组，不进净额）。
	if tot["blocks"] != 2.0 || tot["bypass"] != 1.0 || tot["injects"] != 1.0 ||
		tot["handoffs"] != 1.0 {
		t.Fatalf("科目计数: %v", tot)
	}
	if tot["gross"] != 15.0 || tot["inject_cost"] != 8.0 ||
		tot["handoff_cost"] != 14.0 || tot["net"] != -7.0 {
		t.Fatalf("成效账金额: %v", tot)
	}
	if lins := sv["lineages"].([]any); len(lins) != 1 ||
		lins[0].(map[string]any)["lineage_id"] != "L1" {
		t.Fatalf("族系行: %v", sv["lineages"])
	}
	if resp["savings_computable"] != true || resp["savings_note"] != "" {
		t.Fatalf("可算标注: %v / %v", resp["savings_computable"], resp["savings_note"])
	}
	if resp["econ_provider"] != "zp" {
		t.Fatalf("econ_provider = %v", resp["econ_provider"])
	}
	// 无效保温单列：只数 useless_warm=true 行（有效保温 9.9 不混入）。
	uw := resp["useless_warm"].(map[string]any)
	if uw["count"] != 1.0 || uw["cost_actual"] != 0.5 {
		t.Fatalf("无效保温单列: %v", uw)
	}
}

// ---- 会话 scope ----

func TestQueryReportSessionScope(t *testing.T) {
	e := newQueryEnv(t)
	pc := 1.0
	withPrices(t, zpBook(&pc))
	e.d.Cfg.FerryProvider = "zp"

	mustRec(t, e, "block", e.t0-10, "C:/proj", "LQ", "q-s1",
		accounts.Fields{"prefix_tokens": 100, "idle_s": 30})
	mustRec(t, e, "block", e.t0-10, "C:/proj", "LQ", "q-s2",
		accounts.Fields{"prefix_tokens": 500, "idle_s": 30})

	// 会话 scope：只聚合该会话的行。
	resp := reportGET(t, e, "?scope=session&key=q-s1")
	if tot := resp["savings"].(map[string]any)["totals"].(map[string]any); tot["blocks"] != 1.0 || tot["gross"] != 10.0 {
		t.Fatalf("session scope 应只含 q-s1: %v", tot)
	}
	// 存在但无流水的 key：合法 → 200 零值（不是错误）。
	resp = reportGET(t, e, "?scope=session&key=q-nope")
	if tot := resp["savings"].(map[string]any)["totals"].(map[string]any); tot["blocks"] != 0.0 {
		t.Fatalf("无流水 key 应零值: %v", tot)
	}
}

// ---- 月 scope ----

func TestQueryReportMonthScope(t *testing.T) {
	e := newQueryEnv(t)
	pc := 1.0
	withPrices(t, zpBook(&pc))
	e.d.Cfg.FerryProvider = "zp"

	// 本月内 1 笔，下月 1 笔被排除。
	ms := time.Unix(int64(e.t0), 0).In(time.Local)
	monthStart := time.Date(ms.Year(), ms.Month(), 1, 0, 0, 0, 0, time.Local)
	monthKey := monthStart.Format("2006-01")
	nextStart := monthStart.AddDate(0, 1, 0)
	mustRec(t, e, "block", e.t0-10, "C:/proj", "LM", "m-s1",
		accounts.Fields{"prefix_tokens": 100, "idle_s": 30})
	mustRec(t, e, "block", float64(nextStart.Unix())+100, "C:/proj", "LM", "m-s1",
		accounts.Fields{"prefix_tokens": 777, "idle_s": 30})

	resp := reportGET(t, e, "?scope=month&key="+monthKey)
	if tot := resp["savings"].(map[string]any)["totals"].(map[string]any); tot["blocks"] != 1.0 || tot["gross"] != 10.0 {
		t.Fatalf("month scope 应只含本月: %v", tot)
	}
	if resp["key"] != monthKey {
		t.Fatalf("month key 回显 = %v", resp["key"])
	}
}

// ---- 「节省额不可算」标注路径（价格表缺 p_cache：如实标注，不硬算） ----

func TestQueryReportSavingsNotComputableWithoutPCache(t *testing.T) {
	e := newQueryEnv(t)
	withPrices(t, zpBook(nil)) // p_cache 缺（nil）
	e.d.Cfg.FerryProvider = "zp"

	mustRec(t, e, "block", e.t0-10, "C:/proj", "LN", "n-s1",
		accounts.Fields{"prefix_tokens": 100, "idle_s": 30})

	resp := reportGET(t, e, "?scope=session&key=n-s1")
	if resp["savings_computable"] != false {
		t.Fatalf("缺 p_cache 应标不可算: %v", resp["savings_computable"])
	}
	note, _ := resp["savings_note"].(string)
	if !strings.Contains(note, "节省额不可算") || !strings.Contains(note, "p_cache") {
		t.Fatalf("标注文案应如实说明缺 p_cache: %q", note)
	}
	// 不硬算：有 block 行但毛节省必须为 0（p_cache 缺 → 逐行跳过，SavingsV1 口径）。
	tot := resp["savings"].(map[string]any)["totals"].(map[string]any)
	if tot["blocks"] != 1.0 || tot["gross"] != 0.0 {
		t.Fatalf("不硬算：block 计数在、毛节省为 0: %v", tot)
	}
}

// ---- scope/key 非法 → 400 JSON ----

func TestQueryReportInvalidScopeAndKey(t *testing.T) {
	e := newQueryEnv(t)
	pc := 1.0
	withPrices(t, zpBook(&pc))

	for _, query := range []string{
		"",                            // 缺 scope
		"?key=x",                      // 缺 scope
		"?scope=bogus&key=x",          // scope 非法
		"?scope=project",              // project 缺 key
		"?scope=session&key=",         // session 缺 key（空值视同缺省）
		"?scope=month",                // month 缺 key
		"?scope=month&key=2026-13",    // 月份越界
		"?scope=month&key=not-a-date", // 格式坏
	} {
		code, raw := getRaw(t, e.port, "/report"+query, e.token)
		if code != 400 {
			t.Fatalf("GET /report%s = %d %q, want 400", query, code, raw)
		}
		var body map[string]any
		if err := json.Unmarshal(raw, &body); err != nil || body["error"] == nil {
			t.Fatalf("GET /report%s 400 应为 JSON error: %q", query, raw)
		}
	}
}
