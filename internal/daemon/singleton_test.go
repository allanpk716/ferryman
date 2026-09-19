package daemon

// 规格：tests/test_singleton.py（T35 · 守护进程自举与唯一化）的票15 落点 +
// 票面追加钉子（token 文件生成、401/404/400/200 全路径、读光 body 纪律）。
//
// 归属拆分：
//   - test_windows_double_bind_is_rejected / test_already_running_true… /
//     test_already_running_false…——本文件 1:1 移植；
//   - ensure_launcher 3 例（start-daemon.cmd 生成/uv 回退/install_cc 落盘）——
//     Python 安装器面（venv/uv/cmd 脚本是 Python 分发物），Go 重写无此物，
//     不移植（启动方式归 serve 装配票）；
//   - 票面追加：EnsureToken 64hex 生成、HTTP 全路径语义、非 ASCII 直出、
//     绑定排他（double bind 已由 1:1 例钉住）。
//
// 规格锚点：ferryman/server.py:676-781（ensure_token/DaemonLike/already_running/
// _ExclusiveHTTPServer/make_server Handler 全部）。注释逐字搬运处均就地标注。

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// freePort tests/test_singleton.py::_free_port 1:1（绑 0 取端口即关——有竞窗，
// Python 同款，接受）。
func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	return port
}

// stopDaemon 最小的 Daemon 替身（tests/test_singleton.py::_StopDaemon 1:1；
// Python Protocol 运行时不校验，_StopDaemon 未立目 qwatch_stop——Go 接口必须
// 全实现，补空实现）。health_msg 塞中文+HTML 字符：钉"非 ASCII 直出且不转义
// HTML"（SetEscapeHTML(false) 契约，Python ensure_ascii=False 同位）。
type stopDaemon struct {
	mu       sync.Mutex
	lastRest [3]string // agent, cwd, session_id（/restore 缺省语义钉子用）
}

func (s *stopDaemon) Gate(map[string]any) map[string]any {
	return map[string]any{"decision": "allow"}
}

func (s *stopDaemon) Subagent(map[string]any) (map[string]any, error) {
	return map[string]any{"ok": true}, nil
}

func (s *stopDaemon) QWatchStop() map[string]any { return map[string]any{"stopped": true} }

func (s *stopDaemon) Restore(agent, cwd, sessionID string) map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastRest = [3]string{agent, cwd, sessionID}
	return map[string]any{"context": nil}
}

func (s *stopDaemon) Health() map[string]any {
	return map[string]any{"gate_calls_total": 0, "health_alert": false,
		"health_msg": "启动宽限中&<ok>"}
}

func lastRestore(s *stopDaemon) [3]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastRest
}

// serveBg tests/test_singleton.py::_serve_bg 1:1：起后台 serve，测试结束回收。
func serveBg(t *testing.T, d DaemonLike, port int, token string) {
	t.Helper()
	ln, srv, err := ListenAndServe(d, port, token)
	if err != nil {
		t.Fatalf("ListenAndServe: %v", err)
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
}

// ---- HTTP 小帮手（裸 net/http，断言原始状态码与字节——不用 httptest 以外的方式
// 都行；这里直接打真监听端口，与 Python urlopen 同位） ----

func httpDo(t *testing.T, method, url, token string, body []byte) (int, []byte) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, url, rd)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, got
}

func postRaw(t *testing.T, port int, path, token string, body []byte) (int, []byte) {
	t.Helper()
	return httpDo(t, http.MethodPost, fmt.Sprintf("http://127.0.0.1:%d%s", port, path), token, body)
}

