// quota_test.go — 票 06 验收钉子：三家查询器表驱动 + httptest 假端点（零真实
// 外呼）+ 错误串不含钥/URL 断言（含 net/url.Error 包裹文本防御）。
//
// 夹具形状全部来自 cc-switch 源码实证（coding_plan.rs / balance.rs 注释里
// 的实测形态）与票 06 钉死的分支清单。
package quota

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"ferryman/internal/config"
)

// rewriteClient 把所有请求重定向到 ts 的 http.Client（生产代码从 base_url
// 派生真实主机——端点主机不可注入，测试用传输层重写做零外呼等价验证；
// 请求路径与头原样透传，服务端可断言鉴权形态）。
func rewriteClient(t *testing.T, ts *httptest.Server) *http.Client {
	t.Helper()
	return &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		u := *r.URL
		u.Scheme, u.Host = "http", strings.TrimPrefix(ts.URL, "http://")
		r2 := r.Clone(r.Context())
		r2.URL = &u
		return ts.Client().Do(r2)
	})}
}

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// glmFixture 智谱双窗夹具（新套餐：unit:3 + unit:6）。
func glmFixture(t *testing.T) *GLMQuota {
	t.Helper()
	body := []byte(`{"success":true,"msg":"","data":{"level":"PRO_MAX","limits":[
		{"type":"TOKENS_LIMIT","percentage":38,"nextResetTime":1790300000000,"unit":3,"number":5},
		{"type":"TOKENS_LIMIT","percentage":92,"nextResetTime":1790700000000,"unit":6,"number":1}]}}`)
	q, err := parseGLM(body)
	if err != nil {
		t.Fatalf("parseGLM: %v", err)
	}
	return q
}

func TestParseGLMDualWindows(t *testing.T) {
	q := glmFixture(t)
	if q.Plan != "PRO_MAX" {
		t.Errorf("Plan = %q, want PRO_MAX", q.Plan)
	}
	if q.FiveHour == nil || q.Week == nil {
		t.Fatalf("双窗缺一: %+v", q)
	}
	// percentage=已用%：38→剩 62、92→剩 8（mock 契约演示值同源）
	if q.FiveHour.RemainingPct != 62 || q.Week.RemainingPct != 8 {
		t.Errorf("剩余 = %v/%v, want 62/8", q.FiveHour.RemainingPct, q.Week.RemainingPct)
	}
	if !q.FiveHour.HasReset || q.FiveHour.ResetsAt != time.UnixMilli(1790300000000) {
		t.Errorf("5h 重置 = %v has=%v", q.FiveHour.ResetsAt, q.FiveHour.HasReset)
	}
	if !q.Week.HasReset {
		t.Errorf("周重置缺席")
	}
	if q.FiveHour.HasAbs {
		t.Errorf("GLM 端点无绝对值，HasAbs 必须为 false")
	}
}

func TestParseGLMOldPlanSingleEntry(t *testing.T) {
	// 老套餐（2026-02-12 前）：单条无 unit → 兜底启发式归 5h，周窗降级缺席
	body := []byte(`{"data":{"limits":[
		{"type":"TOKENS_LIMIT","percentage":55,"nextResetTime":1790300000000}]}}`)
	q, err := parseGLM(body)
	if err != nil {
		t.Fatalf("parseGLM: %v", err)
	}
	if q.FiveHour == nil || q.Week != nil {
		t.Fatalf("老套餐应单环: %+v", q)
	}
	if q.FiveHour.RemainingPct != 45 {
		t.Errorf("剩余 = %v, want 45", q.FiveHour.RemainingPct)
	}
}

func TestParseGLMOldPlanDualEntriesHeuristic(t *testing.T) {
	// 老形态双条（全无 unit）：启发式=无 reset 优先归 5h、其余 reset 升序填槽
	body := []byte(`{"data":{"limits":[
		{"type":"TOKENS_LIMIT","percentage":20,"nextResetTime":1790700000000},
		{"type":"TOKENS_LIMIT","percentage":60}]}}`)
	q, err := parseGLM(body)
	if err != nil {
		t.Fatalf("parseGLM: %v", err)
	}
	if q.FiveHour == nil || q.Week == nil {
		t.Fatalf("老形态双条应双窗: %+v", q)
	}
	if q.FiveHour.RemainingPct != 40 { // 无 reset 的 60% 条归 5h
		t.Errorf("5h 剩余 = %v, want 40", q.FiveHour.RemainingPct)
	}
	if q.Week.RemainingPct != 80 {
		t.Errorf("周剩余 = %v, want 80", q.Week.RemainingPct)
	}
}

