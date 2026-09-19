// serve.go — 票17：serve 装配（规格 ferryman/daemon.py:606-663 逐字平移）。
//
// 装配序：配置 → 数据目录 → token → Ledger/Store/Accounts → 工人（队列）→
// QWatchStats → Daemon → 监听（绑定失败分流：唯一化跳过 / 端口被占失败）→
// pid 文件 → Watcher/Worker 起 → 横幅逐字 → 阻塞 → SIGINT/ctx 优雅停
// （watcher.Stop/worker 停/pid 删除）。
//
// 语义同位：Python print(flush=True) → Go fmt.Println 直写（无缓冲 stdout）；
// KeyboardInterrupt → os/signal SIGINT（ctx 取消）；server.shutdown() → srv.Close。
package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"time"

	"github.com/BurntSushi/toml"

	"ferryman/internal/accounts"
	"ferryman/internal/beat"
	"ferryman/internal/clock"
	"ferryman/internal/config"
	"ferryman/internal/ledger"
	"ferryman/internal/store"
)

// FerrySession 生产摆渡执行器（票18 前占位：恒错 → 骨架降级保底不变量——
// 未实装期 serve 照常起、交接全走骨架，与 provider 未配置同一条兜底路；
// 票18 落地 internal/ferry 后以真实实现替换本 var）。
var FerrySession FerryFunc = func(string, Provider, float64,
	string) (string, map[string]any, error) {
	return "", nil, errors.New("摆渡执行器未实装（票18）")
}

// loadProviders 读 ~/ferryman/config.toml [providers.*]（Python
// ferry.load_config 占位平移：无文件/无节/坏 TOML → 空 map=全降级骨架；
// 票18 统一到 ferry 包后删除）。
func loadProviders() map[string]Provider {
	home, err := os.UserHomeDir()
	if err != nil {
		return map[string]Provider{}
	}
	raw, err := os.ReadFile(filepath.Join(home, "ferryman", "config.toml"))
	if err != nil {
		return map[string]Provider{}
	}
	var data map[string]any
	if _, err := toml.Decode(string(raw), &data); err != nil {
		return map[string]Provider{}
	}
	provsAny, _ := data["providers"].(map[string]any)
	out := map[string]Provider{}
	for key, blkAny := range provsAny {
		blk, _ := blkAny.(map[string]any)
		out[key] = Provider{
			Name:    key,
			BaseURL: tomlStr(blk, "base_url"),
			Model:   tomlStr(blk, "model"),
			APIKey:  tomlStr(blk, "api_key"),
			Window:  tomlIntOr(blk, "window", 131072),
		}
	}
	return out
}

func tomlStr(m map[string]any, k string) string {
	s, _ := m[k].(string)
	return s
}

func tomlIntOr(m map[string]any, k string, def int) int {
	switch n := m[k].(type) {
	case int64:
		return int(n)
	case float64:
		return int(n)
	}
	return def
}

// pidFileJSON daemon.pid 的行形（字段序 = Python dict 插入序）。
type pidFileJSON struct {
	PID       int    `json:"pid"`
	Port      int    `json:"port"`
	StartedAt string `json:"started_at"`
}

// Serve serve()（daemon.py:606-663）：配置→…→优雅停。返回进程退出码
// （0 = 正常/唯一化跳过；1 = 配置坏/端口被占）。Python config_mod.load 校验
// 失败未捕获 → traceback + 非零退出；Go 打印错误 + 1。
func Serve(relaxMinGap bool) int {
	cfg, err := config.Load("", relaxMinGap)
	if err != nil {
		fmt.Println(err)
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt) // KeyboardInterrupt 同位
	defer stop()
	return serveConfig(cfg, ctx)
}

