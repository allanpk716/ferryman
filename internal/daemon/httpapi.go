package daemon

// HTTP 面移植（规格 ferryman/server.py:676-781 逐字，票15）：ensure_token /
// already_running / DaemonLike / _ExclusiveHTTPServer / make_server Handler
// 全部。五端点：POST /gate、/subagent、/qwatch_stop；GET /stats、/restore。
//
// 铁律（注释搬运即文档）：
//   - POST 先读光 body 再回话：401/404 路径若留未读数据就关连接，Windows 会发
//     RST 而非 FIN → 客户端读到 10053 连接中断而非状态码（server.py:749-751）；
//   - POST 未知路径 404 在 auth 前；GET 先 auth 后判路径（两处顺序照搬，
//     server.py:752-755 / 774-781）；
//   - 只绑 127.0.0.1；绑定排他（见 ListenAndServe 注释）；
//   - 访问日志静默：net/http 默认无逐请求日志，无需接线（Python log_message
//     → pass 同位）；Server.ErrorLog 保持默认（panic/协议错误仍可见，与
//     BaseHTTPRequestHandler 异常栈上 stderr 同形）。

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

// EnsureToken ensure_token（server.py:676-685 逐字）：daemon.token 缺则生成
// 64 hex（32 字节 crypto/rand）+目录创建+0600 尽力；读出 strip。
func EnsureToken(dataDir string) (string, error) {
	tokenFile := filepath.Join(dataDir, "daemon.token")
	if _, err := os.Stat(tokenFile); errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(filepath.Dir(tokenFile), 0o755); err != nil {
			return "", err
		}
		raw := make([]byte, 32) // secrets.token_hex(32) → 64 字符
		if _, err := rand.Read(raw); err != nil {
			return "", err
		}
		if err := os.WriteFile(tokenFile, []byte(hex.EncodeToString(raw)), 0o600); err != nil {
			return "", err
		}
		// 0600 尽力：Windows/部分文件系统 chmod 不生效，失败不致命
		//（Python try: chmod(0o600) except OSError: pass 同形）。
		_ = os.Chmod(tokenFile, 0o600)
	}
	data, err := os.ReadFile(tokenFile)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

// DaemonLike HTTP 层依赖的最小面（FerryDaemon 结构满足；测试替身也满足）
// ——server.py:688-695 Protocol 的 Go 形（编译期即校验，比 Protocol 更严）。
type DaemonLike interface {
	Gate(body map[string]any) map[string]any
	Subagent(body map[string]any) (map[string]any, error)
	QWatchStop() map[string]any
	Restore(agent, cwd, sessionID string) map[string]any
	Health() map[string]any
}

// Daemon 满足 DaemonLike（结构化即文档：票14 的五方法全在位）。
var _ DaemonLike = (*Daemon)(nil)

// AlreadyRunning already_running（server.py:698-709 逐字）：端口上是否有一个
// 健康的**本程序**实例（/stats + Bearer 通过）。serve() 绑定失败时用它区分
// "唯一化跳过"与"端口被他人占用"。非 200（含 401/连接拒绝/超时）一律 false。
func AlreadyRunning(port int, token string) bool {
	client := &http.Client{Timeout: 2 * time.Second} // timeout=2.0
	req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/stats", port), nil)
	if err != nil {
		return false
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(req) // urlopen 的 OSError 面（URLError/HTTPError/拒绝）→ err
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20)) // 连接可复用的排水礼节
	return resp.StatusCode == http.StatusOK
}

// ListenAndServe make_server + _ExclusiveHTTPServer 的 Go 形（server.py:712-781）。
// 只绑 127.0.0.1。绑定排他：Go net.Listen 在 Windows 上不设 SO_REUSEADDR——
// 同端口二次绑定必败（_ExclusiveHTTPServer allow_reuse_address=False 同位）；
// POSIX 上 Go 的 SO_REUSEADDR 只许 TIME_WAIT 重绑、不许双活监听——排他性两侧
// 同保证（server.py:713-718 注释语义不变）。调用方持返回的 ln/srv 起
// go srv.Serve(ln)（ThreadingHTTPServer.serve_forever 的 Go 形：一连接一
// goroutine，daemon_threads=True 同位）。
func ListenAndServe(d DaemonLike, port int, token string) (net.Listener, *http.Server, error) {
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return nil, nil, err
	}
	srv := &http.Server{Handler: makeHandler(d, token)}
	return ln, srv, nil
}

// makeHandler Handler 类（server.py:721-781）的闭包形：token 与 daemon 由
// make_server 构造器捕获，do_POST/do_GET 落到两个分派函数。
func makeHandler(d DaemonLike, token string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			doPost(d, token, w, r)
		case http.MethodGet:
			doGet(d, token, w, r)
		default: // BaseHTTPRequestHandler 未定义 do_X → send_error(501) 同位
			w.WriteHeader(http.StatusNotImplemented)
			_, _ = w.Write([]byte("Unsupported method"))
		}
	})
}

