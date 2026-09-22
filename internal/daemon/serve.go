// serve.go — 票17：serve 装配（规格 ferryman/daemon.py:606-663 逐字平移）。
//
// 装配序：配置 → 数据目录 → token → Ledger/Store/Accounts → 工人（队列）→
// QWatchStats → Daemon → 监听（绑定失败分流：唯一化跳过 / 端口被占失败）→
// pid 文件 → Watcher/Worker 起 → 横幅逐字 → 阻塞 → SIGINT/ctx 优雅停
// （watcher.Stop/worker 停/pid 删除）；POST /shutdown 管理端点（票04）取消
// 同一 ctx，走同一停序。
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
	"ferryman/internal/dock"
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
// 失败未捕获 → traceback + 非零退出；Go 打印错误 + 1。独立 Serve 形无装配面，
// 版本缺省 dev（合并 exe 的 serveAll 走 ServeContext 注入真实版本——票02）。
func Serve(relaxMinGap bool) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt) // KeyboardInterrupt 同位
	defer stop()
	return ServeContext(ctx, relaxMinGap, "dev")
}

// ServeContext Serve 的 ctx 注入形（票22 合并 exe 停机缝）：托盘退出/Ctrl+C
// 由调用方取消 ctx ≡ KeyboardInterrupt，serveConfig 走优雅停。CLI `serve`
// 子命令走 Serve（自带 SIGINT 装配）；合并 exe 的 serveAll 用本形。version
// 版本号经装配参数传入（票02，规格 §A——cmd/ferryman 的 main.version，
// internal 包不 import cmd，显式传参不做全局单例）。
func ServeContext(ctx context.Context, relaxMinGap bool, version string) int {
	cfg, err := config.Load("", relaxMinGap)
	if err != nil {
		fmt.Println(err)
		return 1
	}
	// 票01：旧 [dock] 单值首启迁移＋预置（加载配置后、渡口构造前；仅此处
	// 写回用户配置——CLI/doctor 只读解析）。失败如实告警并保留旧单值行为
	// 继续（ActiveUpstream 兜底包装），绝不丢配置。
	if err := config.MigrateDockFirstBoot("", cfg); err != nil {
		fmt.Printf("[ferryman] ⚠ [dock] 首启迁移失败（保留旧单值配置继续运行）: %v\n", err)
	}
	return serveConfig(cfg, ctx, version)
}

