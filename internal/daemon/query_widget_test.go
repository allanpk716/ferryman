package daemon

// query_widget_test.go — 票 08 验收钉子：golden 契约形状 / 401 / 单上游失败
// 部分降级 / 缓存窗口内零外呼 / 空表空数组 / 未配置零外呼。
//
// 夹具风格沿 query_report_test.go 的 queryEnv（真 Daemon＋临时端口＋冻结时
// 钟 t0=1.8e9 ≈ 2027-01-15 21:20 +08:00——月界 01-01、周界周一 01-11）；
// 远端经 widgetHTTPClient 缝注入改写传输（quota 包同款零外呼手法）。

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"ferryman/internal/accounts"
	"ferryman/internal/config"
)

// widgetTransportFunc 请求改写传输（所有远端请求重定向到假端点）。
type widgetTransportFunc func(*http.Request) (*http.Response, error)

func (f widgetTransportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// widgetRewriteClient 重定向到 ts 的 client（路径与头原样透传）。
func widgetRewriteClient(t *testing.T, ts *httptest.Server) *http.Client {
	t.Helper()
	host := strings.TrimPrefix(ts.URL, "http://")
	return &http.Client{Transport: widgetTransportFunc(func(r *http.Request) (*http.Response, error) {
		u := *r.URL
		u.Scheme, u.Host = "http", host
		r2 := r.Clone(r.Context())
		r2.URL = &u
		return ts.Client().Do(r2)
	})}
}

// widgetTestReset 缓存清空 + client 注入（用毕还原；测试间互不渗漏）。
func widgetTestReset(t *testing.T, c *http.Client) {
	t.Helper()
	orig := widgetHTTPClient
	widgetHTTPClient = c
	widgetQuotaCache.Lock()
	widgetQuotaCache.m = map[string]*widgetRemote{}
	widgetQuotaCache.Unlock()
	t.Cleanup(func() {
		widgetHTTPClient = orig
		widgetQuotaCache.Lock()
		widgetQuotaCache.m = map[string]*widgetRemote{}
		widgetQuotaCache.Unlock()
	})
}

// widgetGLMUp 智谱上游条目夹具（base_url 大陆站 + 真 key 形态占位）。
func widgetGLMUp(key string) config.DockUpstream {
	return config.DockUpstream{
		BaseURL:  "https://open.bigmodel.cn/api/anthropic",
		APIKey:   key,
		ModelMap: map[string]string{"default": "glm-5.3-flash", "GLM-5.3": "GLM-5.3"},
	}
}

// widgetGET GET /widget/summary 并解成 JSON（断言 200）。
func widgetGET(t *testing.T, e *queryEnv) map[string]any {
	t.Helper()
	code, raw := getRaw(t, e.port, "/widget/summary", e.token)
	if code != 200 {
		t.Fatalf("GET /widget/summary = %d %q", code, raw)
	}
	var resp map[string]any
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("响应非 JSON: %v (%q)", err, raw)
	}
	return resp
}

// metricOf 按键取 metric 行。
func metricOf(t *testing.T, up map[string]any, key string) map[string]any {
	t.Helper()
	ms, _ := up["metrics"].([]any)
	for _, m := range ms {
		if mm, _ := m.(map[string]any); mm != nil && mm["key"] == key {
			return mm
		}
	}
	t.Fatalf("metric %q 缺席: %+v", key, up["metrics"])
	return nil
}

// ---- golden：契约形状全量断言 ----

