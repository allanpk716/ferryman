package daemon

// serve_test.go — 票17：serve 装配（规格 daemon.py:606-663）验收钉子：
// 横幅中文逐字 / pid JSON {pid,port,started_at} / 唯一化分流（已运行跳过
// return 0；非 Ferryman 占用 return 1）/ ctx(≡SIGINT) 优雅停（pid 删除）。

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ferryman/internal/config"
)

// serveTestCfg serve() 的测试配置形（临时目录密闭、快速阈值；serveConfig
// 直收 cfg 绕开 config.Load 的家目录寻址）。
func serveTestCfg(t *testing.T, port int, dataDir string) *config.Config {
	t.Helper()
	tmp := filepath.Dir(dataDir)
	cfg := config.Default()
	cfg.GateCC = "observe"
	cfg.GateCodex = "off"
	cfg.Thresholds = config.ThresholdCfg{SummarizeS: 1.0, BlockS: 2.0,
		MinCtxTokens: 1000, CacheWarnS: 720.0}
	cfg.Watch = config.WatchCfg{PollIntervalS: 3.0, CCProjectsDir: filepath.Join(tmp, "projects"),
		CodexSessionsDir: filepath.Join(tmp, "no-codex"),
		CodexExtraDirs:   []string{}, HarvestUsage: false}
	cfg.Server = config.ServerCfg{Port: port, DataDir: dataDir}
	cfg.FerryProvider = "glm"
	return cfg
}