func getRaw(t *testing.T, port int, path, token string) (int, []byte) {
	t.Helper()
	return httpDo(t, http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d%s", port, path), token, nil)
}

// postJSON/getJSON Harness._post/Harness.get 的 Go 形：200 断言后解码 JSON。
func postJSON(t *testing.T, port int, path, token string, body map[string]any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	code, got := postRaw(t, port, path, token, raw)
	if code != http.StatusOK {
		t.Fatalf("POST %s = %d %q, want 200", path, code, got)
	}
	var out map[string]any
	if err := json.Unmarshal(got, &out); err != nil {
		t.Fatalf("响应非 JSON: %v (%q)", err, got)
	}
	return out
}

func getJSON(t *testing.T, port int, path, token string) map[string]any {
	t.Helper()
	code, got := getRaw(t, port, path, token)
	if code != http.StatusOK {
		t.Fatalf("GET %s = %d %q, want 200", path, code, got)
	}
	var out map[string]any
	if err := json.Unmarshal(got, &out); err != nil {
		t.Fatalf("响应非 JSON: %v (%q)", err, got)
	}
	return out
}

// ---- tests/test_singleton.py 3 例 1:1 ----

func TestWindowsDoubleBindIsRejected(t *testing.T) {
	// 同端口第二个 make_server 必须抛 OSError（Windows 下 allow_reuse_address=1
	// 会静默双绑定——连接归属未定义，唯一化的地基）。
	port := freePort(t)
	ln, srv, err := ListenAndServe(&stopDaemon{}, port, "t1")
	if err != nil {
		t.Fatalf("首次绑定应成功: %v", err)
	}
	defer srv.Close()
	_ = ln
	if _, _, err := ListenAndServe(&stopDaemon{}, port, "t2"); err == nil {
		t.Fatal("同端口二次 Listen 必须失败（排他绑定）")
	}
}

func TestAlreadyRunningTrueForHealthyInstance(t *testing.T) {
	port := freePort(t)
	serveBg(t, &stopDaemon{}, port, "tok-abc")
	if !AlreadyRunning(port, "tok-abc") {
		t.Fatal("健康实例（/stats + Bearer 通过）应判 true")
	}
}

func TestAlreadyRunningFalseForWrongTokenOrDeadPort(t *testing.T) {
	port := freePort(t)
	ln, srv, err := ListenAndServe(&stopDaemon{}, port, "tok-abc")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(ln) }()
	if AlreadyRunning(port, "WRONG") {
		t.Fatal("401 → 不是我们的实例，应 false")
	}
	srv.Close() // shutdown + server_close 的 Go 形
	if AlreadyRunning(port, "tok-abc") {
		t.Fatal("端口已无人监听应 false")
	}
}

// ---- 票面追加钉子 ----

