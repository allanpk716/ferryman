// ferryman 合并 exe——守护 + 面板 + 托盘 + CLI 单进程（票22；吸收 cmd/viewer）。
//
// 行为面（spec C9 console 子系统：终端输出全部可见，窗口性交给启动方）：
//
//	ferryman                     # 无参 = serve：守护(7311) + 面板(15900) + 托盘
//	ferryman serve               # 同上（点火脚本 start-daemon.cmd 调它）
//	ferryman doctor              # 一键体检
//	ferryman version             # 版本号（dev = 非 release 构建）
//	ferryman update [--check] [vX.Y.Z] [--prerelease]  # 无 --check = 执行升级
//	                                   # （监督者：下载/校验/换装/重启/回滚）；--check
//	                                   # 只报告不动手；显式版本支持降级
//	ferryman install-cc [--events E1,E2,…]
//	ferryman install-ccswitch
//	ferryman install-codex [--events E1,E2,…]
//	ferryman install-mcp [--force]
//	ferryman account report [--since …] [--until …] [--project …] [--session …]
//	                        [--kind …] [--provider …] [--json]
//
// 面板族 flags（原 cmd/viewer 语义原样）：--demo / --port N / --no-tray /
// --no-browser / --install-shortcuts——带这些 flag（不带子命令）= 只起面板
// （viewer 形态：演示合成账本、探测短路转开浏览器、建快捷方式后退出）。
//
// serve 装配：internal/viewer/server 挂 15900（缺省；env FERRYMAN_PANEL_PORT
// 或 --port 覆盖）+ daemon.ServeContext(ctx) 在 goroutine 跑守护 + systray
// 主线程阻塞（菜单「打开面板/退出」；退出 = 取消 ctx = 停守护）。
// --no-tray 时前台阻塞，Ctrl+C 优雅停（--smoke 放宽阈值差校验同 Python CLI）。
package main

import (
	"context"
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/getlantern/systray"
	"golang.org/x/sys/windows"

	"ferryman/internal/config"
	"ferryman/internal/cutover"
	"ferryman/internal/daemon"
	"ferryman/internal/installer"
	"ferryman/internal/mcp"
	"ferryman/internal/notify"
	"ferryman/internal/report"
	"ferryman/internal/update"
	"ferryman/internal/viewer/demo"
	"ferryman/internal/viewer/server"
)

//go:embed web
var webFS embed.FS

//go:embed icon.ico
var iconICO []byte

// faviconSVG 内联图标（暗底小帆船，呼应"摆渡人"）：Go 端伺服 /favicon.svg 免 404。
const faviconSVG = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 32 32">` +
	`<rect width="32" height="32" rx="6" fill="#111111"/>` +
	`<line x1="16" y1="6" x2="16" y2="21" stroke="#dddddd" stroke-width="1.6"/>` +
	`<path d="M17 8 L24 19 L17 19 Z" fill="#64b5f6"/>` +
	`<path d="M15 10 L10 19 L15 19 Z" fill="#4caf50"/>` +
	`<path d="M5 22 H27 L23 27 H9 Z" fill="#ffb74d"/>` +
	`</svg>`

const (
	// defaultPanelPort 面板缺省口：快捷方式钉死口（port=0 随机口会让"第二实例
	// 探测转开浏览器"的短路逻辑失灵——快捷方式必须钉死端口）。
	defaultPanelPort = 15900
	// panelPortEnv serve 模式面板口的 env 覆盖（票22：面板口可 env 覆盖）。
	panelPortEnv = "FERRYMAN_PANEL_PORT"
)

// version 版本号：编译期由 -ldflags "-X main.version=…" 注入（build.ps1
// -Release 与发布 CI；值取 git describe --tags --always）；缺省 dev =
// 本地开发构建（ferryman version 时带「非 release 构建」提示）。
var version = "dev"