func TestParseGLMV1ShapeReal(t *testing.T) {
	// V1 套餐真机形状（2026-09-25 max 档实测）：TIME_LIMIT(unit:5 工具/时间
	// 额度) 过滤 + TOKENS_LIMIT(unit:3) 单窗——无周窗（V1 无周/月限制）。
	body := []byte(`{"code":200,"msg":"操作成功","data":{"limits":[
		{"type":"TIME_LIMIT","unit":5,"number":1,"usage":4000,"currentValue":3526,
		 "remaining":474,"percentage":88,"nextResetTime":1790511415998,
		 "usageDetails":[{"modelCode":"search-prime","usage":2987}]},
		{"type":"TOKENS_LIMIT","unit":3,"number":5,"percentage":1,
		 "nextResetTime":1790281688643}],"level":"max"},"success":true}`)
	q, err := parseGLM(body)
	if err != nil {
		t.Fatalf("parseGLM: %v", err)
	}
	if q.Plan != "max" {
		t.Errorf("plan = %q, want max", q.Plan)
	}
	if q.FiveHour == nil || q.Week != nil {
		t.Fatalf("V1 应仅 5h 单环: %+v", q)
	}
	if q.FiveHour.RemainingPct != 99 { // percentage=1 已用 → 剩 99
		t.Errorf("5h 剩余 = %v, want 99", q.FiveHour.RemainingPct)
	}
}

func TestParseGLMUnknownUnitNotMisfiled(t *testing.T) {
	// 版本差异防错标（V2+ 假想月窗 unit:9）：unit 在场 → 严格锚定，未知 unit
	// 条目不进周槽（宁可缺失展示，不错标数据）。
	body := []byte(`{"data":{"limits":[
		{"type":"TOKENS_LIMIT","unit":3,"percentage":10,"nextResetTime":1790281688643},
		{"type":"TOKENS_LIMIT","unit":9,"percentage":50,"nextResetTime":1790900000000}]}}`)
	q, err := parseGLM(body)
	if err != nil {
		t.Fatalf("parseGLM: %v", err)
	}
	if q.FiveHour == nil {
		t.Fatalf("5h 窗缺席: %+v", q)
	}
	if q.Week != nil {
		t.Fatalf("未知 unit:9 不得错标进周槽: %+v", q.Week)
	}
}

func TestParseGLMTypeAnchorTwoStates(t *testing.T) {
	// type 大小写不敏感 + CREDIT_LIMIT 改名两态都认；未知 type 过滤
	cases := []struct {
		typ  string
		want bool // 是否产出窗口
	}{
		{"TOKENS_LIMIT", true},
		{"tokens_limit", true},
		{"CREDIT_LIMIT", true},
		{"Credit_Limit", true},
		{"CONCURRENCY_LIMIT", false},
		{"", false},
	}
	for _, c := range cases {
		body := []byte(fmt.Sprintf(`{"data":{"limits":[
			{"type":%q,"percentage":10,"nextResetTime":1790300000000,"unit":3}]}}`, c.typ))
		q, err := parseGLM(body)
		if !c.want {
			if err == nil {
				t.Errorf("type=%q 应无窗口（err）", c.typ)
			}
			continue
		}
		if err != nil || q.FiveHour == nil {
			t.Errorf("type=%q 应有 5h 窗: err=%v q=%+v", c.typ, err, q)
		}
	}
}

func TestParseGLMBusinessError(t *testing.T) {
	_, err := parseGLM([]byte(`{"success":false,"msg":"quota exceeded","data":null}`))
	if err == nil || !strings.HasPrefix(err.Error(), "上游业务错误：") {
		t.Fatalf("业务错误类别缺失: %v", err)
	}
	if !strings.Contains(err.Error(), "quota exceeded") {
		t.Errorf("msg 原文应保留")
	}
}