func TestWidgetSummaryGolden(t *testing.T) {
	e := newQueryEnv(t)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":{"level":"PRO_MAX","limits":[
			{"type":"TOKENS_LIMIT","percentage":38,"nextResetTime":1790300000000,"unit":3},
			{"type":"TOKENS_LIMIT","percentage":92,"nextResetTime":1790700000000,"unit":6}]}}`)
	}))
	defer ts.Close()
	widgetTestReset(t, widgetRewriteClient(t, ts))
	e.d.Cfg.Dock = &config.DockCfg{Active: "智谱",
		Upstreams: map[string]config.DockUpstream{"智谱": widgetGLMUp("sk-glm-secret")}}

	// 台账：两笔本月 usage（GLM-5.3 映射命中，四列合计 2×(1000+200+50+150)=2800）、
	// 一笔本月 usage 映射未中（claude-*，单上游兜底归智谱 4000）、
	// 一笔上月 usage（月桶不收）、一笔上月末 handoff（月/周都不收）、
	// 两笔本周 handoff + 一笔本月周前 handoff（月 3、周 2）。
	mustRec(t, e, "usage", e.t0-100, "C:/proj", "L1", "s1",
		accounts.Fields{"model": "GLM-5.3", "title": "t", "input_tokens": 1000,
			"cache_read_tokens": 200, "cache_creation_tokens": 50, "output_tokens": 150,
			"offset": 1, "subagent": ""})
	mustRec(t, e, "usage", e.t0-86400, "C:/proj", "L1", "s1",
		accounts.Fields{"model": "GLM-5.3", "title": "t", "input_tokens": 1000,
			"cache_read_tokens": 200, "cache_creation_tokens": 50, "output_tokens": 150,
			"offset": 1, "subagent": ""})
	mustRec(t, e, "usage", e.t0-3600, "C:/proj", "L1", "s1",
		accounts.Fields{"model": "claude-opus-5", "title": "t", "input_tokens": 1000,
			"cache_read_tokens": 1000, "cache_creation_tokens": 1000, "output_tokens": 1000,
			"offset": 1, "subagent": ""})
	mustRec(t, e, "usage", e.t0-16*86400, "C:/proj", "L1", "s1", // 2026-12-30：上月
		accounts.Fields{"model": "GLM-5.3", "title": "t", "input_tokens": 999,
			"cache_read_tokens": 0, "cache_creation_tokens": 0, "output_tokens": 0,
			"offset": 1, "subagent": ""})
	// handoff 行按白名单全字段（price_ver=null：本机摆渡 provider 无价格表）
	hf := accounts.Fields{"provider": "local", "model": "qwen3.8-27b", "price_ver": nil,
		"prompt_tokens": 100, "completion_tokens": 50, "outcome": "fresh", "wall_s": 1.0}
	mustRec(t, e, "handoff", e.t0-16*86400, "C:/proj", "L1", "s1", hf)
	hf2 := accounts.Fields{"provider": "local", "model": "qwen3.8-27b", "price_ver": nil,
		"prompt_tokens": 100, "completion_tokens": 50, "outcome": "fresh", "wall_s": 1.0}
	mustRec(t, e, "handoff", e.t0-6*86400, "C:/proj", "L1", "s1", hf2) // 周一前的本月
	hf3 := accounts.Fields{"provider": "local", "model": "qwen3.8-27b", "price_ver": nil,
		"prompt_tokens": 100, "completion_tokens": 50, "outcome": "fresh", "wall_s": 1.0}
	mustRec(t, e, "handoff", e.t0-86400, "C:/proj", "L1", "s1", hf3)
	hf4 := accounts.Fields{"provider": "local", "model": "qwen3.8-27b", "price_ver": nil,
		"prompt_tokens": 100, "completion_tokens": 50, "outcome": "fresh", "wall_s": 1.0}
	mustRec(t, e, "handoff", e.t0-3600, "C:/proj", "L1", "s1", hf4)

	resp := widgetGET(t, e)

	if v, _ := resp["version"].(float64); v != 1 {
		t.Errorf("version = %v, want 1", resp["version"])
	}
	if ga, _ := resp["generated_at"].(string); ga == "" {
		t.Errorf("generated_at 缺席")
	} else if _, err := time.Parse(time.RFC3339, ga); err != nil {
		t.Errorf("generated_at 非 ISO: %q", ga)
	}

	ups, _ := resp["upstreams"].([]any)
	if len(ups) != 1 {
		t.Fatalf("upstreams = %d, want 1", len(ups))
	}
	up, _ := ups[0].(map[string]any)
	if up["id"] != "智谱" || up["kind"] != "coding_plan" || up["label"] != "GLM" {
		t.Errorf("upstream 头 = %v/%v/%v", up["id"], up["kind"], up["label"])
	}
	if up["plan"] != "PRO_MAX" {
		t.Errorf("plan = %v", up["plan"])
	}
	if _, has := up["note"]; has {
		t.Errorf("双窗（V2+ 语义）不应带版本注记: %v", up["note"])
	}

	fh := metricOf(t, up, "window_5h")
	if fh["source"] != "fetched" || fh["remaining_pct"].(float64) != 62 {
		t.Errorf("window_5h = %+v", fh)
	}
	if ra, _ := fh["resets_at"].(string); ra == "" {
		t.Errorf("resets_at 缺席")
	} else if _, err := time.Parse(time.RFC3339, ra); err != nil {
		t.Errorf("resets_at 非 ISO: %q", ra)
	}
	wk := metricOf(t, up, "week")
	if wk["remaining_pct"].(float64) != 8 {
		t.Errorf("week = %+v", wk)
	}
	mt := metricOf(t, up, "month_tokens")
	if mt["source"] != "estimated" || mt["value"].(float64) != 2800+4000 {
		t.Errorf("month_tokens = %+v, want value 6800", mt)
	}
	if mt["text"] != "月 6.8k tok" {
		t.Errorf("month_tokens text = %v", mt["text"])
	}
	// source 枚举仅两值（golden 纪律）
	for _, m := range up["metrics"].([]any) {
		s := m.(map[string]any)["source"]
		if s != "fetched" && s != "estimated" {
			t.Errorf("source 越界枚举: %v", s)
		}
	}

	ho, _ := resp["handoff"].(map[string]any)
	if ho["id"] != "handoff" || ho["kind"] != "handoff" {
		t.Errorf("handoff 头 = %+v", ho)
	}
	hm := metricOf(t, ho, "handoffs_month")
	if hm["value"].(float64) != 3 {
		t.Errorf("handoffs_month = %+v, want 3", hm)
	}
	hw := metricOf(t, ho, "handoffs_week")
	if hw["value"].(float64) != 2 {
		t.Errorf("handoffs_week = %+v, want 2", hw)
	}
}

// ---- 401：无 token/错 token（与既有 GET 面同序） ----

func TestWidgetSummaryAuth(t *testing.T) {
	e := newQueryEnv(t)
	widgetTestReset(t, widgetRewriteClient(t, httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))))
	if code, _ := getRaw(t, e.port, "/widget/summary", ""); code != 401 {
		t.Errorf("无 token = %d, want 401", code)
	}
	if code, _ := getRaw(t, e.port, "/widget/summary", "wrong-token"); code != 401 {
		t.Errorf("错 token = %d, want 401", code)
	}
}

// ---- 单上游失败：部分降级不塌 + 错误类别不含钥/URL ----

func TestWidgetSummaryPartialFailure(t *testing.T) {
	e := newQueryEnv(t)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "kimi") || strings.Contains(r.URL.Path, "usages") {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		fmt.Fprint(w, `{"data":{"limits":[{"type":"TOKENS_LIMIT","percentage":10,"unit":3}]}}`)
	}))
	defer ts.Close()
	widgetTestReset(t, widgetRewriteClient(t, ts))
	e.d.Cfg.Dock = &config.DockCfg{Active: "智谱", Upstreams: map[string]config.DockUpstream{
		"智谱": widgetGLMUp("sk-glm-secret"),
		"Kimi": {BaseURL: "https://api.kimi.com/coding", APIKey: "sk-kimi-secret",
			ModelMap: map[string]string{"default": "kimi-for-coding"}},
	}}

	resp := widgetGET(t, e) // 整体 200：单条失败不塌
	ups, _ := resp["upstreams"].([]any)
	if len(ups) != 2 {
		t.Fatalf("upstreams = %d, want 2", len(ups))
	}
	byID := map[string]map[string]any{}
	for _, u := range ups {
		um, _ := u.(map[string]any)
		byID[um["id"].(string)] = um
	}
	// 智谱正常出环
	if fh := metricOf(t, byID["智谱"], "window_5h"); fh["remaining_pct"].(float64) != 90 {
		t.Errorf("智谱 window_5h = %+v", fh)
	}
	// Kimi 整体失败：error 类别=上游状态 502；串不含钥与 URL
	kerr, _ := byID["Kimi"]["error"].(map[string]any)
	if kerr == nil || kerr["category"] != "上游状态 502" {
		t.Fatalf("Kimi error = %+v", byID["Kimi"]["error"])
	}
	if s := fmt.Sprint(byID["Kimi"]["error"]); strings.Contains(s, "sk-kimi-secret") || strings.Contains(s, "http") {
		t.Errorf("错误串泄漏钥/URL: %s", s)
	}
	// 失败上游仍带台账估算（数据可用性照实）
	if mt := metricOf(t, byID["Kimi"], "month_tokens"); mt["source"] != "estimated" {
		t.Errorf("Kimi month_tokens = %+v", mt)
	}
}

// ---- V1 套餐语义：单窗 + 版本注记（真机 max 档同形） ----

func TestWidgetSummaryGLMV1Note(t *testing.T) {
	e := newQueryEnv(t)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":{"level":"max","limits":[
			{"type":"TIME_LIMIT","unit":5,"percentage":88,"nextResetTime":1790511415998},
			{"type":"TOKENS_LIMIT","unit":3,"percentage":1,"nextResetTime":1790281688643}]}}`)
	}))
	defer ts.Close()
	widgetTestReset(t, widgetRewriteClient(t, ts))
	e.d.Cfg.Dock = &config.DockCfg{Active: "智谱",
		Upstreams: map[string]config.DockUpstream{"智谱": widgetGLMUp("k")}}

	resp := widgetGET(t, e)
	ups, _ := resp["upstreams"].([]any)
	up, _ := ups[0].(map[string]any)
	if up["plan"] != "max" {
		t.Errorf("plan = %v", up["plan"])
	}
	note, _ := up["note"].(string)
	if note == "" || !strings.Contains(note, "V1") {
		t.Errorf("V1 注记缺席: %q", note)
	}
	// 周窗 metric 缺席（缺席窗不造行），5h 在
	metrics := map[string]bool{}
	for _, m := range up["metrics"].([]any) {
		metrics[m.(map[string]any)["key"].(string)] = true
	}
	if !metrics["window_5h"] || metrics["week"] {
		t.Errorf("V1 metric 集 = %v（want 有 5h 无 week）", metrics)
	}
}