const usage = `ferryman — 摆渡人：会话闲置缓存失效后的自动交接守护（守望→摆渡→闸门→归还）

用法:
  ferryman                # 无参 = serve：守护(127.0.0.1:7311) + 面板(15900) + 托盘
  ferryman serve [--port N] [--no-tray] [--smoke]
  ferryman doctor         # 一键体检：钩子在位/脚本健康/快照覆盖/daemon 活性
  ferryman version        # 打印版本号（dev = 非 release 构建）
  ferryman install-cc [--events 事件1,事件2,…]
  ferryman install-ccswitch
  ferryman install-codex [--events 事件1,事件2,…]
  ferryman install-mcp [--force]    # agent 面 MCP server 注册进 CC 用户级配置
                                   # （幂等；外部/不兼容同名条目默认拒绝，--force 覆盖）
  ferryman autostart install|uninstall|status  # 登录自启 Run 键（票02 常驻保障）
  ferryman watchdog             # 看门单次探活：无监听拉起 daemon（票02，schtasks 每 5 分钟调它）
  ferryman watchdog install|uninstall|status   # 看门计划任务三操作
  ferryman account report [--since 日] [--until 日] [--project 名] [--session id]
                      [--kind 类型] [--provider 键] [--json]
  ferryman mcp [--config 路径]      # stdio MCP server（agent 面只读工具，票04；
                                  # CC 等 MCP 客户端把本命令注册为 server 用）
  ferryman cutover backup [--data 目录] [--dest 目录]
  ferryman cutover rollback-write [--repo 目录] [--data 目录]
  ferryman cutover rollback-drill [--repo 目录] [--dir 临时目录]
  ferryman cutover smoke [--config 沙箱配置]
  ferryman upstream list [--config 路径]       # 渡口上游表：active 标注/base_url/
                                             #   model_map 概要/可用状态/密钥脱敏
  ferryman upstream use <名> [--config 路径]   # 切换 active 并自动重启守护
                                             #   （在途请求中断；守护未起来时如实
                                             #   报告，不自动回滚/重试）

面板（时间线查看器，viewer 原样）:
  ferryman --demo [--port N] [--no-tray] [--no-browser]
  ferryman --install-shortcuts    # 建桌面+开始菜单快捷方式后退出
  ferryman --data 目录 [--port N] [--no-tray] [--no-browser]

--events 缺省 = 全集（UserPromptSubmit,SessionStart,SubagentStart,SubagentStop）；
切换日按用户指令只装三类（闸门 UserPromptSubmit 暂不装，C12）。

cutover 族（票23 切换工具，不执行生产切换）:
  backup         数据全量备份（accounts/*.jsonl+handoffs/*.md+index.json+
                 config.toml+daemon.token → 带时间戳目录；只读复制）
  rollback-write 生成回退工件 <data>/rollback-to-python.cmd（幂等；切换日跑）
  rollback-drill 临时环境演练回退机制（临时 worktree+临时启动器副本，
                 绝不碰真实 start-daemon.cmd；tag 未打自动降级路径探测）
  smoke          沙箱冒烟四链路（独立端口+独立数据目录，直打 API；
                 ①observe 警告 ②摆渡 fresh+账本行 ③归还 ④enforce block 契约）
`

func main() {
	os.Exit(run(os.Args[1:]))
}

// run 子命令分发：无参 = serve；已知子命令各走各的面；其余（含面板族 flags）
// = 只起面板（viewer 形态）。
func run(args []string) int {
	if len(args) == 0 {
		return serveAll(serveOpts{})
	}
	switch args[0] {
	case "serve":
		return cmdServe(args[1:])
	case "doctor":
		return installer.RunDoctor(version) // 版本经装配参数进（票02，规格 §A）
	case "version":
		return cmdVersion(args[1:], os.Stdout)
	case "update":
		return cmdUpdate(args[1:], os.Stdout)
	case "install-cc":
		return cmdInstallCC(args[1:])
	case "install-ccswitch":
		return cmdInstallCCSwitch()
	case "install-codex":
		return cmdInstallCodex(args[1:])
	case "install-mcp":
		return cmdInstallMCP(args[1:])
	case "autostart":
		return cmdAutostart(args[1:])
	case "watchdog":
		return cmdWatchdog(args[1:])
	case "account":
		return cmdAccount(args[1:])
	case "cutover":
		return cmdCutover(args[1:])
	case "upstream":
		return cmdUpstream(args[1:], os.Stdout)
	case "mcp":
		return cmdMCP(args[1:])
	case "help", "-h", "--help":
		fmt.Print(usage)
		return 0
	}
	return runPanel(args)
}

// serveAll serve 模式的可测核心：面板（15900/env/--port）+ 守护（goroutine）
// + 托盘（主线程阻塞；--no-tray 前台 Ctrl+C）。
type serveOpts struct {
	panelPort int // 0 = env FERRYMAN_PANEL_PORT 或 15900
	noTray    bool
	noBrowser bool
	smoke     bool // 放宽阈值差≥120s 校验（秒级阈值冒烟用，Python --smoke 同位）
}

func cmdServe(args []string) int {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	port := fs.Int("port", 0, "面板端口（0=FERRYMAN_PANEL_PORT 或 15900）")
	noTray := fs.Bool("no-tray", false, "不建系统托盘图标（前台运行，Ctrl+C 退出）")
	noBrowser := fs.Bool("no-browser", false, "面板口被占探到活面板时不自动开浏览器")
	smoke := fs.Bool("smoke", false, "放宽阈值差≥120s 校验（秒级阈值冒烟用）")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	return serveAll(serveOpts{panelPort: *port, noTray: *noTray,
		noBrowser: *noBrowser, smoke: *smoke})
}