func TestParseGLMBadShapes(t *testing.T) {
	for name, body := range map[string][]byte{
		"非JSON":      []byte(`not json`),
		"无data信封":    []byte(`{"success":true}`),
		"空limits":    []byte(`{"data":{"limits":[]}}`),
		"data为 null": []byte(`{"data":null}`),
	} {
		if _, err := parseGLM(body); err == nil {
			t.Errorf("%s: 应报错", name)
		} else if !strings.Contains(err.Error(), "解析失败") {
			t.Errorf("%s: 错误类别=%q", name, err)
		}
	}
}

// ---- Kimi ----

func TestParseKimi(t *testing.T) {
	body := []byte(`{
		"usage": {"limit": 5000, "remaining": 850, "resetTime": "2026-09-28T00:00:00+08:00"},
		"limits": [{"detail": {"limit": "1500", "remaining": "1200", "resetTime": 1790280000}}]
	}`)
	q, err := parseKimi(body)
	if err != nil {
		t.Fatalf("parseKimi: %v", err)
	}
	if q.FiveHour == nil || q.Week == nil {
		t.Fatalf("双窗缺一: %+v", q)
	}
	// 数字/字符串两形态通吃；remaining/limit 归一化（周 850/5000=17%、5h 1200/1500=80%）
	if q.FiveHour.RemainingPct != 80 || q.Week.RemainingPct != 17 {
		t.Errorf("剩余%% = %v/%v, want 80/17", q.FiveHour.RemainingPct, q.Week.RemainingPct)
	}
	if !q.FiveHour.HasAbs || q.FiveHour.Remaining != 1200 || q.FiveHour.Limit != 1500 {
		t.Errorf("5h 绝对值 = %v/%v has=%v", q.FiveHour.Remaining, q.FiveHour.Limit, q.FiveHour.HasAbs)
	}
	// resetTime 两格式：字符串 ISO 原生 + 秒级数字自动判位
	// （期望用固定 +08:00 区——夹具即 +08:00，机器时区无关）
	want := time.Date(2026, 9, 28, 0, 0, 0, 0, time.FixedZone("", 8*3600))
	if !q.Week.ResetsAt.Equal(want) {
		t.Errorf("周 resetTime = %v, want %v", q.Week.ResetsAt, want)
	}
	if !q.FiveHour.HasReset || q.FiveHour.ResetsAt != time.Unix(1790280000, 0) {
		t.Errorf("5h resetTime 秒级判位 = %v", q.FiveHour.ResetsAt)
	}
}

func TestParseKimiPercentScaleDefense(t *testing.T) {
	// limit=100 整数：疑百分比口径——归一化照出、绝对数不展示
	q, err := parseKimi([]byte(`{"usage":{"limit":100,"remaining":25}}`))
	if err != nil {
		t.Fatalf("parseKimi: %v", err)
	}
	if q.Week.RemainingPct != 25 {
		t.Errorf("剩余%% = %v, want 25", q.Week.RemainingPct)
	}
	if q.Week.HasAbs {
		t.Errorf("limit≈100 整数口径：绝对数不应展示")
	}
}

