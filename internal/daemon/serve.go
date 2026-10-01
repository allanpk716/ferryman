// serve.go — 票17：serve 装配（规格 ferryman/daemon.py:606-663 逐字平移）。
//
// 装配序：配置 → 数据目录 → 升级锁让路查（票04：update.lock 被活监督者
// 持有即打印一行 + 退出码 0 静默让路，先于一切装配副作用）→ token →
// Ledger/Store/Accounts → 工人（队列）→
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
	"sync"
	"sync/atomic"
	"time"

	"ferryman/internal/accounts"
	"ferryman/internal/beat"
	"ferryman/internal/clock"
	"ferryman/internal/config"
	"ferryman/internal/dock"
	"ferryman/internal/ferry"
	"ferryman/internal/ledger"
	"ferryman/internal/store"
	"ferryman/internal/update"
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

// ---- 票04（规格 Implementation Decisions 第4条，D8）：守护层锁让路 ----

// serveSelfExe 自身映像路径（让路判定里的换装目标 = 守护自己；var 形 = 测试缝）。
var serveSelfExe = os.Executable

// lockYieldProbe 锁让路单源判定的接缝（var 形 = 测试缝，对齐 FerrySession
// 惯例）：update 包导出的只读助手，daemon 只调用、不复制第二份锁逻辑；探针
// 传 nil 用 update 包平台真实现（daemon 不碰进程面）。锁态→判定的映射矩阵
// （不存在/坏锁/持有者死/映像无关/目标映像/副本映像）在 update 包
// TestLockHeldByLiveSupervisor 钉死，此处不重复。
var lockYieldProbe = update.LockHeldByLiveSupervisor