func serveAll(o serveOpts) int {
	// 面板口：显式 --port > env FERRYMAN_PANEL_PORT > 15900
	panelPort := o.panelPort
	if panelPort == 0 {
		if v := strings.TrimSpace(os.Getenv(panelPortEnv)); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				panelPort = n
			}
		}
	}
	if panelPort == 0 {
		panelPort = defaultPanelPort
	}

	// Ctrl+C（--no-tray 前台 / 托盘模式下同样生效）→ ctx 取消 → 守护优雅停
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	// 面板装配（serve 用真实数据根——--demo 是面板族专属）
	dir := resolveDataDir("")
	mux := panelMux(dir, "")
	ln, lerr := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", panelPort))
	panelURL := fmt.Sprintf("http://127.0.0.1:%d", panelPort)
	httpsrv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	if lerr != nil {
		// 面板口被占：探得到 /api/sessions = 面板已在跑 → 开浏览器完事（快捷
		// 方式语义："点一下必达面板"）；否则让位——守护是主职责，面板缺位只告警
		if probeViewer(panelURL) {
			fmt.Println("面板已在运行，直接打开：", panelURL)
			if !o.noBrowser {
				openBrowser(panelURL)
			}
		} else {
			fmt.Printf("[ferryman] 面板口 %d 被非 Ferryman 进程占用——守护照跑，面板缺位\n", panelPort)
		}
		ln = nil
	} else {
		go func() { _ = httpsrv.Serve(ln) }() // 面板开跑（守护停时 Close 收口）
		fmt.Printf("面板: http://%s （数据目录 %s）\n", ln.Addr(), dir)
	}

	// 守护 goroutine：ServeContext(ctx) ≡ Python serve()；ctx 取消 ≡ KeyboardInterrupt
	var daemonCode int
	done := make(chan struct{})
	go func() {
		daemonCode = daemon.ServeContext(ctx, o.smoke, version) // 版本经装配参数进（票02，规格 §A）
		close(done)
	}()

	fmt.Println("Ferryman 合并 exe：守护 + 面板 + 托盘（托盘「退出」= 停守护）")
	if o.noTray {
		fmt.Println("（--no-tray：无托盘，Ctrl+C 退出）")
		<-done // 守护是生命周期主人：唯一化跳过/启动失败/优雅停都从这里回来
		if ln != nil {
			_ = httpsrv.Close()
		}
		return daemonCode
	}

	// 托盘主线程阻塞。守护先死（唯一化跳过/配置失败/Ctrl+C）→ 收托盘；
	// 用户点「退出」→ Run 返回 → 取消 ctx 停守护。
	trayReadyCh := make(chan struct{})
	trayExitedCh := make(chan struct{})
	go func() {
		<-done
		select {
		case <-trayReadyCh:
			systray.Quit() // 守护已停，托盘不留
		case <-trayExitedCh: // 用户先点了退出
		}
	}()
	url := panelURL
	if ln != nil {
		url = fmt.Sprintf("http://%s", ln.Addr())
	}
	systray.Run(func() { trayReady(url); close(trayReadyCh) }, func() {})
	close(trayExitedCh)
	stop() // 「退出」= 停守护（ctx 取消 → serveConfig 优雅停）
	<-done
	if ln != nil {
		_ = httpsrv.Close()
	}
	return daemonCode
}

// panelMux 面板路由装配（面板 API + favicon + 内嵌静态页；serve 与面板族共用）。
// note 非空时贴到面板 API（--demo 的「合成账本」注记）。
func panelMux(dir, note string) *http.ServeMux {
	srv := server.New(dir)
	if note != "" {
		srv.SetNote(note)
	}
	mux := srv.Routes()
	mux.HandleFunc("GET /favicon.svg", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/svg+xml")
		w.Header().Set("Cache-Control", "max-age=86400")
		_, _ = w.Write([]byte(faviconSVG))
	})
	// 版本 API（票02，规格 §A）：页脚版本号的数据源——前端运行时取，不烘焙进
	// 静态资源；viewer server（路径外）不动，端点在面板装配层就地注册。
	mux.HandleFunc("GET /api/version", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		b, err := json.Marshal(map[string]string{"version": version})
		if err != nil { // map[string]string 不可达，护底线
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		_, _ = w.Write(b)
	})
	sub, err := fs.Sub(webFS, "web")
	if err != nil {
		log.Fatal(err)
	}
	mux.Handle("/", http.FileServer(http.FS(sub)))
	return mux
}

// trayItem 托盘菜单项描述（纯数据；装配抽成纯函数——systray 本体不可单测，
// 单测只断言项序与 disabled，票07）。
type trayItem struct {
	title    string // 菜单标题
	tooltip  string // 悬停提示
	disabled bool   // 置灰纯展示项
}

// trayMenuItems 托盘菜单装配（票07，规格 §D）：「版本 vX.Y.Z」（disabled 展示，
// 文本随包级 version——dev 显 dev）→「打开面板」→「检查更新」（只读）→
// 「立即升级」→「退出」。纯函数：返回项与顺序，trayReady 据此 Add。
func trayMenuItems(ver, url string) []trayItem {
	return []trayItem{
		{title: "版本 " + ver, tooltip: "当前版本", disabled: true},
		{title: "打开面板", tooltip: url},
		{title: "检查更新", tooltip: "查询 GitHub 最新版本（只读，不下载）"},
		{title: "立即升级", tooltip: "后台拉起升级监督者：停守护→换装→重启，完成后通知"},
		{title: "退出", tooltip: "停守护并退出（面板随之不可访问）"},
	}
}

// trayReady 托盘就绪：帆船图标 + 面板地址 tooltip + 菜单（装配见 trayMenuItems；
// 票07 新增检查更新/立即升级）。退出 = 停守护——systray.Run 返回后主流程取消
// ctx；检查/升级动作在各自 goroutine 里跑，不堵菜单事件循环。
func trayReady(url string) {
	systray.SetIcon(iconICO)
	systray.SetTooltip("Ferryman 守护+面板 · " + url)
	it := trayMenuItems(version, url)
	ver := systray.AddMenuItem(it[0].title, it[0].tooltip)
	ver.Disable() // 版本展示项置灰
	open := systray.AddMenuItem(it[1].title, it[1].tooltip)
	check := systray.AddMenuItem(it[2].title, it[2].tooltip)
	upNow := systray.AddMenuItem(it[3].title, it[3].tooltip)
	systray.AddSeparator()
	quit := systray.AddMenuItem(it[4].title, it[4].tooltip)
	go func() {
		for {
			select {
			case <-open.ClickedCh:
				openBrowser(url)
			case <-check.ClickedCh:
				go trayCheckUpdate() // 联网检查不堵事件循环
			case <-upNow.ClickedCh:
				go trayUpgradeNow()
			case <-quit.ClickedCh:
				systray.Quit()
				return
			}
		}
	}()
}

