package daemon

// worker_test.go — 票17：摆渡工人单件（规格 tests/test_integration.py 的
// worker 例 + 票面追加：成功路径装配 / recover 护栏（票11 评审硬要求）/
// codex 骨架降级分派）。集成装配例（T10/T11/T14/T31/T42）见
// integration_test.go；serve 装配例见 serve_test.go。

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"ferryman/internal/accounts"
	"ferryman/internal/clock"
	"ferryman/internal/config"
	"ferryman/internal/ferry"
	"ferryman/internal/store"
)

// nowIsoZ Python helpers.now_iso 的 Go 形（UTC ISO + Z 尾）。
func nowIsoZ() string {
	return time.Now().UTC().Format("2006-01-02T15:04:05.000000Z")
}

// ---- 测试小环境 ----

type ferryWenv struct {
	tmp       string
	cfg       *config.Config
	st        *store.Store
	acc       *accounts.Accounts
	providers map[string]ferry.Provider
}

func newFerryWenv(t *testing.T) *ferryWenv {
	t.Helper()
	tmp := t.TempDir()
	st, err := store.New(filepath.Join(tmp, "data"))
	if err != nil {
		t.Fatal(err)
	}
	acc, err := accounts.New(tmp)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.FerryProvider = "fake"
	cfg.Server.DataDir = filepath.Join(tmp, "data")
	return &ferryWenv{
		tmp: tmp,
		cfg: cfg,
		st:  st,
		acc: acc,
		providers: map[string]ferry.Provider{"fake": {
			Name: "fake", BaseURL: "http://127.0.0.1:9/v1", Model: "fake"}}, // httptest 假 provider 形状（不监听）
	}
}

// writeWenvSession tests/helpers.py::write_session 的最小形（user+assistant
// usage+ai-title 三行；mtime=now 供观察窗判定）。
func writeWenvSession(t *testing.T, projects, sid, cwd string) string {
	t.Helper()
	return writeIntegSession(t, projects, sid, cwd, 2000)
}

// captureStdout 复用 test_qwatch_scheduler_test.go 的既有 capsys 同位
// （read := captureStdout(t); …; out := read()）。

// stdoutCollector 后台持续捕获（工人/serve 在 goroutine 里打印时用；
// captureStdout 只覆盖同步段，goroutine 内打印须用本收集器）。
type stdoutCollector struct {
	mu   sync.Mutex
	buf  strings.Builder
	stop func()
}

func startStdoutCapture(t *testing.T) *stdoutCollector {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	c := &stdoutCollector{}
	done := make(chan struct{})
	go func() { // 增量读：snapshot 须在管道关闭前可见（waitContains 轮询用）
		defer close(done)
		chunk := make([]byte, 4096)
		for {
			n, err := r.Read(chunk)
			if n > 0 {
				c.mu.Lock()
				c.buf.Write(chunk[:n])
				c.mu.Unlock()
			}
			if err != nil {
				return
			}
		}
	}()
	c.stop = func() {
		os.Stdout = old
		_ = w.Close()
		<-done
		_ = r.Close()
	}
	t.Cleanup(c.stop)
	return c
}

func (c *stdoutCollector) snapshot() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.String()
}

// waitContains 轮询收集缓冲直到出现 want。
func (c *stdoutCollector) waitContains(t *testing.T, want string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if strings.Contains(c.snapshot(), want) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("stdout %q 内未出现 %q", c.snapshot(), want)
}

func waitForCond(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("waitFor 条件超时")
}

// succFerry 恒成功替身（假 md+meta，字段齐全供记账断言）。
func succFerry(path string, pr ferry.Provider, timeoutS float64, agent string) (string, map[string]any, error) {
	md := "[Ferryman 交接 · 会话 集成测试会话]\n\n<<<INJECT>>>\n注入层：干完了 fb.py\n<<</INJECT>>\n\n# 全文\n干完了：fb.py\n"
	meta := map[string]any{
		"title": "集成测试会话", "mode": "L1", "wall_s": 1.2,
		"covers_until_iso": nowIsoZ(), "mat_tokens_est": 1, "chunks": 1,
		"model": "glm-5.3",
		"usage": map[string]any{"prompt_tokens": float64(100),
			"completion_tokens": float64(50), "total_tokens": float64(150)},
		"call_walls": []float64{1.2}, "source": path,
	}
	return md, meta, nil
}

