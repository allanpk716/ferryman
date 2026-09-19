package daemon

// integration_test.go — 票17：tests/test_integration.py 的 1:1 移植（守望+工人
// +HTTP 全装配；恒败 ferryFunc 替身走骨架路径；clock 注入；临时目录）。
//
// 归属清点（票17 验收「integration 全部用例 1:1 绿」）：
//   - 本文件：T10 全链路 / T11 骨架降级 / T14+T14b 懒富化单次性 / T31 悬空
//     推迟 / T42 用量采集（含 disabled）；
//   - worker_test.go：T12 墙钟强杀 / T13 队列背压 / T39 provider 警告；
//   - 已由先票转绿的同名钉子：T15 鉴权与健康 → singleton_test.go
//     （TestHTTPHandlerAllPaths 401/200）+ health_test.go
//     （TestHealthAlertAfterGrace / TestHealthGracePeriodAfterDaemonRestart）；
//     T48 异步等待全链路 → gate_test.go 异步停车套件（TestAsyncStopParks…/
//     TestAckTurnWithinGrace…/TestParkedWindowDefersFerryAndResumesAfterClose）
//     + merge_t51_parking_mutex_test.go；
//   - test_big_session.py → internal/extract/big_session_test.go（slow 标记）。

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"ferryman/internal/accounts"
	"ferryman/internal/beat"
	"ferryman/internal/cctrans"
	"ferryman/internal/clock"
	"ferryman/internal/config"
	"ferryman/internal/extract"
	"ferryman/internal/ferry"
	"ferryman/internal/ledger"
	"ferryman/internal/store"
)