// ---- 托盘升级动作（票07，D5/D6：检查只读、升级纯手动） ----

// updateCheckFlow 「检查更新」动作流：进程内调 update.Check——纯只读（D6：
// 升级纯手动，不下载不换文件），结论以 Check 的人话串经 push 推出；查询失败
// 也推（用户点了要有回音）。eps 与 push 均注入：单测用 httptest 假端点 +
// 通知桩断言「Check 被调且通知发出、release 查询之外零触碰」。
func updateCheckFlow(eps update.Endpoints, current string, push func(title, msg string)) {
	out, err := update.Check(eps, current, "", false)
	if err != nil {
		push("Ferryman 检查更新", "检查更新失败："+err.Error())
		return
	}
	push("Ferryman 检查更新", out.String())
}

// trayCheckUpdate 托盘「检查更新」点击入口：真实端点只读检查，结论经
// NotifyAlert 推送（票05 已接线的双通道）；config 载不动就不推——旁路尽力
// 而为，与 runUpdateExecute 同纪律。
func trayCheckUpdate() {
	var cfg *config.Config
	if c, err := config.Load("", false); err == nil {
		cfg = c
	}
	updateCheckFlow(update.Endpoints{}, version, func(title, msg string) {
		if cfg != nil {
			notify.NotifyAlert(title, msg, cfg)
		}
	})
}

// upgradeLaunchFlow 「立即升级」动作流：detached 隐藏拉起 `ferryman update
// --supervise`（票05 内部旗标 = 监督者执行，规格 §C 统一监督者）。start 注入
// ——单测桩断言参数形态，真实 spawn 不在单测里跑。守护自己不退出：监督者会
// 经 /shutdown 叫停本守护、换装、拉起新守护，生命周期归它，菜单不用管。
func upgradeLaunchFlow(exe string, start func(exe string, args ...string) error) error {
	return start(exe, "update", "--supervise")
}

// trayUpgradeNow 托盘「立即升级」点击入口：detached 隐藏拉起本 exe 的
// update --supervise。失败只记日志（托盘侧无对话框；notify 是升级结果通道，
// 拉不起监督者时无结果可报）。
func trayUpgradeNow() {
	exe, err := os.Executable()
	if err != nil {
		log.Printf("[tray] 立即升级：拿不到 exe 路径，放弃: %v", err)
		return
	}
	if err := upgradeLaunchFlow(exe, spawnDetachedHidden); err != nil {
		log.Printf("[tray] 立即升级：拉起监督者失败: %v", err)
	}
}

// spawnDetachedHidden detached 隐藏拉起（票07）：与 internal/update 的
// launchCmdImpl 同款 SysProcAttr 形态（该助手非导出不能跨包复用，就地镜像）
// ——HideWindow 不闪窗，DETACHED_PROCESS|CREATE_NEW_PROCESS_GROUP 脱离父
// 控制台（监督者长命于托盘进程）。Start 不 Wait，拉起即走。Windows 形态：
// 本程序只发 Windows exe（release.yml 单 windows-latest 流水线，规格 §B）。
func spawnDetachedHidden(exe string, args ...string) error {
	c := exec.Command(exe, args...)
	c.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: windows.DETACHED_PROCESS | windows.CREATE_NEW_PROCESS_GROUP,
	}
	return c.Start()
}

// ---- 子命令 ----

// parseEvents --events 逗号列表 → 子集（空白裁剪、空段丢弃）；空/缺省 = nil =
// installer.normalizeEvents 的全集语义。
func parseEvents(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	var out []string
	for _, p := range strings.Split(s, ",") {
		if v := strings.TrimSpace(p); v != "" {
			out = append(out, v)
		}
	}
	return out
}

func cmdInstallCC(args []string) int {
	fs := flag.NewFlagSet("install-cc", flag.ExitOnError)
	events := fs.String("events", "", "逗号分隔事件子集（缺省全集；切换日三类："+
		"SessionStart,SubagentStart,SubagentStop）")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	return installer.InstallCC("", "", "", "", parseEvents(*events))
}

func cmdInstallCCSwitch() int {
	n := installer.InjectCCSwitch("", "", nil)
	if n >= 0 {
		return 0
	}
	return 1
}

func cmdInstallCodex(args []string) int {
	fs := flag.NewFlagSet("install-codex", flag.ExitOnError)
	events := fs.String("events", "", "逗号分隔事件子集（缺省全集；同 install-cc 三类子集）")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if installer.InstallCodex("", "", "", parseEvents(*events)) < 0 {
		return 1
	}
	return 0
}