// explodingFerry 恒败替身（Python exploding 同位）。
func explodingFerry(string, ferry.Provider, float64, string) (string, map[string]any, error) {
	return "", nil, fmt.Errorf("provider down")
}

// runWorkerCtx 起 Run 并注册回收。
func runWorkerCtx(t *testing.T, w *Worker) context.Context {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	go w.Run(ctx)
	t.Cleanup(func() { cancel(); w.Stop() })
	return ctx
}

// ---- Python: test_integration.py::test_t39_unconfigured_provider_warns ----

func TestT39UnconfiguredProviderWarns(t *testing.T) {
	env := newFerryWenv(t)
	env.cfg.FerryProvider = "" // 未配置
	read := captureStdout(t)
	NewWorker(env.cfg, env.st, nil, map[string]ferry.Provider{}, nil)
	out := read()
	if !strings.Contains(out, "未配置") || !strings.Contains(out, "config.toml") {
		t.Fatalf("未配置告警缺失: %q", out)
	}

	cfg2 := config.Default()
	cfg2.FerryProvider = "ghost" // 配了名字但无定义
	read2 := captureStdout(t)
	NewWorker(cfg2, env.st, nil, map[string]ferry.Provider{}, nil)
	out2 := read2()
	if !strings.Contains(out2, "ghost") {
		t.Fatalf("ghost 告警缺失: %q", out2)
	}
}

// ---- Python: test_integration.py::test_t13_queue_backpressure_delays_not_dies ----
// 差异声明：Go Worker 队列深度按票面钉死 cap 10（Python Harness maxsize=1）——
// 语义同位钉法：填满至 cap → 满 → 打印+false=延迟；排空后可再入。

func TestT13QueueBackpressureDelaysNotDies(t *testing.T) {
	env := newFerryWenv(t)
	w := NewWorker(env.cfg, env.st, nil, env.providers, succFerry)
	item := map[string]any{"transcript_path": "x.jsonl", "agent": "cc",
		"session_id": "bp-1", "cwd": "C:/proj"}
	for i := 0; i < cap(w.Tasks); i++ { // 填满（无 Run 在消费）
		if !w.Enqueue(item) {
			t.Fatalf("第 %d 项应可入队", i+1)
		}
	}
	read := captureStdout(t)
	ok := w.Enqueue(item) // 队满：打印+false（read 前置：打印即捕获）
	out := read()
	if ok {
		t.Fatal("队满 → 调用方语义=延迟(false)")
	}
	if !strings.Contains(out, "[queue] 满，任务延迟（下轮轮询重试）") {
		t.Fatalf("队满打印缺失: %q", out)
	}
	// 排空后可再入（工人跑起来消费）
	runWorkerCtx(t, w)
	waitForCond(t, 10*time.Second, func() bool {
		return len(env.st.RestoreCandidates("cc", "C:/proj")) > 0
	})
	// 队已排空（处理完 10 项后 chan 缓冲清空）——等 Tasks 腾空再入
	waitForCond(t, 10*time.Second, func() bool { return len(w.Tasks) == 0 })
	if !w.Enqueue(item) {
		t.Fatal("排空后应可再入")
	}
}

// ---- 票面：成功路径装配测试（附录#11 补强） ----

