// 彩排台（tools/rehearsal，票05）：可复用的升级彩排资产——影子实例装配、
// 三场景（D10 口径）、五项故障注入、结果落盘 .scratch/upgrade-reliability/
// rehearsal/。一条命令可跑：
//
//	go run ./tools/rehearsal -all        # 完整验收（静默路径 10 次事务）
//	go run ./tools/rehearsal -quick      # CI/冒烟形态（静默路径 3 次）
//	go test ./tools/rehearsal/           # 同 -quick（见 rehearsal_test.go）
//
// 本 exe 同时充当影子世界里的 ferryman 本尊（同一构建两次 ldflags 注版本，
// 既是换装目标也是监督者载体——生产 `ferryman update` 的同映像形态：自身
// 映像==换装目标时直接两步换装，v0.5.2 票02 删自中继副本后不再交棒副本），
// 子命令面：
//
//	serve        # 影子守护（daemon.ServeContext；版本烤进 main.version）
//	supervisor … # 升级监督者（update.NewSupervisor 装配全注入；恒同步跑完）
//	sleeper …    # 占位进程（F4 备份位被占的运行映像；带探活口）
//
// 零生产面：一切端口高位随机（黑名单护栏见 util.go），目录全临时，
// USERPROFILE/FERRYMAN_CONFIG 重定向隔离（见 env.go 文件头）。
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"time"

	"ferryman/internal/daemon"
	"ferryman/internal/update"
)

// version 影子版本号：编译期 -ldflags "-X main.version=…" 注入——/stats
// version 的单源，必须等于桩 release 的 tag（版本校验谓词）。
var version = "dev"

func main() {
	args := os.Args[1:]
	if len(args) == 0 {
		usageMain()
		os.Exit(2)
	}
	switch args[0] {
	case "serve":
		os.Exit(runServe())
	case "supervisor":
		os.Exit(runSupervisor(args[1:]))
	case "sleeper":
		os.Exit(runSleeper(args[1:]))
	}
	os.Exit(runOrchestrate(args))
}

func usageMain() {
	fmt.Fprint(os.Stderr, `用法:
  rehearsal -all|-quick [-deadline-min N] [-scenario 名] [-fault 名]   # 编排彩排
  rehearsal serve                                                      # 影子守护（内部）
  rehearsal supervisor …                                               # 监督者（内部）
  rehearsal sleeper …                                                  # 占位进程（内部）
阶段名: quiet|force|longstream|launch-die|swarm|stale-lock|backup-occupied|drain-kill
`)
}

// ---- 影子守护 ----

// runServe 影子守护形态：真 daemon.ServeContext（配置经 FERRYMAN_CONFIG 注入
// 影子 config.toml；版本烤进 exe）。boot 行给蜂群/诊断断言留证。
func runServe() int {
	fmt.Printf("rehearsal shadow daemon boot: version=%s pid=%d\n", version, os.Getpid())
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	return daemon.ServeContext(ctx, false, version)
}

// ---- 编排面 CLI ----

func runOrchestrate(args []string) int {
	fs := flag.NewFlagSet("rehearsal", flag.ExitOnError)
	all := fs.Bool("all", false, "完整验收：三场景（静默路径 10 次事务）+ 五故障注入")
	quick := fs.Bool("quick", false, "冒烟形态：静默路径 3 次事务，其余不变")
	deadlineMin := fs.Float64("deadline-min", 0, "全局截止（分钟；缺省 all=15 / quick=8）")
	scenario := fs.String("scenario", "", "只跑指定场景（调试）")
	fault := fs.String("fault", "", "只跑指定故障注入（调试）")
	_ = fs.Parse(args)
	if !*all && !*quick && *scenario == "" && *fault == "" {
		usageMain()
		return 2
	}
	o := rehearsalOpts{Quick: *quick || *scenario != "" || *fault != ""}
	if *deadlineMin > 0 {
		o.Deadline = time.Duration(*deadlineMin * float64(time.Minute))
	} else if o.Quick {
		o.Deadline = 8 * time.Minute
	} else {
		o.Deadline = 15 * time.Minute
	}
	if *scenario != "" {
		o.Only = append(o.Only, *scenario)
	}
	if *fault != "" {
		o.Only = append(o.Only, *fault)
	}
	rep, err := runRehearsal(o)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[rehearsal] 失败: %v\n", err)
		return 1
	}
	fmt.Printf("[rehearsal] 总体: %s（%d 阶段，%.0fs）\n",
		passMark(rep.OverallPass), len(rep.Phases), rep.TotalDurSec)
	if !rep.OverallPass {
		return 1
	}
	return 0
}

// ---- 监督者形态（supervisor 子命令） ----