// cmdInstallMCP `ferryman install-mcp`：把 agent 面 MCP server 注册进 CC
// 用户级 MCP 配置（claude mcp add --scope user 等效；幂等——自有旧条目直接
// 刷新，外部/不兼容同名条目默认拒绝并说明原因，仅 --force 覆盖）。
func cmdInstallMCP(args []string) int {
	fs := flag.NewFlagSet("install-mcp", flag.ExitOnError)
	force := fs.Bool("force", false, "覆盖 mcpServers.ferryman 下的外部/不兼容条目（默认拒绝并说明原因）")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	return installer.InstallMCP("", "", *force)
}

// ---- 常驻保障（票02）：自启 Run 键 + 看门 ----

// cmdAutostart Run 键自启三操作（缺省 = status——只读最安全）。
func cmdAutostart(args []string) int {
	if len(args) == 0 {
		return installer.AutostartStatus()
	}
	switch args[0] {
	case "install":
		return installer.AutostartInstall()
	case "uninstall":
		return installer.AutostartUninstall()
	case "status":
		return installer.AutostartStatus()
	}
	fmt.Fprintf(os.Stderr, "未知 autostart 子命令: %q（install|uninstall|status）\n", args[0])
	return 2
}

// cmdWatchdog 看门：无参 = 单次探活（schtasks 注册的就是这个形态——
// 无监听才拉起/占用只告警不双拉）；install/uninstall/status = 计划任务三操作。
func cmdWatchdog(args []string) int {
	if len(args) == 0 {
		return installer.RunWatchdogCLI()
	}
	switch args[0] {
	case "install":
		return installer.WatchdogTaskInstall()
	case "uninstall":
		return installer.WatchdogTaskUninstall()
	case "status":
		return installer.WatchdogTaskStatus()
	}
	fmt.Fprintf(os.Stderr, "未知 watchdog 子命令: %q（install|uninstall|status；无参 = 单次探活）\n", args[0])
	return 2
}

func cmdAccount(args []string) int {
	if len(args) == 0 || args[0] != "report" {
		fmt.Fprintln(os.Stderr, "用法: ferryman account report [--since 日] [--until 日]"+
			" [--project 名] [--session id] [--kind 类型] [--provider 键] [--json]")
		return 2
	}
	fs := flag.NewFlagSet("account report", flag.ExitOnError)
	since := fs.String("since", "", "起始日 YYYY-MM-DD（本地时区）")
	until := fs.String("until", "", "截止日 YYYY-MM-DD（本地时区）")
	project := fs.String("project", "", "按项目过滤")
	session := fs.String("session", "", "按会话 id 过滤")
	kind := fs.String("kind", "", "handoff|beat|block|inject|bypass|window")
	provider := fs.String("provider", "", "block 侧经济价格表键（缺省=ferry provider）")
	asJSON := fs.Bool("json", false, "输出 JSON")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	return report.Run(report.Args{
		Since: *since, Until: *until, Project: *project, Session: *session,
		Kind: *kind, Provider: *provider, JSON: *asJSON,
	})
}

// ---- version（发布链：可注入版本号） ----

// cmdVersion `ferryman version`：打印版本号。dev = 非 release 构建（本地
// go build 未注入）；release 值由 build.ps1 -Release / CI 以
// -ldflags "-X main.version=<git describe --tags --always>" 编译期注入。
func cmdVersion(args []string, w io.Writer) int {
	fs := flag.NewFlagSet("version", flag.ExitOnError)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if version == "dev" {
		fmt.Fprintf(w, "%s（非 release 构建；release 版由 build.ps1 -Release / CI 注入）\n", version)
		return 0
	}
	fmt.Fprintln(w, version)
	return 0
}

// ---- update（发布链票03 --check 只读；票05 监督者执行） ----

// cmdUpdate `ferryman update`：--check 解析目标版本并与当前版本比较（已是最新/
// 发现新版/显式降级注明；dev 等非 semver 如实提示无从比较），只报告不动手
// （D6：升级纯手动；D8：网络走环境代理；D11：固定产物名）。无 --check =
// 监督者执行（票05：锁/journal/停旧/原子换装/校验/回滚/崩溃恢复）；--supervise
// 为托盘派生用的内部旗标（detached 隐藏 spawn），行为与无参一致（规格 §C
// 统一监督者）——两入口收敛同一 runUpdateExecute。
func cmdUpdate(args []string, w io.Writer) int {
	fs := flag.NewFlagSet("update", flag.ExitOnError)
	check := fs.Bool("check", false, "只检查并报告，不下载不换文件")
	pre := fs.Bool("prerelease", false, "检查纳入预发布版（rc/beta；缺省只看稳定版）")
	_ = fs.Bool("supervise", false, "内部旗标：托盘隐藏派生用，行为与无参一致")
	selfRelay := fs.Bool("self-relay", false, "内部旗标：本进程是自中继副本，不再自中继")
	if err := fs.Parse(orderFlagsFirst(args)); err != nil {
		return 2
	}
	if fs.NArg() > 1 {
		fmt.Fprintln(os.Stderr, "用法: ferryman update [--check] [vX.Y.Z] [--prerelease]")
		return 2
	}
	spec := ""
	if fs.NArg() == 1 {
		spec = fs.Arg(0)
	}
	if !*check {
		return runUpdateExecute(spec, *pre, *selfRelay, w)
	}
	out, err := update.Check(update.Endpoints{}, version, spec, *pre)
	if err != nil {
		fmt.Fprintf(os.Stderr, "检查更新失败: %v\n", err)
		return 1
	}
	fmt.Fprintln(w, out)
	return 0
}

