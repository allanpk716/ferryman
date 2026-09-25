// dock_active_test.go — 票01：渡口装配按 active 条目的 daemon 级验收钉子。
//
//   - 装配点不再读旧单值字段：新表+active 与旧单值并存时，转发目的地/出站
//     真钥/model_map 全部来自 active 条目（F9 解析优先级的端到端证明）；
//   - 首启迁移经 ServeContext 全链路发生（加载配置后、渡口构造前）。
package daemon

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"ferryman/internal/config"
)

func TestServeDockAssemblesFromActiveUpstream(t *testing.T) {
	// 旧单值指向 stale 后端（若被命中即败）；新表+active 指向 real 后端——
	// 装配必须以新表为准：目的地、出站真钥、model_map 全来自 active 条目。
	var staleHits atomic.Int64
	stale := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		staleHits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer stale.Close()

	var mu sync.Mutex
	var gotAuth, gotModel string
	real := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		gotAuth, gotModel = r.Header.Get("Authorization"), string(b)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("real-ok"))
	}))
	defer real.Close()

	dockPort := freePort(t)
	dockAddr := fmt.Sprintf("127.0.0.1:%d", dockPort)
	tmp := t.TempDir()
	dataDir := filepath.Join(tmp, "data")
	cfg := serveTestCfg(t, freePort(t), dataDir)
	cfg.Dock = &config.DockCfg{
		Listen:          dockAddr,
		UpstreamBaseURL: stale.URL, // 旧单值：并存时不得生效
		APIKey:          "sk-stale-legacy",
		Active:          "direct",
		Upstreams: map[string]config.DockUpstream{
			"direct": {
				BaseURL:  real.URL,
				APIKey:   "sk-active-real",
				ModelMap: map[string]string{"default": "glm-active", "claude-opus-5": "glm-active"},
			},
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	codeCh := make(chan int, 1)
	go func() { codeCh <- serveConfig(cfg, ctx, "dev") }()
	waitPidFile(t, filepath.Join(dataDir, "daemon.pid"), 10*time.Second)
	if !waitDial(t, dockAddr, 10*time.Second) {
		cancel()
		t.Fatal("渡口端口未就绪")
	}

	body := []byte(`{"model":"claude-sonnet-5","metadata":{"session_id":"active-wins"},"messages":[]}`)
	req, _ := http.NewRequest(http.MethodPost, fmt.Sprintf("http://%s/v1/messages", dockAddr),
		bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer placeholder")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		cancel()
		t.Fatalf("经渡口请求失败: %v", err)
	}
	respBody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	cancel()
	select {
	case code := <-codeCh:
		if code != 0 {
			t.Fatalf("优雅停 code = %d, want 0", code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("serveConfig 未在 ctx 取消后返回")
	}

	if string(respBody) != "real-ok" {
		t.Fatalf("响应 = %q, want real-ok（须转发到 active 条目）", respBody)
	}
	if n := staleHits.Load(); n != 0 {
		t.Fatalf("旧单值后端被命中 %d 次——并存时装配不得读旧单值", n)
	}
	mu.Lock()
	defer mu.Unlock()
	if gotAuth != "Bearer sk-active-real" {
		t.Fatalf("出站 Authorization = %q, want active 条目真钥", gotAuth)
	}
	if !strings.Contains(gotModel, `"model":"glm-active"`) {
		t.Fatalf("模型未按 active 条目 model_map 改写: %s", gotModel)
	}
}

func TestServeContextMigratesLegacyDockOnFirstBoot(t *testing.T) {
	// 守护首启（ServeContext 全链路）：加载配置后、渡口构造前完成旧单值迁移
	// ——文件写回新表（cc-switch＋三预置），横幅 active=cc-switch。
	tmp := t.TempDir()
	dataDir := filepath.Join(tmp, "data")
	port := freePort(t)
	dockPort := freePort(t)
	cfgPath := filepath.Join(tmp, "config.toml")
	src := fmt.Sprintf(`
[server]
port = %d
data_dir = "%s"

[watch]
cc_projects_dir = "%s"
codex_sessions_dir = "%s"
harvest_usage = false

[dock]
upstream_base_url = "http://127.0.0.1:15721"
api_key = "sk-mig-key"
rewrite_enabled = true
listen = "127.0.0.1:%d"

[dock.model_map]
claude-opus-5 = "glm-5.5"
default = "glm-4.7-flash"
`, port, filepath.ToSlash(dataDir),
		filepath.ToSlash(filepath.Join(tmp, "projects")),
		filepath.ToSlash(filepath.Join(tmp, "no-codex")), dockPort)
	if err := os.WriteFile(cfgPath, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FERRYMAN_CONFIG", cfgPath)

	read := captureStdout(t)
	ctx, cancel := context.WithCancel(context.Background())
	codeCh := make(chan int, 1)
	go func() { codeCh <- ServeContext(ctx, false, "test-mig") }()
	waitPidFile(t, filepath.Join(dataDir, "daemon.pid"), 15*time.Second)

	out := read()
	cancel()
	select {
	case code := <-codeCh:
		if code != 0 {
			t.Fatalf("优雅停 code = %d, want 0（输出: %q）", code, out)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("ServeContext 未在 ctx 取消后返回")
	}

	// 文件已迁移：cc-switch 条目在、旧单值键不在、三预置在
	migratedRaw, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	migrated := string(migratedRaw)
	if !strings.Contains(migrated, "[dock.upstreams.cc-switch]") {
		t.Fatalf("迁移后文件缺 cc-switch 条目:\n%s", migrated)
	}
	if !strings.Contains(migrated, `[dock.upstreams."智谱"]`) {
		t.Fatalf("迁移后文件缺智谱预置:\n%s", migrated)
	}
	cfg2, err := config.Load(cfgPath, false)
	if err != nil {
		t.Fatalf("迁移后 Load: %v", err)
	}
	if cfg2.Dock.Active != "cc-switch" {
		t.Fatalf("active = %q, want cc-switch", cfg2.Dock.Active)
	}
	if _, up := cfg2.Dock.ActiveUpstream(); up == nil || up.APIKey != "sk-mig-key" ||
		up.ModelMap["default"] != "glm-4.7-flash" {
		t.Fatalf("cc-switch 条目未继承旧值: %+v", up)
	}
	// 横幅：渡口按 active 装配
	if !strings.Contains(out, "active=cc-switch") {
		t.Fatalf("横幅缺 active=cc-switch:\n%s", out)
	}
}
