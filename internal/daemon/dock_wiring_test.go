// dock_wiring_test.go — 票01：daemon 渡口接线验收钉子。
// F11 可执行证明：无 [dock] 节不监听渡口端口；有节才起——真实 TCP 端到端
// 经渡口转发到上游；渡口端口被占只告警不拖垮主服务。
package daemon

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"ferryman/internal/config"
	"ferryman/internal/dock"
)

// dialOK 端口是否可连（F11 判据的探针）。
func dialOK(addr string) bool {
	c, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

// waitDial 轮询直到端口可连或超时。
func waitDial(t *testing.T, addr string, timeout time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if dialOK(addr) {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

// listeningPortsOf 枚举某 PID 名下的 LISTENING 本地端口（netstat 文本解析，
// Windows 形；解析失败返回 nil＝调用方跳过该断言，不误报）。
func listeningPortsOf(pid int) map[string]bool {
	out, err := exec.Command("netstat", "-ano", "-p", "tcp").Output()
	if err != nil {
		return nil
	}
	ports := map[string]bool{}
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		// 形如: TCP  127.0.0.1:7311  0.0.0.0:0  LISTENING  <pid>
		if len(f) == 5 && f[0] == "TCP" && f[3] == "LISTENING" {
			if p, err := strconv.Atoi(f[4]); err == nil && p == pid {
				ports[f[1]] = true
			}
		}
	}
	return ports
}

func TestServeWithoutDockDoesNotListenOnDockPort(t *testing.T) {
	// F11：配置无 [dock] 节 → daemon 起来后不新增任何监听端口（含 15722）。
	// 主断言＝进程级端口差集（机器无关，比单查 15722 更强）；15722 拨测是
	// 附加断言——若被外部进程占用（如实验转发器）只跳过拨测不跳过主断言。
	const dockAddr = "127.0.0.1:15722"
	foreignOn15722 := dialOK(dockAddr)

	tmp := t.TempDir()
	dataDir := filepath.Join(tmp, "data")
	port := freePort(t)
	cfg := serveTestCfg(t, port, dataDir) // Dock 恒 nil（Default 不构造）

	before := listeningPortsOf(os.Getpid())
	ctx, cancel := context.WithCancel(context.Background())
	codeCh := make(chan int, 1)
	go func() { codeCh <- serveConfig(cfg, ctx, "dev") }()
	waitPidFile(t, filepath.Join(dataDir, "daemon.pid"), 10*time.Second)
	time.Sleep(300 * time.Millisecond) // 给潜在误启动留出窗口

	if before != nil {
		after := listeningPortsOf(os.Getpid())
		for addr := range after {
			if before[addr] {
				continue
			}
			if addr == fmt.Sprintf("127.0.0.1:%d", port) {
				continue // 主服务端口：唯一允许的新监听
			}
			cancel()
			t.Fatalf("无 [dock] 节却新增监听 %s——违反 F11 零行为变化（新增端口集: %v）",
				addr, diffPorts(before, after))
		}
	}
	if !foreignOn15722 && dialOK(dockAddr) {
		cancel()
		t.Fatal("无 [dock] 节却监听了 15722——违反 F11 零行为变化")
	}
	cancel()
	select {
	case code := <-codeCh:
		if code != 0 {
			t.Fatalf("优雅停 code = %d, want 0", code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("serveConfig 未在 ctx 取消后返回")
	}
}

// diffPorts 端口差集（after−before），失败信息用。
func diffPorts(before, after map[string]bool) []string {
	var out []string
	for a := range after {
		if !before[a] {
			out = append(out, a)
		}
	}
	return out
}

func TestServeWithDockSectionForwardsEndToEnd(t *testing.T) {
	// 有 [dock] 节：真实 TCP 经渡口 → 上游，体保真、头照抄、响应回流
	var mu sync.Mutex
	var gotBody []byte
	var gotUA, gotBeta string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		gotBody, gotUA, gotBeta = b, r.Header.Get("User-Agent"), r.Header.Get("Anthropic-Beta")
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("backend-ok"))
	}))
	defer backend.Close()

	port := freePort(t)
	dockAddr := fmt.Sprintf("127.0.0.1:%d", port)
	tmp := t.TempDir()
	dataDir := filepath.Join(tmp, "data")
	cfg := serveTestCfg(t, freePort(t), dataDir)
	cfg.Dock = &config.DockCfg{Listen: dockAddr, UpstreamBaseURL: backend.URL}

	ctx, cancel := context.WithCancel(context.Background())
	codeCh := make(chan int, 1)
	go func() { codeCh <- serveConfig(cfg, ctx, "dev") }()
	waitPidFile(t, filepath.Join(dataDir, "daemon.pid"), 10*time.Second)
	if !waitDial(t, dockAddr, 10*time.Second) {
		cancel()
		t.Fatal("渡口端口未就绪：[dock] 节存在但未启动监听")
	}

	body := []byte(`{"model":"claude-opus-5","metadata":{"session_id":"wire-e2e"},"messages":[]}`)
	req, _ := http.NewRequest(http.MethodPost, fmt.Sprintf("http://%s/v1/messages", dockAddr),
		bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "claude-cli/2.0.0 (external)")
	req.Header.Set("Anthropic-Beta", "claude-code-20250219")
	req.Header.Set("Authorization", "Bearer placeholder")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		cancel()
		t.Fatalf("经渡口请求失败: %v", err)
	}
	respBody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(respBody) != "backend-ok" {
		t.Fatalf("响应未回流: %q", respBody)
	}
	mu.Lock()
	defer mu.Unlock()
	if string(gotBody) != string(body) {
		t.Fatalf("上游收到体不保真: %q", gotBody)
	}
	if gotUA != "claude-cli/2.0.0 (external)" || gotBeta != "claude-code-20250219" {
		t.Fatalf("上游收到头不保真: ua=%q beta=%q", gotUA, gotBeta)
	}

	cancel()
	select {
	case code := <-codeCh:
		if code != 0 {
			t.Fatalf("优雅停 code = %d, want 0", code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("serveConfig 未在 ctx 取消后返回")
	}
	if waitDial(t, dockAddr, 3*time.Second) {
		t.Fatal("优雅停后渡口仍在监听")
	}
}

func TestServeWithDockPortTakenWarnsNotCrash(t *testing.T) {
	// 渡口端口被占：只告警降级，主服务照常起、照常优雅停
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	tmp := t.TempDir()
	dataDir := filepath.Join(tmp, "data")
	cfg := serveTestCfg(t, freePort(t), dataDir)
	cfg.Dock = &config.DockCfg{
		Listen:          fmt.Sprintf("127.0.0.1:%d", port),
		UpstreamBaseURL: "http://127.0.0.1:15721",
	}

	read := captureStdout(t)
	ctx, cancel := context.WithCancel(context.Background())
	codeCh := make(chan int, 1)
	go func() { codeCh <- serveConfig(cfg, ctx, "dev") }()
	waitPidFile(t, filepath.Join(dataDir, "daemon.pid"), 10*time.Second)
	cancel()
	code := <-codeCh
	out := read()
	if code != 0 {
		t.Fatalf("渡口失败不得拖垮主服务: code = %d, want 0", code)
	}
	if !strings.Contains(out, "渡口未启动") {
		t.Fatalf("缺渡口降级告警:\n%q", out)
	}
}

func TestDaemonDockSnapshotAccessor(t *testing.T) {
	// 未启用返回 nil；接线后返回快照句柄（票03 HttpBeatSender 依赖形状）
	d := NewDaemon(config.Default(), nil, nil, nil, nil, 0, nil)
	if d.DockSnapshot() != nil {
		t.Fatal("默认（渡口未启用）DockSnapshot() 应为 nil")
	}
	st := dock.NewSnapshotStore()
	d.DockSnap = st
	if d.DockSnapshot() != st {
		t.Fatal("接线后 DockSnapshot() 应返回注入的句柄")
	}
}
