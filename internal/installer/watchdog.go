// watchdog.go — 票02：看门单次探活（规格 F5：HTTP 健康端点探活非探进程）＋
// 看门计划任务 schtasks 注册（每 5 分钟调 `ferryman watchdog`）。
//
// 判定三分支（钉死语义，评审 F5：HTTP 探活/无监听才拉起/占用不双拉）：
//   ① 有 HTTP 响应（任何状态码，含 401/5xx）＝ daemon 在 → 正常退出；
//   ② connection refused ＝ 无监听 → 拉起 daemon（与 Run 键同一命令构造
//      daemonStartScript）后本次结束；
//   ③ 端口被占但非 daemon（超时/断连等一切非拒绝错误）→ 只记日志退出，
//      绝不双拉（单实例约束；不杀进程——占口者身份不可知，双拉只会雪上加霜）。
//
// 探活目标：daemon 无 /health 路由（httpapi.go 五端点），按票面指示探 GET
// /stats——GET 先 auth 后判路径，无 token 也回 401，语义同为「HTTP 有响应」。
// 端口 FERRYMAN_PORT 环境变量（缺省 7311，钩子自举 ferryman-ensure.ps1 同源）。
//
// 真实冒烟清单（runbook 票引用；本票单测走 httptest/注入 fake/构造层，
// 绝不真建/删计划任务、不真写注册表）：
//   1. ferryman watchdog                  → daemon 活：打印有响应、退出 0
//   2. 停 daemon 再跑                     → 打印无监听→拉起；几秒后 7311 在听
//   3. 用不发 HTTP 的进程占 7311 再跑      → 打印占用告警、不拉起、退出 0
//   4. ferryman watchdog install          → schtasks /Query /TN FerrymanWatchdog 在位
//   5. schtasks /Run /TN FerrymanWatchdog → 立即触发一次（无窗口闪现）
//   6. 重启机器登录后等 5 分钟            → 看门自动补位（钩子自举够不着的兜底）
//   7. ferryman watchdog uninstall        → schtasks /Query 报任务不存在
//
// Windows 专属（syscall.Errno/WSAECONNREFUSED/SysProcAttr.HideWindow）。
package installer

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// 常量：口/环境变量/任务名/超时（票面逐字：2s 短超时、每 5 分钟、缺省 7311）。
const (
	DefaultDaemonPort = 7311
	DaemonPortEnv     = "FERRYMAN_PORT"
	WatchdogTaskName  = "FerrymanWatchdog"
	watchdogTimeout   = 2 * time.Second
	daemonProbePath   = "/stats"
	watchdogLogName   = "watchdog.log"
)

// DaemonPort 探活口：FERRYMAN_PORT 数值优先，缺省/坏值回落 7311。
func DaemonPort() int {
	if v := strings.TrimSpace(os.Getenv(DaemonPortEnv)); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return DefaultDaemonPort
}

// DaemonProbeURL 探活目标（/stats：见文件头「探活目标」注）。
func DaemonProbeURL(port int) string {
	return fmt.Sprintf("http://127.0.0.1:%d%s", port, daemonProbePath)
}

// probeHTTP 真探针：nil = 拿到 HTTP 响应（任何状态码——语义是「有应答」非
// 「健康报表」，5xx/401 都算 daemon 在）。
func probeHTTP(url string, timeout time.Duration) error {
	client := &http.Client{Timeout: timeout}
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20)) // 排水礼节（连接可复用）
	return nil
}

// wsaEConnRefused Windows 连接拒绝码（10061）；stdlib syscall 不导出 WSA 常量
// （syscall.ECONNREFUSED 在 Windows 上是发明的假值，真实拒绝是 10061）。
const wsaEConnRefused syscall.Errno = 10061

// isConnRefused 判「无监听」：拒绝在 Windows 是 WSAECONNREFUSED(10061)，POSIX
// 是 ECONNREFUSED。错误链是 url.Error→net.OpError→os.SyscallError→Errno，
// 用 errors.As 钻到 Errno 再比对（OpError.Err 直接断言会打空——中间还包了
// 一层 SyscallError；errors.Is 的 ECONNREFUSED 哨兵在 Windows 上也对不上，
// syscall.ECONNREFUSED 在 Windows 是发明的假值）。
func isConnRefused(err error) bool {
	var errno syscall.Errno
	if !errors.As(err, &errno) {
		return false
	}
	return errno == syscall.ECONNREFUSED || errno == wsaEConnRefused
}

// WatchdogDeps 看门可注入面（测试注 fake 探针/拉起；真装配 realWatchdogDeps）。
type WatchdogDeps struct {
	Port    int // 0 = FERRYMAN_PORT 或 7311
	Timeout time.Duration
	Probe   func(url string, timeout time.Duration) error
	Launch  func() error
	Logf    func(format string, args ...any)
}