// ---- 缓存：同一窗口内重复请求零外呼 ----

func TestWidgetSummaryCacheHit(t *testing.T) {
	e := newQueryEnv(t)
	hits := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		fmt.Fprint(w, `{"data":{"limits":[{"type":"TOKENS_LIMIT","percentage":10,"unit":3}]}}`)
	}))
	defer ts.Close()
	widgetTestReset(t, widgetRewriteClient(t, ts))
	e.d.Cfg.Dock = &config.DockCfg{Active: "智谱",
		Upstreams: map[string]config.DockUpstream{"智谱": widgetGLMUp("k")}}

	widgetGET(t, e)
	widgetGET(t, e)
	widgetGET(t, e)
	if hits != 1 {
		t.Errorf("外呼 %d 次, want 1（缓存窗口内零外呼）", hits)
	}
}

// ---- 空表：空数组而非错误 / 未配置：零外呼 + 「未配置」类别 ----

func TestWidgetSummaryEmptyAndUnconfigured(t *testing.T) {
	e := newQueryEnv(t)
	hits := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
	}))
	defer ts.Close()
	widgetTestReset(t, widgetRewriteClient(t, ts))

	e.d.Cfg.Dock = &config.DockCfg{} // 无表：空数组
	resp := widgetGET(t, e)
	if ups, _ := resp["upstreams"].([]any); len(ups) != 0 {
		t.Errorf("空表应空数组, got %v", resp["upstreams"])
	}

	// 未配置 key：零外呼 + error=未配置（D11 不配不显示的同款诚实）
	e.d.Cfg.Dock = &config.DockCfg{Active: "智谱", Upstreams: map[string]config.DockUpstream{
		"智谱": {BaseURL: "https://open.bigmodel.cn/api/anthropic", APIKey: "",
			ModelMap: map[string]string{"default": "glm-5.3-flash"}}}}
	resp = widgetGET(t, e)
	ups, _ := resp["upstreams"].([]any)
	if len(ups) != 1 {
		t.Fatalf("未配置上游应仍列出（带错误）, got %v", resp["upstreams"])
	}
	um, _ := ups[0].(map[string]any)
	kerr, _ := um["error"].(map[string]any)
	if kerr == nil || kerr["category"] != "未配置" {
		t.Errorf("未配置 error = %+v", um["error"])
	}
	if hits != 0 {
		t.Errorf("未配置不应外呼, got %d", hits)
	}
}

