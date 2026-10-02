package main

// status.go —— 票03：`ferryman status`（守护探活）与 `ferryman stop`（优雅停）。
//
// status 三面如实报告（守护/版本、渡口监听、台账摘要），恒 exit 0——状态查询
// 不是失败（票面明示）。stop 只走端点：POST /shutdown（既有管理端点，票04）→
// 等端口释放 + 进程退场（排水窗语义，与 internal/update 监督者停旧同序——只读
// 参照不复用包）；超时如实报告 exit 1，全程无 kill 路径——绝不硬杀。
//
// 可测性：HTTP 交互收在包级函数（daemonStats/daemonSessionsSummary/
// daemonShutdownPost），决策树收在 statusRun/stopRun，两者经 statusDeps/
// stopDeps 注桩（providerSwitchDeps 同范式）——单测 httptest 桩钉请求形态，
// 绝不发真请求到 15700/15722。

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/windows"

	"ferryman/internal/config"
	"ferryman/internal/daemon"
	"ferryman/internal/installer"
)

// statusUsage / stopUsage 各自的用法面（票01 契约：-h/--help 打印 usage 退 0，
// 用法错退 2；-h 与用法错共用一份常量）。
const statusUsage string = `用法:
  ferryman status               # 守护探活：守护/版本、渡口监听、台账摘要（只读，不在线也如实报告）
`

const stopUsage string = `用法:
  ferryman stop [--wait 秒]     # 优雅停守护：POST /shutdown + 等端口释放与进程退场
                                #   （--wait = 排水窗预算秒数，缺省 240；0=不等；超时如实报告，绝不硬杀）
`

// defaultStopWaitS stop 的排水窗预算缺省（票面明示 240s：渡口排水窗 + 优雅停
// 的实测量级之上留余量；--wait 可调）。
const defaultStopWaitS = 240

var (
	// errDaemonOffline 管理口拨不通（连接拒绝/超时）——「守护不在线」的单一
	// 判据：status 据此如实标注各面，stop 据此判「无需停止」。
	errDaemonOffline = errors.New("守护不在线")
	// errAuth 端点应答但鉴权失败（401/403）——在线但本机 token 与守护错位。
	errAuth = errors.New("鉴权失败")
	// errDaemonPIDUnknown daemon.pid 缺失/坏值/非法——「无从跟踪」不是失败
	// （supervisor 语义：pid≤0 视为放行）。
	errDaemonPIDUnknown = errors.New("daemon.pid 缺失或不可读")
)

// ---- 守护面 HTTP 客户端（真装配用；单测另有 httptest 直打） ----

// daemonGet 共用 GET（/stats、/sessions）：Bearer 鉴权，2s 超时（AlreadyRunning
// 同位——本机环回拖长无意义）。网络错 → errDaemonOffline；401/403 → errAuth；
// 其余非 200 → 带状态码错误。
func daemonGet(port int, token, path string) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet,
		fmt.Sprintf("http://127.0.0.1:%d%s", port, path), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := (&http.Client{Timeout: 2 * time.Second}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w（127.0.0.1:%d 无应答）", errDaemonOffline, port)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 读一次：先排水后解码会扑空
	if err != nil {
		return nil, err
	}
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return nil, errAuth
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return body, nil
}