// runWatchdog 单次判定（返回进程退出码：0 = 正常/占用告警；1 = 拉起失败/
// 装配空洞——绝不假装看门成功）。
func runWatchdog(d WatchdogDeps) int {
	port := d.Port
	if port == 0 {
		port = DaemonPort()
	}
	timeout := d.Timeout
	if timeout == 0 {
		timeout = watchdogTimeout
	}
	logf := d.Logf
	if logf == nil {
		logf = realWatchdogLogf
	}
	url := DaemonProbeURL(port)
	err := d.Probe(url, timeout)
	if err == nil {
		// 分支①：有响应（任何状态码）
		logf("[watchdog] daemon 有响应（%s）——正常退出", url)
		return 0
	}
	if isConnRefused(err) {
		// 分支②：无监听才拉起（单实例约束的正面）
		logf("[watchdog] %s 无监听（connection refused）——拉起 daemon", url)
		if d.Launch == nil {
			logf("[watchdog] 未装配拉起动作（装配错误）")
			return 1
		}
		if lerr := d.Launch(); lerr != nil {
			logf("[watchdog] daemon 拉起失败: %v", lerr)
			return 1
		}
		return 0
	}
	// 分支③：占用但非 daemon——只告警，绝不双拉（票面铁律）
	logf("[watchdog] %s 端口被占但无 daemon 响应（%v）——只告警不双拉（单实例）", url, err)
	return 0
}

// ---- daemon 拉起（与 Run 键同一命令构造，杜绝两处漂移） ----

// daemonStartScript 无窗口拉起点火脚本的 -Command 脚本文本（ferryman-ensure.ps1
// 的 Start-Process -WindowStyle Hidden 形态 1:1，生产已验证；\" 转义见
// autostart.go 值形注）。
func daemonStartScript(launcher string) string {
	return fmt.Sprintf(`Start-Process -FilePath $env:ComSpec -ArgumentList '/c','\"%s\"' -WindowStyle Hidden`, launcher)
}

// daemonStartPSArgs exec argv 形（LaunchDaemon 用；Run 键值走 AutostartCommand
// 的可视单串，两者同源 daemonStartScript）。
func daemonStartPSArgs(launcher string) []string {
	return []string{"-NoProfile", "-WindowStyle", "Hidden", "-Command", daemonStartScript(launcher)}
}

// LaunchDaemon 无窗口拉起 daemon（分支②专用）：Start 不 Wait——拉起即走，
// 本次看门结束（等就绪是钩子自举的职责，不归看门）。
func LaunchDaemon(launcher string) error {
	cmd := exec.Command("powershell", daemonStartPSArgs(launcher)...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true} // 双保险：PS 进程本体也不闪窗
	return cmd.Start()
}

// ---- 看门计划任务（schtasks；构造与执行分层，单测只打构造层） ----

// WatchdogTR 计划任务 TR：无窗口拉起本 exe 的 watchdog 子命令（Start-Process
// 包装与 Run 键同形态——schtasks 每 5 分钟在交互会话拉进程，不包一层会闪黑窗）。
func WatchdogTR(exe string) string {
	return fmt.Sprintf(`powershell -NoProfile -WindowStyle Hidden -Command "Start-Process -FilePath '%s' -ArgumentList 'watchdog' -WindowStyle Hidden"`, exe)
}

// schtasks 参数构造（纯函数；单测逐字断言，真实执行全走 runner 注入）。
func schtasksCreateArgs(task, tr string) []string {
	// /F：已存在即覆盖——install 幂等（重跑不报错），exe 挪窝后 TR 漂移同修。
	return []string{"/Create", "/F", "/TN", task, "/SC", "MINUTE", "/MO", "5", "/TR", tr}
}

func schtasksDeleteArgs(task string) []string {
	// /F：免交互确认（schtasks /Delete 默认问 y/n，无窗口任务里会挂死）。
	return []string{"/Delete", "/TN", task, "/F"}
}

func schtasksQueryArgs(task string) []string {
	return []string{"/Query", "/TN", task, "/FO", "LIST"}
}

// TaskRunner 执行面（exec.CombinedOutput 同签名；测试注 fake）。
type TaskRunner interface {
	CombinedOutput(name string, args ...string) ([]byte, error)
}

// execRunner 真执行。
type execRunner struct{}

func (execRunner) CombinedOutput(name string, args ...string) ([]byte, error) {
	return exec.Command(name, args...).CombinedOutput()
}

// taskDeps 计划任务可注入面。
type taskDeps struct {
	tr     string
	runner TaskRunner
}

// realTaskDeps 真装配：TR 指向当前 exe 的 watchdog 子命令。
func realTaskDeps() taskDeps {
	return taskDeps{tr: WatchdogTR(exePath()), runner: execRunner{}}
}