func TestWorkerFreshHandoffAssembly(t *testing.T) {
	env := newFerryWenv(t)
	projects := filepath.Join(env.tmp, "projects")
	f := writeWenvSession(t, projects, "wf-1", "C:/proj")
	w := NewWorker(env.cfg, env.st, env.acc, env.providers, succFerry)
	runWorkerCtx(t, w)
	if !w.Enqueue(map[string]any{"transcript_path": f, "agent": "cc",
		"session_id": "wf-1", "cwd": "C:/proj"}) {
		t.Fatal("入队失败")
	}
	waitForCond(t, 10*time.Second, func() bool {
		return len(env.acc.Read(accounts.ReadOpts{Kind: "handoff"})) > 0
	})
	rows := env.acc.Read(accounts.ReadOpts{Kind: "handoff", Session: "wf-1"})
	if len(rows) != 1 {
		t.Fatalf("handoff 行数 = %d, want 1: %v", len(rows), rows)
	}
	e := rows[0]
	if e["outcome"] != "fresh" {
		t.Fatalf("outcome = %v, want fresh", e["outcome"])
	}
	if e["provider"] != "fake" || e["model"] != "glm-5.3" {
		t.Fatalf("provider/model = %v/%v, want fake/glm-5.3", e["provider"], e["model"])
	}
	if e["prompt_tokens"].(float64) != 100 || e["completion_tokens"].(float64) != 50 {
		t.Fatalf("tokens = %v/%v, want 100/50", e["prompt_tokens"], e["completion_tokens"])
	}
	if e["wall_s"].(float64) != 1.2 {
		t.Fatalf("wall_s = %v, want 1.2", e["wall_s"])
	}
	if lin, _ := e["lineage_id"].(string); !strings.HasSuffix(lin, "wf-1.jsonl") {
		t.Fatalf("lineage_id = %v, want 尾缀 wf-1.jsonl", e["lineage_id"])
	}
	if e["session_id"] != "wf-1" || e["project"] != "C:/proj" || e["agent"] != "cc" {
		t.Fatalf("身份字段 = %v/%v/%v", e["session_id"], e["project"], e["agent"])
	}
	if pv, ok := e["price_ver"].(string); ok && pv != "" && !strings.Contains(pv, "@") {
		t.Fatalf("price_ver = %v, want key@YYYY-MM-DD 形或 null（本机无 [prices.fake] 即 null）", e["price_ver"])
	}
	for _, banned := range []string{"content", "md"} { // 隐私不变量：无正文
		if _, exists := e[banned]; exists {
			t.Fatalf("隐私不变量：行不得含 %s 键: %v", banned, e)
		}
	}
	// ValidHandoff 命中（fresh 交接可被闸门取用）
	h := env.st.ValidHandoff("cc", "C:/proj", clock.Now())
	if h == nil || h.Status != "fresh" {
		t.Fatalf("ValidHandoff 未命中 fresh: %v", h)
	}
}

// ---- Python: test_integration.py::test_t12_wall_clock_kill ----

func TestT12WallClockKill(t *testing.T) {
	old := FerryWallTimeoutS
	FerryWallTimeoutS = 0.5
	defer func() { FerryWallTimeoutS = old }()

	sleeping := func(path string, pr ferry.Provider, timeoutS float64, agent string) (string, map[string]any, error) {
		time.Sleep(30 * time.Second) // 超墙钟：线程被弃的 Go 形（goroutine 遗弃）
		return "never", map[string]any{}, nil
	}
	env := newFerryWenv(t)
	projects := filepath.Join(env.tmp, "projects")
	f := writeWenvSession(t, projects, "integ-0003", "C:/proj") // 真实转录（Python 同款：守望见真文件入队）
	w := NewWorker(env.cfg, env.st, nil, env.providers, sleeping)
	runWorkerCtx(t, w)
	w.Enqueue(map[string]any{"transcript_path": f, "agent": "cc",
		"session_id": "integ-0003", "cwd": "C:/proj"})
	waitForCond(t, 20*time.Second, func() bool {
		for _, e := range env.st.RestoreCandidates("cc", "C:/proj") {
			if e.Status == "skeleton" {
				return true
			}
		}
		return false
	})
	if len(w.Tasks) != 0 { // 工人没被卡死
		t.Fatalf("墙钟超时后队列应已消费, len = %d", len(w.Tasks))
	}
}

// ---- 票11 评审 recover 护栏双例 ----

// TestWorkerRecoversFromFerryPanic：ferry 替身 panic → 线程内异常带回主线程
// （Python _run 的 except 同位）→ 骨架降级；工人不死、继续处理下一任务。
func TestWorkerRecoversFromFerryPanic(t *testing.T) {
	env := newFerryWenv(t)
	projects := filepath.Join(env.tmp, "projects")
	f1 := writeWenvSession(t, projects, "panic-1", "C:/proj")
	panicky := func(path string, pr ferry.Provider, timeoutS float64, agent string) (string, map[string]any, error) {
		panic("ferry 炸点")
	}
	w := NewWorker(env.cfg, env.st, nil, env.providers, panicky)
	runWorkerCtx(t, w)
	w.Enqueue(map[string]any{"transcript_path": f1, "agent": "cc",
		"session_id": "panic-1", "cwd": "C:/proj"})
	waitForCond(t, 10*time.Second, func() bool { // 降级骨架已落盘
		for _, e := range env.st.RestoreCandidates("cc", "C:/proj") {
			if e.Status == "skeleton" {
				return true
			}
		}
		return false
	})
	// 工人活着：换成功替身，下一单照常 fresh
	w.Ferry = succFerry
	f2 := writeWenvSession(t, projects, "panic-2", "C:/proj")
	w.Enqueue(map[string]any{"transcript_path": f2, "agent": "cc",
		"session_id": "panic-2", "cwd": "C:/proj"})
	waitForCond(t, 10*time.Second, func() bool {
		for _, e := range env.st.RestoreCandidates("cc", "C:/proj") {
			if e.SessionID == "panic-2" && e.Status == "fresh" {
				return true
			}
		}
		return false
	})
}

