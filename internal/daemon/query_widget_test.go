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
	"io"
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
			{"type":"TIME_LIMIT","unit":5,"usage":4000,"currentValue":3526,"remaining":474,
			 "percentage":88,"nextResetTime":1790511415998,
			 "usageDetails":[{"modelCode":"search-prime","usage":2987},{"modelCode":"web-reader","usage":538}]},
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
	// 周窗 metric 缺席（缺席窗不造行），5h 在；工具额度在（真机同形 TIME_LIMIT）
	metrics := map[string]bool{}
	for _, m := range up["metrics"].([]any) {
		metrics[m.(map[string]any)["key"].(string)] = true
	}
	if !metrics["window_5h"] || metrics["week"] {
		t.Errorf("V1 metric 集 = %v（want 有 5h 无 week）", metrics)
	}
	if !metrics["tools_quota"] {
		t.Fatalf("工具额度 metric 缺席: %v", metrics)
	}
	tq := metricOf(t, up, "tools_quota")
	if v := tq["remaining_pct"].(float64); v < 11.8 || v > 11.9 { // 474/4000=11.85
		t.Errorf("tools remaining_pct = %v, want ≈11.85", v)
	}
	if tq["abs"] != "474 / 4000" {
		t.Errorf("tools abs = %v", tq["abs"])
	}
	ds, _ := tq["details"].([]any)
	if len(ds) != 2 { // 夹具 TIME_LIMIT usageDetails 两条
		t.Fatalf("tools details = %v", tq["details"])
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

// ---- CORS：壳内 webview 跨源 fetch 的预检与响应头（票08 补遗） ----
// Tauri 壳页面源是 tauri.localhost，fetch 127.0.0.1:15700 且带 Authorization 头
// → WebView2 强制预检；daemon 不答预检则 fetch 永远失败（widget 恒「不可达」
// ——2026-09-28 真机所见，curl 不走浏览器 CORS 故终检未逮）。

// widgetPreflightDo 发一条不带 Bearer 的 OPTIONS 预检（浏览器预检本就不带凭据）。
func widgetPreflightDo(t *testing.T, e *queryEnv, origin string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodOptions,
		fmt.Sprintf("http://127.0.0.1:%d/widget/summary", e.port), nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Origin", origin)
	req.Header.Set("Access-Control-Request-Method", http.MethodGet)
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("OPTIONS 预检: %v", err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

// widgetGetWithOrigin 带可选 Origin 的 GET，回状态码+响应头。
func widgetGetWithOrigin(t *testing.T, e *queryEnv, origin string) (int, http.Header) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet,
		fmt.Sprintf("http://127.0.0.1:%d/widget/summary", e.port), nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+e.token)
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("GET /widget/summary: %v", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode, resp.Header
}

func TestWidgetCORSPreflight(t *testing.T) {
	e := newQueryEnv(t)

	// 白名单源：204 + 回声 ACAO + 放行 GET 与 Authorization（免鉴权）
	for _, origin := range []string{
		"http://tauri.localhost", "https://tauri.localhost", "tauri://localhost",
	} {
		resp := widgetPreflightDo(t, e, origin)
		if resp.StatusCode != http.StatusNoContent {
			t.Errorf("预检[%s] = %d, want 204", origin, resp.StatusCode)
		}
		if got := resp.Header.Get("Access-Control-Allow-Origin"); got != origin {
			t.Errorf("预检[%s] ACAO = %q, want 回声", origin, got)
		}
		if m := resp.Header.Get("Access-Control-Allow-Methods"); !strings.Contains(m, "GET") {
			t.Errorf("预检[%s] Allow-Methods = %q, 缺 GET", origin, m)
		}
		if h := resp.Header.Get("Access-Control-Allow-Headers"); !strings.Contains(h, "Authorization") {
			t.Errorf("预检[%s] Allow-Headers = %q, 缺 Authorization", origin, h)
		}
	}

	// 白名单外：无 ACAO、不 204（维持未实现 501 原样——任意网页不得过预检）
	resp := widgetPreflightDo(t, e, "https://evil.example")
	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("白名单外 ACAO = %q, want 空", got)
	}
	if resp.StatusCode == http.StatusNoContent {
		t.Errorf("白名单外预检 = 204, want 非 204")
	}
}

func TestWidgetCORSONGET(t *testing.T) {
	e := newQueryEnv(t)
	widgetTestReset(t, widgetRewriteClient(t, httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))))
	e.d.Cfg.Dock = &config.DockCfg{} // 空上游表：装配走通即够（断言头不断言数）

	// 带 Origin 的真 GET：200 + 回声 ACAO（实际请求也要过 CORS，非只预检）
	code, hdr := widgetGetWithOrigin(t, e, "http://tauri.localhost")
	if code != 200 {
		t.Errorf("带 Origin GET = %d, want 200", code)
	}
	if got := hdr.Get("Access-Control-Allow-Origin"); got != "http://tauri.localhost" {
		t.Errorf("GET ACAO = %q, want 回声", got)
	}

	// 无 Origin（curl/壳外语境）：行为原样，不带 CORS 头
	code, hdr = widgetGetWithOrigin(t, e, "")
	if code != 200 {
		t.Errorf("无 Origin GET = %d, want 200", code)
	}
	if got := hdr.Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("无 Origin GET ACAO = %q, want 空", got)
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