// installWatchdogTask 建任务（/F 覆盖＝幂等，重跑修漂移）。
func installWatchdogTask(d taskDeps) error {
	_, err := d.runner.CombinedOutput("schtasks", schtasksCreateArgs(WatchdogTaskName, d.tr)...)
	return err
}

// uninstallWatchdogTask 幂等卸载：任务不在（查询非零退出）即已达成，不再删。
func uninstallWatchdogTask(d taskDeps) error {
	st, err := queryTask(d)
	if err != nil || !st.Exists {
		return err
	}
	_, err = d.runner.CombinedOutput("schtasks", schtasksDeleteArgs(WatchdogTaskName)...)
	return err
}

// TaskStatus 任务两态 + 尽力下次运行时间。
type TaskStatus struct {
	Exists  bool
	NextRun string // 解析不出（本地化/GBK 代码页）留空——存在性才是承重信息
}

// nextRunRe 下次运行时间行（EN/zh-CN UTF-8 面最佳努力）：zh-CN 控制台的
// schtasks 输出走 OEM 代码页（GBK），UTF-8 字面匹配不上就留空；存在性
// （退出码）与代码页无关不受影响。不引新依赖解 GBK（票面禁新增依赖）。
var nextRunRe = regexp.MustCompile(`(?m)^(?:Next Run Time|下次运行时间)\s*:\s*(\S[^\r\n]*)`)

// queryTask 查任务：ExitError（schtasks 对不存在任务非零退出）= 缺失；
// 其余错误（schtasks 不在 PATH 等）上抛，doctor 呈「查询失败」。
func queryTask(d taskDeps) (TaskStatus, error) {
	out, err := d.runner.CombinedOutput("schtasks", schtasksQueryArgs(WatchdogTaskName)...)
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return TaskStatus{Exists: false}, nil
		}
		return TaskStatus{}, err
	}
	st := TaskStatus{Exists: true}
	if m := nextRunRe.FindStringSubmatch(string(out)); m != nil {
		st.NextRun = strings.TrimSpace(m[1])
	}
	return st, nil
}

// ---- CLI 真实入口（cmd/ferryman 薄分发） ----

// RunWatchdogCLI `ferryman watchdog` 真实入口（单次探活；schtasks 注册的就是
// 这个形态）。
func RunWatchdogCLI() int {
	return runWatchdog(realWatchdogDeps())
}

// realWatchdogDeps 真装配：真探针 + 真拉起（点火脚本与 Run 键同款路径）+
// 日志双写（见 realWatchdogLogf）。
func realWatchdogDeps() WatchdogDeps {
	return WatchdogDeps{
		Probe:  probeHTTP,
		Launch: func() error { return LaunchDaemon(filepath.Join(homeDir(), "ferryman", LauncherName)) },
		Logf:   realWatchdogLogf,
	}
}

// realWatchdogLogf 日志双写：stdout（手动跑看得见）+ ~/ferryman/watchdog.log
// （计划任务无窗跑的唯一留痕；尽力而为，日志失败不影响判定）。
func realWatchdogLogf(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	fmt.Println(msg)
	f, err := os.OpenFile(filepath.Join(homeDir(), "ferryman", watchdogLogName),
		os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s %s\n", time.Now().Format("2006-01-02 15:04:05"), msg)
}

// WatchdogTaskInstall 建看门计划任务（ferryman watchdog install）。
func WatchdogTaskInstall() int {
	if err := installWatchdogTask(realTaskDeps()); err != nil {
		fmt.Printf("看门计划任务安装失败: %v\n", err)
		return 1
	}
	fmt.Printf("看门计划任务已安装（%s：每 5 分钟触发一次 ferryman watchdog）\n", WatchdogTaskName)
	return 0
}

// WatchdogTaskUninstall 删看门计划任务（ferryman watchdog uninstall；幂等）。
func WatchdogTaskUninstall() int {
	if err := uninstallWatchdogTask(realTaskDeps()); err != nil {
		fmt.Printf("看门计划任务卸载失败: %v\n", err)
		return 1
	}
	fmt.Printf("看门计划任务已卸载（%s 不在即达成）\n", WatchdogTaskName)
	return 0
}

// WatchdogTaskStatus 报任务两态（ferryman watchdog status）。
func WatchdogTaskStatus() int {
	st, err := queryTask(realTaskDeps())
	if err != nil {
		fmt.Printf("看门计划任务查询失败: %v\n", err)
		return 1
	}
	if !st.Exists {
		fmt.Println("看门计划任务: missing")
		return 0
	}
	if st.NextRun == "" {
		fmt.Println("看门计划任务: installed（下次运行时间解析不出/未排）")
		return 0
	}
	fmt.Printf("看门计划任务: installed（下次运行: %s）\n", st.NextRun)
	return 0
}