// doPost do_POST（server.py:748-772 逐字）。
func doPost(d DaemonLike, token string, w http.ResponseWriter, r *http.Request) {
	// 先读光 body 再回话：401/404 路径若留未读数据就关连接，Windows 会发 RST
	// 而非 FIN → 客户端读到 10053 连接中断而非状态码（server.py:749-751 注释搬运）。
	bodyRaw, _ := io.ReadAll(r.Body)
	// Python self.path 是 request-target 原文（带 query 即不匹配 → 404），
	// 故用 RequestURI 精确匹配而非 r.URL.Path。
	if r.RequestURI != "/gate" && r.RequestURI != "/subagent" && r.RequestURI != "/qwatch_stop" {
		notFound(w) // 未知 POST 路径 404 在 auth 前（server.py:752-754 顺序照搬）
		return
	}
	if !isAuthed(r, token) {
		unauthorized(w)
		return
	}
	if r.RequestURI == "/qwatch_stop" { // T51 票04 一键停：无请求体
		writeJSON(w, http.StatusOK, d.QWatchStop())
		return
	}
	body, err := decodeJSONObject(bodyRaw) // json.loads(body_raw.decode("utf-8"))
	if err != nil {
		badRequest(w, err)
		return
	}
	if r.RequestURI == "/gate" {
		writeJSON(w, http.StatusOK, d.Gate(body))
		return
	}
	resp, err := d.Subagent(body)
	if err != nil { // Subagent 的 error 通道 → 400（票15 票面）
		badRequest(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// doGet do_GET（server.py:774-781 逐字）：先 auth 后判路径（与 POST 相反）。
func doGet(d DaemonLike, token string, w http.ResponseWriter, r *http.Request) {
	if !isAuthed(r, token) {
		unauthorized(w)
		return
	}
	switch r.URL.Path { // Python urlparse(self.path).path——解析后路径
	case "/stats":
		writeJSON(w, http.StatusOK, d.Health())
	case "/restore":
		q := r.URL.Query()
		writeJSON(w, http.StatusOK, d.Restore(
			qsOr(q, "agent", "cc"),
			qsOr(q, "cwd", ""),
			qsOr(q, "session_id", "")))
	default:
		// 票01接线（唯一改动点）：未命中端点先交只读查询面（queryapi.go
		// 注册表，/sessions 等；鉴权已过），仍未命中才 404。
		if dispatchQuery(d, w, r) {
			return
		}
		notFound(w)
	}
}

// isAuthed _authed 的判定向（回复 401 的半边独立成 unauthorized）。
func isAuthed(r *http.Request, token string) bool {
	return r.Header.Get("Authorization") == "Bearer "+token
}

// unauthorized _authed 的 401 半边（server.py:724-729 逐字）：裸 JSON 直写、
// 不带 Content-Type（net/http 会补嗅探的 text/plain——Python 同样不带，
// 语义无差）。
func unauthorized(w http.ResponseWriter) {
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
}

// notFound 未知路径（server.py:753-754/780-781 逐字）。
func notFound(w http.ResponseWriter) {
	writeJSON(w, http.StatusNotFound, map[string]any{"error": "not found"})
}

// badRequest JSON 坏/UTF-8 坏/Subagent error 的统一 400 面
// （except (ValueError, UnicodeDecodeError) → f"bad request: {e}" 同形）。
func badRequest(w http.ResponseWriter, err error) {
	writeJSON(w, http.StatusBadRequest, map[string]any{"error": fmt.Sprintf("bad request: %v", err)})
}

// writeJSON _json（server.py:731-737 逐字）：Content-Type
// application/json; charset=utf-8 + Content-Length；ensure_ascii=False →
// SetEscapeHTML(false)+Encode（非 ASCII 直出、不转义 HTML；Encoder 尾部换行
// 修掉以对齐 json.dumps 字节面）。
func writeJSON(w http.ResponseWriter, code int, obj map[string]any) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(obj); err != nil { // map[string]any 不可达，护底线
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	data := bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Length", fmt.Sprint(len(data)))
	w.WriteHeader(code)
	_, _ = w.Write(data)
}

// decodeJSONObject json.loads(body_raw.decode("utf-8")) 的 Go 形：Python 先
// UTF-8 解码（UnicodeDecodeError）再 json.loads（ValueError），两路都落 400。
// 非 JSON 对象（数组/标量）Python 会漏进 daemon 面，Go 收进 400（更严，无害）。
func decodeJSONObject(raw []byte) (map[string]any, error) {
	if !utf8.Valid(raw) {
		return nil, errors.New("body is not valid utf-8")
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	return m, nil
}

// qsOr parse_qs(keep_blank_values=False) + q.get(k, [def])[0] 的 Go 形：
// 空值视同缺省（parse_qs 默认丢空值），同名多值取第一。
func qsOr(q url.Values, key, def string) string {
	for _, v := range q[key] {
		if v != "" {
			return v
		}
	}
	return def
}
