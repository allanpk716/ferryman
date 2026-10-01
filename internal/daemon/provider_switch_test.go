package daemon

// provider_switch_test.go — 票02（供应商接管，F4/D5）：活跃供应商热切换——
// 内存态原子换绑（dockUpstreamState）＋守护管理口 POST /provider_switch＋
// 渡口不重启不断流的端到端验收。
//
// 验收口径（票 02）：
//   - 换绑+持久化原子：落盘失败内存不动；成功后盘/内存一致（重读往返）；
//   - 非法名拒绝；无上游表（旧单值形态）拒绝；
//   - 端点守门序与 /shutdown 同族：loopback → 方法 → Bearer；
//   - 端到端：切换时在途请求旧上游跑完、下一请求即刻新上游、渡口无空窗。

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"ferryman/internal/config"
)

// ---- dockUpstreamState 单元 ----

// writeSwitchCfg 写两-entry 上游表配置（active=a），返回路径。
func writeSwitchCfg(t *testing.T, upA, upB string) string {
	t.Helper()
	f := filepath.Join(t.TempDir(), "config.toml")
	src := fmt.Sprintf(`
[dock]
listen = "127.0.0.1:15922"
active = "a"

[dock.upstreams.a]
base_url = %q
api_key = "kA"

[dock.upstreams.a.model_map]
default = "glm-a"

[dock.upstreams.b]
base_url = %q
api_key = "kB"
dialect = "openai_responses"

[dock.upstreams.b.model_map]
default = "glm-b"
codex = "glm-b"
`, upA, upB)
	if err := os.WriteFile(f, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	return f
}

func TestDockUpstreamStateSwitchToPersistsAndRebinds(t *testing.T) {
	f := writeSwitchCfg(t, "https://a.example/api", "https://b.example/api")
	cfg, err := config.Load(f, false)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	h := newDockUpstreamState(cfg.Dock, f)

	// 初始：active=a
	if name, up := h.ActiveUpstream(); name != "a" || up.APIKey != "kA" {
		t.Fatalf("初始 = %q/%v, want a/kA", name, up)
	}

	// 换绑成功：内存即刻新条目＋盘上 active=b（重读往返）
	resp, err := h.switchTo("b")
	if err != nil {
		t.Fatalf("switchTo(b): %v", err)
	}
	if resp["active"] != "b" || resp["ok"] != true || resp["codex"] != config.CodexNative {
		t.Fatalf("switchTo 回显 = %v, want active=b ok=true codex=native（dialect 推导）", resp)
	}
	if name, up := h.ActiveUpstream(); name != "b" || up.APIKey != "kB" {
		t.Fatalf("换绑后内存 = %q/%v, want b/kB", name, up)
	}
	cfg2, err := config.Load(f, false)
	if err != nil {
		t.Fatalf("重读: %v", err)
	}
	if cfg2.Dock.Active != "b" {
		t.Fatalf("盘上 active = %q, want b（持久化往返）", cfg2.Dock.Active)
	}

	// 非法名拒绝：内存与盘都不动
	if _, err := h.switchTo("nope"); err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("非法名 err = %v, want 含 nope", err)
	}
	if name, _ := h.ActiveUpstream(); name != "b" {
		t.Fatalf("非法名后内存 = %q, want b（不得换绑）", name)
	}
	before, _ := os.ReadFile(f)
	if _, err := h.switchTo("nope"); err == nil {
		t.Fatal("二次非法名应再拒")
	}
	if after, _ := os.ReadFile(f); string(after) != string(before) {
		t.Fatal("非法名不得改写配置文件")
	}
}

func TestDockUpstreamStateSwitchToGuardCases(t *testing.T) {
	// 无表（旧单值形态）：无可切条目
	legacy := &config.DockCfg{UpstreamBaseURL: "http://127.0.0.1:15721"}
	h := newDockUpstreamState(legacy, filepath.Join(t.TempDir(), "absent.toml"))
	if _, err := h.switchTo("x"); err == nil || !strings.Contains(err.Error(), "上游表") {
		t.Fatalf("旧单值 err = %v, want 含 上游表", err)
	}

	// 落盘失败（路径不可读）：内存不动（盘/内存不失配）
	f := writeSwitchCfg(t, "https://a.example/api", "https://b.example/api")
	cfg, err := config.Load(f, false)
	if err != nil {
		t.Fatal(err)
	}
	badPath := filepath.Join(t.TempDir(), "no-such-dir", "config.toml")
	hb := newDockUpstreamState(cfg.Dock, badPath)
	if _, err := hb.switchTo("b"); err == nil {
		t.Fatal("落盘路径不可达应报错")
	}
	if name, _ := hb.ActiveUpstream(); name != "a" {
		t.Fatalf("落盘失败后内存 = %q, want a（不得换绑）", name)
	}
	// 原 f 文件未被触碰
	cfg3, err := config.Load(f, false)
	if err != nil || cfg3.Dock.Active != "a" {
		t.Fatalf("落盘失败不得殃及他文件: %v active=%q", err, cfg3.Dock.Active)
	}
}