// waitPidFile 轮询 pid 文件出现并返回其内容。
func waitPidFile(t *testing.T, path string, timeout time.Duration) []byte {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(path); err == nil {
			return data
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("pid 文件未出现: %s", path)
	return nil
}

// ---- 唯一化分流一：已有健康实例 → 打印跳过 + return 0（不落 pid） ----

func TestServeUniqueifySkipsWhenHealthyInstanceRunning(t *testing.T) {
	tmp := t.TempDir()
	dataDir := filepath.Join(tmp, "data")
	port := freePort(t)
	token, err := EnsureToken(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	serveBg(t, &stopDaemon{}, port, token) // 唯一化判据：同 token 的健康实例
	cfg := serveTestCfg(t, port, dataDir)

	read := captureStdout(t)
	code := serveConfig(cfg, context.Background(), "dev")
	out := read()
	if code != 0 {
		t.Fatalf("唯一化应 return 0, got %d", code)
	}
	want := fmt.Sprintf("[ferryman] 守护进程已在 127.0.0.1:%d 运行，本次启动跳过（唯一化）", port)
	if !containsLine(out, want) {
		t.Fatalf("唯一化文案缺失:\nwant: %s\ngot:  %q", want, out)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "daemon.pid")); !os.IsNotExist(err) {
		t.Fatal("唯一化跳过路径不得写 pid 文件")
	}
}

// ---- 唯一化分流二：端口被非 Ferryman 进程占用 → return 1 ----

func TestServePortTakenByForeignProcessReturns1(t *testing.T) {
	tmp := t.TempDir()
	ln, err := net.Listen("tcp", "127.0.0.1:0") // 裸监听：/stats 探测必败 = 非 Ferryman
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port
	cfg := serveTestCfg(t, port, filepath.Join(tmp, "data"))

	read := captureStdout(t)
	code := serveConfig(cfg, context.Background(), "dev")
	out := read()
	if code != 1 {
		t.Fatalf("被他人占用应 return 1, got %d", code)
	}
	want := fmt.Sprintf("[ferryman] 端口 %d 被非 Ferryman 进程占用，启动失败", port)
	if !containsLine(out, want) {
		t.Fatalf("占用失败文案缺失:\nwant: %s\ngot:  %q", want, out)
	}
}

// ---- 横幅逐字 + pid JSON + 优雅停 ----

func TestServePidBannerAndGracefulStop(t *testing.T) {
	tmp := t.TempDir()
	dataDir := filepath.Join(tmp, "data")
	port := freePort(t)
	cfg := serveTestCfg(t, port, dataDir)
	cfg.GateCC = "enforce" // 横幅字段差异化钉值

	col := startStdoutCapture(t)
	ctx, cancel := context.WithCancel(context.Background())
	codeCh := make(chan int, 1)
	go func() { codeCh <- serveConfig(cfg, ctx, "dev") }()

	pidPath := filepath.Join(dataDir, "daemon.pid")
	pidRaw := waitPidFile(t, pidPath, 10*time.Second)
	var pidObj map[string]any
	if err := json.Unmarshal(pidRaw, &pidObj); err != nil {
		t.Fatalf("pid 文件非 JSON: %v (%q)", err, pidRaw)
	}
	if pidObj["pid"].(float64) != float64(os.Getpid()) {
		t.Fatalf("pid = %v, want %d", pidObj["pid"], os.Getpid())
	}
	if pidObj["port"].(float64) != float64(port) {
		t.Fatalf("port = %v, want %d", pidObj["port"], port)
	}
	if s, _ := pidObj["started_at"].(string); s == "" {
		t.Fatalf("started_at 缺失: %v", pidObj)
	}

	// 横幅逐字（daemon.py:650-653；float 走 Python str(float) 渲染：1.0/2.0）
	wantBanner := fmt.Sprintf("[ferryman] serve: 127.0.0.1:%d · gate cc=%s codex=%s · 总结%ss/拦截%ss · provider=%s · 数据目录 %s",
		port, "enforce", "off",
		config.PyFloatStr(cfg.Thresholds.SummarizeS), config.PyFloatStr(cfg.Thresholds.BlockS),
		cfg.FerryProvider, dataDir)
	col.waitContains(t, wantBanner, 10*time.Second)

	// ctx 取消 ≡ KeyboardInterrupt → 优雅停 + pid 删除
	cancel()
	select {
	case code := <-codeCh:
		if code != 0 {
			t.Fatalf("优雅停应 return 0, got %d", code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("serveConfig 未在 ctx 取消后返回")
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(pidPath); os.IsNotExist(err) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("优雅停后 pid 文件未删除")
}

// TestServeConfigStatsCarriesVersion 版本装配链（票02，规格 §A）：main 的版本
// 经 ServeContext→serveConfig 注入 Daemon，/stats JSON 顶层 version 原样上报
// （面板页脚与升级探活校验的数据源）。
func TestServeConfigStatsCarriesVersion(t *testing.T) {
	tmp := t.TempDir()
	dataDir := filepath.Join(tmp, "data")
	port := freePort(t)
	cfg := serveTestCfg(t, port, dataDir)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	codeCh := make(chan int, 1)
	go func() { codeCh <- serveConfig(cfg, ctx, "test-ver-02") }()

	// 等 token 落盘 + /stats 健康（守护起完），再验 version 字段
	var token string
	waitForCond(t, 10*time.Second, func() bool {
		select {
		case code := <-codeCh:
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
	code, body := getRaw(t, port, "/stats", token)
	if code != 200 {
		t.Fatalf("GET /stats = %d %q, want 200", code, body)
	}
	var stats map[string]any
	if err := json.Unmarshal(body, &stats); err != nil {
		t.Fatalf("/stats 非 JSON: %v (%q)", err, body)
	}
	if v, _ := stats["version"].(string); v != "test-ver-02" {
		t.Fatalf("/stats version = %v, want test-ver-02", stats["version"])
	}

	cancel()
	select {
	case code := <-codeCh:
		if code != 0 {
			t.Fatalf("优雅停应 return 0, got %d", code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("serveConfig 未在 ctx 取消后返回")
	}
}

// containsLine 行级包含（横幅是整行打印）。
func containsLine(s, line string) bool {
	for _, l := range splitLines(s) {
		if l == line {
			return true
		}
	}
	return false
}

func splitLines(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}
