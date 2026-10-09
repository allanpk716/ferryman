package daemon

// settings_cors_test.go — 设置族 CORS 回声钉子（2026-10-09 设置窗「连不上
// 守护」事故）。事故形态：/widget/summary、/stats 各自回声 ACAO，设置族
// （ferryman-settings.html 跨源 fetch 的 /settings 全家）实际响应不带头 →
// WebView2 拦响应 → 界面报「连不上守护」（预检过、200 也到了，浏览器照拦
// ——curl 验不了 CORS，6c38d34 同款）。回声修在 makeHandler 唯一漏斗按
// /settings 前缀一次落位，本文件钉：
//   - GET 读面 200 回声（事故现场原样复刻）；
//   - 401（token 不对）也回声——壳内才分得清「token 不对」与「守护不在线」；
//   - PUT/POST 与管理端点拦截（restart）同样过漏斗回声；
//   - 无 Origin / 白名单外源不设头（curl/任意网页语境行为原样）；
//   - 预检方法/头面=守护动词全集（设置写面要 PUT/POST/DELETE+Content-Type）。
//
// 白名单源本身（三源收录/白名单外 501）由 query_widget_test.go
// TestWidgetCORSPreflight 钉，此处不重复。

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

// scDo 打真监听口（newSettingsEnv 同族）：按参带 Authorization/Origin，读光
// body 后返回（状态码, 响应头）。
func scDo(t *testing.T, e *queryEnv, method, path, token, origin string) (int, http.Header) {
	t.Helper()
	req, err := http.NewRequest(method, fmt.Sprintf("http://127.0.0.1:%d%s", e.port, path), nil)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode, resp.Header
}

// scPreflight OPTIONS 预检（带请求方法/头清单，浏览器预检真形态）。
func scPreflight(t *testing.T, e *queryEnv, path, origin, reqMethod, reqHeaders string) (int, http.Header) {
	t.Helper()
	req, err := http.NewRequest(http.MethodOptions, fmt.Sprintf("http://127.0.0.1:%d%s", e.port, path), nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Origin", origin)
	req.Header.Set("Access-Control-Request-Method", reqMethod)
	if reqHeaders != "" {
		req.Header.Set("Access-Control-Request-Headers", reqHeaders)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode, resp.Header
}

func TestSettingsCORSEchoOnRead(t *testing.T) {
	e, _ := newSettingsEnv(t)

	// 事故现场原样：带 Origin 的真 GET → 200 且回声 ACAO（修复前 200 无头，
	// WebView2 拦响应=界面「连不上守护」）。
	code, hdr := scDo(t, e, http.MethodGet, "/settings", e.token, "http://tauri.localhost")
	if code != http.StatusOK {
		t.Fatalf("带 Origin GET /settings = %d, want 200", code)
	}
	if got := hdr.Get("Access-Control-Allow-Origin"); got != "http://tauri.localhost" {
		t.Fatalf("GET /settings ACAO = %q, want 回声", got)
	}

	// 快照清单读面同族（doGet 另一 case）：
	code, hdr = scDo(t, e, http.MethodGet, "/settings/snapshots", e.token, "http://tauri.localhost")
	if code != http.StatusOK {
		t.Fatalf("带 Origin GET /settings/snapshots = %d, want 200", code)
	}
	if got := hdr.Get("Access-Control-Allow-Origin"); got != "http://tauri.localhost" {
		t.Fatalf("GET /settings/snapshots ACAO = %q, want 回声", got)
	}

	// 401 也回声：token 不对时壳内 fetch 拿得到状态码，分得清「token 不对」
	// 与「守护不在线」（浏览器拦无头响应是一刀切 TypeError）。
	code, hdr = scDo(t, e, http.MethodGet, "/settings", "wrong-token", "http://tauri.localhost")
	if code != http.StatusUnauthorized {
		t.Fatalf("错 token GET /settings = %d, want 401", code)
	}
	if got := hdr.Get("Access-Control-Allow-Origin"); got != "http://tauri.localhost" {
		t.Fatalf("401 ACAO = %q, want 回声", got)
	}

	// 无 Origin（curl/壳外）与白名单外源（任意网页）：不设头，行为原样。
	_, hdr = scDo(t, e, http.MethodGet, "/settings", e.token, "")
	if got := hdr.Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("无 Origin GET ACAO = %q, want 空", got)
	}
	_, hdr = scDo(t, e, http.MethodGet, "/settings", e.token, "https://evil.example")
	if got := hdr.Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("白名单外 GET ACAO = %q, want 空", got)
	}
}

func TestSettingsCORSEchoOnWrites(t *testing.T) {
	e, _ := newSettingsEnv(t)

	// 三条写路径各过一遍漏斗（错 token → 401，零副作用；断言回声不断言写效）：
	//   PUT 实体（doPost→handleSettingsWrite）、POST 快照（doPost 白名单面）、
	//   POST restart（makeHandler 管理端点拦截）。
	for _, tc := range []struct {
		method, path string
	}{
		{http.MethodPut, "/settings/providers/glm"},
		{http.MethodPost, "/settings/snapshots"},
		{http.MethodPost, "/settings/restart"},
	} {
		code, hdr := scDo(t, e, tc.method, tc.path, "wrong-token", "http://tauri.localhost")
		if code != http.StatusUnauthorized {
			t.Errorf("%s %s = %d, want 401", tc.method, tc.path, code)
			continue
		}
		if got := hdr.Get("Access-Control-Allow-Origin"); got != "http://tauri.localhost" {
			t.Errorf("%s %s 401 ACAO = %q, want 回声", tc.method, tc.path, got)
		}
	}
}

func TestSettingsCORSPreflightMethodFace(t *testing.T) {
	e, _ := newSettingsEnv(t)

	// 设置写面真形态预检：PUT + authorization,content-type → 204，方法/头面
	// 覆盖全集（GET, PUT, POST, DELETE + Content-Type）。修复前静态只放
	// GET+Authorization → 写操作预检即死。
	for _, m := range []string{"PUT", "POST", "DELETE"} {
		code, hdr := scPreflight(t, e, "/settings", "http://tauri.localhost", m, "authorization, content-type")
		if code != http.StatusNoContent {
			t.Errorf("预检[%s] = %d, want 204", m, code)
			continue
		}
		if ms := hdr.Get("Access-Control-Allow-Methods"); !strings.Contains(ms, m) {
			t.Errorf("预检[%s] Allow-Methods = %q, 缺 %s", m, ms, m)
		}
		if hs := hdr.Get("Access-Control-Allow-Headers"); !strings.Contains(hs, "Content-Type") {
			t.Errorf("预检[%s] Allow-Headers = %q, 缺 Content-Type", m, hs)
		}
	}

	// GET 轮询面不回归：只读预检原样过。
	code, hdr := scPreflight(t, e, "/widget/summary", "http://tauri.localhost", "GET", "authorization")
	if code != http.StatusNoContent {
		t.Fatalf("GET 预检 = %d, want 204", code)
	}
	if got := hdr.Get("Access-Control-Allow-Origin"); got != "http://tauri.localhost" {
		t.Fatalf("GET 预检 ACAO = %q, want 回声", got)
	}
}
