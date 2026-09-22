package server

// config_tuning_test.go — 票08:配置与调参页(只读)钉子。
//
// 验收对照:
//   - /api/config-tuning:服务端持 token 代理守护 GET /config_tuning(token
//     永不出服务端);守护不可达/token 缺席 → 200 {ok:false, note} 如实降级
//     (面板照常渲染说明,不编造数据);
//   - /config-tuning 页面:自包含 HTML,渲染三列+徽章+全配置总览;纯只读
//     (零按钮/零表单/零 POST——写操作只在 CLI,D11/D12)。

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ctFakeDaemon 假守护:验 Bearer 后回 canned /config_tuning 响应,并记录
// 收到的 Authorization(证 token 由服务端附上)。
func ctFakeDaemon(t *testing.T) (*httptest.Server, *string) {
	t.Helper()
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/config_tuning" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = w.Write([]byte(`{"mode":"recommend","upstreams":[{"upstream":"glm",` +
			`"configured_min":25,"effective_ok":true,"effective_min":20,` +
			`"suggestion":null,"suggestion_text":"无建议"}],` +
			`"config":{"found":false,"path":"x","sections":[]}}`))
	}))
	t.Cleanup(srv.Close)
	return srv, &gotAuth
}

// ctPanelDir 面板数据目录夹具:config.toml(port 指假守护)+ daemon.token。
func ctPanelDir(t *testing.T, daemonPort int, withToken bool) string {
	t.Helper()
	dir := t.TempDir()
	toml := "[server]\nport = " + fmt.Sprintf("%d", daemonPort) + "\n"
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(toml), 0o600); err != nil {
		t.Fatal(err)
	}
	if withToken {
		if err := os.WriteFile(filepath.Join(dir, "daemon.token"), []byte("tok-123\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestConfigTuningProxyRelaysDaemonResponse(t *testing.T) {
	fake, auth := ctFakeDaemon(t)
	port := strings.TrimPrefix(fake.URL, "http://")
	i := strings.LastIndex(port, ":")
	port = port[i+1:]
	var p int
	if _, err := fmt.Sscanf(port, "%d", &p); err != nil {
		t.Fatal(err)
	}
	dir := ctPanelDir(t, p, true)
	ts := serve(t, dir)
	code, body := call(t, ts, http.MethodGet, "/api/config-tuning", "")
	if code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", code, body)
	}
	if !strings.Contains(string(body), `"recommend"`) || !strings.Contains(string(body), "无建议") {
		t.Fatalf("应原样中转守护响应: %s", body)
	}
	if *auth != "Bearer tok-123" {
		t.Fatalf("代理应持 token 调守护, got %q", *auth)
	}
	// token 永不出现在给页面的响应里。
	if strings.Contains(string(body), "tok-123") {
		t.Fatal("代理响应不得泄漏 daemon.token")
	}
}

func TestConfigTuningProxyHonestWhenDaemonDown(t *testing.T) {
	// 无人监听的端口:连接拒绝 → 200 + ok:false + note(页面渲染说明)。
	dir := ctPanelDir(t, freePortForCT(t), true)
	ts := serve(t, dir)
	code, body := call(t, ts, http.MethodGet, "/api/config-tuning", "")
	if code != http.StatusOK {
		t.Fatalf("降级也应 200, got %d", code)
	}
	m := decode(t, body)
	if m["ok"] != false || !strings.Contains(m["note"].(string), "守护") {
		t.Fatalf("应 ok:false + 人话 note: %v", m)
	}
}

func TestConfigTuningProxyHonestWhenTokenMissing(t *testing.T) {
	fake, _ := ctFakeDaemon(t)
	port := 0
	if _, err := fmt.Sscanf(strings.TrimPrefix(fake.URL, "http://127.0.0.1:"), "%d", &port); err != nil {
		t.Fatal(err)
	}
	dir := ctPanelDir(t, port, false) // 无 daemon.token
	ts := serve(t, dir)
	code, body := call(t, ts, http.MethodGet, "/api/config-tuning", "")
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	m := decode(t, body)
	if m["ok"] != false || !strings.Contains(m["note"].(string), "token") {
		t.Fatalf("应 ok:false + token 缺席说明: %v", m)
	}
}

// TestConfigTuningPageReadOnly 页面钉子:标题/三列/徽章/无建议字样在场;
// 零按钮零表单零 POST(整页字节 grep——写操作只在 CLI)。
func TestConfigTuningPageReadOnly(t *testing.T) {
	ts := serve(t, t.TempDir())
	code, body := call(t, ts, http.MethodGet, "/config-tuning", "")
	if code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", code, body)
	}
	html := string(body)
	for _, want := range []string{"配置与调参", "配置值", "生效值", "建议值",
		"无建议", "只读", "/api/config-tuning", "待审"} {
		if !strings.Contains(html, want) {
			t.Fatalf("页面缺 %q", want)
		}
	}
	for _, forbid := range []string{"<button", "<form", "<input", "<textarea",
		"method: \"POST\"", "method:\"POST\"", "type: 'POST'"} {
		if strings.Contains(html, forbid) {
			t.Fatalf("只读页不得含 %q", forbid)
		}
	}
	// 三列分列:表头三列是独立 <th>(分列显示禁止混排的静态面)。
	for _, th := range []string{"<th>配置值", "<th>生效值", "<th>建议值"} {
		if !strings.Contains(html, th) {
			t.Fatalf("三列须分列(缺 %q)", th)
		}
	}
}

// freePortForCT 拿一个大概率无人监听的端口(降级测试用;与 daemon 测试的
// freePort 同思路,viewer 包内独立一份避免跨包依赖)。
func freePortForCT(t *testing.T) int {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer srv.Close()
	addr := srv.Listener.Addr().String()
	i := strings.LastIndex(addr, ":")
	var p int
	if _, err := fmt.Sscanf(addr[i+1:], "%d", &p); err != nil {
		t.Fatal(err)
	}
	return p
}