// daemonStats GET /stats → 原样 map（版本/健康面字段名是 API 契约，status 只读
// 其中的 version；其余字段原样保留给将来扩面）。
func daemonStats(port int, token string) (map[string]any, error) {
	body, err := daemonGet(port, token, "/stats")
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// statusSessionsLimit 台账摘要的 limit：/sessions 缺省 50 会截断——status 要
// 在册总数，一次传大值取全（台账规模 = 会话数级，远小于该值）。
const statusSessionsLimit = 100000

// daemonSessionsSummary GET /sessions 台账摘要：在册会话数与闲置（凉）数。
// 闲置判定沿用端点的 stale 字段（idle ≥ 该 Agent 拦截阈值，与闸门同口径）——
// CLI 不自算阈值，避免两处口径漂移。
func daemonSessionsSummary(port int, token string) (registered, idle int, err error) {
	body, err := daemonGet(port, token, fmt.Sprintf("/sessions?limit=%d", statusSessionsLimit))
	if err != nil {
		return 0, 0, err
	}
	var out struct {
		Sessions []struct {
			Stale bool `json:"stale"`
		} `json:"sessions"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return 0, 0, err
	}
	for _, s := range out.Sessions {
		if s.Stale {
			idle++
		}
	}
	return len(out.Sessions), idle, nil
}

// daemonShutdownPost POST /shutdown（supervisor.postShutdown 同形态：POST +
// Bearer + 排水礼节；3s 超时——排水中的守护应答可能偏慢）。非 200 报错；
// 拨不通 → errDaemonOffline；401/403 → errAuth。
func daemonShutdownPost(port int, token string) error {
	req, err := http.NewRequest(http.MethodPost,
		fmt.Sprintf("http://127.0.0.1:%d/shutdown", port), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := (&http.Client{Timeout: 3 * time.Second}).Do(req)
	if err != nil {
		return fmt.Errorf("%w（127.0.0.1:%d 无应答）", errDaemonOffline, port)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20)) // 排水（RST 纪律）
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return errAuth
	case resp.StatusCode != http.StatusOK:
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}

// ---- 排水等待原语（internal/update 监督者同语义，就地镜像不复用包） ----

// waitPortFreeAddr 等端口释放（supervisor.waitPortFree 对齐）：拨不通 = 空。
// 500ms 拨号超时 + 200ms 间隔；预算 0 = 单次探测即判。
func waitPortFreeAddr(addr string, budget time.Duration) bool {
	deadline := time.Now().Add(budget)
	for {
		conn, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
		if err != nil {
			return true
		}
		_ = conn.Close()
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// stillActive GetExitCodeProcess 的「未退」惯例值（update 包同款本地常量——
// x/sys/windows 未导出 STILL_ACTIVE，supervisor 侧亦就地定义）。
const stillActive = 259

// procAliveWin PID 活性（update.procAliveImpl 就地镜像——非导出不能跨包复用）：
// OpenProcess(QUERY_LIMITED) 成功且退出码仍为 STILL_ACTIVE；打不开一律不活。
func procAliveWin(pid int) bool {
	if pid <= 0 {
		return false
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)
	var code uint32
	if err := windows.GetExitCodeProcess(h, &code); err != nil {
		return false
	}
	return code == stillActive
}

// waitPIDExit 等进程真正退出（supervisor.waitProcessExit 对齐）：端口释放 ≠
// 进程退场（监听口先关、进程后走——v0.1.1 演练实证）。pid≤0（daemon.pid 缺失
// 的罕见形态）视为放行。alive 注入 = 单测可钉，真装配传 procAliveWin。
func waitPIDExit(pid int, budget time.Duration, alive func(int) bool) bool {
	if pid <= 0 || budget <= 0 {
		return true
	}
	deadline := time.Now().Add(budget)
	for time.Now().Before(deadline) {
		if !alive(pid) {
			return true
		}
		time.Sleep(200 * time.Millisecond)
	}
	return !alive(pid)
}

// readDaemonPIDFile daemon.pid 读取（update.readDaemonPID 语义对齐）：JSON
// {pid:…}；缺失/坏值/非法 → errDaemonPIDUnknown（调用方放行，不挡优雅停）。
func readDaemonPIDFile(dataDir string) (int, error) {
	if dataDir == "" {
		return 0, errDaemonPIDUnknown
	}
	b, err := os.ReadFile(filepath.Join(dataDir, "daemon.pid"))
	if err != nil {
		return 0, errDaemonPIDUnknown
	}
	var pj struct {
		PID int `json:"pid"`
	}
	if err := json.Unmarshal(b, &pj); err != nil || pj.PID <= 0 {
		return 0, errDaemonPIDUnknown
	}
	return pj.PID, nil
}

// ---- status ----

// statusDeps status 可注入面（providerSwitchDeps 同范式）：真装配经
// realStatusDeps（port/token/渡口地址从 config 派生），HTTP/探测可注桩——
// 单测零网络。
type statusDeps struct {
	Port     int    // 守护管理口
	Token    string // Bearer（绝不打印）
	DockAddr string // 渡口监听地址 host:port；空 = 渡口未启用
	Stats    func(port int, token string) (map[string]any, error)
	Sessions func(port int, token string) (registered, idle int, err error)
	Dial     func(addr string, timeout time.Duration) error
}

// realStatusDeps 真装配：port/token 与守护同源（Server.Port 缺省 15700 与数据
// 目录 daemon.token——provider switch realProviderSwitchDeps 同纪律）；渡口
// 地址从 Dock.Listen 派生（节缺失 = 未启用，F11 opt-in）。cfg 不可得（nil）时
// 按缺省口探测并留空 token（端点 401 如实报）。
func realStatusDeps(cfg *config.Config) *statusDeps {
	port := installer.DefaultDaemonPort
	dockAddr := fmt.Sprintf("127.0.0.1:%d", installer.DefaultDockPort) // 配置不可读时的缺省探测口
	dataDir := ""
	if cfg != nil {
		if cfg.Server.Port != 0 {
			port = cfg.Server.Port
		}
		if cfg.Dock == nil {
			dockAddr = "" // [dock] 节缺失 = 渡口不启动——如实标「未启用」而非瞎探
		} else {
			dockAddr = cfg.Dock.Listen
			if dockAddr == "" {
				dockAddr = fmt.Sprintf("127.0.0.1:%d", installer.DefaultDockPort)
			}
		}
		dataDir = cfg.DataDir()
	}
	var token string
	if dataDir != "" {
		// EnsureToken 失败留空：端点会 401，如实报（provider switch 同纪律）；
		// 守护只要跑过一次 token 即在场——status 的常态是只读。
		token, _ = daemon.EnsureToken(dataDir)
	}
	return &statusDeps{Port: port, Token: token, DockAddr: dockAddr,
		Stats:    daemonStats,
		Sessions: daemonSessionsSummary,
		Dial: func(addr string, timeout time.Duration) error {
			conn, err := net.DialTimeout("tcp", addr, timeout)
			if err != nil {
				return err
			}
			_ = conn.Close()
			return nil
		}}
}

// statusProbeTimeout 渡口拨号探测超时（probeViewer 同位 800ms——本机环回）。
const statusProbeTimeout = 800 * time.Millisecond

// statusRun status 可测核心：三面（守护/渡口/台账）如实报告，恒 exit 0。
func statusRun(w io.Writer, deps *statusDeps) int {
	fmt.Fprintf(w, "Ferryman 状态（管理口 127.0.0.1:%d）\n", deps.Port)

	// 守护面：/stats 探活 + 版本。守护在线与否决定台账面可读性。
	stats, err := deps.Stats(deps.Port, deps.Token)
	switch {
	case err == nil:
		ver, _ := stats["version"].(string)
		if ver == "" {
			ver = "未知（/stats 无 version 字段）"
		}
		fmt.Fprintf(w, "守护: 在线（版本 %s）\n", ver)
	case errors.Is(err, errDaemonOffline):
		fmt.Fprintf(w, "守护: 不在线（127.0.0.1:%d 无应答）\n", deps.Port)
	case errors.Is(err, errAuth):
		fmt.Fprintf(w, "守护: 在线，但鉴权失败（401）——本机 daemon.token 读不到或与守护不一致\n")
	default:
		fmt.Fprintf(w, "守护: 探测异常: %v\n", err)
	}

	// 渡口面：TCP 拨号探测（渡口是守护同进程的另一监听口；守护死了口会随退，
	// 但残留进程/别家占用也探得出——如实报，不推断）。
	switch {
	case deps.DockAddr == "":
		fmt.Fprintln(w, "渡口: 未启用（配置无 [dock] 节）")
	case deps.Dial(deps.DockAddr, statusProbeTimeout) != nil:
		fmt.Fprintf(w, "渡口: 不在线（%s 拨不通）\n", deps.DockAddr)
	default:
		fmt.Fprintf(w, "渡口: 在线（%s）\n", deps.DockAddr)
	}

	// 台账面：在册/闲置（/sessions 端点）。守护面不可用（不在线/鉴权失败）时
	// 注定同败——不重复打一次吃超时，如实交代无从读取。
	if err != nil {
		fmt.Fprintln(w, "台账: 无从读取（守护面不可用）")
		return 0
	}
	reg, idle, serr := deps.Sessions(deps.Port, deps.Token)
	if serr != nil {
		fmt.Fprintf(w, "台账: 读取失败: %v\n", serr)
		return 0
	}
	fmt.Fprintf(w, "台账: 在册会话 %d，其中闲置(凉) %d\n", reg, idle)
	return 0
}

// cmdStatus `ferryman status` 入口（help 安全契约：-h/--help 打印 usage 退 0；
// 不接受任何参数——用法错退 2）。恒 exit 0：状态查询不是失败。
func cmdStatus(args []string) int {
	switch {
	case len(args) == 0: // 正路
	case len(args) == 1 && (args[0] == "-h" || args[0] == "--help"):
		fmt.Print(statusUsage)
		return 0
	default:
		fmt.Fprintf(os.Stderr, "未知参数 %q——ferryman status 不接受参数\n%s", args[0], statusUsage)
		return 2
	}
	cfg, err := config.Load("", false)
	if err != nil {
		// 配置载不动不挡探活：按缺省口如实报（升级监督者同纪律）
		fmt.Fprintf(os.Stderr, "配置加载失败（按缺省口 %d 探测）: %v\n", installer.DefaultDaemonPort, err)
		return statusRun(os.Stdout, realStatusDeps(nil))
	}
	return statusRun(os.Stdout, realStatusDeps(cfg))
}

// ---- stop ----

// stopDeps stop 可注入面（单测注桩钉决策树；真装配 realStopDeps）。
type stopDeps struct {
	Port     int
	Token    string
	DataDir  string
	Shutdown func(port int, token string) error
	WaitFree func(addr string, budget time.Duration) bool
	WaitExit func(pid int, budget time.Duration) bool
	ReadPID  func() (int, error)
	Dial     func(addr string, timeout time.Duration) error
}

// realStopDeps 真装配：port/token 与守护同源（realStatusDeps 同纪律）；等待
// 原语接排水窗实现；PID 从数据目录 daemon.pid 读（update 监督者同源）。
func realStopDeps(cfg *config.Config) *stopDeps {
	port := installer.DefaultDaemonPort
	dataDir := ""
	if cfg != nil {
		if cfg.Server.Port != 0 {
			port = cfg.Server.Port
		}
		dataDir = cfg.DataDir()
	}
	var token string
	if dataDir != "" {
		token, _ = daemon.EnsureToken(dataDir) // 失败留空：端点 401 如实报
	}
	return &stopDeps{Port: port, Token: token, DataDir: dataDir,
		Shutdown: daemonShutdownPost,
		WaitFree: waitPortFreeAddr,
		WaitExit: func(pid int, budget time.Duration) bool {
			return waitPIDExit(pid, budget, procAliveWin)
		},
		ReadPID: func() (int, error) { return readDaemonPIDFile(dataDir) },
		Dial: func(addr string, timeout time.Duration) error {
			conn, err := net.DialTimeout("tcp", addr, timeout)
			if err != nil {
				return err
			}
			_ = conn.Close()
			return nil
		}}
}

// stopRun stop 可测核心：POST /shutdown → 等端口释放 + 进程退场（排水窗语义，
// 监督者停旧同序）。超时如实报告 exit 1；本函数无任何 kill 路径——绝不硬杀。
func stopRun(w io.Writer, wait time.Duration, deps *stopDeps) int {
	addr := fmt.Sprintf("127.0.0.1:%d", deps.Port)
	// 先记旧 PID 再发 /shutdown（supervisor.stopDaemon 同纪律）：优雅停机过程会
	// 删 daemon.pid（serveConfig 倒数第二句），等端口释放后再读就没了——读不到
	// 只能跳过进程等待，"进程已退场"便成未经验证的断言。
	pid, _ := deps.ReadPID()
	err := deps.Shutdown(deps.Port, deps.Token)
	switch {
	case err == nil:
		fmt.Fprintf(w, "停机指令已应（/shutdown 200），等待让位（预算 %s）…\n", wait)
		if !deps.WaitFree(addr, wait) {
			fmt.Fprintf(w, "超时：端口 %s 在预算 %s 内未释放——未硬杀，守护可能仍在排水，稍后重试\n", addr, wait)
			return 1
		}
		// 端口释放 ≠ 进程退场（监听口先关、进程后走）；pid 无从跟踪时放行
		//（supervisor 同语义）。
		if pid > 0 && !deps.WaitExit(pid, wait) {
			fmt.Fprintf(w, "超时：端口已释放但 PID %d 在预算 %s 内未退出——未硬杀，请人工检查\n", pid, wait)
			return 1
		}
		fmt.Fprintln(w, "守护已停：端口已释放，进程已退场")
		return 0
	case errors.Is(err, errDaemonOffline):
		// 拨不通 ≠ 一定无监听（客户端超时也会落到这）：再拨一次区分
		// 「无监听（无需停）」与「有监听但不应答（非守护进程，不处置）」。
		if derr := deps.Dial(addr, statusProbeTimeout); derr != nil {
			fmt.Fprintf(w, "守护不在线（%s 无监听）——无需停止\n", addr)
			return 0
		}
		fmt.Fprintf(w, "端口 %s 有监听但 /shutdown 未应——非 Ferryman 守护?不处置（绝不硬杀）\n", addr)
		return 1
	case errors.Is(err, errAuth):
		fmt.Fprintln(w, "停机被拒：鉴权失败（401）——本机 daemon.token 与守护不一致，守护未停")
		return 1
	default:
		fmt.Fprintf(w, "停机失败: %v——守护未停\n", err)
		return 1
	}
}

// cmdStop `ferryman stop` 入口（help 安全契约：-h/--help 打印 usage 退 0）。
// 配置载不动不挡停机：按缺省口发令（token 留空 → 401 如实报）。
func cmdStop(args []string) int {
	if len(args) > 0 && (args[0] == "-h" || args[0] == "--help") {
		fmt.Print(stopUsage)
		return 0
	}
	fs := flag.NewFlagSet("stop", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	waitS := fs.Int("wait", defaultStopWaitS, "排水窗预算（秒：等端口释放+进程退场的上限；0=不等；缺省 240）")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) { // 迟到的 -h（如 `stop --wait 5 -h`）：flag 已打用法
			return 0
		}
		fmt.Fprint(os.Stderr, stopUsage)
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "未知参数 %q——ferryman stop 只接受 --wait\n%s", fs.Arg(0), stopUsage)
		return 2
	}
	if *waitS < 0 {
		fmt.Fprintf(os.Stderr, "--wait 不能为负（0=不等）\n%s", stopUsage)
		return 2
	}
	wait := time.Duration(*waitS) * time.Second
	cfg, err := config.Load("", false)
	if err != nil {
		fmt.Fprintf(os.Stderr, "配置加载失败（按缺省口 %d 发停机令）: %v\n", installer.DefaultDaemonPort, err)
		return stopRun(os.Stdout, wait, realStopDeps(nil))
	}
	return stopRun(os.Stdout, wait, realStopDeps(cfg))
}