func TestEnsureTokenCreatesHex64File(t *testing.T) {
	// ensure_token（server.py:676-685）1:1：缺文件→建目录+64hex+0600 尽力；
	// 已存在→原样读出 strip。
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "nested") // 缺目录 → mkdir parents
	tok, err := EnsureToken(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(tok) != 64 {
		t.Fatalf("token 长度 = %d, want 64", len(tok))
	}
	if _, err := hex.DecodeString(tok); err != nil {
		t.Fatalf("token 应为 hex: %v", err)
	}
	if raw, err := os.ReadFile(filepath.Join(dataDir, "daemon.token")); err != nil || string(raw) != tok {
		t.Fatalf("daemon.token 落盘 = %q (err=%v), want %q", raw, err, tok)
	}
	tok2, err := EnsureToken(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if tok2 != tok {
		t.Fatal("文件已存在不得重生成（幂等）")
	}

	// 已有文件带首尾空白 → 读出 strip（Python read_text().strip()）。
	ws := filepath.Join(dir, "ws")
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "daemon.token"), []byte("  abc\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := EnsureToken(ws)
	if err != nil {
		t.Fatal(err)
	}
	if got != "abc" {
		t.Fatalf("strip 后 = %q, want %q", got, "abc")
	}
}

func TestHTTPHandlerAllPaths(t *testing.T) {
	// 票面验收：401/404/400/200 全路径；顺序语义照搬 server.py:745-781。
	port := freePort(t)
	d := &stopDaemon{}
	serveBg(t, d, port, "tok")

	// 200：POST /gate 合法
	code, body := postRaw(t, port, "/gate", "tok", []byte(`{"hook_event_name":"x"}`))
	if code != 200 || !strings.Contains(string(body), `"decision":"allow"`) {
		t.Fatalf("POST /gate = %d %q", code, body)
	}
	if ct := "application/json; charset=utf-8"; !ctContentType(t, port, "/stats", ct) {
		t.Fatalf("Content-Type 应为 %q", ct)
	}

	// 非 ASCII 直出且不转义 HTML（ensure_ascii=False / SetEscapeHTML(false)）
	code, body = getRaw(t, port, "/stats", "tok")
	if code != 200 {
		t.Fatalf("GET /stats = %d", code)
	}
	if !bytes.Contains(body, []byte("启动宽限中&<ok>")) {
		t.Fatalf("非 ASCII/HTML 应直出, got %q", body)
	}

	// 401：已知 POST 路径 + 错 token（先读光 body 再回话——客户端须收到状态码）
	code, body = postRaw(t, port, "/gate", "bad", []byte(`{"x":1}`))
	if code != 401 || string(body) != `{"error":"unauthorized"}` {
		t.Fatalf("错 token = %d %q, want 401 {\"error\":\"unauthorized\"}", code, body)
	}

	// 404 在 auth 前：未知 POST 路径不带 token 也是 404（server.py:752-754 顺序）
	code, body = postRaw(t, port, "/nope", "", []byte(`{"x":1}`))
	if code != 404 || string(body) != `{"error":"not found"}` {
		t.Fatalf("未知 POST（无 token）= %d %q, want 404（auth 前）", code, body)
	}

	// 400：JSON 坏
	code, body = postRaw(t, port, "/gate", "tok", []byte(`{oops`))
	if code != 400 || !strings.HasPrefix(string(body), `{"error":"bad request: `) {
		t.Fatalf("坏 JSON = %d %q", code, body)
	}
	// 400：UTF-8 坏（UnicodeDecodeError 等价面）
	code, _ = postRaw(t, port, "/gate", "tok", []byte{0xff, 0xfe, 0x00})
	if code != 400 {
		t.Fatalf("坏 UTF-8 = %d, want 400", code)
	}

	// /qwatch_stop 无请求体
	code, body = postRaw(t, port, "/qwatch_stop", "tok", nil)
	if code != 200 || !strings.Contains(string(body), `"stopped":true`) {
		t.Fatalf("POST /qwatch_stop = %d %q", code, body)
	}

	// GET 先 auth 后判路径（与 POST 相反）：未知 GET 无 token → 401
	if code, _ = getRaw(t, port, "/nope", ""); code != 401 {
		t.Fatalf("未知 GET 无 token = %d, want 401（GET 先 auth）", code)
	}
	// GET /stats + 未知 GET 带 token → 404
	if code, _ = getRaw(t, port, "/stats", "tok"); code != 200 {
		t.Fatalf("GET /stats = %d", code)
	}
	if code, body = getRaw(t, port, "/nope", "tok"); code != 404 || string(body) != `{"error":"not found"}` {
		t.Fatalf("未知 GET = %d %q", code, body)
	}

	// /restore query 缺省：agent=cc、cwd/session_id=""（parse_qs 丢空值同位）
	if code, _ = getRaw(t, port, "/restore", "tok"); code != 200 {
		t.Fatalf("GET /restore = %d", code)
	}
	if got := lastRestore(d); got != [3]string{"cc", "", ""} {
		t.Fatalf("restore 缺省 = %v, want [cc  ]", got)
	}
	if _, _ = getRaw(t, port, "/restore?agent=codex&cwd=C%3A%5Cx&session_id=s1", "tok"); lastRestore(d) != [3]string{"codex", `C:\x`, "s1"} {
		t.Fatalf("restore 显式 = %v", lastRestore(d))
	}
	// 空值视同缺省（parse_qs keep_blank_values=False）
	if _, _ = getRaw(t, port, "/restore?agent=&cwd=x", "tok"); lastRestore(d) != [3]string{"cc", "x", ""} {
		t.Fatalf("restore 空值回缺省 = %v", lastRestore(d))
	}
}

func ctContentType(t *testing.T, port int, path, want string) bool {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d%s", port, path), nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer tok")
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.Header.Get("Content-Type") == want
}

func TestPostDrainsBodyBeforeAuthFailReply(t *testing.T) {
	// 读光 body 纪律的可观测钉子：401 路径 + 大于 net/http 256KB 默认排水量
	// 的请求体，客户端仍须干净收到 401 而非连接中断（Windows RST/10053 回归）。
	port := freePort(t)
	serveBg(t, &stopDaemon{}, port, "tok")
	big := bytes.Repeat([]byte("x"), 300_000)
	code, body := postRaw(t, port, "/gate", "bad", big)
	if code != 401 || string(body) != `{"error":"unauthorized"}` {
		t.Fatalf("大 body 401 路径 = %d %q", code, body)
	}
}
