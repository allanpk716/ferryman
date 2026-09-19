// smoke.go — 沙箱冒烟（票23；评审附录#11 + spec「Testing Decisions」观察/enforce
// 沙箱冒烟条款）：独立端口 + 独立数据目录 + 假会话样本，直打 API 触发三链路
// 与 enforce block 契约，全部断言内部完成。
//
// 沙箱纪律（票面铁律）：
//   - 端口：freePort 临时端口（绝不占 7311）；
//   - 数据：os.MkdirTemp 一次性数据目录（绝不写 ~/ferryman；accounts/handoffs/
//     index/token 全落沙箱）；
//   - env：FERRYMAN_CONFIG / FERRYMAN_DATA 沙箱化（进程级改写，退出还原）；
//   - 通知：Notify 强制关闭 + NotifyBlock seam 注入空操作（不出网、不弹 toast）；
//   - 摆渡：恒成功假 provider（httptest 环回），绝不触真实 provider 网络。

package cutover

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"ferryman/internal/accounts"
	"ferryman/internal/beat"
	"ferryman/internal/clock"
	"ferryman/internal/config"
	"ferryman/internal/daemon"
	"ferryman/internal/ferry"
	"ferryman/internal/ledger"
	"ferryman/internal/store"
)

// SmokeAll 四链路沙箱冒烟（断言全部内部完成，输出四行结果；任一断言失败
// 返回带链路号的 error）：
//
//	① observe 警告链路：直打 /gate → additional_context 警告（只提醒不拦）；
//	② 摆渡链路：恒成功假 provider（httptest）→ handoff fresh 落盘 + 账本行；
//	③ 归还链路：/restore → ctx 含交接与待续原话；
//	④ enforce block 契约：enforce 配置 + 交接在库 → /gate block +
//	   suppressOriginalPrompt + handoff_path。
//
// cfgPath 可选：作为阈值基座（relaxMinGap 加载）；端口/数据目录/守望目录/
// 通知/问询守望/闸门模式一律强制沙箱值，不随配置走。
func SmokeAll(cfgPath string) error {
	// env 沙箱化：进程内任何 config.Load("") 兜底路径都不会摸到真实配置/数据
	tmp, err := os.MkdirTemp("", "ferryman-smoke-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	sandboxCfg := filepath.Join(tmp, "config.toml")
	sandboxData := filepath.Join(tmp, "env-data")
	for _, kv := range [][2]string{{"FERRYMAN_CONFIG", sandboxCfg}, {"FERRYMAN_DATA", sandboxData}} {
		old, hadOld := os.LookupEnv(kv[0])
		os.Setenv(kv[0], kv[1])
		defer func() {
			if hadOld {
				os.Setenv(kv[0], old)
			} else {
				os.Unsetenv(kv[0])
			}
		}()
	}

	// 阈值基座：显式配置（relax 加载）或全默认
	base := config.Default()
	if cfgPath != "" {
		if base, err = config.Load(cfgPath, true); err != nil {
			return fmt.Errorf("沙箱配置不可用: %w", err)
		}
	}

	// 恒成功假 provider（httptest 环回）：应答带 INJECT 层（真实交接契约形状）
	fakeReply := "<<<INJECT>>>\n冒烟注入层：登录页 bug 已修，下一步跑全量回归。\n<<</INJECT>>>\n" +
		"# 目标\n修复登录页 bug（冒烟假全文）\n"
	chatSrv := newFakeChatServer(fakeReply)
	defer chatSrv.Close()

	// ---- 链路①：observe 警告（沙箱 A：独立端口+数据目录）----
	res1 := smokeObserveWarn(base, tmp)

	// ---- 链路②④③：enforce 沙箱 B 全装配（守望除外的摆渡/闸门/归还链路）----
	res2, res3, res4 := smokeEnforceChains(base, tmp, chatSrv.URL)

	// 四行结果（固定顺序输出；空串 = 该链路抛错前未及产出）
	for _, line := range [4]string{res1, res2, res3, res4} {
		if line != "" {
			fmt.Println(line)
		}
	}
	for _, line := range [4]string{res1, res2, res3, res4} {
		if line == "" {
			return fmt.Errorf("冒烟未完成（某链路中途失败，见上方输出）")
		}
	}
	return nil
}

// smokeObserveWarn 链路①：observe 沙箱 + 闲置达阈会话 → /gate allow+警告。
// 返回结果行（空串=失败，失败详情已打印）。
func smokeObserveWarn(base *config.Config, tmp string) (res string) {
	sb, err := newSandbox(tmp, "observe", base, "")
	if err != nil {
		fmt.Println("[smoke] ① observe 警告链路: FAIL —", err)
		return ""
	}
	defer sb.close()
	proj := filepath.Join(sb.dir, "proj")
	f := writeSmokeSession(sb.dir, "smoke-obs-1", proj)
	// 台账登记：闲置远超 block 阈值（stale 相对阈值伸缩，任何合法配置都入窗）
	stale := clock.Now() - (sb.cfg.Thresholds.BlockS*2 + 60)
	sb.led.TouchFull("cc", "smoke-obs-1", f, stale, 123, proj, "冒烟观察会话", 2000, 0)

	gateBody := map[string]any{"agent": "cc", "session_id": "smoke-obs-1",
		"transcript_path": f, "cwd": proj, "prompt": "看看进度"}
	resp, err := sb.post("/gate", gateBody)
	if err != nil {
		fmt.Println("[smoke] ① observe 警告链路: FAIL —", err)
		return ""
	}
	decision, _ := resp["decision"].(string)
	warn, _ := resp["additional_context"].(string)
	if decision != "allow" {
		fmt.Printf("[smoke] ① observe 警告链路: FAIL — decision=%s（want allow）响应=%v\n", decision, resp)
		return ""
	}
	if !strings.Contains(warn, "Ferryman") || !strings.Contains(warn, "闲置") {
		fmt.Printf("[smoke] ① observe 警告链路: FAIL — additional_context 缺警告文案: %q\n", warn)
		return ""
	}
	if !strings.Contains(warn, "observe") {
		fmt.Printf("[smoke] ① observe 警告链路: FAIL — 警告未声明 observe 只提醒不拦: %q\n", warn)
		return ""
	}
	return fmt.Sprintf("[smoke] ① observe 警告链路: PASS — /gate 直打 decision=allow，"+
		"additional_context 警告在位（observe 只提醒不拦；端口 %d）", sb.port)
}

// smokeEnforceChains 链路②④③：enforce 沙箱全装配。顺序 ②摆渡→④block→③归还
// （④ 产生的待续原话恰是 ③ 的注入素材，真实时序同形）。
func smokeEnforceChains(base *config.Config, tmp, fakeBaseURL string) (res2, res3, res4 string) {
	sb, err := newSandbox(tmp, "enforce", base, fakeBaseURL)
	if err != nil {
		fmt.Println("[smoke] ② 摆渡链路: FAIL —", err)
		return "", "", ""
	}
	defer sb.close()

	proj := filepath.Join(sb.dir, "proj")
	sid := "smoke-ferry-1"
	f := writeSmokeSession(sb.dir, sid, proj)
	stale := clock.Now() - (sb.cfg.Thresholds.BlockS*2 + 60)
	sb.led.TouchFull("cc", sid, f, stale, 123, proj, "冒烟摆渡会话", 2000, 0)

	// ---- ② 摆渡链路：入队（serve 同款闭包）→ 真 FerrySession 打假 provider ----
	if !sb.enqueue(sid, f, proj) {
		fmt.Println("[smoke] ② 摆渡链路: FAIL — 入队失败")
		return "", "", ""
	}
	var entry *store.Entry
	if !waitFor(20*time.Second, func() bool {
		for _, e := range sb.st.RestoreCandidates("cc", proj) {
			if e.SessionID == sid && e.Status == "fresh" {
				entry = &e
				return true
			}
		}
		return false
	}) {
		fmt.Println("[smoke] ② 摆渡链路: FAIL — fresh 交接未落盘（20s 超时）")
		return "", "", ""
	}
	rows := sb.acc.Read(accounts.ReadOpts{Kind: "handoff", Session: sid})
	if len(rows) != 1 || rows[0]["outcome"] != "fresh" {
		fmt.Printf("[smoke] ② 摆渡链路: FAIL — 账本 handoff 行缺失或 outcome 异常: %v\n", rows)
		return "", "", ""
	}
	res2 = fmt.Sprintf("[smoke] ② 摆渡链路: PASS — 恒成功假 provider（httptest），fresh 交接落盘"+
		"（%s）+ 账本 handoff 行 outcome=fresh", filepath.Base(entry.Path))

	// ---- ④ enforce block 契约：交接在库 + 闲置达阈 → block ----
	prompt := "冒烟待续原话-123：继续修登录页"
	gateBody := map[string]any{"agent": "cc", "session_id": sid,
		"transcript_path": f, "cwd": proj, "prompt": prompt}
	resp, err := sb.post("/gate", gateBody)
	if err != nil {
		fmt.Println("[smoke] ④ enforce block 契约: FAIL —", err)
		return res2, "", ""
	}
	decision, _ := resp["decision"].(string)
	suppress, _ := resp["suppressOriginalPrompt"].(bool)
	handoffPath, _ := resp["handoff_path"].(string)
	reason, _ := resp["reason"].(string)
	if decision != "block" || !suppress || handoffPath == "" ||
		!strings.Contains(reason, "交接文档: ") {
		fmt.Printf("[smoke] ④ enforce block 契约: FAIL — decision=%s suppress=%v "+
			"handoff_path=%q reason 含交接文档=%v 响应=%v\n",
			decision, suppress, handoffPath, strings.Contains(reason, "交接文档: "), resp)
		return res2, "", ""
	}
	if handoffPath != entry.Path {
		fmt.Printf("[smoke] ④ enforce block 契约: FAIL — handoff_path %q ≠ 在库交接 %q\n",
			handoffPath, entry.Path)
		return res2, "", ""
	}
	blockRows := sb.acc.Read(accounts.ReadOpts{Kind: "block", Session: sid})
	if len(blockRows) != 1 {
		fmt.Printf("[smoke] ④ enforce block 契约: FAIL — 账本 block 行 = %d, want 1\n", len(blockRows))
		return res2, "", ""
	}
	res4 = fmt.Sprintf("[smoke] ④ enforce block 契约: PASS — decision=block + "+
		"suppressOriginalPrompt=true + handoff_path 在库一致 + 账本 block 行在账（端口 %d）", sb.port)

	// ---- ③ 归还链路：/restore → ctx 含交接与待续 ----
	q := url.Values{"agent": {"cc"}, "cwd": {proj}, "session_id": {"smoke-restore-new"}}
	rest, err := sb.get("/restore?" + q.Encode())
	if err != nil {
		fmt.Println("[smoke] ③ 归还链路: FAIL —", err)
		return res2, "", res4
	}
	ctxText, _ := rest["context"].(string)
	for _, want := range []string{"不可信", "冒烟注入层", "待续 prompt", prompt, "完整交接文档: "} {
		if !strings.Contains(ctxText, want) {
			fmt.Printf("[smoke] ③ 归还链路: FAIL — context 缺 %q: %.300q\n", want, ctxText)
			return res2, "", res4
		}
	}
	res3 = "[smoke] ③ 归还链路: PASS — /restore 直打 ctx 含交接（注入层+不可信声明+完整文档路径）与待续原话"
	return res2, res3, res4
}

// ---- 沙箱守护装配（internal/daemon 出口件组合；serve 装配的沙箱同形） ----

// sandbox 单个沙箱守护：独立端口+独立数据目录；守望不装配（链路用直打 API/
// 显式入队触发，附录#11「不依赖钩子」的沙箱同义）。
type sandbox struct {
	dir      string // 沙箱数据目录
	cfg      *config.Config
	port     int
	token    string
	led      *ledger.Ledger
	st       *store.Store
	acc      *accounts.Accounts
	worker   *daemon.Worker
	shutdown func()
}

// newSandbox 装配沙箱守护。mode 决定闸门模式（① observe / ②③④ enforce）。
// fakeBaseURL 非空 = 装配工人（真 FerrySession 打假 provider）；空 = 纯闸门沙箱
// （入队 no-op，链路①只要警告不要摆渡副作用）。
func newSandbox(tmp, mode string, base *config.Config, fakeBaseURL string) (*sandbox, error) {
	dir, err := os.MkdirTemp(tmp, "data-")
	if err != nil {
		return nil, err
	}
	port, err := freePort()
	if err != nil {
		return nil, err
	}
	cfg := *base // 阈值等非沙箱字段随基座；以下全部强制沙箱值
	cfg.GateCC = mode
	cfg.GateCodex = "off"
	cfg.Thresholds.BlockS = blockOr(base) // 保持基座（防御占位：见 blockOr）
	cfg.Watch = config.WatchCfg{PollIntervalS: base.Watch.PollIntervalS,
		CCProjectsDir:    filepath.Join(dir, "no-cc"),
		CodexSessionsDir: filepath.Join(dir, "no-codex"),
		CodexExtraDirs:   []string{}, HarvestUsage: true}
	cfg.Server = config.ServerCfg{Port: port, DataDir: dir}
	cfg.Notify = config.NotifyCfg{Enabled: false} // 沙箱铁律：不出网不弹窗
	cfg.QuestionWatch.Mode = "off"
	cfg.Heartbeat.Enabled = false
	cfg.FerryProvider = "fake"

	token, err := daemon.EnsureToken(dir)
	if err != nil {
		return nil, err
	}
	led := ledger.New()
	st, err := store.New(dir)
	if err != nil {
		return nil, err
	}
	acc, err := accounts.New(dir)
	if err != nil {
		return nil, err
	}

	sb := &sandbox{dir: dir, cfg: &cfg, port: port, token: token,
		led: led, st: st, acc: acc}

	if fakeBaseURL != "" {
		providers := map[string]ferry.Provider{"fake": {Name: "fake",
			BaseURL: fakeBaseURL + "/v1", Model: "glm-smoke"}}
		sb.worker = daemon.NewWorker(&cfg, st, acc, providers, daemon.FerrySession)
	}
	enqueue := func(s *ledger.SessionState) bool {
		if sb.worker == nil {
			return false // 闸门沙箱：警告链路不产生摆渡副作用
		}
		led.Mu().Lock()
		cwd := s.Cwd
		led.Mu().Unlock()
		return sb.worker.Enqueue(map[string]any{"transcript_path": s.TranscriptPath,
			"agent": s.Agent, "session_id": s.SessionID, "cwd": cwd})
	}
	d := daemon.NewDaemon(&cfg, led, st, enqueue, acc, clock.Now(), beat.NewQWatchStats())
	d.NotifyBlock = func(string, string, string, string, string, *config.Config) {} // 沙箱铁律：通知空操作（票08 seam 加宽）
	ln, srv, err := daemon.ListenAndServe(d, port, token)
	if err != nil {
		return nil, fmt.Errorf("沙箱端口 %d 绑定失败: %w", port, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	if sb.worker != nil {
		go sb.worker.Run(ctx)
	}
	go func() { _ = srv.Serve(ln) }()
	sb.shutdown = func() {
		cancel()
		if sb.worker != nil {
			sb.worker.Stop()
		}
		_ = srv.Close()
		_ = ln.Close()
	}
	return sb, nil
}

// blockOr 阈值基座的防御读数（BlockS 不得为 0——配置校验本已保证，此处兜底）。
func blockOr(base *config.Config) float64 {
	if base.Thresholds.BlockS > 0 {
		return base.Thresholds.BlockS
	}
	return config.Default().Thresholds.BlockS
}

func (s *sandbox) close() {
	if s.shutdown != nil {
		s.shutdown()
	}
}

// enqueue serve 装配同款摆渡入队（显式触发链路②）。
func (s *sandbox) enqueue(sid, path, cwd string) bool {
	if s.worker == nil {
		return false
	}
	return s.worker.Enqueue(map[string]any{"transcript_path": path,
		"agent": "cc", "session_id": sid, "cwd": cwd})
}

func (s *sandbox) post(path string, body map[string]any) (map[string]any, error) {
	return postJSON(s.port, s.token, path, body)
}

func (s *sandbox) get(path string) (map[string]any, error) {
	return getJSON(s.port, s.token, path)
}

// writeSmokeSession 假 CC 会话样本（user+assistant usage+ai-title 三行；
// 时间戳=now → 提取器 covers_until 落在新鲜窗内）。写在 dir 根（沙箱守望不装配，
// 无需 projects 子目录层级）。
func writeSmokeSession(dir, sid, cwd string) string {
	now := time.Now().UTC().Format("2006-01-02T15:04:05.000000Z")
	lines := []string{
		`{"type": "user", "timestamp": "` + now + `", "cwd": ` + pyQuote(cwd) +
			`, "sessionId": "` + sid + `", "message": {"role": "user", "content": "修一下登录页"}}`,
		`{"type": "assistant", "timestamp": "` + now + `", "message": {"role": "assistant",` +
			` "content": [{"type": "text", "text": "修完了"}], "usage": {"input_tokens": 2000,` +
			` "cache_read_input_tokens": 100, "cache_creation_input_tokens": 0, "output_tokens": 5}}}`,
		`{"type": "ai-title", "aiTitle": "冒烟会话"}`,
	}
	f := filepath.Join(dir, sid+".jsonl")
	if err := os.WriteFile(f, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		panic(err) // 沙箱临时目录写不进 = 环境问题，直接崩出
	}
	return f
}

// pyQuote JSON 字符串字面量（路径进样本行）。
func pyQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// newFakeChatServer 恒成功 OpenAI 兼容假 provider：content 原样回放（含 INJECT
// 层的交接契约形状），usage 三键固定值（供账本行字段断言）。
func newFakeChatServer(content string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body) // 先读光请求体（服务器礼节）
		resp := map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{"content": content}}},
			"usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 5,
				"total_tokens": 15},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
}

// ---- HTTP 客户端与轮询小工具 ----

func freePort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port, nil
}

func smokeClient() *http.Client { return &http.Client{Timeout: 5 * time.Second} }

func postJSON(port int, token, path string, body map[string]any) (map[string]any, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodPost,
		fmt.Sprintf("http://127.0.0.1:%d%s", port, path), strings.NewReader(string(raw)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := smokeClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s → HTTP %d: %s", path, resp.StatusCode, string(data))
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("%s 响应非 JSON 对象: %w", path, err)
	}
	return out, nil
}

func getJSON(port int, token, path string) (map[string]any, error) {
	req, err := http.NewRequest(http.MethodGet,
		fmt.Sprintf("http://127.0.0.1:%d%s", port, path), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := smokeClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s → HTTP %d: %s", path, resp.StatusCode, string(data))
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("%s 响应非 JSON 对象: %w", path, err)
	}
	return out, nil
}

// waitFor 轮询条件（冒烟断言的等待语义；返回 false = 超时）。
func waitFor(timeout time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return cond()
}