// serveConfig serve() 的可测核心：cfg 由调用方给定（Serve 走 config.Load），
// ctx 取消 ≡ KeyboardInterrupt（优雅停）。返回退出码。
func serveConfig(cfg *config.Config, ctx context.Context) int {
	dataDir := cfg.DataDir()
	if err := os.MkdirAll(dataDir, 0o755); err != nil { // mkdir(parents=True, exist_ok=True)
		fmt.Println(err)
		return 1
	}
	token, err := EnsureToken(dataDir)
	if err != nil {
		fmt.Println(err)
		return 1
	}
	led := ledger.New()
	st, err := store.New(dataDir)
	if err != nil {
		fmt.Println(err)
		return 1
	}
	acc, err := accounts.New(dataDir)
	if err != nil {
		fmt.Println(err)
		return 1
	}
	worker := NewWorker(cfg, st, acc, loadProviders(), FerrySession)
	startedAt := clock.Now()
	qwatchStats := beat.NewQWatchStats() // 票04：daemon/watcher 共享计数器
	enqueue := func(s *ledger.SessionState) bool {
		// 身份字段（Agent/SessionID/TranscriptPath）建后不变直读（watcher 记账
		// 同纪律）；Cwd 可变 → 台账锁内快照。
		led.Mu().Lock()
		cwd := s.Cwd
		led.Mu().Unlock()
		return worker.Enqueue(map[string]any{
			"transcript_path": s.TranscriptPath,
			"agent":           s.Agent,
			"session_id":      s.SessionID,
			"cwd":             cwd,
		})
	}
	d := NewDaemon(cfg, led, st, enqueue, acc, startedAt, qwatchStats)
	ln, srv, err := ListenAndServe(d, cfg.Server.Port, token)
	if err != nil {
		// 唯一化（钩子自举的并发兜底）：绑定失败 = 端口已有监听者
		if AlreadyRunning(cfg.Server.Port, token) {
			fmt.Printf("[ferryman] 守护进程已在 127.0.0.1:%d 运行，"+
				"本次启动跳过（唯一化）\n", cfg.Server.Port)
			return 0
		}
		fmt.Printf("[ferryman] 端口 %d 被非 Ferryman 进程占用，启动失败\n", cfg.Server.Port)
		return 1
	}
	// pid 文件（daemon.py:639-643 逐字）：ensure_ascii=False → SetEscapeHTML(false)；
	// 结构体字段序 = Python dict 插入序（pid, port, started_at）。
	pidFile := filepath.Join(dataDir, "daemon.pid")
	var pidBuf bytes.Buffer
	enc := json.NewEncoder(&pidBuf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(pidFileJSON{
		PID:       os.Getpid(),
		Port:      cfg.Server.Port,
		StartedAt: time.Now().Format("2006-01-02 15:04:05"),
	})
	if err := os.WriteFile(pidFile, bytes.TrimRight(pidBuf.Bytes(), "\n"), 0o644); err != nil {
		fmt.Println(err)
		_ = srv.Close()
		_ = ln.Close()
		return 1
	}

	watcher := NewWatcher(cfg, led, st, enqueue, startedAt, acc, d, nil, qwatchStats)
	go func() { _ = srv.Serve(ln) }() // serve_forever 的 Go 形（一连接一 goroutine）
	go watcher.Run(ctx)
	go worker.Run(ctx)
	fmt.Printf("[ferryman] serve: 127.0.0.1:%d · gate cc=%s codex=%s · "+
		"总结%ss/拦截%ss · provider=%s · 数据目录 %s\n",
		cfg.Server.Port, cfg.GateCC, cfg.GateCodex,
		config.PyFloatStr(cfg.Thresholds.SummarizeS),
		config.PyFloatStr(cfg.Thresholds.BlockS),
		cfg.FerryProvider, dataDir)
	<-ctx.Done() // serve_forever 阻塞；KeyboardInterrupt ≈ ctx 取消
	fmt.Println("\n[ferryman] 停止中…")
	_ = srv.Close()        // server.shutdown()
	watcher.Stop()         // watcher.stop()
	worker.Stop()          // worker.stop()
	_ = os.Remove(pidFile) // unlink(missing_ok=True)
	return 0
}