// ---- 悬浮窗聚合索引供给（ADR-0027 改判）：与逐行解析参照的一致性钉子 ----

// TestWidgetLedgerAggregateParity 聚合供给（AggregateSnapshot）必须与旧「窗口
// Read＋逐行归属」逐字等价：月桶边界、周桶边界、上月行不收、归属不明宁缺勿猜。
// 时间全从 now 派生（不硬编码日期），时区无关。
func TestWidgetLedgerAggregateParity(t *testing.T) {
	acc, err := accounts.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(int64(1.8e9), 0).In(time.Local) // queryEnv 同款冻结锚
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.Local)
	weekStart := widgetWeekStart(now)

	rec := func(kind string, ts time.Time, f accounts.Fields) {
		t.Helper()
		if _, err := acc.Record(kind, float64(ts.Unix())+0.5, f); err != nil {
			t.Fatalf("Record %s: %v", kind, err)
		}
	}
	usageF := func(model string) accounts.Fields {
		return accounts.Fields{
			"agent": "cc", "session_id": "s", "lineage_id": "", "project": "p",
			"model": model, "title": "t", "input_tokens": 100, "cache_read_tokens": 10,
			"cache_creation_tokens": 20, "output_tokens": 30, "offset": 0, "subagent": "",
		}
	}
	handoffF := accounts.Fields{
		"agent": "cc", "session_id": "s", "lineage_id": "", "project": "p",
		"provider": "glm", "model": "glm-5.3", "price_ver": "v",
		"prompt_tokens": 1, "completion_tokens": 1, "outcome": "fresh", "wall_s": 1,
	}

	rec("usage", now.Add(-time.Hour), usageF("glm-5.3"))           // 今日：月桶
	rec("usage", monthStart.Add(time.Hour), usageF("GLM-5.3"))     // 月初：月桶（大小写归一）
	rec("usage", monthStart.Add(-24*time.Hour), usageF("kimi-k3")) // 上月末：不收
	rec("usage", now.Add(-2*time.Hour), usageF("orphan-model"))    // 归属不明：宁缺勿猜
	rec("handoff", now.Add(-time.Hour), handoffF)                  // 今日：M+W
	rec("handoff", weekStart.Add(-time.Hour), handoffF)            // 周界前（本月内）：仅 M
	rec("handoff", monthStart.Add(-24*time.Hour), handoffF)        // 上月末：既非 M 也非 W

	ups := map[string]config.DockUpstream{
		"glm":  {ModelMap: map[string]string{"g": "glm-5.3"}},
		"kimi": {ModelMap: map[string]string{"k": "kimi-k3"}},
	}
	got := widgetLedgerAggregate(acc, ups, now)

	// 参照：旧逐行实现（Read 窗口＋归属循环）原样内联。
	since := float64(monthStart.Unix())
	if float64(weekStart.Unix()) < since {
		since = float64(weekStart.Unix())
	}
	until := float64(monthStart.AddDate(0, 1, 0).Unix()) - 0.001
	owner := map[string]string{}
	for n, u := range ups {
		for _, v := range u.ModelMap {
			if v != "" {
				owner[strings.ToLower(v)] = n
			}
		}
	}
	ref := &widgetLedgerAgg{monthTokens: map[string]float64{}}
	for _, e := range acc.Read(accounts.ReadOpts{Since: since, Until: until}) {
		k, _ := e["kind"].(string)
		ts, _ := e["ts"].(float64)
		switch k {
		case "usage":
			if ts < float64(monthStart.Unix()) {
				continue
			}
			name := owner[strings.ToLower(strVal(e, "model"))]
			if name == "" {
				continue
			}
			ref.monthTokens[name] += acctNum(e, "input_tokens") +
				acctNum(e, "cache_read_tokens") + acctNum(e, "cache_creation_tokens") +
				acctNum(e, "output_tokens")
		case "handoff":
			if ts >= float64(monthStart.Unix()) {
				ref.handoffsM++
			}
			if ts >= float64(weekStart.Unix()) {
				ref.handoffsW++
			}
		}
	}

	if len(got.monthTokens) != len(ref.monthTokens) {
		t.Fatalf("monthTokens 键数 %d ≠ 参照 %d: %v vs %v",
			len(got.monthTokens), len(ref.monthTokens), got.monthTokens, ref.monthTokens)
	}
	for k, v := range ref.monthTokens {
		if got.monthTokens[k] != v {
			t.Fatalf("monthTokens[%q] = %v, want %v（参照）", k, got.monthTokens[k], v)
		}
	}
	if got.handoffsM != ref.handoffsM || got.handoffsW != ref.handoffsW {
		t.Fatalf("handoffs = %d/%d, want 参照 %d/%d",
			got.handoffsM, got.handoffsW, ref.handoffsM, ref.handoffsW)
	}
	if ref.handoffsM != 2 || ref.handoffsW != 1 { // 参照自检（夹具失效即报）
		t.Fatalf("参照自检不符: M=%d W=%d, want 2/1", ref.handoffsM, ref.handoffsW)
	}
}