// serveConfig serve() 的可测核心：cfg 由调用方给定（Serve 走 config.Load），
// ctx 取消 ≡ KeyboardInterrupt（优雅停）。version 版本号装配进 Daemon（/stats
// version 字段；测试传 "" 或 "dev" = 未注入回落态）。返回退出码。
func serveConfig(cfg *config.Config, ctx context.Context, version string) int {
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
	worker.Ledger = led // ADR-0013：摆渡产出回写处置边界（HandledContentTS）
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
	d.Version = version // 票02：/stats version 字段（装配显式传参）
	// /shutdown（票04，规格 §C 第5条 停旧）：子 ctx 派生——管理端点的 cancel
	// 与 os.Interrupt 取消同一 Done 源，触发同一优雅停序：面板（srv）与守护
	// （渡口/watcher/worker/pid）一起收。
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	ln, srv, err := ListenAndServeWithShutdown(d, cfg.Server.Port, token, cancel)
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

	// 渡口（票01，F11 opt-in 裁定）：配置 [dock] 节才构造并启动——不配＝
	// 不绑端口、零行为变化。独立 listener/生命周期：构造或绑定失败只告警
	// 降级，绝不拖垮主服务（渡口挂＝CC 直连上游旧行为，base_url 指回即回退）。
	// 把 CC base_url 指到渡口是人工操作，不在本程序职责内。
	// 票01 装配按 active：目的地/真钥/model_map/text_only 全部取自 active 条目
	// （ActiveUpstream 单源——新旧并存以新表为准，F9）；旧单值字段此处不读。
	var dockSrv *dock.Server
	if cfg.Dock != nil {
		if name, up := cfg.Dock.ActiveUpstream(); up == nil {
			fmt.Printf("[ferryman] ⚠ 渡口未启动：[dock].active %q 未指向上游表中的任何条目\n",
				cfg.Dock.Active)
		} else {
			// 票06 接线（票01 起条目化）：改写隐含开启，守卫/doctor 判定在
			// dock 包内单源裁决（本地中转地址拒绝即退纯透传）。
			ds, derr := dock.NewWithOptions(cfg.Dock.Listen, up.BaseURL, dock.Options{
				Upstream: up,
				Accounts: acc,
				Alert:    dock.AlertViaNotify(cfg),
			})
			if derr != nil {
				fmt.Printf("[ferryman] ⚠ 渡口未启动（配置无效）: %v\n", derr)
			} else if derr = ds.Start(); derr != nil {
				fmt.Printf("[ferryman] ⚠ 渡口未启动（监听 %s 失败，主服务不受影响）: %v\n",
					cfg.Dock.Listen, derr)
			} else {
				dockSrv = ds
				d.DockSnap = ds.Snapshots()
				label := name
				if label == "" {
					label = "旧单值" // 无表兜底（未迁移/迁移失败回退）
				}
				fmt.Printf("[ferryman] 渡口: http://%s → %s（active=%s；模式由守卫裁决；快照内存态，重启即失）\n",
					cfg.Dock.Listen, up.BaseURL, label)
			}
		}
	}

	// 心跳真身注入（票03）：渡口开→HttpBeatSender（发往渡口入站口，与真
	// 流量同路径同改写）；渡口关→nil＝watcher 既有"enforce 无 sender→observe
	// 演练＋告警一次"降级路径原样保留。
	watcher := NewWatcher(cfg, led, st, enqueue, startedAt, acc, d,
		newBeatSender(cfg, d.DockSnap), qwatchStats)
	go func() { _ = srv.Serve(ln) }() // serve_forever 的 Go 形（一连接一 goroutine）
	go watcher.Run(ctx)
	go worker.Run(ctx)
	// 交接库 30 天清理（DESIGN §6.15 TODO 落地，ADR-0013）：启动一次 + 每日
	// 例行；异常吞掉（清理是尽力而为的卫生件，绝不拖垮守护）。
	go func() {
		prune := func() {
			defer func() {
				if r := recover(); r != nil {
					fmt.Printf("[store] ⚠ 30 天清理异常（忽略继续）: %v\n", r)
				}
			}()
			if n := st.PruneOlderThan(store.PruneAge); n > 0 {
				fmt.Printf("[store] 交接库 30 天清理：%d 个超龄文件已删\n", n)
			}
		}
		prune()
		tk := time.NewTicker(24 * time.Hour)
		defer tk.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tk.C:
				prune()
			}
		}
	}()
	fmt.Printf("[ferryman] serve: 127.0.0.1:%d · gate cc=%s codex=%s · "+
		"总结%ss/拦截%ss · provider=%s · 数据目录 %s\n",
		cfg.Server.Port, cfg.GateCC, cfg.GateCodex,
		config.PyFloatStr(cfg.Thresholds.SummarizeS),
		config.PyFloatStr(cfg.Thresholds.BlockS),
		cfg.FerryProvider, dataDir)
	<-ctx.Done() // serve_forever 阻塞；KeyboardInterrupt ≈ ctx 取消
	fmt.Println("\n[ferryman] 停止中…")
	_ = srv.Close() // server.shutdown()
	if dockSrv != nil {
		_ = dockSrv.Close() // 渡口随主服务优雅停（快照随进程消失，不持久化）
	}
	watcher.Stop()         // watcher.stop()
	worker.Stop()          // worker.stop()
	_ = os.Remove(pidFile) // unlink(missing_ok=True)
	return 0
}

// DockSnapshot 渡口快照只读句柄（未启用返回 nil）。票03 HttpBeatSender 经
// 此取会话主快照（最大体）做心跳前缀源——daemon 其余代码不碰快照内部。
func (d *Daemon) DockSnapshot() *dock.SnapshotStore { return d.DockSnap }

// newBeatSender 票03 serve 注入点：渡口开（配了 [dock] 且快照句柄在——含
// 渡口构造/绑定失败降级为 nil 的情形）→ HttpBeatSender（发往渡口入站口）；
// 渡口关 → nil＝watcher.sendBeat 既有降级（enforce 无 sender→observe 演练＋
// 告警一次），行为分支不动。
func newBeatSender(cfg *config.Config, dockSnap *dock.SnapshotStore) beat.Sender {
	if cfg.Dock == nil || dockSnap == nil {
		return nil
	}
	return beat.NewHttpBeatSender("http://"+cfg.Dock.Listen, dockSnap)
}
