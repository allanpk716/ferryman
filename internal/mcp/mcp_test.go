package mcp

// mcp_test.go — 票04 测试夹具：临时 config（FERRYMAN_CONFIG 指向）＋临时端口＋
// 临时 token＋真 Daemon（ledger/store/accounts 全真件，起法镜像 internal/daemon
// query_sessions_test.go 的 newQueryEnv——但夹具在本包自建，不 import daemon
// 测试代码）；MCP server 经 io.Pipe 按行驱动（stdio JSON-RPC 的同位形）。
//
// 铁律（规格 docs/superpowers/specs/20260920-agent-surface-mcp-readonly-spec.md
// 「Testing Decisions」节）：测试显式断言所用端口非生产端口（15722/15724 渡口
// 双轨与 15721 上游、7311 守护、15900 面板一并避开）；绝不触生产端口、绝不杀
// 生产进程；生产 daemon 正在本机服务真实流量。

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"ferryman/internal/accounts"
	"ferryman/internal/clock"
	"ferryman/internal/config"
	"ferryman/internal/daemon"
	"ferryman/internal/ledger"
	"ferryman/internal/pathsx"
	"ferryman/internal/store"
)

// testT0 冻结时钟锚（idle 判定确定性，daemon 夹具同款）。
const testT0 = 1_800_000_000.0

// testToken 夹具 token（反向断言对象：任何响应/错误文本不得出现它）。
const testToken = "mcp-e2e-token-7f3a9c"

// testVersion 夹具版本（票02，规格 §A）：经装配参数注入 Server（New/Run 的
// version 形参），doctor 响应 version 字段的断言锚。
const testVersion = "test-ver-02"

// prodPorts 生产端口全集：渡口双轨 15722/15724、上游 cc-switch 15721、守护
// 7311、面板 15900。测试端口必须避开（验收钉子显式覆盖 15722/15724）。
var prodPorts = map[int]bool{15721: true, 15722: true, 15724: true, 7311: true, 15900: true}

// freePort 绑 0 取空闲口即关（daemon singleton_test 同款，竞窗接受）。
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

// assertNotProdPort 验收钉子：所用端口显式断言非生产端口。
func assertNotProdPort(t *testing.T, port int) {
	t.Helper()
	if prodPorts[port] {
		t.Fatalf("测试撞生产端口 %d（15722/15724 渡口等）——换口", port)
	}
}

// freezeClockAt 冻结包级时钟（用毕 Cleanup 还原）。
func freezeClockAt(t *testing.T, v float64) {
	t.Helper()
	orig := clock.Now
	cur := v
	clock.Now = func() float64 { return cur }
	t.Cleanup(func() { clock.Now = orig })
}

// mcpEnv 票04 E2E 夹具：临时 config＋临时数据目录＋（可选）真 daemon。
type mcpEnv struct {
	cfg     *config.Config
	cfgPath string
	dataDir string
	port    int
	token   string
}