// orderFlagsFirst 旗标前置规整：Go flag 在首个位置参数后停止解析，
// `update v0.2.0 --prerelease` 原样 Parse 会把 --prerelease 当多余位置参数——
// 重排后任意顺序可用。'-‘ 前缀视作旗标（负版本号不存在，无误伤面）。
func orderFlagsFirst(args []string) []string {
	var flags, pos []string
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			flags = append(flags, a)
		} else {
			pos = append(pos, a)
		}
	}
	return append(flags, pos...)
}

// runUpdateExecute 监督者执行路径（var 形 = main_test 注入缝，不真升级）。
// 装配：config 可载则用其 DataDir/守护口，载不动回落缺省（~/ferryman、7311）
// ——升级不应因配置坏而不可用。结果 stdout 报告 + notify 通道推送（seam F，
// 规格 §C 第10条：NotifyAlert 已是通用双通道函数，notify 包零改动）。
var runUpdateExecute = func(spec string, pre, selfRelay bool, w io.Writer) int {
	dataDir, port := "", update.DefaultDaemonPort
	var cfg *config.Config
	if c, err := config.Load("", false); err == nil {
		cfg = c
		dataDir = c.DataDir()
		if c.Server.Port != 0 {
			port = c.Server.Port
		}
	}
	res := update.NewSupervisor(update.Config{
		DataDir:    dataDir,
		Port:       port,
		Current:    version,
		Spec:       spec,
		Prerelease: pre,
		SelfRelay:  selfRelay,
	}).Run()
	if res.Relayed {
		// 自中继交棒：副本进程已接手（detached 无控制台），结果走 notify；
		// journal/doctor 可查。本进程使命结束，退出码 0。
		fmt.Fprintln(w, "已转交升级代理进程接手（自身即换装目标，副本接力）；结果将经通知推送")
		return 0
	}
	var summary string
	switch {
	case res.Success:
		summary = fmt.Sprintf("升级完成：%s → %s", res.From, res.To)
	case res.RolledBack:
		summary = fmt.Sprintf("升级失败（%v），已回滚并恢复 %s 服务", res.Err, res.From)
	case res.RollbackErr != "":
		summary = fmt.Sprintf("升级失败且回滚未成：%v；回滚问题：%s——请人工检查", res.Err, res.RollbackErr)
	default:
		summary = fmt.Sprintf("升级失败：%v（现场未动/已恢复）", res.Err)
	}
	fmt.Fprintln(w, summary)
	if cfg != nil {
		notify.NotifyAlert("Ferryman 升级", summary, cfg) // 旁路尽力而为
	}
	if res.Success {
		return 0
	}
	return 1
}

// ---- mcp（票04：agent 面 stdio MCP server） ----

// cmdMCP `ferryman mcp`：stdio JSON-RPC 2.0 的 MCP server（六件只读工具中的
// 五件转发 daemon 查询端点；doctor 在票05）。stdout 专留给 JSON-RPC，任何
// 诊断只走 stderr；--config 缺省时走 FERRYMAN_CONFIG 或默认路径。
func cmdMCP(args []string) int {
	fs := flag.NewFlagSet("mcp", flag.ExitOnError)
	cfgPath := fs.String("config", "", "配置文件路径（缺省 = FERRYMAN_CONFIG 环境变量或 ~/ferryman/config.toml）")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	return mcp.Run(*cfgPath, version) // 版本经装配参数进（票02，规格 §A）
}

// ---- cutover 族（票23：切换工具，不执行生产切换） ----

// cmdCutover cutover 子命令分发：backup / rollback-write / rollback-drill /
// smoke（各面语义见 usage 与 internal/cutover）。
func cmdCutover(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "用法: ferryman cutover <backup|rollback-write|rollback-drill|smoke> [flags]")
		return 2
	}
	switch args[0] {
	case "backup":
		return cmdCutoverBackup(args[1:])
	case "rollback-write":
		return cmdCutoverRollbackWrite(args[1:])
	case "rollback-drill":
		return cmdCutoverRollbackDrill(args[1:])
	case "smoke":
		return cmdCutoverSmoke(args[1:])
	}
	fmt.Fprintf(os.Stderr, "未知 cutover 子命令: %q\n", args[0])
	return 2
}

// defaultDataDir ~/ferryman（cutover 族共用的数据目录缺省）。
func defaultDataDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "ferryman"
	}
	return filepath.Join(home, "ferryman")
}

// cmdCutoverBackup 数据全量备份（只读复制；缺省 data=~/ferryman、dest=data 同卷上级）。
func cmdCutoverBackup(args []string) int {
	fs := flag.NewFlagSet("cutover backup", flag.ExitOnError)
	data := fs.String("data", defaultDataDir(), "数据目录（守护数据根）")
	dest := fs.String("dest", "", "备份父目录（缺省 = 数据目录同卷上级）")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	d, err := cutover.BackupData(*data, *dest)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Println(d)
	return 0
}