// TestWorkerRecoversFromStorePanic：store 落盘 panic（盘满/杀软锁文件的确定性
// 替身：handoffs/ 目录替换为同名文件）→ runOne recover 打印+弃该任务；修好后
// 工人继续处理下一任务（票11 评审硬要求）。
func TestWorkerRecoversFromStorePanic(t *testing.T) {
	env := newFerryWenv(t)
	projects := filepath.Join(env.tmp, "projects")
	w := NewWorker(env.cfg, env.st, env.acc, env.providers, succFerry)
	runWorkerCtx(t, w)
	col := startStdoutCapture(t)
	// 弄坏 store：handoffs/ 目录 → 同名普通文件（MD 写入必败 → store panic）
	handoffsDir := filepath.Join(env.tmp, "data", "handoffs")
	if err := os.RemoveAll(handoffsDir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(handoffsDir, []byte("not a dir"), 0o644); err != nil {
		t.Fatal(err)
	}
	f1 := writeWenvSession(t, projects, "store-panic-1", "C:/proj")
	w.Enqueue(map[string]any{"transcript_path": f1, "agent": "cc",
		"session_id": "store-panic-1", "cwd": "C:/proj"})
	col.waitContains(t, "[ferry] 任务异常", 10*time.Second) // 弃该任务且工人未死

	// 修好 store（同一 Store 实例恢复可写），下一单照常落盘
	if err := os.Remove(handoffsDir); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(handoffsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	f2 := writeWenvSession(t, projects, "store-panic-2", "C:/proj")
	w.Enqueue(map[string]any{"transcript_path": f2, "agent": "cc",
		"session_id": "store-panic-2", "cwd": "C:/proj"})
	waitForCond(t, 10*time.Second, func() bool {
		for _, e := range env.st.RestoreCandidates("cc", "C:/proj") {
			if e.SessionID == "store-panic-2" && e.Status == "fresh" {
				return true
			}
		}
		return false
	})
}

// TestSkeletonSavePanicStillBooksFailed：失败路径骨架保存炸（store panic）→
// Python finally 语义——记账（一行 failed）先行完成后异常上抛由 runOne 兜
// （终审#2：失败路径只记一行 failed，且在骨架保存之后）。
func TestSkeletonSavePanicStillBooksFailed(t *testing.T) {
	env := newFerryWenv(t)
	projects := filepath.Join(env.tmp, "projects")
	w := NewWorker(env.cfg, env.st, env.acc, env.providers, explodingFerry)
	runWorkerCtx(t, w)
	col := startStdoutCapture(t)

	// 弄坏 store：handoffs/ 目录 → 同名普通文件（MD 写入必败 → store panic）
	handoffsDir := filepath.Join(env.tmp, "data", "handoffs")
	if err := os.RemoveAll(handoffsDir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(handoffsDir, []byte("not a dir"), 0o644); err != nil {
		t.Fatal(err)
	}
	f1 := writeWenvSession(t, projects, "sk-panic-1", "C:/proj")
	w.Enqueue(map[string]any{"transcript_path": f1, "agent": "cc",
		"session_id": "sk-panic-1", "cwd": "C:/proj"})
	// finally 语义的钉子：骨架保存炸了，failed 行仍入账
	waitForCond(t, 10*time.Second, func() bool {
		return len(env.acc.Read(accounts.ReadOpts{Kind: "handoff", Session: "sk-panic-1"})) == 1
	})
	if rows := env.acc.Read(accounts.ReadOpts{Kind: "handoff", Session: "sk-panic-1"}); rows[0]["outcome"] != "failed" {
		t.Fatalf("outcome = %v, want failed", rows[0]["outcome"])
	}
	col.waitContains(t, "[ferry] 任务异常", 10*time.Second) // 异常上抛被 runOne 兜
	// 工人未死：修好 store，下一单走骨架照常
	if err := os.Remove(handoffsDir); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(handoffsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	f2 := writeWenvSession(t, projects, "sk-panic-2", "C:/proj")
	w.Enqueue(map[string]any{"transcript_path": f2, "agent": "cc",
		"session_id": "sk-panic-2", "cwd": "C:/proj"})
	waitForCond(t, 10*time.Second, func() bool {
		for _, e := range env.st.RestoreCandidates("cc", "C:/proj") {
			if e.SessionID == "sk-panic-2" && e.Status == "skeleton" {
				return true
			}
		}
		return false
	})
}

// ---- Python: test_codex_extract.py::test_skeleton_fallback_uses_codex_extraction ----
// 归属注明：落 daemon 侧（票17）——被测物是骨架降级路的 codex 分派（工人
// saveSkeleton 的 agent 分流）；codextrans 提取语义本征已由
// codextrans_extract_test.go 的 4 例钉住。守望-入队腿（rollout 被 watcher
// 拾取入队）由票16 守望测试覆盖，此处按最小装配器直驱工人通道。

func TestSkeletonFallbackUsesCodexExtraction(t *testing.T) {
	env := newFerryWenv(t)
	rollout := writeRolloutFile(t, filepath.Join(env.tmp, "codex"))
	w := NewWorker(env.cfg, env.st, nil, env.providers, explodingFerry)
	runWorkerCtx(t, w)
	w.Enqueue(map[string]any{"transcript_path": rollout, "agent": "codex",
		"session_id": "01a0ad46-ac17-7d73-a203-072c58812fd0", "cwd": "C:\\proj"})
	var md string
	waitForCond(t, 10*time.Second, func() bool {
		for _, e := range env.st.RestoreCandidates("codex", "C:\\proj") {
			if e.Status == "skeleton" {
				data, err := os.ReadFile(e.Path)
				if err != nil {
					return false
				}
				md = string(data)
				return true
			}
		}
		return false
	})
	for _, want := range []string{"auth.py", "rg def login", "25000"} {
		if !strings.Contains(md, want) {
			t.Fatalf("codex 骨架缺 %q（非 CC 空骨架）:\n%s", want, md)
		}
	}
}

// writeRolloutFile codex rollout 样本（test_codex_extract.py::_sample_lines
// 的最小拷贝：session_meta+user/assistant+exec_command+token_count）。
// 差异声明：Python 版用固定 ts 且直读 store._index（绕开 24h 新鲜窗）；
// Go 观察面走 RestoreCandidates（带新鲜窗过滤），故 ts 取 now——内容断言
// （文件清单/命令清单/峰值上下文）不受影响。
func writeRolloutFile(t *testing.T, dir string) string {
	t.Helper()
	ts := func(offMS int) string {
		return time.Now().UTC().Add(time.Duration(offMS) * time.Millisecond).
			Format("2006-01-02T15:04:05.000Z")
	}
	lines := []string{
		`{"timestamp": "` + ts(0) + `", "type": "session_meta", "payload": {"session_id": "01a0ad46", "cwd": "C:\\proj", "originator": "codex-tui"}}`,
		`{"timestamp": "` + ts(1000) + `", "type": "response_item", "payload": {"type": "message", "role": "user", "content": [{"type": "input_text", "text": "修一下登录页的 bug"}]}}`,
		`{"timestamp": "` + ts(2000) + `", "type": "response_item", "payload": {"type": "message", "role": "assistant", "content": [{"type": "output_text", "text": "好的，我先看下 auth.py 的登录分支"}]}}`,
		`{"timestamp": "` + ts(3000) + `", "type": "response_item", "payload": {"type": "function_call", "name": "exec_command", "arguments": "{\"cmd\": \"rg def login C:\\\\proj\"}"}}`,
		`{"timestamp": "` + ts(4000) + `", "type": "response_item", "payload": {"type": "function_call", "name": "apply_patch", "arguments": "{\"input\": \"*** Update File: C:\\\\proj\\\\auth.py\"}"}}`,
		`{"timestamp": "` + ts(5000) + `", "type": "event_msg", "payload": {"type": "token_count", "info": {"last_token_usage": {"input_tokens": 25000, "cached_input_tokens": 22000, "cache_write_input_tokens": 0}}}}`,
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "rollout-2026-09-17T10-51-25-01a0ad46-ac17-7d73-a203-072c58812fd0.jsonl")
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}