// newEnv withDaemon=起真 daemon（含种子数据与一枚在飞等待窗）；withToken=预写
// daemon.token（false＝连 token 文件都不存在，钉 token 缺失错误面）。
func newEnv(t *testing.T, withDaemon, withToken bool) *mcpEnv {
	t.Helper()
	port := freePort(t)
	assertNotProdPort(t, port)
	tmp := t.TempDir()
	e := &mcpEnv{port: port, token: testToken, dataDir: filepath.Join(tmp, "data")}
	e.cfgPath = filepath.Join(tmp, "config.toml")
	// TOML 单引号字面串：Windows 反斜杠路径原样合法。
	toml := fmt.Sprintf("[server]\nport = %d\ndata_dir = '%s'\n", port, e.dataDir)
	if err := os.WriteFile(e.cfgPath, []byte(toml), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(e.dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if withToken {
		if err := os.WriteFile(filepath.Join(e.dataDir, "daemon.token"),
			[]byte(testToken), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// FERRYMAN_CONFIG 优先级同生产（config.Load：显式参数 > env > 默认路径）。
	t.Setenv("FERRYMAN_CONFIG", e.cfgPath)
	cfg, err := config.Load("", false)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.Port != port || cfg.DataDir() != e.dataDir {
		t.Fatalf("config 解析不符: port=%d dataDir=%q（want %d/%q）",
			cfg.Server.Port, cfg.DataDir(), port, e.dataDir)
	}
	e.cfg = cfg
	if withDaemon {
		e.startDaemon(t)
	}
	return e
}

// startDaemon 起真 daemon（ledger/store/accounts 真件＋种子数据＋一枚在飞
// 等待窗），绑临时端口＋夹具 token；测试结束回收。不走 serveConfig——夹具
// 直接 ListenAndServe，绝不触 watcher/worker/生产目录。
func (e *mcpEnv) startDaemon(t *testing.T) {
	t.Helper()
	freezeClockAt(t, testT0)
	led := ledger.New()
	st, err := store.New(e.dataDir)
	if err != nil {
		t.Fatal(err)
	}
	acc, err := accounts.New(e.dataDir)
	if err != nil {
		t.Fatal(err)
	}
	e.seedLedger(t, led)
	e.seedAccounts(t, acc)
	d := daemon.NewDaemon(e.cfg, led, st,
		func(*ledger.SessionState) bool { return true }, acc, 0, nil)
	// 一枚在飞等待窗（/beats 面非空）：subagent start 开窗。
	if _, err := d.Subagent(map[string]any{"event": "start", "agent": "cc",
		"session_id": "sd-1"}); err != nil {
		t.Fatal(err)
	}
	ln, srv, err := daemon.ListenAndServe(d, e.cfg.Server.Port, e.token)
	if err != nil {
		t.Fatalf("临时 daemon 监听失败: %v", err)
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
}

// seedLedger 三条会话：cc×2（不同项目）＋codex×1；idle 相对 testT0。
func (e *mcpEnv) seedLedger(t *testing.T, led *ledger.Ledger) {
	t.Helper()
	reg := func(agent, sid, path, cwd string, idleS float64, peak int) {
		t.Helper()
		led.TouchFull(agent, sid, path, testT0-idleS, 10, cwd, "", peak, 0)
	}
	reg("cc", "sd-1", `C:\tmp\sd-1.jsonl`, `C:\proj`, 5, 5000)
	reg("cc", "sd-2", `C:\tmp\sd-2.jsonl`, `C:\proj2`, 10, 4000)
	reg("codex", "cx-1", `C:\tmp\cx-1.jsonl`, `C:\proj3`, 1, 3000)
}

// seedAccounts sd-1 族系 usage 流水：主转录两行＋子代理一行（input 合计 1300，
// 全落 project "C:/proj"——cost_report scope=project 的过滤证据）。
func (e *mcpEnv) seedAccounts(t *testing.T, acc *accounts.Accounts) {
	t.Helper()
	lin := pathsx.NormPath(`C:\tmp\sd-1.jsonl`)
	rec := func(sid string, ts, in, cr, cc, out float64) {
		t.Helper()
		if _, err := acc.Record("usage", ts, accounts.Fields{
			"agent": "cc", "session_id": sid, "lineage_id": lin, "project": "C:/proj",
			"model": "glm-5.3", "title": "t",
			"input_tokens": in, "cache_read_tokens": cr, "cache_creation_tokens": cc,
			"output_tokens": out, "offset": 0,
			// bd1d3ef 起 subagent 为 usage 必填标记字段（ADR-0008；主会话行空串
			// 合法）——夹具同步，补法同 daemon 侧 query_report_test.go 票04。
			"subagent": "",
		}); err != nil {
			t.Fatal(err)
		}
	}
	rec("sd-1", testT0-40, 100, 10, 5, 30)
	rec("sd-1", testT0-20, 200, 20, 10, 40)
	rec("sd-1-sub1", testT0-10, 1000, 100, 50, 500)
}

// ---- MCP server 管道驱动（按行分帧同位形） ----

// mcpPipes 在 io.Pipe 上跑一个 Server：send 写一行请求、recv 读一行响应。
type mcpPipes struct {
	t    *testing.T
	inW  *io.PipeWriter
	out  *bufio.Reader
	done chan error
}

// startPipes 起 Server 于后台 goroutine（stdin EOF → Serve 返回）。
func startPipes(t *testing.T, cfg *config.Config) *mcpPipes {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	p := &mcpPipes{t: t, inW: inW, out: bufio.NewReader(outR), done: make(chan error, 1)}
	go func() { p.done <- New(cfg, testVersion).Serve(context.Background(), inR, outW) }()
	t.Cleanup(func() { _ = inW.Close() })
	return p
}

func (p *mcpPipes) send(line string) {
	p.t.Helper()
	if _, err := p.inW.Write([]byte(line + "\n")); err != nil {
		p.t.Fatalf("写请求失败: %v", err)
	}
}

// recv 读一行响应（10s 超时——挂死测试不能吃默认超时）。
func (p *mcpPipes) recv() map[string]any {
	p.t.Helper()
	line, err := readLineTimeout(p.out, 10*time.Second)
	if err != nil {
		p.t.Fatalf("读响应失败: %v", err)
	}
	var resp map[string]any
	if err := json.Unmarshal([]byte(line), &resp); err != nil {
		p.t.Fatalf("响应行非 JSON: %v (%q)", err, line)
	}
	return resp
}

var rpcSeq int

// call 发请求并校验响应 id 回显。
func (p *mcpPipes) call(method string, params any) map[string]any {
	p.t.Helper()
	rpcSeq++
	req := map[string]any{"jsonrpc": "2.0", "id": rpcSeq, "method": method}
	if params != nil {
		req["params"] = params
	}
	b, err := json.Marshal(req)
	if err != nil {
		p.t.Fatal(err)
	}
	p.send(string(b))
	resp := p.recv()
	if fmt.Sprint(resp["id"]) != fmt.Sprint(rpcSeq) {
		p.t.Fatalf("响应 id 不匹配: got %v want %d（上一行可能是通知的错误回话）",
			resp["id"], rpcSeq)
	}
	return resp
}

// callTool tools/call 便捷形：错误协议对象非空时原样返回；否则拆 result。
func (p *mcpPipes) callTool(name string, args map[string]any) (text string, isErr bool, errObj map[string]any) {
	p.t.Helper()
	resp := p.call("tools/call", map[string]any{"name": name, "arguments": args})
	if e, ok := resp["error"].(map[string]any); ok {
		return "", false, e
	}
	res, ok := resp["result"].(map[string]any)
	if !ok {
		p.t.Fatalf("tools/call 无 result: %v", resp)
	}
	return toolText(p.t, res), res["isError"] == true, nil
}

// toolText 取 content[0].text（MCP 文本内容形状钉子）。
func toolText(t *testing.T, res map[string]any) string {
	t.Helper()
	arr, ok := res["content"].([]any)
	if !ok || len(arr) == 0 {
		t.Fatalf("content 缺失: %v", res)
	}
	c0, ok := arr[0].(map[string]any)
	if !ok || c0["type"] != "text" {
		t.Fatalf("content[0] 非 text: %v", arr[0])
	}
	s, _ := c0["text"].(string)
	return s
}

// readLineTimeout 带超时的按行读（管道无 deadline，用 goroutine+select 兜）。
func readLineTimeout(r *bufio.Reader, d time.Duration) (string, error) {
	type res struct {
		s   string
		err error
	}
	ch := make(chan res, 1)
	go func() {
		s, err := r.ReadString('\n')
		ch <- res{s, err}
	}()
	select {
	case x := <-ch:
		if x.err != nil && x.s == "" {
			return "", x.err
		}
		return x.s, nil
	case <-time.After(d):
		return "", fmt.Errorf("读取响应超时（%v）", d)
	}
}

// resMap 响应 result 必为对象。
func resMap(t *testing.T, resp map[string]any) map[string]any {
	t.Helper()
	res, ok := resp["result"].(map[string]any)
	if !ok {
		t.Fatalf("响应无 result 对象: %v", resp)
	}
	return res
}

// errCode JSON-RPC error.code（无 error 时 fatal）。
func errCode(t *testing.T, resp map[string]any) int {
	t.Helper()
	e, ok := resp["error"].(map[string]any)
	if !ok {
		t.Fatalf("响应无 error 对象: %v", resp)
	}
	f, ok := e["code"].(float64)
	if !ok {
		t.Fatalf("error.code 非数字: %v", e)
	}
	return int(f)
}

// mustJSON 工具 text（daemon 响应 JSON 原样）解码。
func mustJSON(t *testing.T, text string, v any) {
	t.Helper()
	if err := json.Unmarshal([]byte(text), v); err != nil {
		t.Fatalf("工具 text 非 JSON: %v (%q)", err, text)
	}
}

// listedTools tools/list 的 name→条目 映射。
func listedTools(t *testing.T, p *mcpPipes) map[string]map[string]any {
	t.Helper()
	res := resMap(t, p.call("tools/list", nil))
	arr, ok := res["tools"].([]any)
	if !ok {
		t.Fatalf("tools/list 无 tools 数组: %v", res)
	}
	out := map[string]map[string]any{}
	for _, it := range arr {
		tm, ok := it.(map[string]any)
		if !ok {
			t.Fatalf("工具条目非对象: %v", it)
		}
		name, _ := tm["name"].(string)
		out[name] = tm
	}
	return out
}