// cmdCutoverRollbackWrite 生成回退工件（repo 缺省 = exe 所在目录——build.ps1
// 把 exe 出到仓库根；写盘目标 = data 目录，缺省 ~/ferryman）。
func cmdCutoverRollbackWrite(args []string) int {
	fs := flag.NewFlagSet("cutover rollback-write", flag.ExitOnError)
	repo := fs.String("repo", "", "仓库根（缺省 = exe 所在目录）")
	data := fs.String("data", defaultDataDir(), "数据目录（工件写到此目录）")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	r := *repo
	if r == "" {
		r = filepath.Dir(exePathOrFallback())
	}
	if _, err := cutover.WriteRollbackScript(r, *data, exePathOrFallback()); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

// cmdCutoverRollbackDrill 隔离演练回退机制（临时 worktree+临时启动器副本；
// dir 缺省 = 系统临时目录一次性子目录；tag 未打自动降级路径探测，不失败）。
func cmdCutoverRollbackDrill(args []string) int {
	fs := flag.NewFlagSet("cutover rollback-drill", flag.ExitOnError)
	repo := fs.String("repo", "", "仓库根（缺省 = exe 所在目录）")
	dir := fs.String("dir", "", "演练临时目录（缺省 = 系统临时区新建）")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	r := *repo
	if r == "" {
		r = filepath.Dir(exePathOrFallback())
	}
	if err := cutover.DrillRollback(r, *dir); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

// cmdCutoverSmoke 沙箱冒烟四链路（独立端口+独立数据目录+假 provider，
// 直打 API 断言；--config 可选阈值基座，端口/数据目录/通知强制沙箱值）。
func cmdCutoverSmoke(args []string) int {
	fs := flag.NewFlagSet("cutover smoke", flag.ExitOnError)
	cfgPath := fs.String("config", "", "沙箱配置路径（可选；仅阈值生效，"+
		"端口/数据目录/通知/闸门模式强制沙箱值）")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if err := cutover.SmokeAll(*cfgPath); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

// exePathOrFallback 当前 exe 路径（installer.exePath 的本地镜像——避免为取路径
// 引入 installer 包依赖；拿不到退 "ferryman.exe" 仅提示性）。
func exePathOrFallback() string {
	exe, err := os.Executable()
	if err != nil {
		return "ferryman.exe"
	}
	return exe
}

// ---- 面板族（原 cmd/viewer 形态原样） ----

// runPanel 只起面板（不带子命令、带面板族 flags 的入口；viewer 原样）：
// --demo 合成账本 / --port 固定口 / --no-tray / --no-browser / --install-shortcuts。
func runPanel(args []string) int {
	fs := flag.NewFlagSet("panel", flag.ExitOnError)
	data := fs.String("data", "", "账本目录或数据根（默认 $FERRYMAN_DATA 或 ~/ferryman；根下无 *.jsonl 而有 accounts/ 时自动下钻）")
	port := fs.Int("port", 0, "监听端口（0=随机）")
	noBrowser := fs.Bool("no-browser", false, "启动后不自动打开浏览器")
	isDemo := fs.Bool("demo", false, "演示模式：加载确定性合成账本（非真实数据），忽略 --data")
	noTray := fs.Bool("no-tray", false, "不建系统托盘图标（无界面环境/服务化运行）")
	install := fs.Bool("install-shortcuts", false, "创建桌面+开始菜单快捷方式后退出（Windows；快捷方式钉 15900 口）")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "未知命令或参数: %q\n\n", fs.Arg(0))
		fmt.Fprint(os.Stderr, usage)
		return 2
	}

	if *install {
		pin := *port
		if pin == 0 {
			pin = defaultPanelPort
		}
		exe, err := os.Executable()
		if err != nil {
			log.Fatal(err)
		}
		if err := installShortcuts(exe, pin); err != nil {
			log.Fatal(err)
		}
		return 0
	}

	// --demo 独占数据源：忽略 --data/$FERRYMAN_DATA，合成账本写进一次性临时目录，
	// 绝不碰真实账本；目录留给系统临时区清理（查看器只读，进程期间无人回收它）。
	var dir string
	if *isDemo {
		d, err := os.MkdirTemp("", "ferryman-demo-")
		if err != nil {
			log.Fatal(err)
		}
		if _, err := demo.Write(d, demoBase(time.Now())); err != nil {
			log.Fatal(err)
		}
		dir = d
	} else {
		dir = resolveDataDir(*data)
	}

	// --demo 注记贴在面板 API（viewer 原文；serve 装配传空串不受影响）
	note := ""
	if *isDemo {
		note = "演示模式：当前数据为合成账本（含未来心跳事件的预演），非真实流水"
	}
	mux := panelMux(dir, note)
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", *port))
	if err != nil {
		// 固定口被占：探得到 /api/sessions = 面板已在跑 → 开浏览器完事（快捷方式语义）
		url := fmt.Sprintf("http://127.0.0.1:%d", *port)
		if *port != 0 && probeViewer(url) {
			fmt.Println("面板已在运行，直接打开：", url)
			openBrowser(url)
			return 0
		}
		log.Fatal(err)
	}
	url := fmt.Sprintf("http://%s", ln.Addr())
	if *isDemo {
		fmt.Printf("时间线查看器(演示模式): %s （合成数据目录 %s）\n", url, dir)
	} else {
		fmt.Printf("时间线查看器: %s （数据目录 %s）\n", url, dir)
	}
	if *noTray {
		fmt.Println("（--no-tray：无托盘，Ctrl+C 退出）")
	} else {
		fmt.Println("（托盘图标常驻：菜单可打开面板/退出）")
	}
	if !*noBrowser {
		openBrowser(url)
	}
	httpsrv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	if *noTray {
		log.Fatal(httpsrv.Serve(ln)) // 前台阻塞，Ctrl+C 即退
	}
	go func() { log.Fatal(httpsrv.Serve(ln)) }()
	systray.Run(func() { trayReady(url) }, func() {})
	return 0
}

// probeViewer 探测 url 上是否活着一个本查看器（800ms 超时）。判定 /api/sessions
// 回 200 即认——够区分"自己在跑"与"端口被无关程序占用"。
func probeViewer(url string) bool {
	c := &http.Client{Timeout: 800 * time.Millisecond}
	resp, err := c.Get(url + "/api/sessions")
	if err != nil {
		return false
	}
	_ = resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// openBrowser 按 GOOS 起系统默认浏览器；起不起来都不影响服务，错误忽略。
// Windows 用 rundll32 而非 `cmd /c start`：cmd 会闪黑窗，rundll32 不会。
func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Start()
}

// installShortcuts 用 PowerShell 的 WScript.Shell COM 建 .lnk：桌面 + 开始菜单，
// 名「Ferryman 面板」，目标 exe + `serve --port <pin>`（合并 exe：点击 = 起
// 守护+面板+托盘；已跑则守护唯一化跳过、面板口探测转开浏览器），WindowStyle
// minimized（票22：双击不闪黑窗、托盘常驻），图标用 exe 同目录 icon.ico
// （缺则 exe 默认图标）。
func installShortcuts(exe string, pin int) error {
	if runtime.GOOS != "windows" {
		return fmt.Errorf("--install-shortcuts 仅支持 Windows")
	}
	exeDir := filepath.Dir(exe)
	icon := filepath.Join(exeDir, "icon.ico")
	if _, err := os.Stat(icon); err != nil {
		icon = exe // 没带 icon.ico 就退 exe 默认图标（难看但能跑）
	}
	args := fmt.Sprintf("serve --port %d", pin)
	ps := fmt.Sprintf(`$ws = New-Object -ComObject WScript.Shell
$name = 'Ferryman 面板.lnk'
foreach ($dir in @([Environment]::GetFolderPath('Desktop'), [Environment]::GetFolderPath('Programs'))) {
  $lnk = $ws.CreateShortcut((Join-Path $dir $name))
  $lnk.TargetPath = '%s'
  $lnk.Arguments = '%s'
  $lnk.WorkingDirectory = '%s'
  $lnk.IconLocation = '%s'
  $lnk.Description = 'Ferryman 守护+面板：点击启动（已运行则打开面板）'
  $lnk.WindowStyle = 7
  $lnk.Save()
  Write-Output (Join-Path $dir $name)
}`, filepath.ToSlash(exe), args, filepath.ToSlash(exeDir), filepath.ToSlash(icon))
	out, err := exec.Command("powershell", "-NoProfile", "-Command", ps).CombinedOutput()
	if err != nil {
		return fmt.Errorf("创建快捷方式失败: %v\n%s", err, strings.TrimSpace(string(out)))
	}
	fmt.Println("已创建快捷方式：")
	fmt.Print(string(out))
	fmt.Printf("（目标 %s %s；WindowStyle minimized；可右键 .lnk 选「固定到任务栏」）\n", exe, args)
	return nil
}

// demoBase 取演示时间锚：今天本地 09:00，未到 09:00 则取昨日——剧本最晚事件在
// base+101min，锚定 09:00 既让整条时间线落在白天，也保证所有时间戳都已过去
// （演示里冒出"未来"的时间戳会露馅）。
func demoBase(now time.Time) time.Time {
	b := time.Date(now.Year(), now.Month(), now.Day(), 9, 0, 0, 0, now.Location())
	if b.After(now) {
		b = b.AddDate(0, 0, -1)
	}
	return b
}

// resolveDataDir 解析账本目录。三种来源（--data / $FERRYMAN_DATA / ~/ferryman）都按
// "数据根"语义对待：目录本身没有 *.jsonl 而其下有 accounts/ 子目录时自动下钻一层；
// 直接传 accounts 目录（或任何已含 *.jsonl 的目录）原样使用。其余情况原样返回，
// 交由服务端按「数据目录不存在」提示。
func resolveDataDir(flagVal string) string {
	dir := flagVal
	if dir == "" {
		dir = os.Getenv("FERRYMAN_DATA")
	}
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "ferryman" // 拿不到家目录时退相对路径，仅提示性
		}
		dir = home + "/ferryman"
	}
	if hasJSONL(dir) {
		return dir
	}
	if st, err := os.Stat(filepath.Join(dir, "accounts")); err == nil && st.IsDir() {
		return filepath.Join(dir, "accounts")
	}
	return dir
}

// hasJSONL 目录下（不含子目录）是否存在至少一个 *.jsonl。
func hasJSONL(dir string) bool {
	des, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, de := range des {
		if !de.IsDir() && strings.HasSuffix(de.Name(), ".jsonl") {
			return true
		}
	}
	return false
}