// runSupervisor 监督者形态：装配全注入地跑一次升级事务。自身映像==换装目标
// （生产同形态）→ v0.5.2（票02）删自中继副本后不再交棒 .supervisor-copy 副本：
// 直接两步换装（改名让位对运行映像放行），本进程同步跑完全程并亲自写
// result-out。
func runSupervisor(args []string) int {
	fs := flag.NewFlagSet("supervisor", flag.ExitOnError)
	dataDir := fs.String("data-dir", "", "影子 DataDir")
	port := fs.Int("port", 0, "影子守护管理口")
	apiBase := fs.String("api-base", "", "桩发布 API 基址")
	dlBase := fs.String("dl-base", "", "桩发布下载基址")
	startCmd := fs.String("start-cmd", "", "影子点火脚本")
	spec := fs.String("spec", "", "目标版本 tag")
	force := fs.Bool("force", false, "跳静默门硬切")
	waitQuiet := fs.Int("wait-quiet", 60, "静默门等待预算秒（0=不等）")
	pollTimeoutS := fs.Int("poll-timeout-s", 0, "版本校验轮询上限秒（0=包缺省）")
	portWaitS := fs.Int("port-wait-s", 0, "停旧等端口/进程退场上限秒（0=包缺省）")
	pollIntervalMs := fs.Int("poll-interval-ms", 0, "轮询间隔毫秒（0=包缺省）")
	probeDelayMs := fs.Int("probe-delay-ms", 0, "拉起验证首测延迟毫秒（0=包缺省）")
	probeTimeoutMs := fs.Int("probe-timeout-ms", 0, "拉起验证截止毫秒（0=包缺省）")
	resultOut := fs.String("result-out", "", "事务结果 JSON 落点")
	_ = fs.Parse(args)

	if version == "dev" || *dataDir == "" || *port == 0 || *apiBase == "" ||
		*dlBase == "" || *startCmd == "" || *resultOut == "" {
		fmt.Fprintln(os.Stderr, "supervisor 形态要求完整装配（且必须 ldflags 版本构建）")
		return 2
	}

	res, alerts := runSupervisorCore(update.Config{
		DataDir:      *dataDir,
		Port:         *port,
		Endpoints:    update.Endpoints{APIBase: *apiBase, DLBase: *dlBase, HTTP: loopbackClient()},
		Current:      version,
		Spec:         *spec,
		StartCmd:     *startCmd,
		WaitQuiet:    waitQuietFlag(*waitQuiet),
		Force:        *force,
		PollTimeout:  secondsDur(*pollTimeoutS),
		PortWait:     secondsDur(*portWaitS),
		PollInterval: milliDur(*pollIntervalMs),
		ProbeDelay:   milliDur(*probeDelayMs),
		ProbeTimeout: milliDur(*probeTimeoutMs),
	})
	writeTxResult(*resultOut, res, alerts)
	return txExitCode(res)
}

// runSupervisorCore 跑监督者并收集告警（告警进结果 JSON——拉起失败告警等
// 断言的数据源）。
func runSupervisorCore(cfg update.Config) (update.Result, []string) {
	var mu sync.Mutex
	var alerts []string
	cfg.Alert = func(title, message string) {
		mu.Lock()
		alerts = append(alerts, title+": "+message)
		mu.Unlock()
	}
	res := update.NewSupervisor(cfg).Run()
	mu.Lock()
	defer mu.Unlock()
	return res, alerts
}

// writeTxResult 事务结论原子落盘（编排进程 200ms 轮询读）。
func writeTxResult(path string, res update.Result, alerts []string) {
	if path == "" {
		return
	}
	b, err := json.Marshal(txResult{
		Success: res.Success, From: res.From, To: res.To,
		RolledBack: res.RolledBack, RollbackErr: res.RollbackErr,
		Err: errString(res.Err), Alerts: alerts,
	})
	if err != nil {
		return
	}
	_ = writeAtomic(path, b)
}

func txExitCode(res update.Result) int {
	if res.Success {
		return 0
	}
	return 1
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// loopbackClient 桩端点客户端：显式不走环境代理（ProxyFromEnvironment 可能
// 把环回也喂给代理，本地桩必须直连）。
func loopbackClient() *http.Client {
	return &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: 5 * time.Minute}
}

// waitQuietFlag 秒 → Config.WaitQuiet（0 = 不等哨兵负值，对齐 cmd/ferryman）。
func waitQuietFlag(seconds int) time.Duration {
	if seconds == 0 {
		return -1
	}
	return time.Duration(seconds) * time.Second
}

func secondsDur(s int) time.Duration { return time.Duration(s) * time.Second }

func milliDur(ms int) time.Duration { return time.Duration(ms) * time.Millisecond }

// ---- 占位进程（F4） ----

// runSleeper 占位进程：占着备份位的运行映像。探活口 GET /ping；POST /stop
// 自行退出（编排侧零强杀）。
func runSleeper(args []string) int {
	fs := flag.NewFlagSet("sleeper", flag.ExitOnError)
	pingPort := fs.Int("ping-port", 0, "探活口")
	pidOut := fs.String("pid-out", "", "PID 落点")
	_ = fs.Parse(args)
	if *pingPort == 0 {
		return 2
	}
	if *pidOut != "" {
		_ = os.WriteFile(*pidOut, []byte(strconv.Itoa(os.Getpid())), 0o644)
	}
	stop := make(chan struct{})
	mux := http.NewServeMux()
	mux.HandleFunc("/ping", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("alive"))
	})
	mux.HandleFunc("/stop", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("bye"))
		close(stop)
	})
	srv := &http.Server{Handler: mux, Addr: fmt.Sprintf("127.0.0.1:%d", *pingPort)}
	go func() {
		<-stop
		_ = srv.Close()
	}()
	if err := srv.ListenAndServe(); err != nil {
		select {
		case <-stop:
			return 0 // 正常收场（/stop 触发的 Close 错误）
		default:
			return 1
		}
	}
	return 0
}