// TestDockUpstreamStateConcurrentSwitchConsistency 并发 switch 串行化：终态
// 盘/内存一致（active 同值）。
func TestDockUpstreamStateConcurrentSwitchConsistency(t *testing.T) {
	f := writeSwitchCfg(t, "https://a.example/api", "https://b.example/api")
	cfg, err := config.Load(f, false)
	if err != nil {
		t.Fatal(err)
	}
	h := newDockUpstreamState(cfg.Dock, f)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			name := "a"
			if i%2 == 0 {
				name = "b"
			}
			_, _ = h.switchTo(name)
		}(i)
	}
	wg.Wait()
	name, _ := h.ActiveUpstream()
	cfg2, err := config.Load(f, false)
	if err != nil {
		t.Fatal(err)
	}
	if cfg2.Dock.Active != name {
		t.Fatalf("并发后失配: 内存=%q 盘=%q", name, cfg2.Dock.Active)
	}
}

// ---- 端点守门（handler 级，与 shutdown_test 同款缩形） ----

func TestProviderSwitchEndpointGuards(t *testing.T) {
	var switched []string
	var mu sync.Mutex
	hook := func(name string) (map[string]any, error) {
		if name == "bad" {
			return nil, fmt.Errorf("条目 %q 不在上游表内", name)
		}
		mu.Lock()
		switched = append(switched, name)
		mu.Unlock()
		return map[string]any{"ok": true, "active": name}, nil
	}
	h := makeHandler(&stopDaemon{}, "tok-sw", nil, hook)

	post := func(remote, token, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/provider_switch", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		req.RemoteAddr = remote
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	cases := []struct {
		name string
		rec  *httptest.ResponseRecorder
		want int
	}{
		{"非 loopback 拒", post("203.0.113.7:443", "tok-sw", `{"name":"b"}`), http.StatusForbidden},
		{"GET 方法拒", func() *httptest.ResponseRecorder {
			req := httptest.NewRequest(http.MethodGet, "/provider_switch", nil)
			req.Header.Set("Authorization", "Bearer tok-sw")
			req.RemoteAddr = "127.0.0.1:5555"
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			return rec
		}(), http.StatusMethodNotAllowed},
		{"错 token 拒", post("127.0.0.1:5555", "WRONG", `{"name":"b"}`), http.StatusUnauthorized},
		{"坏 JSON 拒", post("127.0.0.1:5555", "tok-sw", `not-json`), http.StatusBadRequest},
		{"空名拒", post("127.0.0.1:5555", "tok-sw", `{"name":"  "}`), http.StatusBadRequest},
		{"未知条目拒(业务 400)", post("127.0.0.1:5555", "tok-sw", `{"name":"bad"}`), http.StatusBadRequest},
		{"合法切换过", post("127.0.0.1:5555", "tok-sw", `{"name":"b"}`), http.StatusOK},
	}
	for _, tc := range cases {
		if tc.rec.Code != tc.want {
			t.Errorf("%s: code = %d %q, want %d", tc.name, tc.rec.Code, tc.rec.Body.String(), tc.want)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(switched) != 1 || switched[0] != "b" {
		t.Fatalf("钩子触发 = %v, want 恰好一次 b（拒绝轮不得触发）", switched)
	}

	// 钩子未装（渡口关）：POST /provider_switch 落未知路径 404（doPost auth 前判路径）
	hNil := makeHandler(&stopDaemon{}, "tok-sw", nil, nil)
	req := httptest.NewRequest(http.MethodPost, "/provider_switch", strings.NewReader(`{"name":"b"}`))
	req.RemoteAddr = "127.0.0.1:5555"
	rec := httptest.NewRecorder()
	hNil.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("未装钩子 = %d %q, want 404", rec.Code, rec.Body.String())
	}
}

// ---- 端到端：真 serveConfig 热切换（F4 主线） ----

// blockUpstream 阻塞假上游：收请求即通告，压住响应至放行。
type blockUpstream struct {
	srv     *httptest.Server
	arrived chan string // "auth|model"
	release chan struct{}
}

func newBlockUpstream(t *testing.T) *blockUpstream {
	t.Helper()
	b := &blockUpstream{arrived: make(chan string, 8), release: make(chan struct{})}
	b.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		model := ""
		if i := strings.Index(string(body), `"model":"`); i >= 0 {
			rest := string(body[i+9:])
			model = rest[:strings.Index(rest, `"`)]
		}
		b.arrived <- r.Header.Get("Authorization") + "|" + model
		<-b.release
		_, _ = w.Write([]byte("ok"))
	}))
	t.Cleanup(b.srv.Close)
	return b
}