// writeIntegSession tests/helpers.py::write_session 1:1（三行：user+assistant
// usage+ai-title；mtime=now 供观察窗判定；返回转录路径）。
func writeIntegSession(t *testing.T, projects, sid, cwd string, usageInput int) string {
	t.Helper()
	d := filepath.Join(projects, "C--proj")
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	f := filepath.Join(d, sid+".jsonl")
	j := func(v any) string {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	lines := []string{
		j(map[string]any{"type": "user", "timestamp": nowIsoZ(), "cwd": cwd,
			"sessionId": sid, "message": map[string]any{"role": "user", "content": "做点活"}}),
		j(map[string]any{"type": "assistant", "timestamp": nowIsoZ(),
			"message": map[string]any{"role": "assistant",
				"content": []any{map[string]any{"type": "text", "text": "干完了"}},
				"usage": map[string]any{"input_tokens": usageInput,
					"cache_read_input_tokens": 100, "cache_creation_input_tokens": 0,
					"output_tokens": 5}}}),
		j(map[string]any{"type": "ai-title", "aiTitle": "集成测试会话"}),
	}
	if err := os.WriteFile(f, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	now := time.Now() // os.utime(f, None) → mtime=now
	if err := os.Chtimes(f, now, now); err != nil {
		t.Fatal(err)
	}
	return f
}

// integHarness tests/helpers.py::Harness 的 Go 形：最小 daemon（守望+工人+HTTP）；
// 摆渡模型由注入替身替代（离线）。
type integHarness struct {
	t                      *testing.T
	tmp, projects, dataDir string
	cfg                    *config.Config
	token                  string
	led                    *ledger.Ledger
	st                     *store.Store
	acc                    *accounts.Accounts
	w                      *Worker
	d                      *Daemon
	watcher                *Watcher
	port                   int

	mu         sync.Mutex
	enqueuedOK []string
}

func newIntegHarness(t *testing.T, ferryFn FerryFunc) *integHarness {
	t.Helper()
	tmp := t.TempDir()
	projects := filepath.Join(tmp, "projects")
	dataDir := filepath.Join(tmp, "data")
	port := freePort(t)
	cfg := config.Default()
	cfg.GateCC = "enforce"
	cfg.Thresholds = config.ThresholdCfg{SummarizeS: 1.0, BlockS: 2.0,
		MinCtxTokens: 1000, CacheWarnS: 720.0}
	cfg.Watch = config.WatchCfg{PollIntervalS: 0.2, CCProjectsDir: projects,
		CodexSessionsDir: filepath.Join(tmp, "no-codex"),
		CodexExtraDirs:   []string{}, HarvestUsage: true}
	cfg.Server = config.ServerCfg{Port: port, DataDir: dataDir}
	cfg.FerryProvider = "fake" // T39 去内置默认后，测试密闭：注入假 provider
	token, err := EnsureToken(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	led := ledger.New()
	st, err := store.New(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	acc, err := accounts.New(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	w := NewWorker(cfg, st, acc,
		map[string]ferry.Provider{"fake": {Name: "fake",
			BaseURL: "http://127.0.0.1:9/v1", Model: "fake"}}, ferryFn)
	h := &integHarness{t: t, tmp: tmp, projects: projects, dataDir: dataDir,
		cfg: cfg, token: token, led: led, st: st, acc: acc, w: w, port: port}
	enqueue := func(s *ledger.SessionState) bool {
		led.Mu().Lock()
		cwd := s.Cwd // Cwd 可变字段锁内快照（身份字段建后不变直读）
		led.Mu().Unlock()
		if !w.Enqueue(map[string]any{"transcript_path": s.TranscriptPath,
			"agent": s.Agent, "session_id": s.SessionID, "cwd": cwd}) {
			return false
		}
		h.mu.Lock()
		h.enqueuedOK = append(h.enqueuedOK, s.SessionID)
		h.mu.Unlock()
		return true
	}
	qwatchStats := beat.NewQWatchStats()
	d := NewDaemon(cfg, led, st, enqueue, acc, clock.Now(), qwatchStats)
	watcher := NewWatcher(cfg, led, st, enqueue, clock.Now(), acc, d, nil, qwatchStats)
	ln, srv, err := ListenAndServe(d, port, token)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = srv.Serve(ln) }()
	go watcher.Run(ctx)
	go w.Run(ctx)
	t.Cleanup(func() {
		cancel()
		watcher.Stop()
		w.Stop()
		_ = srv.Close()
	})
	h.d = d
	h.watcher = watcher
	return h
}

func (h *integHarness) enqueued() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.enqueuedOK...)
}

func (h *integHarness) gate(body map[string]any) map[string]any {
	return postJSON(h.t, h.port, "/gate", h.token, body)
}

func (h *integHarness) get(path string) map[string]any {
	return getJSON(h.t, h.port, path, h.token)
}

// handoffInStore store._index["handoffs"] 的 Go 观察形（RestoreCandidates 覆盖
// fresh|skeleton 两态 + 24h 新鲜窗——测试时窗内恒真）。
func (h *integHarness) handoffInStore(agent, cwd string, pred func(*store.Entry) bool) bool {
	cands := h.st.RestoreCandidates(agent, cwd)
	for i := range cands {
		if pred(&cands[i]) {
			return true
		}
	}
	return false
}

// ---- T10 全链路（Python test_t10_full_loop） ----

func TestT10FullLoop(t *testing.T) {
	h := newIntegHarness(t, succFerry)
	proj := filepath.Join(h.tmp, "proj")
	sid := "integ-0001"
	f := writeIntegSession(t, h.projects, sid, proj, 2000)
	body := map[string]any{"agent": "cc", "session_id": sid,
		"transcript_path": f, "cwd": proj, "prompt": "继续"}

	// 守望→富化→入队→假摆渡→交接落盘
	waitForCond(t, 15*time.Second, func() bool {
		return h.handoffInStore("cc", proj, func(e *store.Entry) bool { return e.SessionID == sid })
	})
	// 闲置达阈值后 gate 分支5
	waitForCond(t, 10*time.Second, func() bool {
		return h.gate(body)["decision"] == "block"
	})
	r := h.gate(body)
	if hp, _ := r["handoff_path"].(string); hp == "" || !strings.Contains(pyStr(r["reason"]), "交接") {
		t.Fatalf("block 响应缺 handoff_path/交接: %v", r)
	}
	// 归还注入（含不可信声明 + 注入层 + 待续 prompt）
	ctxResp := h.get("/restore?agent=cc&cwd=" + url.QueryEscape(proj) + "&session_id=new-1")
	ctxText := pyStr(ctxResp["context"])
	if ctxText == "" || !strings.Contains(ctxText, "不可信") || !strings.Contains(ctxText, "注入层") {
		t.Fatalf("restore context 缺不可信/注入层: %.200q", ctxText)
	}
	if !strings.Contains(ctxText, "继续") { // 待续 prompt 已随交接注入
		t.Fatalf("restore context 缺待续 prompt: %.200q", ctxText)
	}
	// 健康计数
	stats := h.get("/stats")
	if stats["gate_calls_total"].(float64) < 2 || stats["health_alert"] != false {
		t.Fatalf("stats = %v", stats)
	}
}

// ---- T11 skeleton 降级（Python test_t11_skeleton_on_ferry_failure） ----

func TestT11SkeletonOnFerryFailure(t *testing.T) {
	h := newIntegHarness(t, explodingFerry)
	proj := filepath.Join(h.tmp, "proj")
	sid := "integ-0002"
	f := writeIntegSession(t, h.projects, sid, proj, 2000)
	waitForCond(t, 15*time.Second, func() bool {
		return h.handoffInStore("cc", proj, func(e *store.Entry) bool {
			return e.SessionID == sid && e.Status == "skeleton"
		})
	})
	body := map[string]any{"agent": "cc", "session_id": sid,
		"transcript_path": f, "cwd": proj, "prompt": "x"}
	waitForCond(t, 10*time.Second, func() bool {
		return h.gate(body)["decision"] == "block"
	})
}

// ---- T14 懒富化单次性（Python test_t14_enrich_once_per_version） ----

func TestT14EnrichOncePerVersion(t *testing.T) {
	tmp := t.TempDir()
	projects := filepath.Join(tmp, "projects")
	cfg := config.Default()
	cfg.Thresholds = config.ThresholdCfg{SummarizeS: 0.1, BlockS: 1.0,
		MinCtxTokens: 10_000_000} // 峰值永远不够 → 只富化不入队
	cfg.Watch.CCProjectsDir = projects
	cfg.Watch.CodexSessionsDir = filepath.Join(tmp, "no-codex")
	led := ledger.New()
	proj := filepath.Join(tmp, "proj")
	f := writeIntegSession(t, projects, "enrich-1", proj, 2000)
	st := led.TouchFull("cc", "enrich-1", f, clock.Now(), 10, proj, "", 0, 0)
	w := NewWatcher(cfg, led, nil, func(*ledger.SessionState) bool { return true },
		0, nil, nil, nil, nil) // 只测 maybeEnqueue（Python __new__ 同位）
	led.Mu().Lock()
	st.LastWrite -= 5 // 造闲置
	led.Mu().Unlock()
	calls := 0
	orig := extractFacts
	extractFacts = func(path string) (extract.Facts, []extract.Item, []cctrans.Turn) {
		calls++
		return orig(path)
	}
	defer func() { extractFacts = orig }()
	for i := 0; i < 3; i++ {
		w.maybeEnqueue(st)
	}
	if calls != 1 { // 同一 last_write 版本只读盘一次
		t.Fatalf("extract 调用数 = %d, want 1", calls)
	}
}

// ---- T14b 队满时版本章仍生效（终审 I1 防回潮） ----

func TestT14BEnrichOnceEvenWhenQueueFull(t *testing.T) {
	tmp := t.TempDir()
	projects := filepath.Join(tmp, "projects")
	cfg := config.Default()
	cfg.Thresholds = config.ThresholdCfg{SummarizeS: 0.1, BlockS: 1.0, MinCtxTokens: 10}
	cfg.Watch.CCProjectsDir = projects
	cfg.Watch.CodexSessionsDir = filepath.Join(tmp, "no-codex")
	led := ledger.New()
	proj := filepath.Join(tmp, "proj")
	f := writeIntegSession(t, projects, "enrich-2", proj, 2000)
	st := led.TouchFull("cc", "enrich-2", f, clock.Now(), 10, proj, "", 0, 0)
	w := NewWatcher(cfg, led, nil, func(*ledger.SessionState) bool { return false }, // 队满：入队永远失败
		0, nil, nil, nil, nil)
	led.Mu().Lock()
	st.LastWrite -= 5 // 造闲置
	led.Mu().Unlock()
	calls := 0
	orig := extractFacts
	extractFacts = func(path string) (extract.Facts, []extract.Item, []cctrans.Turn) {
		calls++
		return orig(path)
	}
	defer func() { extractFacts = orig }()
	for i := 0; i < 4; i++ {
		w.maybeEnqueue(st)
	}
	if calls != 1 { // 入队失败不回滚版本章 → 只读盘一次
		t.Fatalf("extract 调用数 = %d, want 1", calls)
	}
}

// ---- T31 悬空 tool_use 推迟入队（Python _dangling_session+test_t31） ----

func integDanglingSession(t *testing.T, projects, sid, cwd string) string {
	t.Helper() // 写一个尾部悬空 tool_use（工具/子代理运行中）的会话
	d := filepath.Join(projects, "C--proj")
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	f := filepath.Join(d, sid+".jsonl")
	j := func(v any) string {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	lines := []string{
		j(map[string]any{"type": "user", "timestamp": "2026-09-16T12:00:00.000Z",
			"cwd": cwd, "sessionId": sid,
			"message": map[string]any{"role": "user", "content": "跑个长任务"}}),
		j(map[string]any{"type": "assistant", "timestamp": "2026-09-16T12:00:02.000Z",
			"message": map[string]any{"role": "assistant",
				"content": []any{
					map[string]any{"type": "text", "text": "好"},
					map[string]any{"type": "tool_use", "id": "toolu_dangle1",
						"name": "Bash", "input": map[string]any{}}},
				"usage": map[string]any{"input_tokens": 2000,
					"cache_read_input_tokens": 100, "cache_creation_input_tokens": 0,
					"output_tokens": 5}}}),
	}
	if err := os.WriteFile(f, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return f
}

func TestT31DanglingDefersEnqueue(t *testing.T) {
	// 达阈值的 CC 会话若尾部悬空 tool_use → 不入队；对照组正常会话 → 入队
	tmp := t.TempDir()
	projects := filepath.Join(tmp, "projects")
	cfg := config.Default()
	cfg.Thresholds = config.ThresholdCfg{SummarizeS: 0.1, BlockS: 1.0, MinCtxTokens: 10}
	cfg.Watch.CCProjectsDir = projects
	cfg.Watch.CodexSessionsDir = filepath.Join(tmp, "no-codex")
	led := ledger.New()
	proj := filepath.Join(tmp, "proj")
	var enqueued []string
	w := NewWatcher(cfg, led, nil, func(st *ledger.SessionState) bool {
		enqueued = append(enqueued, st.SessionID)
		return true
	}, 0, nil, nil, nil, nil)
	f1 := integDanglingSession(t, projects, "dangle-1", proj)
	f2 := writeIntegSession(t, projects, "calm-2", proj, 2000) // 对照：无悬空
	for _, tc := range []struct{ sid, path string }{
		{"dangle-1", f1}, {"calm-2", f2},
	} {
		st := led.TouchFull("cc", tc.sid, tc.path, clock.Now(), 10, proj, "", 0, 0)
		led.Mu().Lock()
		st.LastWrite -= 5 // 造闲置
		led.Mu().Unlock()
		for i := 0; i < 3; i++ {
			w.maybeEnqueue(st)
		}
	}
	sawCalm, sawDangle := false, false
	for _, s := range enqueued {
		if s == "calm-2" {
			sawCalm = true
		}
		if s == "dangle-1" {
			sawDangle = true
		}
	}
	if !sawCalm || sawDangle { // 运行中 → 每轮都推迟
		t.Fatalf("enqueued = %v, want 含 calm-2 不含 dangle-1", enqueued)
	}
}

// ---- T42 用量采集：守望接线（Python test_usage_harvested_to_accounts） ----

func TestT42UsageHarvestedToAccounts(t *testing.T) {
	h := newIntegHarness(t, succFerry)
	sid := "usid-0001"
	f := writeIntegSession(t, h.projects, sid, "C:/proj", 1234)
	waitForCond(t, 15*time.Second, func() bool {
		return len(h.acc.Read(accounts.ReadOpts{Kind: "usage", Session: sid})) == 1
	})
	rows := h.acc.Read(accounts.ReadOpts{Kind: "usage", Session: sid})
	r := rows[0]
	if r["agent"] != "cc" || r["session_id"] != sid {
		t.Fatalf("身份字段 = %v/%v", r["agent"], r["session_id"])
	}
	if r["input_tokens"].(float64) != 1234 || r["cache_read_tokens"].(float64) != 100 {
		t.Fatalf("tokens = %v/%v, want 1234/100", r["input_tokens"], r["cache_read_tokens"])
	}
	if r["model"] != "" || r["title"] != "集成测试会话" {
		t.Fatalf("model/title = %v/%v", r["model"], r["title"])
	}
	if lin, _ := r["lineage_id"].(string); lin == "" {
		t.Fatalf("lineage_id 为空: %v", r)
	}
	if off, _ := r["offset"].(float64); off <= 0 {
		t.Fatalf("offset = %v, want > 0", r["offset"])
	}
	// 追加一条 assistant → 只 +1 行
	appended := `{"type": "assistant", "timestamp": "` + nowIsoZ() +
		`", "message": {"role": "assistant", "content": [{"type": "text", "text": "又一步"}], "usage": {"input_tokens": 5, "cache_read_input_tokens": 2000, "cache_creation_input_tokens": 0, "output_tokens": 7}}}` + "\n"
	fh, err := os.OpenFile(f, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fh.WriteString(appended); err != nil {
		t.Fatal(err)
	}
	_ = fh.Close()
	waitForCond(t, 15*time.Second, func() bool {
		return len(h.acc.Read(accounts.ReadOpts{Kind: "usage", Session: sid})) == 2
	})
}

// ---- T42 disabled（Python test_usage_harvest_disabled） ----

func TestT42UsageHarvestDisabled(t *testing.T) {
	tmp := t.TempDir()
	projects := filepath.Join(tmp, "projects")
	cfg := config.Default()
	cfg.Watch = config.WatchCfg{PollIntervalS: 0.2, CCProjectsDir: projects,
		CodexSessionsDir: filepath.Join(tmp, "no-codex"),
		CodexExtraDirs:   []string{}, HarvestUsage: false}
	cfg.Server = config.ServerCfg{Port: freePort(t), DataDir: filepath.Join(tmp, "data")}
	acc, err := accounts.New(cfg.Server.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.New(cfg.Server.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	watcher := NewWatcher(cfg, ledger.New(), st, func(*ledger.SessionState) bool { return true },
		clock.Now(), acc, nil, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	go watcher.Run(ctx)
	t.Cleanup(func() { cancel(); watcher.Stop() })
	writeIntegSession(t, projects, "usid-0002", "C:/proj", 2000)
	time.Sleep(1 * time.Second)
	if rows := acc.Read(accounts.ReadOpts{Kind: "usage"}); len(rows) != 0 {
		t.Fatalf("watch.harvest_usage=False 不得落 usage 行: %v", rows)
	}
}
