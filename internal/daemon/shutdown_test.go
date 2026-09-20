package daemon

// shutdown_test.go — 票04（发布与自升级规格 §C 第5条 停旧）：POST /shutdown
// 守护内部管理端点。只测外部行为：token 对/错、方法错、非 loopback 拒收、
// 优雅停机语义（端点 cancel ≡ os.Interrupt 同一 Done 源 → 监听口释放）。

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ferryman/internal/config"
)

// TestShutdownEndToEndServeConfig 验收主线：真 serveConfig（整装守护）下
// 正确 token + loopback → 守护优雅退出（退出码 0、监听口释放、pid 清除）。
func TestShutdownEndToEndServeConfig(t *testing.T) {
	tmp := t.TempDir()
	port := freePort(t)
	dataDir := filepath.Join(tmp, "data")
	cfg := config.Default()
	cfg.Server = config.ServerCfg{Port: port, DataDir: dataDir}
	cfg.FerryProvider = "fake" // 测试密闭：注入假 provider（同 integ 纪律）
	cfg.Watch = config.WatchCfg{PollIntervalS: 0.2,
		CCProjectsDir:    filepath.Join(tmp, "projects"),
		CodexSessionsDir: filepath.Join(tmp, "no-codex"),
		CodexExtraDirs:   []string{}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan int, 1)
	go func() { done <- serveConfig(cfg, ctx) }()

	// 等 token 落盘 + /stats 健康（守护起完）
	var token string
	waitForCond(t, 10*time.Second, func() bool {
		select {
		case code := <-done:
			t.Fatalf("serveConfig 提前退出: %d", code)
		default:
		}
		b, err := os.ReadFile(filepath.Join(dataDir, "daemon.token"))
		if err != nil {
			return false
		}
		token = strings.TrimSpace(string(b))
		return token != "" && AlreadyRunning(port, token)
	})
	if token == "" {
		t.Fatal("daemon.token 未就绪")
	}

	code, got := postRaw(t, port, "/shutdown", token, nil)
	if code != http.StatusOK {
		t.Fatalf("POST /shutdown = %d %q, want 200", code, got)
	}
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("serveConfig 退出码 = %d, want 0（优雅停）", code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("POST /shutdown 后 serveConfig 未在时限内返回")
	}
	// 监听口释放 + pid 清除（优雅停序走完的落盘证据）
	portFree := func() bool {
		c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), time.Second)
		if err != nil {
			return true
		}
		_ = c.Close()
		return false
	}
	pidGone := func() bool {
		_, err := os.Stat(filepath.Join(dataDir, "daemon.pid"))
		return os.IsNotExist(err)
	}
	waitForCond(t, 5*time.Second, func() bool { return portFree() && pidGone() })
	if !portFree() {
		t.Fatal("停机后监听口未释放")
	}
	if !pidGone() {
		t.Fatal("停机后 daemon.pid 未清除")
	}
}

// shutdownFixture 带停机钩子的真监听（serveConfig 尾段同构缩形）：钩子注入
// 取消源，Done 后 srv.Close——与 os.Interrupt 同一取消路径的缩形。
type shutdownFixture struct {
	port    int
	fired   chan struct{} // 钩子被触发（停机请求过了守门）
	stopped chan struct{} // 优雅停尾段走完（srv.Close 已回）
}

func newShutdownFixture(t *testing.T, port int, token string) *shutdownFixture {
	t.Helper()
	f := &shutdownFixture{port: port,
		fired: make(chan struct{}, 1), stopped: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	hook := func() {
		cancel()
		f.fired <- struct{}{}
	}
	ln, srv, err := ListenAndServeWithShutdown(&stopDaemon{}, port, token, hook)
	if err != nil {
		t.Fatalf("ListenAndServeWithShutdown: %v", err)
	}
	go func() { _ = srv.Serve(ln) }()
	go func() {
		<-ctx.Done()
		_ = srv.Close()
		close(f.stopped)
	}()
	t.Cleanup(func() { cancel(); <-f.stopped })
	return f
}

func TestShutdownGuardsWrongTokenNoTokenAndGET(t *testing.T) {
	port := freePort(t)
	f := newShutdownFixture(t, port, "tok-shut")
	cases := []struct {
		name string
		do   func() (int, []byte)
		want int
	}{
		{"错 token", func() (int, []byte) { return postRaw(t, port, "/shutdown", "WRONG", nil) },
			http.StatusUnauthorized},
		{"无 token", func() (int, []byte) { return postRaw(t, port, "/shutdown", "", nil) },
			http.StatusUnauthorized},
		{"GET 方法", func() (int, []byte) { return getRaw(t, port, "/shutdown", "tok-shut") },
			http.StatusMethodNotAllowed},
	}
	for _, tc := range cases {
		code, got := tc.do()
		if code != tc.want {
			t.Fatalf("%s: /shutdown = %d %q, want %d", tc.name, code, got, tc.want)
		}
		select {
		case <-f.fired:
			t.Fatalf("%s: 守护被误停（钩子不应触发）", tc.name)
		default:
		}
	}
	// 逐轮拒绝后守护不受影响：/stats 仍健康
	if code, got := getRaw(t, port, "/stats", "tok-shut"); code != http.StatusOK {
		t.Fatalf("拒绝轮后 /stats = %d %q, want 200（守护应不受影响）", code, got)
	}
	// 正确 token 仍可正常停
	code, got := postRaw(t, port, "/shutdown", "tok-shut", nil)
	if code != http.StatusOK {
		t.Fatalf("POST /shutdown = %d %q, want 200", code, got)
	}
	select {
	case <-f.stopped:
	case <-time.After(10 * time.Second):
		t.Fatal("POST /shutdown 后未在时限内优雅停")
	}
}

func TestShutdownRejectsNonLoopback(t *testing.T) {
	// handler 级：伪造 RemoteAddr——真 listener 只绑 127.0.0.1，攻面在装配层；
	// 端点仍按来源逐请求复核（纵深），测试据此覆盖拒收半边。
	var called int
	h := makeHandler(&stopDaemon{}, "tok-shut", func() { called++ })

	// 非 loopback（TEST-NET-3）+ 正确 token → 403，钩子不动
	//（target 用 request-target 原文——RequestURI 精确匹配与线上同形）
	req := httptest.NewRequest(http.MethodPost, "/shutdown", nil)
	req.Header.Set("Authorization", "Bearer tok-shut")
	req.RemoteAddr = "203.0.113.7:443"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("非 loopback POST /shutdown = %d %q, want 403", rec.Code, rec.Body.String())
	}
	if called != 0 {
		t.Fatal("非 loopback 请求不得触发停机钩子")
	}

	// loopback + 正确 token → 200，钩子恰好触发一次
	req = httptest.NewRequest(http.MethodPost, "/shutdown", nil)
	req.Header.Set("Authorization", "Bearer tok-shut")
	req.RemoteAddr = "127.0.0.1:5555"
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("loopback POST /shutdown = %d %q, want 200", rec.Code, rec.Body.String())
	}
	if called != 1 {
		t.Fatalf("停机钩子应恰好触发一次, got %d", called)
	}
}
