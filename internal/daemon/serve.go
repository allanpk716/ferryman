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
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"time"

	"ferryman/internal/accounts"
	"ferryman/internal/beat"
	"ferryman/internal/clock"
	"ferryman/internal/config"
	"ferryman/internal/ferry"
	"ferryman/internal/ledger"
	"ferryman/internal/store"
)

// FerrySession 生产摆渡执行器（票18：internal/ferry 落地，票17 的恒错占位
// 退役；保留 var 形 = 测试可注入缝，签名即 FerryFunc）。
var FerrySession FerryFunc = ferry.FerrySession

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
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt) // KeyboardInterrupt 同位
	defer stop()
	return ServeContext(ctx, relaxMinGap)
}

// ServeContext Serve 的 ctx 注入形（票22 合并 exe 停机缝）：托盘退出/Ctrl+C
// 由调用方取消 ctx ≡ KeyboardInterrupt，serveConfig 走优雅停。CLI `serve`
// 子命令走 Serve（自带 SIGINT 装配）；合并 exe 的 serveAll 用本形。
func ServeContext(ctx context.Context, relaxMinGap bool) int {
	cfg, err := config.Load("", relaxMinGap)
	if err != nil {
		fmt.Println(err)
		return 1
	}
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
	// providers 配置（票18）：LoadProviders 恢复 Python load_config 语义——
	// 坏 TOML 上抛。Python FerryWorker.__init__ 里 load_providers() 无兜底，
	// 坏 TOML 直接炸 serve；Go 决断（已声明偏差）：此处捕获 → 打印警告 +
	// 空 map 继续起——一次性切换+日志驱动修复语境下，不炸守护比逐字上抛更
	// 合理（骨架兜底不变量优先）。
	providers, err := ferry.LoadProviders("")
	if err != nil {
		fmt.Printf("[ferry] ⚠ providers 配置解析失败——摆渡降级骨架: %v\n", err)
		providers = map[string]ferry.Provider{}
	}
	worker := NewWorker(cfg, st, acc, providers, FerrySession)
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