func TestProviderSwitchEndToEndHotSwitchNoRestart(t *testing.T) {
	upA := newBlockUpstream(t) // A 阻塞：制造在途窗口
	upB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Write([]byte("B|" + r.Header.Get("Authorization") + "|" + string(body)))
	}))
	defer upB.Close()

	f := writeSwitchCfg(t, upA.srv.URL, upB.URL)
	t.Setenv("FERRYMAN_CONFIG", f) // serveConfig 热切换落盘路径与 Load 同源
	cfg, err := config.Load(f, false)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	tmp := filepath.Dir(filepath.Dir(f))
	port := freePort(t)
	dockAddr := fmt.Sprintf("127.0.0.1:%d", freePort(t))
	cfg.Server = config.ServerCfg{Port: port, DataDir: filepath.Join(tmp, "data-e2e")}
	cfg.Watch = config.WatchCfg{PollIntervalS: 3.0,
		CCProjectsDir:    filepath.Join(tmp, "projects"),
		CodexSessionsDir: filepath.Join(tmp, "no-codex"),
		CodexExtraDirs:   []string{}, HarvestUsage: false}
	cfg.FerryProvider = "glm"
	cfg.Dock.Listen = dockAddr

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan int, 1)
	go func() { done <- serveConfig(cfg, ctx, "dev") }()

	// 等守护起完（token + 控制面健康）+ 渡口监听就绪
	dataDir := cfg.Server.DataDir
	var token string
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(filepath.Join(dataDir, "daemon.token")); err == nil {
			token = strings.TrimSpace(string(b))
			if token != "" && AlreadyRunning(port, token) {
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	if token == "" || !waitDial(t, dockAddr, 10*time.Second) {
		cancel()
		t.Fatal("守护/渡口未就绪")
	}

	dockPost := func() (int, string) {
		resp, err := http.Post("http://"+dockAddr+"/v1/messages", "application/json",
			strings.NewReader(`{"model":"claude-x","metadata":{"session_id":"e2e"},"messages":[]}`))
		if err != nil {
			return 0, "ERR:" + err.Error()
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}

	// 请求 1 → A（阻塞＝在途）
	got1 := make(chan string, 1)
	go func() { _, b := dockPost(); got1 <- b }()
	select {
	case obs := <-upA.arrived:
		if obs != "Bearer kA|glm-a" {
			t.Fatalf("A 收到 %q, want Bearer kA|glm-a", obs)
		}
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("请求 1 未到达 A")
	}

	// 热切换（渡口在途、不重启）
	code, body := postRaw(t, port, "/provider_switch", token, []byte(`{"name":"b"}`))
	if code != http.StatusOK {
		cancel()
		t.Fatalf("POST /provider_switch = %d %q, want 200", code, body)
	}
	if !strings.Contains(string(body), `"active":"b"`) {
		cancel()
		t.Fatalf("切换回显缺 active=b: %s", body)
	}

	// 下一请求即刻走 B（A 仍阻塞＝渡口没等排水、没有空窗）
	type rb struct {
		code int
		body string
	}
	got2 := make(chan rb, 1)
	go func() { c, b := dockPost(); got2 <- rb{c, b} }()
	select {
	case r2 := <-got2:
		if r2.code != 200 || !strings.HasPrefix(r2.body, "B|Bearer kB|") {
			cancel()
			t.Fatalf("切换后新请求 = %d %q, want B|Bearer kB|…", r2.code, r2.body)
		}
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("切换后新请求未在时限内经 B 返回（A 仍阻塞）")
	}

	// 在途请求旧上游跑完
	close(upA.release)
	select {
	case b1 := <-got1:
		if b1 != "ok" {
			cancel()
			t.Fatalf("在途请求收尾 = %q, want ok（旧上游跑完）", b1)
		}
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("在途请求未在放行后跑完")
	}

	// 持久化往返：盘上 active=b；守护仍健康（未重启——重启则渡口口已随旧进程
	// 消失，新请求不可能即刻可用，上面已证；此处再核控制面活性）
	cfg2, err := config.Load(f, false)
	if err != nil || cfg2.Dock.Active != "b" {
		cancel()
		t.Fatalf("盘上 active = %q err=%v, want b", cfg2.Dock.Active, err)
	}
	if code, _ := getRaw(t, port, "/stats", token); code != http.StatusOK {
		cancel()
		t.Fatalf("切换后 /stats = %d, want 200（守护不受影响）", code)
	}

	cancel()
	select {
	case c := <-done:
		if c != 0 {
			t.Fatalf("优雅停 code = %d, want 0", c)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("serveConfig 未在 ctx 取消后返回")
	}
}