// ---- 纯函数钉子 ----

func TestWidgetTokText(t *testing.T) {
	cases := map[float64]string{
		0:             "月 0 tok",
		980:           "月 980 tok",
		1200:          "月 1.2k tok",
		3_200_000:     "月 3.2M tok",
		4_000_000:     "月 4M tok",
		9_223_359_693: "月 9.2G tok",
	}
	for in, want := range cases {
		if got := widgetTokText(in); got != want {
			t.Errorf("widgetTokText(%v) = %q, want %q", in, got, want)
		}
	}
}

func TestWidgetWeekStart(t *testing.T) {
	// 2027-01-15 是周五 → 周界 2027-01-11 周一 00:00 本地
	fri := time.Date(2027, 1, 15, 21, 20, 0, 0, time.Local)
	want := time.Date(2027, 1, 11, 0, 0, 0, 0, time.Local)
	if got := widgetWeekStart(fri); !got.Equal(want) {
		t.Errorf("widgetWeekStart(周五) = %v, want %v", got, want)
	}
	// 周一当天 → 自身 00:00
	mon := time.Date(2027, 1, 11, 15, 0, 0, 0, time.Local)
	if got := widgetWeekStart(mon); !got.Equal(want) {
		t.Errorf("widgetWeekStart(周一) = %v, want %v", got, want)
	}
	// 周日 → 本周一（不是下周）
	sun := time.Date(2027, 1, 17, 23, 0, 0, 0, time.Local)
	if got := widgetWeekStart(sun); !got.Equal(want) {
		t.Errorf("widgetWeekStart(周日) = %v, want %v", got, want)
	}
}