func TestResetTimeOfStringForms(t *testing.T) {
	// 数字串兼容（票 01）：ISO 布局全败后按纯数字解析、秒/毫秒自动判位
	// （阈值 1e12，与数字形态同款）；非数字/空串/≤0 判无重置；TrimSpace
	// 顺带放宽带空白串（显式申报的宽容化）。
	cases := []struct {
		name string
		in   string
		want time.Time
		ok   bool
	}{
		{"秒级数字串", "1761412800", time.Unix(1761412800, 0), true},
		{"毫秒级数字串", "1790280000000", time.UnixMilli(1790280000000), true},
		{"ISO 不回归", "2026-09-28T00:00:00+08:00",
			time.Date(2026, 9, 28, 0, 0, 0, 0, time.FixedZone("", 8*3600)), true},
		{"带空白数字串", " 1761412800 ", time.Unix(1761412800, 0), true},
		{"带空白ISO", "\t2026-09-28T00:00:00+08:00\n",
			time.Date(2026, 9, 28, 0, 0, 0, 0, time.FixedZone("", 8*3600)), true},
		{"非数字", "abc", time.Time{}, false},
		{"空串", "", time.Time{}, false},
		{"零", "0", time.Time{}, false},
		{"负数", "-5", time.Time{}, false},
	}
	for _, c := range cases {
		got, ok := resetTimeOf(c.in)
		if ok != c.ok {
			t.Errorf("%s: ok = %v, want %v", c.name, ok, c.ok)
			continue
		}
		if ok && !got.Equal(c.want) {
			t.Errorf("%s: 时刻 = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestParseKimiNumericStringResetTime(t *testing.T) {
	// 全链路（票 01）：resetTime 以数字字符串到达——周窗顶层 usage 秒级、
	// 5h limits[].detail 毫秒级；修复前 ISO 全败即判无重置，倒计时静默丢失。
	body := []byte(`{
		"usage": {"limit": 5000, "remaining": 2500, "resetTime": "1790280000"},
		"limits": [{"detail": {"limit": "1500", "remaining": "1200", "resetTime": "1790280000000"}}]
	}`)
	q, err := parseKimi(body)
	if err != nil {
		t.Fatalf("parseKimi: %v", err)
	}
	if q.Week == nil || q.FiveHour == nil {
		t.Fatalf("双窗缺一: %+v", q)
	}
	if !q.Week.HasReset || q.Week.ResetsAt != time.Unix(1790280000, 0) {
		t.Errorf("周 resetTime = %v has=%v, want Unix(1790280000)", q.Week.ResetsAt, q.Week.HasReset)
	}
	if !q.FiveHour.HasReset || q.FiveHour.ResetsAt != time.UnixMilli(1790280000000) {
		t.Errorf("5h resetTime = %v has=%v, want UnixMilli(1790280000000)", q.FiveHour.ResetsAt, q.FiveHour.HasReset)
	}
}

func TestParseKimiBadShapes(t *testing.T) {
	for name, body := range map[string][]byte{
		"非JSON":   []byte(`{`),
		"两窗全缺":    []byte(`{}`),
		"limit≤0": []byte(`{"usage":{"limit":0,"remaining":5}}`),
	} {
		if _, err := parseKimi(body); err == nil {
			t.Errorf("%s: 应报错", name)
		}
	}
}

// ---- DeepSeek ----

func TestParseDeepSeek(t *testing.T) {
	// 金额为字符串字面量（官方文档形态）；拆分字段透传
	body := []byte(`{"is_available":true,"balance_infos":[
		{"currency":"CNY","total_balance":"110.00","granted_balance":"10.00","topped_up_balance":"100.00"}]}`)
	b, err := parseDeepSeek(body)
	if err != nil {
		t.Fatalf("parseDeepSeek: %v", err)
	}
	if b.Total != "110.00" || b.Granted != "10.00" || b.ToppedUp != "100.00" {
		t.Errorf("金额拆分 = %v/%v/%v", b.Total, b.Granted, b.ToppedUp)
	}
	if !b.Available {
		t.Errorf("缺省应可用")
	}
}

func TestParseDeepSeekNumericAndUnavailable(t *testing.T) {
	// 数字金额形态 + is_available=false
	body := []byte(`{"is_available":false,"balance_infos":[
		{"currency":"CNY","total_balance":0,"granted_balance":0,"topped_up_balance":0}]}`)
	b, err := parseDeepSeek(body)
	if err != nil {
		t.Fatalf("parseDeepSeek: %v", err)
	}
	if b.Total != "0" || b.Available {
		t.Errorf("数字形态/不可用: %+v", b)
	}
}

func TestParseDeepSeekBadShapes(t *testing.T) {
	for name, body := range map[string][]byte{
		"非JSON":  []byte(`x`),
		"空数组":    []byte(`{"balance_infos":[]}`),
		"缺total": []byte(`{"balance_infos":[{"granted_balance":"1"}]}`),
	} {
		if _, err := parseDeepSeek(body); err == nil {
			t.Errorf("%s: 应报错", name)
		}
	}
}

func TestParseGLMToolsQuota(t *testing.T) {
	// MCP 工具增值服务额度（TIME_LIMIT unit:5，按次数，所有版本/档位都有——
	// 2026-09-25 用户口径；字段语义真机实证）
	body := []byte(`{"data":{"limits":[
		{"type":"TIME_LIMIT","unit":5,"number":1,"usage":4000,"currentValue":3526,
		 "remaining":474,"percentage":88,"nextResetTime":1790511415998,
		 "usageDetails":[{"modelCode":"search-prime","usage":2987},
		                 {"modelCode":"web-reader","usage":538},{"modelCode":"zread","usage":1}]},
		{"type":"TOKENS_LIMIT","unit":3,"percentage":1,"nextResetTime":1790281688643}]}}`)
	q, err := parseGLM(body)
	if err != nil {
		t.Fatalf("parseGLM: %v", err)
	}
	tq := q.Tools
	if tq == nil {
		t.Fatalf("工具额度缺席")
	}
	if tq.Total != 4000 || tq.Remaining != 474 {
		t.Errorf("总额/剩余 = %v/%v, want 4000/474", tq.Total, tq.Remaining)
	}
	if mathRound(tq.RemainingPct) != 12 { // 474/4000=11.85≈12
		t.Errorf("剩余%% = %v, want ≈12", tq.RemainingPct)
	}
	if !tq.HasReset || tq.ResetsAt != time.UnixMilli(1790511415998) {
		t.Errorf("重置 = %v has=%v", tq.ResetsAt, tq.HasReset)
	}
	if len(tq.Details) != 3 || tq.Details[0].Name != "search-prime" || tq.Details[0].Used != 2987 {
		t.Errorf("明细 = %+v", tq.Details)
	}
	// 配额窗不受影响：5h 照出
	if q.FiveHour == nil || q.FiveHour.RemainingPct != 99 {
		t.Errorf("5h = %+v", q.FiveHour)
	}
}

func TestParseGLMToolsFallbackAndBadShapes(t *testing.T) {
	// 缺 remaining → 回落 100−percentage
	q, err := parseGLM([]byte(`{"data":{"limits":[
		{"type":"time_limit","unit":5,"usage":"5000","percentage":30},
		{"type":"TOKENS_LIMIT","unit":3,"percentage":1}]}}`))
	if err != nil {
		t.Fatalf("parseGLM: %v", err)
	}
	if q.Tools == nil || mathRound(q.Tools.RemainingPct) != 70 || q.Tools.Total != 5000 {
		t.Errorf("回落口径: %+v", q.Tools)
	}
	// 总额度≤0 / 非 unit:5 的 TIME_LIMIT → 不造数
	for name, body := range map[string][]byte{
		"总额0": []byte(`{"data":{"limits":[{"type":"TIME_LIMIT","unit":5,"usage":0,"remaining":0},
			{"type":"TOKENS_LIMIT","unit":3,"percentage":1}]}}`),
		"unit错": []byte(`{"data":{"limits":[{"type":"TIME_LIMIT","unit":7,"usage":100,"remaining":9},
			{"type":"TOKENS_LIMIT","unit":3,"percentage":1}]}}`),
		"绝对数全缺": []byte(`{"data":{"limits":[{"type":"TIME_LIMIT","unit":5,"usage":100},
			{"type":"TOKENS_LIMIT","unit":3,"percentage":1}]}}`),
	} {
		q, err := parseGLM(body)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if q.Tools != nil {
			t.Errorf("%s: 不应造工具额度: %+v", name, q.Tools)
		}
	}
}

// mathRound 半价四舍五入（仅测试断言用）。
func mathRound(v float64) float64 {
	return float64(int64(v + 0.5))
}

// ---- Wire 层（httptest：鉴权形态/超时/状态码/未配置/错误串防泄漏） ----

func TestFetchGLMWireBareKeyAndPath(t *testing.T) {
	const key = "sk-test-key-xyz"
	var gotAuth, gotPath string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotPath = r.Header.Get("Authorization"), r.URL.Path
		fmt.Fprint(w, `{"data":{"limits":[{"type":"TOKENS_LIMIT","percentage":10,"unit":3}]}}`)
	}))
	defer ts.Close()
	q, err := FetchGLMWithClient(rewriteClient(t, ts), &config.DockUpstream{
		BaseURL: "https://open.bigmodel.cn/api/anthropic", APIKey: key})
	if err != nil || q == nil || q.FiveHour == nil {
		t.Fatalf("FetchGLM = %v, %v", q, err)
	}
	if gotAuth != key { // 裸 key：不带 Bearer（cc-switch 先例）
		t.Errorf("Authorization = %q, want 裸 key", gotAuth)
	}
	if gotPath != glmQuotaPath {
		t.Errorf("path = %q, want %q", gotPath, glmQuotaPath)
	}
}

