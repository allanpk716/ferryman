package main

// main_mcp_test.go — 票04 验收钉子：以子进程驱动 `ferryman mcp`（真 exe，
// FERRYMAN_CONFIG 指向临时 config、临时端口），完成 initialize → tools/list →
// tools/call 全链；stdin 关闭后进程退出码 0；token 不回显。
//
// 构建纪律（票面副作用声明）：被测 exe 构建进测试临时目录，绝不覆盖仓库根 exe；
// daemon 夹具为临时端口临时 token 真件，绝不触生产端口（15722/15724/15721/
// 7311/15900 显式避开）。

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"ferryman/internal/clock"
	"ferryman/internal/config"
	"ferryman/internal/daemon"
	"ferryman/internal/ledger"
	"ferryman/internal/store"
)

// syncBuf 并发安全的 stderr 收集（子进程 stderr 持续排空防管道涨满）。
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// readLineTimeout 带超时按行读（子进程挂死不能吃 go test 默认超时）。
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
		return "", fmt.Errorf("读取子进程响应超时（%v）", d)
	}
}

// TestMCPSubprocessFullChain 子进程驱动 ferryman mcp：三方法全链对临时 daemon。
func TestMCPSubprocessFullChain(t *testing.T) {
	// 夹具：临时端口（显式避开生产端口）＋临时 config＋临时 token＋真 daemon。
	port := 0
	for i := 0; i < 10; i++ {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		port = ln.Addr().(*net.TCPAddr).Port
		ln.Close()
		if port != 15721 && port != 15722 && port != 15724 && port != 7311 && port != 15900 {
			break
		}
		port = 0
	}
	if port == 0 {
		t.Fatal("连续撞生产端口，放弃")
	}
	const subT0 = 1_800_000_000.0
	const token = "mcp-subproc-token-4e2b"
	tmp := t.TempDir()
	dataDir := filepath.Join(tmp, "data")
	cfgPath := filepath.Join(tmp, "config.toml")
	if err := os.WriteFile(cfgPath, []byte(fmt.Sprintf(
		"[server]\nport = %d\ndata_dir = '%s'\n", port, dataDir)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "daemon.token"),
		[]byte(token), 0o600); err != nil {
		t.Fatal(err)
	}
	orig := clock.Now
	cur := subT0
	clock.Now = func() float64 { return cur }
	t.Cleanup(func() { clock.Now = orig })
	led := ledger.New()
	led.TouchFull("cc", "sd-1", `C:\tmp\sd-1.jsonl`, subT0-5, 10, `C:\proj`, "", 5000, 0)
	st, err := store.New(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(cfgPath, false)
	if err != nil {
		t.Fatal(err)
	}
	d := daemon.NewDaemon(cfg, led, st,
		func(*ledger.SessionState) bool { return true }, nil, 0, nil)
	ln, srv, err := daemon.ListenAndServe(d, port, token)
	if err != nil {
		t.Fatalf("临时 daemon 监听失败: %v", err)
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })

	// 构建被测 exe（临时目录，不覆盖仓库根 exe）。
	exe := filepath.Join(tmp, "ferryman-mcp-test.exe")
	build := exec.Command("go", "build", "-o", exe, ".")
	if out, berr := build.CombinedOutput(); berr != nil {
		t.Fatalf("go build 失败: %v\n%s", berr, out)
	}

	cmd := exec.Command(exe, "mcp")
	cmd.Env = append(os.Environ(), "FERRYMAN_CONFIG="+cfgPath)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stderr := &syncBuf{}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	out := bufio.NewReader(stdout)

	var seq int
	send := func(line string) {
		t.Helper()
		if _, werr := stdin.Write([]byte(line + "\n")); werr != nil {
			t.Fatalf("写 stdin 失败: %v", werr)
		}
	}
	recv := func() map[string]any {
		t.Helper()
		line, rerr := readLineTimeout(out, 20*time.Second)
		if rerr != nil {
			t.Fatalf("读 stdout 失败: %v（stderr: %s）", rerr, stderr.String())
		}
		var resp map[string]any
		if jerr := json.Unmarshal([]byte(line), &resp); jerr != nil {
			t.Fatalf("响应行非 JSON: %v (%q)", jerr, line)
		}
		return resp
	}
	call := func(method string, params any) map[string]any {
		t.Helper()
		seq++
		req := map[string]any{"jsonrpc": "2.0", "id": seq, "method": method}
		if params != nil {
			req["params"] = params
		}
		b, merr := json.Marshal(req)
		if merr != nil {
			t.Fatal(merr)
		}
		send(string(b))
		resp := recv()
		if fmt.Sprint(resp["id"]) != fmt.Sprint(seq) {
			t.Fatalf("响应 id 不匹配: got %v want %d", resp["id"], seq)
		}
		return resp
	}

	// ① initialize：capabilities.tools 在位＋serverInfo。
	initRes, ok := call("initialize", map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "test", "version": "1"},
	})["result"].(map[string]any)
	if !ok {
		t.Fatal("initialize 无 result")
	}
	caps, ok := initRes["capabilities"].(map[string]any)
	if !ok {
		t.Fatalf("capabilities 缺失: %v", initRes)
	}
	if _, ok := caps["tools"]; !ok {
		t.Fatalf("capabilities.tools 缺失: %v", caps)
	}
	si, _ := initRes["serverInfo"].(map[string]any)
	if si == nil || si["name"] != "ferryman" {
		t.Fatalf("serverInfo.name 应为 ferryman: %v", si)
	}
	// 通知静默。
	send(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)

	// ② tools/list：恰好六件（票05 起 doctor 入列——本测试同步遗留的五件断言）。
	listRes, ok := call("tools/list", nil)["result"].(map[string]any)
	if !ok {
		t.Fatal("tools/list 无 result")
	}
	arr, _ := listRes["tools"].([]any)
	names := map[string]bool{}
	for _, it := range arr {
		tm, _ := it.(map[string]any)
		n, _ := tm["name"].(string)
		names[n] = true
	}
	want := []string{"sessions", "session_detail", "gate_check", "cost_report",
		"heartbeat_status", "doctor"}
	if len(names) != len(want) {
		t.Fatalf("工具数 = %d, want %d: %v", len(names), len(want), names)
	}
	for _, w := range want {
		if !names[w] {
			t.Fatalf("缺工具 %s: %v", w, names)
		}
	}

	// ③ tools/call：sessions 打真临时 daemon，响应 JSON 原样透传。
	callRes, ok := call("tools/call", map[string]any{"name": "sessions",
		"arguments": map[string]any{}})["result"].(map[string]any)
	if !ok {
		t.Fatal("tools/call 无 result")
	}
	if callRes["isError"] == true {
		t.Fatalf("tools/call sessions 应成功: %v", callRes)
	}
	cnt, _ := callRes["content"].([]any)
	if len(cnt) == 0 {
		t.Fatalf("content 缺失: %v", callRes)
	}
	c0, _ := cnt[0].(map[string]any)
	text, _ := c0["text"].(string)
	var sr struct {
		Sessions []map[string]any `json:"sessions"`
	}
	if jerr := json.Unmarshal([]byte(text), &sr); jerr != nil {
		t.Fatalf("工具 text 非 JSON: %v (%q)", jerr, text)
	}
	if len(sr.Sessions) != 1 || sr.Sessions[0]["session_id"] != "sd-1" {
		t.Fatalf("sessions 应回夹具会话 sd-1: %v", sr.Sessions)
	}
	if strings.Contains(text, token) {
		t.Fatalf("工具响应泄漏 token: %s", text)
	}

	// ④ stdin 关闭 → 进程以退出码 0 收口。
	if err := stdin.Close(); err != nil {
		t.Fatal(err)
	}
	waitCh := make(chan error, 1)
	go func() { waitCh <- cmd.Wait() }()
	select {
	case werr := <-waitCh:
		if werr != nil {
			t.Fatalf("mcp 退出码非零: %v（stderr: %s）", werr, stderr.String())
		}
	case <-time.After(20 * time.Second):
		t.Fatal("mcp 子进程未在 stdin 关闭后退出")
	}
}