// serveConfig serve() 的可测核心：cfg 由调用方给定（Serve 走 config.Load），
// ctx 取消 ≡ KeyboardInterrupt（优雅停）。version 版本号装配进 Daemon（/stats
// version 字段；测试传 "" 或 "dev" = 未注入回落态）。返回退出码。
func serveConfig(cfg *config.Config, ctx context.Context, version string) int {
	dataDir := cfg.DataDir()
	// 守护层锁让路（票04，D8）：升级事务进行中（update.lock 被活监督者持有）
	// → 打印一行 + 静默退出码 0（唯一化跳过 return 0 的同款早退，但更早——
	// 绑端口与一切装配副作用之前）：升级窗口内蜂群/看门/Run 键抢拉起的实例
	// 到此为止，事务拉起权归监督者独占。锁不存在/陈旧/映像无关 → 照常启动
	// ——正常开机/钩子拉起时 update.lock 不存在，零行为变化，只在升级事务
	// 持锁期间生效。自身映像取不到留空 → samePath 恒 false → 判定恒不持有
	// → 照常启动（让路面绝不阻塞启动）。
	// 监督者自拉起豁免(票04 集成缺陷修):带 SupervisorLaunchEnv 标记启动的
	// 是升级正主,不得给持锁的监督者让路——否则监督者拉的新守护一律静默
	// 退出,升级必败。蜂群/看门/Run 键不带标记,照常让路。
	if os.Getenv(update.SupervisorLaunchEnv) == "" {
		selfExe, _ := serveSelfExe()
		if holderPID, held := lockYieldProbe(dataDir, selfExe, nil, nil); held {
			fmt.Printf("[ferryman] 升级事务进行中（持有者 PID %d）——本实例静默让路\n", holderPID)
			return 0
		}
	}
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
	worker.Ledger = led                               // ADR-0013：摆渡产出回写处置边界（HandledContentTS）
	wireFerryChain(cfg.FerryChain, providers, worker) // 票03：显式链才开（见函数注释）
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
	// 关停来源落日志（票03，spec 热修第 5 件；扩票路径后三源皆真源头）：三个
	// 自愿退出源各记一行（来源＋时间），首源锁存（sourceMarker）防双记。
	// 端点源＝下方 cancel 包装处（取消动作发生处）；Ctrl+C 源＝信号观察者
	// （signal.Notify 多路广播挂同一信号，与上游 NotifyContext 互不干扰，信号
	// 到达即记）；托盘退出＝合并 exe 托盘「退出」点击动作处（cmd/ferryman
	// trayReady 菜单事件循环，票03 扩票路径纳入）经 NoteShutdownSource 预记，
	// Done 汇聚点（见下）消费落日志——三源共用同一把锁存，双源竞发不双记。
	// 外部强杀/崩溃无进程内日志机会（声明式残余，日志措辞不承诺全覆盖）；托盘
	// GUI 点击链本身不可单测（仅人工冒烟可验），daemon 侧预记→消费机制另有单测。
	marker := &sourceMarker{}
	srcCh := make(chan os.Signal, 1)
	signal.Notify(srcCh, os.Interrupt)
	defer signal.Stop(srcCh)
	go watchInterruptOnce(ctx, srcCh, marker.mark)
	// 票02（供应商接管，F4）：活跃上游内存态持有者——渡口逐请求读它（热切换
	// seam），/provider_switch 管理端点对它换绑＋落盘（provider_switch.go）。
	// 渡口未启用（[dock] 缺失）＝nil，端点不装。落盘路径与守护 Load 同源解析
	// （显式参数 > FERRYMAN_CONFIG > ~/ferryman/config.toml 单源）。
	var dockState *dockUpstreamState
	if cfg.Dock != nil {
		dockState = newDockUpstreamState(cfg.Dock, config.ResolveConfigPath(""))
	}
	var providerSwitchHook ProviderSwitchFunc
	if dockState != nil {
		providerSwitchHook = dockState.switchTo
	}
	ln, srv, err := ListenAndServeWithShutdown(d, cfg.Server.Port, token, func() {
		marker.mark("shutdown端点")
		cancel()
	}, providerSwitchHook)
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

	// 控制面即刻开始服务（v0.2.6：从渡口段之后提前至此）——pid 落盘后 7311 即
	// 应答 /stats：渡口绑定重试（最长 bind_retry_s=240s）阻塞装配期间，监督者
	// 的 90s 版本校验、看门/ensure 探活、restart 脚本的健康等待都必须能看到
	// 控制面应答（v0.2.5 实战回滚教训：Serve 排在渡口段后＝重试期 7311 只绑
	// 不服务，版本校验必超时）。
	go func() { _ = srv.Serve(ln) }() // serve_forever 的 Go 形（一连接一 goroutine）

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
			// 2026-09-29 复盘：单发绑定改有界重试（[dock].bind_retry_s 缺省
			// 240s）——升级/重启排水窗内渡口口被旧守护暂占时等得起，不再直接
			// 降级"无渡口"半死形态；控制口已先绑，重试期间探活面健康。
			newDock := func() (*dock.Server, error) {
				return dock.NewWithOptions(cfg.Dock.Listen, up.BaseURL, dock.Options{
					Upstream: up,
					// 票02（F4）：逐请求活跃上游 seam——热切换即时生效（在途请求
					// 持既有上游跑完）；dockState 恒非 nil（本分支 cfg.Dock 非 nil）。
					Resolver: dockState,
					Accounts: acc,
					Alert:    dock.AlertViaNotify(cfg),
				})
			}
			ds, derr := startDockWithRetry(newDock,
				time.Duration(cfg.Dock.BindRetryS*float64(time.Second)), dockRetryInterval,
				func(f string, a ...any) { fmt.Printf("[ferryman] %s\n", fmt.Sprintf(f, a...)) })
			if derr != nil {
				fmt.Printf("[ferryman] ⚠ 渡口未启动（监听 %s 失败，重试 %.0fs 后放弃，主服务不受影响）: %v\n",
					cfg.Dock.Listen, cfg.Dock.BindRetryS, derr)
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
	// 票03（D6）：判热钟换持久形——快照落 dataDir(reqclock.json)，构造时回种
	// （缺/坏=空钟 fail-safe），重启后判热门按真实钟值判，重启观察窗不再把
	// 缓存仍活的会话整批误判冷。装配在此处替换而非下沉 NewWatcher：测试直构
	// 形态（NewWatcher 全部测试）保持纯内存零落盘——cfg.DataDir() 缺省解析到
	// 生产家目录，不可让测试写生产数据目录。
	watcher.ReqClock = beat.NewPersistentLastRequestClock(
		filepath.Join(dataDir, "reqclock.json"))
	// （srv.Serve 已提前至 pid 落盘后——见上；此处不再重复起服务。）
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
	// 托盘源消费（票03 扩票路径）：合并 exe 托盘「退出」点击处已在取消链真源头
	// 预记（NoteShutdownSource → externalShutdownSource，stop() 取消 ctx 之前），
	// 这里经首源锁存落日志。未预记（守护被外部编排直取消/测试直 cancel）不误记
	// ——旧排除法兜底（睡 50ms 后「无端点无信号即记托盘」）已废：三源皆真源头，
	// 无源即无行，不再从「都没有」倒推归因。Ctrl+C 行由信号观察者自行落（信号
	// 到达即成为可运行，先于关停序的网络/落盘步骤完成，无须在此兜底查通道）。
	if src, ok := externalShutdownSource.Load().(string); ok && src != "" {
		marker.mark(src)
	}
	fmt.Println("\n[ferryman] 停止中…")
	_ = srv.Close() // server.shutdown()
	// 渡口随主服务优雅停（票03 接线：先 Shutdown 排水——上限取
	// [dock].drain_timeout_s，到期收尾由 Server 内部完成——再 Close 兜底；
	// 快照随进程消失，不持久化）。cfg.Dock == nil（未启用）不取键不解引用。
	if dockSrv != nil {
		shutdownDockGracefully(dockSrv, cfg.Dock.DrainTimeoutS)
	}
	watcher.Stop()         // watcher.stop()
	worker.Stop()          // worker.stop()
	_ = os.Remove(pidFile) // unlink(missing_ok=True)
	return 0
}

// DockSnapshot 渡口快照只读句柄（未启用返回 nil）。票03 HttpBeatSender 经
// 此取会话主快照（最大体）做心跳前缀源——daemon 其余代码不碰快照内部。
func (d *Daemon) DockSnapshot() *dock.SnapshotStore { return d.DockSnap }

// wireFerryChain 票03：显式顺位链装配——chainNames 非空（[ferry] chain 显式
// 配置）才解析装上链执行器；provider 单键不开链（等价单元素链的既有单级
// 路径行为零变化——结构校验/记账粒度对单 provider 配置保持原样）。解析失败
// （config.Load 已对同文件校验，正常不可达；providers 坏 TOML 降级空表后
// 可达）→ 警告不开链，回落 provider 既有路径（骨架兜底不变量优先）。返回
// 是否已开链（装配缝，测试直调）。
func wireFerryChain(chainNames []string, providers map[string]ferry.Provider, worker *Worker) bool {
	if len(chainNames) == 0 {
		return false
	}
	chain, err := ferry.ResolveChain(chainNames, providers)
	if err != nil {
		fmt.Printf("[ferry] ⚠ 摆渡链解析失败，链未启用（回落 provider 既有路径）: %v\n", err)
		return false
	}
	worker.Chain = chain
	worker.ChainFerry = ferry.ChainSession
	return true
}

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

// ---- 票03（spec 错误契约热修 1/5 的 daemon 侧）：排水接线与关停来源日志 ----

// externalShutdownSource 托盘退出源的进程级预记（票03 扩票路径）：合并 exe 的
// 托盘菜单事件循环（cmd/ferryman，daemon 包外）在取消 ctx 前写入，serveConfig
// 于 Done 汇聚点消费——跨包传递只经此一缝，不导出 marker 本体。仅生产托盘路径
// 写入（直取消形态/测试不触碰；未写或空串 = 无托盘源，Done 汇聚点不误记）。
var externalShutdownSource atomic.Value // 存 string

// NoteShutdownSource 预记关停来源（票03 扩票路径）：cmd/ferryman 托盘「退出」
// 点击动作处（取消链真源头：点击 → systray.Quit → Run 返回 → stop() → ctx
// 取消）在 systray.Quit 前调用；serveConfig 于 Done 汇聚点经首源锁存落日志，
// 与端点/Ctrl+C 源同锁存，双源竞发不双记。仅 serve 形态消费；面板形态托盘
// 退出无守护可归因，不调本函数。
func NoteShutdownSource(source string) {
	externalShutdownSource.Store(source)
}

// logShutdownSource 关停来源一行日志（来源＋时间）。var 形＝测试缝（对齐
// FerrySession 惯例——fmt 直写 stdout 的行经桩收形断言，不真采 stdout）。
var logShutdownSource = func(source string) {
	fmt.Printf("[ferryman] 关停来源: %s（%s）\n", source, time.Now().Format("2006-01-02 15:04:05"))
}

// sourceMarker 关停来源首源锁存：一次关停只有一个发起者，首个到源者记日志，
// 后到者静默——双源竞发（如排水期间又按 Ctrl+C）不重复记、不顶替已记事实。
type sourceMarker struct {
	mu  sync.Mutex
	set bool
}

func (m *sourceMarker) mark(source string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.set {
		return
	}
	m.set = true
	logShutdownSource(source)
}

// watchInterruptOnce Ctrl+C 源的信号观察者：信号到达即记（这就是"取消动作
// 发生处"——os.Interrupt 一到，上游 signal.NotifyContext 同瞬取消 ctx）。
// 注册侧用 signal.Notify 多路广播挂同一信号，与上游 NotifyContext 互不干扰、
// 互不抢道；ctx 结束（含 serveConfig 各早退路径的 defer cancel）即退出。
func watchInterruptOnce(ctx context.Context, ch <-chan os.Signal, mark func(string)) {
	select {
	case <-ch:
		mark("Ctrl+C中断")
	case <-ctx.Done():
	}
}

// shutdownDockGracefully 渡口排水关停（票03 接线，spec 热修第 1 件 daemon
// 侧）：Shutdown(排水ctx)——上限取 [dock].drain_timeout_s（config 解析层保证
// >0），传入 ctx 即排水上限的时钟（票02 语义）；到期收尾（流内 error 事件＋
// 硬收）由 Server 内部完成，daemon 不重复造收尾逻辑。之后 Close 兜底（票02
// 幂等，自然结束时重复收无害）。不悬挂：Shutdown 必在排水上限＋注入交付宽限
// 内返回。dockSrv 为 nil（[dock] 未配 / 构造或绑定失败降级）直接跳过——关停
// 路径不空引用，行为与未启用时不变。
func shutdownDockGracefully(dockSrv *dock.Server, drainTimeoutS float64) {
	if dockSrv == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(),
		time.Duration(drainTimeoutS*float64(time.Second)))
	defer cancel()
	if err := dockSrv.Shutdown(ctx); err != nil {
		fmt.Printf("[ferryman] ⚠ 渡口排水到期强收: %v\n", err)
	}
	_ = dockSrv.Close() // 兜底硬收
}