func TestFetchKimiAndDeepSeekWireBearer(t *testing.T) {
	const key = "sk-kimi"
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if auth := r.Header.Get("Authorization"); auth != "Bearer "+key {
			t.Errorf("Authorization = %q, want Bearer", auth)
		}
		if r.URL.Path == "/coding/v1/usages" {
			fmt.Fprint(w, `{"usage":{"limit":5000,"remaining":4000}}`)
			return
		}
		fmt.Fprint(w, `{"balance_infos":[{"total_balance":"9.9"}]}`)
	}))
	defer ts.Close()
	c := rewriteClient(t, ts)
	if _, err := FetchKimiWithClient(c, &config.DockUpstream{BaseURL: "https://api.kimi.com/x", APIKey: key}); err != nil {
		t.Errorf("FetchKimi: %v", err)
	}
	if _, err := FetchDeepSeekWithClient(c, &config.DockUpstream{BaseURL: "https://api.deepseek.com/x", APIKey: key}); err != nil {
		t.Errorf("FetchDeepSeek: %v", err)
	}
}

func TestFetchWireErrorCategories(t *testing.T) {
	key := "sk-secret-key-abc"
	up := &config.DockUpstream{BaseURL: "https://open.bigmodel.cn/api/anthropic", APIKey: key}

	t.Run("未配置零HTTP", func(t *testing.T) {
		if _, err := FetchGLMWithClient(rewriteClient(t, httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			t.Errorf("未配置不应外呼")
		}))), nil); !errors.Is(err, ErrNotConfigured) {
			t.Errorf("nil 条目应未配置, got %v", err)
		}
		if _, err := FetchGLMWithClient(nil, &config.DockUpstream{BaseURL: "https://open.bigmodel.cn/x"}); !errors.Is(err, ErrNotConfigured) {
			t.Errorf("无 key 应未配置, got %v", err)
		}
	})

	t.Run("上游状态", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusBadGateway)
		}))
		defer ts.Close()
		_, err := FetchGLMWithClient(rewriteClient(t, ts), up)
		if err == nil || err.Error() != "上游状态 502" {
			t.Errorf("err = %v, want 上游状态 502", err)
		}
	})

	t.Run("超时", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			time.Sleep(300 * time.Millisecond)
		}))
		defer ts.Close()
		c := rewriteClient(t, ts)
		c.Timeout = 50 * time.Millisecond
		_, err := FetchGLMWithClient(c, up)
		if err == nil || err.Error() != "网络错误（超时）" {
			t.Errorf("err = %v, want 网络错误（超时）", err)
		}
	})

	t.Run("连接拒绝→网络错误且不含URL", func(t *testing.T) {
		// 传输层重写到无人监听端口：真实走 net 层拿连接拒绝错误（url.Error
		// 包裹文本防御——错误串绝不含 URL 原文），路径与生产同源。
		plain := &http.Client{}
		c := &http.Client{}
		c.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
			u := *r.URL
			u.Scheme, u.Host = "http", "127.0.0.1:1"
			r2 := r.Clone(r.Context())
			r2.URL = &u
			return plain.Do(r2)
		})
		_, err := FetchGLMWithClient(c, up)
		if err == nil || err.Error() != "网络错误" {
			t.Errorf("err = %v, want 网络错误", err)
		}
		if err != nil && (strings.Contains(err.Error(), key) || strings.Contains(err.Error(), "http")) {
			t.Errorf("错误串泄漏钥/URL: %q", err)
		}
	})
}

func TestDetectKind(t *testing.T) {
	cases := map[string]string{
		"https://open.bigmodel.cn/api/anthropic": "glm",
		"https://api.z.ai/api/anthropic":         "glm",
		"https://API.KIMI.COM/coding":            "kimi",
		"https://api.deepseek.com/v1":            "deepseek",
		"https://api.openai.com/v1":              "",
		"http://127.0.0.1:15721":                 "",
	}
	for in, want := range cases {
		if got := DetectKind(in); got != want {
			t.Errorf("DetectKind(%q) = %q, want %q", in, got, want)
		}
	}
}
