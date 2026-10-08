// stack.go — L2 沙箱起栈/收尾（票面「沙箱起栈」节逐条）。
//
// 起栈序：端口锁（固定锁文件，抢不到换 25904-25909 段并记录）→ 沙箱目录树 →
// dsh home 备料（复制源只读；junction 群按原目标重链不 deref——同 app 解析
// 等价且免大拷贝，插件两落点换装为备料快照的 junction＋node_modules 拷贝，
// 镜像生产安装形态）→ 生成面重写（config/home patch/profile patch/
// package.json）→ 隔离扫描（生产端口零残迹）→ 沙箱 daemon（os.Executable()
// 自身 exe 第二实例，serve --no-tray --no-browser --smoke）→ 沙箱 DSH web
// 宿主（$DSH_CLI web --no-open）。
//
// 收尾序：web 宿主杀树 → daemon 优雅停（POST /shutdown，等端口让位）→ 兜底
// 杀树 → 临时目录清理（KeepRoot/红错现场保留，路径进摘要）→ 锁文件删除。
// 全部子进程经 spawnProc 工厂（零闪窗铁律；工厂记 PID 供树形击杀——Windows
// 不杀子进程链的防孤儿锚点）。
package dshsandbox

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// DefaultDSHCLI DSH CLI 定位函数变量（票面「DSH CLI 定位函数变量」——测试
// 注入假件/真机覆写点）。缺省＝本机安装位（tools/e2e_dsh/lib.sh:27 同锚），
// LOCALAPPDATA 缺席时按 home 相对位推导。
var DefaultDSHCLI = func() string {
	if la := os.Getenv("LOCALAPPDATA"); la != "" {
		return filepath.Join(la, "Programs", "DeepSeek Harness",
			"resources", "runtime", "cli", "bin", "dsh.cmd")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, "AppData", "Local", "Programs", "DeepSeek Harness",
		"resources", "runtime", "cli", "bin", "dsh.cmd")
}

// ErrNoDSH 环境缺 DSH（L2 明确报跳过不算绿的哨兵；run.go 侧 ProbeErr → 黄）。
var ErrNoDSH = errors.New("环境缺 DSH（dsh CLI 不可用——L2 无从起沙箱宿主）")

// portCandidates 端口基址候选（票面：缺省 25900 段；与 e2e 并跑抢不到换
// 25904-25909 段。四口＝基址+0..3，基址+3 须 ≤25909）。
var portCandidates = []int{25900, 25904, 25905, 25906}

// 生产端口残迹（隔离扫描黑名单——e2e isolation_scan 同款五行）。
var prodPortMarks = []string{"15700", "15722", "3080", "3081", "15900"}

// Options 沙箱起栈参数（CLI/测试装配；零值字段走缺省解析链）。
type Options struct {
	// ExePath 沙箱 daemon 二进制（生产 exe 起第二实例）。空＝os.Executable()。
	ExePath string
	// DSHCLI dsh 命令行入口（$DSH_CLI）。空＝DefaultDSHCLI()。
	DSHCLI string
	// DSHHomeSrc 沙箱 dsh home 复制源（只读）。空＝<home>/.dsh。
	DSHHomeSrc string
	// PluginSrc 插件备料快照源（仓库 plugin/ferryman-dsh）。空＝解析链：
	// exe 同仓 plugin/ferryman-dsh（工作树构建形态）→ 生产 web profile
	// junction 目标（安装形态）。
	PluginSrc string
	// DockUpstreams 渡口上游表＋active（「最便宜上游」＝active 条目；nil/空＝
	// 拒绝起栈——沙箱模型路由无从指）。
	DockUpstreams map[string]DockUpstream
	DockActive    string
	// TempRoot 沙箱根基目录（锁文件同落此处）。空＝os.TempDir()。
	TempRoot string
	// KeepRoot 收尾不清理沙箱目录（红/错时排障现场；绿时恒清）。
	KeepRoot bool

	// ---- 等待上限（真机量级；测试可压） ----
	DaemonReadyTimeout time.Duration // daemon /stats 就绪；缺省 60s
	WebReadyTimeout    time.Duration // web 宿主就绪行；缺省 150s
	HTTPTimeout        time.Duration // daemon/web HTTP 单请求超时；缺省 5s
}

// DockUpstream 渡口上游条目的序列化形状（config.DockUpstream 的拷贝形状——
// 由装配层从生产 config 抄入，本包不 import config 保持单向依赖）。
type DockUpstream struct {
	BaseURL    string
	APIKey     string
	ModelMap   map[string]string
	TextOnly   []string
	BalanceURL string
	Dialect    string
	Codex      string
	Pi         string
}

// portSet 四口（全部 25xxx 段；生产口零接触）。
type portSet struct {
	Daemon int // daemon 控制口（[server].port＋FERRYMAN_PORT）
	Panel  int // serve 面板口（--port）
	Web    int // dsh web 宿主口（--port）
	Dock   int // 沙箱渡口监听口（[dock].listen）
}

// portFree 端口空闲探测（var 形＝测试注入）。Listen 即关——与真实绑定间的
// 竞窗由「手动工具低频运行」现实吸收。
var portFree = func(port int) bool {
	l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return false
	}
	_ = l.Close()
	return true
}

// portLock 端口段文件锁（固定锁文件路径；内容＝持有 PID＋时刻，陈锁超龄窃取）。
type portLock struct {
	path string
	base int
}

// lockStaleAfter 陈锁窃取阈（进程崩溃残留的锁文件不永久烧段）。
const lockStaleAfter = 2 * time.Hour

// acquirePortLock 依次试候选基址：四口全空闲 ∧ 锁文件可独占创建 → 得锁。
// 抢不到换下一候选（票面「抢不到换 25904-25909 段并记录」——试过的段进错误面）。
func acquirePortLock(tempRoot string, candidates []int) (*portLock, error) {
	if err := os.MkdirAll(tempRoot, 0o755); err != nil {
		return nil, err
	}
	var tried []string
	for _, base := range candidates {
		ps := portSet{Daemon: base, Panel: base + 1, Web: base + 2, Dock: base + 3}
		free := portFree(ps.Daemon) && portFree(ps.Panel) && portFree(ps.Web) && portFree(ps.Dock)
		tried = append(tried, fmt.Sprintf("%d(free=%v)", base, free))
		if !free {
			continue
		}
		path := filepath.Join(tempRoot, fmt.Sprintf("ferryman-verify-dsh-%d.lock", base))
		if info, err := os.Stat(path); err == nil && time.Since(info.ModTime()) > lockStaleAfter {
			_ = os.Remove(path) // 陈锁窃取（超龄＝崩溃残留）
		}
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err != nil {
			continue // 被并跑实例持有——换段
		}
		fmt.Fprintf(f, "%d\n%d\n", os.Getpid(), time.Now().Unix())
		_ = f.Close()
		return &portLock{path: path, base: base}, nil
	}
	return nil, fmt.Errorf("端口段全被占（试过 %s；与 e2e 并跑？）", strings.Join(tried, ", "))
}

// release 删锁文件（收尾必经；失败静默——下轮陈锁窃取兜底）。
func (l *portLock) release() {
	if l == nil {
		return
	}
	_ = os.Remove(l.path)
}

// Stack 一个起好的沙箱栈（probe.go 消费；Stop 收尾）。
type Stack struct {
	Opt        Options
	Ports      portSet
	Root       string // 沙箱根（<temp>/ferryman-verify-dsh-<stamp>）
	Home       string // 沙箱 dsh home
	Data       string // ferryman 数据目录（token/账本/gate.log）
	Logs       string // 日志目录（daemon.log/web.log/probe.log）
	Workspace  string // 探针会话 cwd（流水行探针标记的 project 面）
	PluginSnap string // 插件备料快照目录
	WebToken   string // web 宿主登录 token（就绪行捕获）

	lock       *portLock
	daemonProc *managedProc
	webProc    *managedProc
	stopped    bool
}

// Start 起栈（备料→daemon→web 宿主）。错误＝基础设施故障（调用方按
// ProbeRunner 契约回 error——未完成验证非断言失败）；返回的 Stack 恒需 Stop。
func Start(o Options) (*Stack, error) {
	tmpRoot := o.TempRoot
	if strings.TrimSpace(tmpRoot) == "" {
		tmpRoot = os.TempDir()
	}
	o.TempRoot = tmpRoot
	// ---- 装配解析 ----
	exe := o.ExePath
	if exe == "" {
		p, err := os.Executable()
		if err != nil {
			return nil, fmt.Errorf("os.Executable 解析失败: %w", err)
		}
		exe = p
	}
	dshcli := o.DSHCLI
	if dshcli == "" {
		dshcli = DefaultDSHCLI()
	}
	if dshcli == "" || !fileExists(dshcli) {
		return nil, ErrNoDSH // 明确报缺、跳过不算绿（票面钉死）
	}
	homeSrc := o.DSHHomeSrc
	if homeSrc == "" {
		h, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("家目录解析失败: %w", err)
		}
		homeSrc = filepath.Join(h, ".dsh")
	}
	if !dirExists(homeSrc) {
		return nil, fmt.Errorf("dsh home 复制源不存在: %s", homeSrc)
	}
	pluginSrc, err := resolvePluginSrc(o.PluginSrc, exe, homeSrc)
	if err != nil {
		return nil, err
	}
	if len(o.DockUpstreams) == 0 || strings.TrimSpace(o.DockActive) == "" {
		return nil, errors.New("渡口上游表未装配（active 缺）——沙箱模型路由无从指")
	}
	if _, ok := o.DockUpstreams[o.DockActive]; !ok {
		return nil, fmt.Errorf("渡口 active=%q 不在上游表", o.DockActive)
	}
	if o.DaemonReadyTimeout <= 0 {
		o.DaemonReadyTimeout = 60 * time.Second
	}
	if o.WebReadyTimeout <= 0 {
		o.WebReadyTimeout = 150 * time.Second
	}
	if o.HTTPTimeout <= 0 {
		o.HTTPTimeout = 5 * time.Second
	}

	lock, err := acquirePortLock(tmpRoot, portCandidates)
	if err != nil {
		return nil, err
	}
	s := &Stack{Opt: o, Ports: portSet{Daemon: lock.base, Panel: lock.base + 1,
		Web: lock.base + 2, Dock: lock.base + 3}, lock: lock}
	fail := func(err error) (*Stack, error) {
		s.Stop()
		return nil, err
	}

	// ---- 沙箱目录树 ----
	s.Root = filepath.Join(tmpRoot, "ferryman-verify-dsh-"+time.Now().Format("20060102-150405.000"))
	s.Home = filepath.Join(s.Root, "dsh-home")
	s.Data = filepath.Join(s.Root, "ferryman-data")
	s.Logs = filepath.Join(s.Root, "logs")
	s.Workspace = filepath.Join(s.Root, "probe-workspace")
	s.PluginSnap = filepath.Join(s.Root, "plugin", "ferryman-dsh")
	for _, d := range []string{
		filepath.Join(s.Root, "watch", "cc-projects"),
		filepath.Join(s.Root, "watch", "codex-sessions"),
		s.Logs, s.Data, s.Workspace,
		filepath.Join(s.Home, "sessions"),
	} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return fail(err)
		}
	}

	// ---- 备料（home 复制＋插件快照＋junction）＋生成面 ----
	if err := stageHome(s, homeSrc, pluginSrc); err != nil {
		return fail(fmt.Errorf("沙箱 home 备料失败: %w", err))
	}
	if err := writeSandboxConfig(s); err != nil {
		return fail(fmt.Errorf("沙箱 config 生成失败: %w", err))
	}
	if err := isolationScan(s); err != nil {
		return fail(err)
	}

	// ---- 沙箱 daemon（生产 exe 第二实例；env/参数照 e2e start.sh:96-105） ----
	daemonSpec := procSpec{
		Path: exe,
		Args: []string{"serve", "--port", fmt.Sprint(s.Ports.Panel),
			"--no-tray", "--no-browser", "--smoke"},
		Env: sandboxEnv(map[string]string{
			"USERPROFILE":     s.Root,
			"FERRYMAN_CONFIG": filepath.Join(s.Root, "config.toml"),
			"FERRYMAN_DATA":   s.Data,
			"FERRYMAN_PORT":   fmt.Sprint(s.Ports.Daemon),
		}, "CODEX_HOME", "DSH_HOME"),
		Dir:        s.Root,
		StdoutFile: filepath.Join(s.Logs, "daemon.log"),
		StderrFile: filepath.Join(s.Logs, "daemon.log"),
	}
	dp, err := spawnProc(daemonSpec)
	if err != nil {
		return fail(fmt.Errorf("沙箱 daemon 拉起失败: %w", err))
	}
	s.daemonProc = dp
	if err := waitDaemonReady(s); err != nil {
		return fail(err)
	}

	// ---- 沙箱 DSH web 宿主（env 照 e2e start.sh:139-143） ----
	webSpec := procSpec{
		Path: dshcli,
		Args: []string{"web", "--no-open", "--port", fmt.Sprint(s.Ports.Web)},
		Env: sandboxEnv(map[string]string{
			"DSH_HOME":            s.Home,
			"FERRYMAN_PORT":       fmt.Sprint(s.Ports.Daemon),
			"FERRYMAN_TOKEN_FILE": filepath.Join(s.Data, "daemon.token"),
		}, "FERRYMAN_DISABLE"),
		Dir:        s.Root,
		StdoutFile: filepath.Join(s.Logs, "web.log"),
		StderrFile: filepath.Join(s.Logs, "web.log"),
	}
	wp, err := spawnProc(webSpec)
	if err != nil {
		return fail(fmt.Errorf("沙箱 web 宿主拉起失败: %w", err))
	}
	s.webProc = wp
	if err := waitWebReady(s); err != nil {
		return fail(err)
	}
	return s, nil
}

// Stop 收尾：栈全停（进程树击杀）＋锁释放；临时目录清理（KeepRoot＝保留排障
// 现场）。幂等；杀树失败不阻断后续清理（尽力而为，e2e stop.sh 同纪律）。
func (s *Stack) Stop() {
	if s == nil || s.stopped {
		return
	}
	s.stopped = true
	if s.webProc != nil {
		if err := s.webProc.KillTree(); err != nil {
			logf(s, "web 宿主杀树失败: %v", err)
		}
	}
	if s.daemonProc != nil {
		// 优雅停先试（守护内部收口序：watcher/worker 停、pid 文件删）；
		// 应答不挑——发了就算（e2e shutdown_daemon 同纪律）。
		s.postShutdown()
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) && !portFree(s.Ports.Daemon) {
			time.Sleep(500 * time.Millisecond)
		}
		if err := s.daemonProc.KillTree(); err != nil {
			logf(s, "daemon 杀树失败: %v", err)
		}
	}
	s.lock.release()
	if s.Opt.KeepRoot {
		return
	}
	_ = os.RemoveAll(s.Root)
}

// DaemonToken 沙箱 daemon 管理口 Bearer（<data>/daemon.token；就绪后必在）。
func (s *Stack) DaemonToken() string {
	raw, err := os.ReadFile(filepath.Join(s.Data, "daemon.token"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}

// daemonHTTPClient 沙箱 daemon 管理口共用客户端：DisableKeepAlives——逐请求
// 关连接，不留闲置 keep-alive 连接占住 daemon 口（Stop 的端口让位等待会被
// 自家轮询连接卡满 10s；Windows 对同口既有连接阻塞重绑）。
func daemonHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout:   timeout,
		Transport: &http.Transport{DisableKeepAlives: true},
	}
}

// postShutdown POST /shutdown（token 就绪才发；失败静默——杀树兜底）。
func (s *Stack) postShutdown() {
	tok := s.DaemonToken()
	if tok == "" {
		return
	}
	req, err := http.NewRequest(http.MethodPost,
		fmt.Sprintf("http://127.0.0.1:%d/shutdown", s.Ports.Daemon), nil)
	if err != nil {
		return
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := daemonHTTPClient(s.Opt.HTTPTimeout).Do(req)
	if err == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}
}

// waitDaemonReady 等 /stats 200（token 文件由守护 EnsureToken 落盘——先等文件
// 再探端点；超时带 daemon.log 指路）。
func waitDaemonReady(s *Stack) error {
	deadline := time.Now().Add(s.Opt.DaemonReadyTimeout)
	for time.Now().Before(deadline) {
		if tok := s.DaemonToken(); tok != "" {
			req, _ := http.NewRequest(http.MethodGet,
				fmt.Sprintf("http://127.0.0.1:%d/stats", s.Ports.Daemon), nil)
			req.Header.Set("Authorization", "Bearer "+tok)
			if resp, err := daemonHTTPClient(s.Opt.HTTPTimeout).Do(req); err == nil {
				_, _ = io.Copy(io.Discard, resp.Body)
				_ = resp.Body.Close()
				if resp.StatusCode == http.StatusOK {
					return nil
				}
			}
		}
		time.Sleep(1 * time.Second)
	}
	return fmt.Errorf("沙箱 daemon %.0fs 未就绪（日志: %s）",
		s.Opt.DaemonReadyTimeout.Seconds(), filepath.Join(s.Logs, "daemon.log"))
}

// webReadyPrefix web 宿主就绪行（auth e2e 实锚：`dsh web: http://127.0.0.1:<port>/?token=<43>`）。
const webReadyPrefix = "dsh web: http://127.0.0.1:"

// waitWebReady 等 web.log 出现就绪行并捕获登录 token。
func waitWebReady(s *Stack) error {
	logPath := filepath.Join(s.Logs, "web.log")
	deadline := time.Now().Add(s.Opt.WebReadyTimeout)
	for time.Now().Before(deadline) {
		if raw, err := os.ReadFile(logPath); err == nil {
			for _, ln := range strings.Split(string(raw), "\n") {
				if i := strings.Index(ln, webReadyPrefix); i >= 0 {
					rest := ln[i+len(webReadyPrefix):]
					if j := strings.IndexAny(rest, " \t\r"); j >= 0 {
						rest = rest[:j]
					}
					if tok, err2 := parseWebToken(rest, s.Ports.Web); err2 == nil {
						s.WebToken = tok
						return nil
					}
				}
			}
		}
		time.Sleep(1 * time.Second)
	}
	return fmt.Errorf("沙箱 web 宿主 %.0fs 未就绪（日志: %s）",
		s.Opt.WebReadyTimeout.Seconds(), logPath)
}

// parseWebToken 就绪行（host:port/?token=X）→ token（端口对上才认——防串口）。
func parseWebToken(rest string, wantPort int) (string, error) {
	rest = strings.TrimPrefix(rest, "127.0.0.1:")
	slash := strings.IndexByte(rest, '/')
	if slash < 0 {
		return "", fmt.Errorf("就绪行无路径: %s", rest)
	}
	port := 0
	if _, err := fmt.Sscanf(rest[:slash], "%d", &port); err != nil || port != wantPort {
		return "", fmt.Errorf("就绪行端口不符（want %d）: %s", wantPort, rest)
	}
	q := rest[slash:]
	if i := strings.IndexByte(q, '#'); i >= 0 {
		q = q[:i]
	}
	for _, kv := range strings.Split(strings.TrimPrefix(q, "/?"), "&") {
		if strings.HasPrefix(kv, "token=") {
			return strings.TrimPrefix(kv, "token="), nil
		}
	}
	return "", fmt.Errorf("就绪行无 token: %s", rest)
}

// ---- env 构造（「-u 清单＋沙箱钉值」；混合正斜杠路径——TOML/node/env 通用） ----

// sandboxEnv 完整子进程环境：os.Environ() 去 unset 清单与全部 FERRYMAN_*/DSH_HOME
// 钉值键（防父进程生产值渗入），再覆盖 overrides（路径值转正斜杠混合形态）。
func sandboxEnv(overrides map[string]string, unset ...string) []string {
	drop := map[string]bool{}
	for _, k := range unset {
		drop[k] = true
	}
	for _, k := range []string{
		"FERRYMAN_CONFIG", "FERRYMAN_DATA", "FERRYMAN_PORT", "FERRYMAN_TOKEN_FILE",
		"FERRYMAN_PANEL_PORT", "DSH_HOME",
	} {
		drop[k] = true
	}
	var out []string
	for _, kv := range os.Environ() {
		eq := strings.IndexByte(kv, '=')
		if eq <= 0 || drop[kv[:eq]] {
			continue
		}
		out = append(out, kv)
	}
	keys := make([]string, 0, len(overrides))
	for k := range overrides {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		out = append(out, k+"="+filepath.ToSlash(overrides[k]))
	}
	return out
}

// ---- 备料 ----

// skipRule 备料剔除规则（rel 相对源根，正斜杠归一；isDir＝条目目录性）。
type skipRule func(rel string, isDir bool) bool

// skipStagingJunk 插件快照拷贝的剔除面（e2e setup.sh 同款：调研残留不带进沙箱）。
func skipStagingJunk(rel string, isDir bool) bool {
	r := filepath.ToSlash(rel)
	return r == ".playwright-cli" || strings.HasPrefix(r, ".playwright-cli/") ||
		r == ".git" || strings.HasPrefix(r, ".git/")
}

// homeSkipRule home 复制剔除规则（e2e prepare_sandbox_home 收尾卫生的复制侧
// 前置形态：sessions 清空、生产专属工件不带、bak 残留不带、三 profile 的插件
// 两落点由备料换装——插件位在生产是 junction（DirEntry IsDir=false），故这些
// 条目不设 isDir 门槛）。
func homeSkipRule(rel string, isDir bool) bool {
	r := filepath.ToSlash(rel)
	top := r
	if i := strings.IndexByte(r, '/'); i >= 0 {
		top = r[:i]
	}
	switch {
	case top == "sessions", top == "remote-link":
		return true
	case r == "start-web.cmd" || r == "start-web-hidden.vbs" ||
		r == "autostart-runkeys.reg" || r == "enable-autologon-step1.cmd":
		return true
	case strings.Contains(r, ".bak-"):
		return true
	case r == "profiles/web/ferryman-dsh" ||
		r == "profiles/web/node_modules/ferryman-dsh" ||
		r == "profiles/desktop/ferryman-dsh" ||
		r == "profiles/desktop/node_modules/ferryman-dsh" ||
		r == "profiles/headless/ferryman-dsh" ||
		r == "profiles/headless/node_modules/ferryman-dsh":
		return true
	}
	return false
}

// stageHome 沙箱 dsh home 备料：插件快照 → home 复制（junction 重链＋文件拷）→
// 插件两落点换装（junction 指备料快照＋node_modules 拷贝——镜像生产安装形态）
// → 生成面重写（profile patch/package.json/home patch）。
func stageHome(s *Stack, homeSrc, pluginSrc string) error {
	if err := copyTree(pluginSrc, s.PluginSnap, skipStagingJunk); err != nil {
		return fmt.Errorf("插件快照拷贝失败: %w", err)
	}
	var links [][2]string
	if err := copyTreeRelink(homeSrc, s.Home, homeSkipRule, &links); err != nil {
		return err
	}
	// 插件两落点换装（复制阶段已跳过生产插件位）。
	if err := os.MkdirAll(filepath.Join(s.Home, "profiles", "web"), 0o755); err != nil {
		return err
	}
	links = append(links, [2]string{
		filepath.Join(s.Home, "profiles", "web", "ferryman-dsh"), s.PluginSnap})
	if err := copyTree(s.PluginSnap, filepath.Join(s.Home, "profiles", "web",
		"node_modules", "ferryman-dsh"), skipStagingJunk); err != nil {
		return fmt.Errorf("node_modules 插件拷贝失败: %w", err)
	}
	if len(links) > 0 {
		if err := makeJunctions(links); err != nil {
			return fmt.Errorf("junction 备料失败: %w", err)
		}
	}
	if err := writeProfilePatch(s); err != nil {
		return err
	}
	if err := writeProfilePackageJSON(s, homeSrc); err != nil {
		return err
	}
	return rewriteHomePatch(s, homeSrc)
}

// copyTree 递归拷目录（无链接收集面——插件快照等纯实体树）。
func copyTree(src, dst string, skip skipRule) error {
	return copyTreeRelink(src, dst, skip, nil)
}

// copyTreeRelink 递归拷目录；链接目录条目（junction/符号链接——本机 Go 1.23/
// Win10 实锚：junction 的 DirEntry.Type() 呈 ModeIrregular('?‍')而非
// ModeSymlink、IsDir()=false，故判据＝Readlink 试探而非 Type 位）**不 deref**：
// 记 (dst, 原目标绝对位) 进 links 交 makeJunctions 重链（同 app 解析等价且免
// 大拷贝——profiles/node_modules 的 junction 群指向宿主 app 资源，deref 会把
// 整个 app 资源树搬进沙箱）。悬垂目标照链（生产既有形态，宿主回落 app 自带
// 副本解析）。链接文件条目无预期面，跳过。
func copyTreeRelink(src, dst string, skip skipRule, links *[][2]string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(src, p)
		if rerr != nil {
			return rerr
		}
		if rel == "." {
			return nil
		}
		isDir := d.IsDir()
		if skip != nil && skip(rel, isDir) {
			if isDir {
				return filepath.SkipDir
			}
			return nil
		}
		// 链接判定：Readlink 成功即链接（junction/symlink 通用；Type 位在
		// Windows 对 junction 不可靠——见头注实锚）。目录链接（Stat 跟随后
		// IsDir）记重链；WalkDir 对 IsDir=false 的条目本就不递归，真符号链接
		// 目录（Type 带 ModeSymlink 且 IsDir=false）同不递归。
		if d.Type()&(os.ModeSymlink|os.ModeIrregular) != 0 {
			if t, lerr := os.Readlink(p); lerr == nil {
				if links != nil {
					if fi, serr := os.Stat(p); serr == nil && fi.IsDir() {
						*links = append(*links, [2]string{filepath.Join(dst, rel),
							resolveLinkTarget(filepath.Dir(p), t)})
					}
				}
				return nil // 链接条目不拷本体（重链面/悬垂容忍）
			}
		}
		target := filepath.Join(dst, rel)
		if isDir {
			return os.MkdirAll(target, 0o755)
		}
		in, oerr := os.Open(p)
		if oerr != nil {
			return oerr
		}
		defer in.Close()
		if merr := os.MkdirAll(filepath.Dir(target), 0o755); merr != nil {
			return merr
		}
		out, cerr := os.Create(target)
		if cerr != nil {
			return cerr
		}
		if _, cerr = io.Copy(out, in); cerr != nil {
			out.Close()
			return cerr
		}
		return out.Close()
	})
}

// fileExists / dirExists 小件。
func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}

func dirExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

// logf 沙箱内探针日志（<logs>/probe.log 追加；失败静默——日志不弄断主流程）。
func logf(s *Stack, format string, args ...any) {
	if s == nil || s.Logs == "" {
		return
	}
	f, err := os.OpenFile(filepath.Join(s.Logs, "probe.log"),
		os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s %s\n", time.Now().Format("2006-01-02 15:04:05"),
		fmt.Sprintf(format, args...))
}

// resolvePluginSrc 插件备料源解析链：显式传入 → exe 同仓 plugin/ferryman-dsh
// （工作树/开发构建形态——exe 在仓内时天然命中夜链工作树）→ 生产 web profile
// junction 目标（安装形态；junction 指活树是生产实锚）。都不可得＝响亮报错。
func resolvePluginSrc(explicit, exe, homeSrc string) (string, error) {
	if strings.TrimSpace(explicit) != "" {
		if !dirExists(explicit) {
			return "", fmt.Errorf("插件备料源不存在: %s", explicit)
		}
		return explicit, nil
	}
	if exe != "" {
		if cand := filepath.Join(filepath.Dir(exe), "plugin", "ferryman-dsh"); dirExists(cand) {
			return cand, nil
		}
		// go build ./cmd/ferryman 产物落在 cmd/ 下——exe 上跳一层再试。
		if cand := filepath.Join(filepath.Dir(filepath.Dir(exe)), "plugin", "ferryman-dsh"); dirExists(cand) {
			return cand, nil
		}
	}
	link := filepath.Join(homeSrc, "profiles", "web", "ferryman-dsh")
	if target, err := os.Readlink(link); err == nil {
		resolved := resolveLinkTarget(filepath.Dir(link), target)
		if dirExists(resolved) {
			return resolved, nil
		}
	}
	return "", fmt.Errorf("插件备料源不可得（exe=%s 无同仓 plugin；生产 junction %s 不可解析）",
		exe, link)
}

// resolveLinkTarget 链接目标归一（\??\ 前缀剥除/UNC 还原/相对补全——
// dshverify l0.go 同款逻辑的镜像，包内自持避免反向依赖）。
func resolveLinkTarget(linkDir, target string) string {
	t := target
	for _, pfx := range []string{`\??\`, `\\?\`} {
		if strings.HasPrefix(t, pfx) {
			t = strings.TrimPrefix(t, pfx)
			break
		}
	}
	if strings.HasPrefix(t, `UNC\`) {
		t = `\\` + strings.TrimPrefix(t, `UNC\`)
	}
	if t != "" && !filepath.IsAbs(t) {
		t = filepath.Join(linkDir, t)
	}
	return t
}

// ---- 生成面 ----

// writeSandboxConfig 沙箱 daemon 测试 config（e2e write_sandbox_config 的 Go
// 形态＋票面差异：[dock] 只携 active 上游——「走沙箱 daemon 配置的最便宜上游」
// 的落点；[heartbeat].ttl_s＋[dsh_compact] 秒级参数驱动压缩链两道各自然触发）。
func writeSandboxConfig(s *Stack) error {
	var b strings.Builder
	b.WriteString("# Ferryman verify-dsh L2 sandbox config - generated (do not hand-edit).\n" +
		"# Isolation contract: every path stays under the sandbox root; ports stay in 25xxx.\n\n")
	fmt.Fprintf(&b, "[server]\nport = %d\ndata_dir = %q\n\n",
		s.Ports.Daemon, filepath.ToSlash(s.Data))
	b.WriteString("[gate]\ncc_mode = \"off\"\ncodex_mode = \"off\"\ndsh_mode = \"enforce\"\n\n")
	b.WriteString("[thresholds]\nsummarize_s = 45\nblock_s = 90\nmin_ctx_tokens = 500\ncache_warn_s = 0\n\n")
	fmt.Fprintf(&b, "[watch]\npoll_interval_s = 1\ncc_projects_dir = %q\ncodex_sessions_dir = %q\ndsh_sessions_dir = %q\n\n",
		filepath.ToSlash(filepath.Join(s.Root, "watch", "cc-projects")),
		filepath.ToSlash(filepath.Join(s.Root, "watch", "codex-sessions")),
		filepath.ToSlash(filepath.Join(s.Home, "sessions")))
	// [heartbeat].ttl_s＝压缩链触发基准（Enabled 缺省 false——同模型摆渡关停
	// 形态不动，只有 TTL 供 dsh_compact 推导）。
	b.WriteString("[heartbeat]\nttl_s = 60\n\n")
	// 压缩链秒级参数（e2e suite setup.sh 同源量级）：触发线 0.5×TTL＝30s；
	// min_peak=0（服务面腿会话用量极小也要能触发——「值得压」的门槛不是本探针
	// 的断言面）；poll_hint_s=2（插件侧 10s 下限托底）；标记窗 3×TTL。
	b.WriteString("[dsh_compact]\nenabled = true\ntrigger_ratio = 0.5\nmin_peak_tokens = 0\n" +
		"command_ttl_ratio = 0.5\npoll_hint_s = 2\ncompressed_flag_ttl_ratio = 3.0\n\n")
	// [dock]：只携 active 上游（沙箱渡口＝模型路由的最便宜上游承载面）。
	fmt.Fprintf(&b, "[dock]\nlisten = \"127.0.0.1:%d\"\nactive = %q\n\n", s.Ports.Dock, s.Opt.DockActive)
	up := s.Opt.DockUpstreams[s.Opt.DockActive]
	// 上游名可含非 ASCII（生产实锚「智谱」）——TOML 裸键不收，表名/值一律引号。
	fmt.Fprintf(&b, "[dock.upstreams.%q]\nbase_url = %q\n", s.Opt.DockActive, up.BaseURL)
	if up.APIKey != "" {
		fmt.Fprintf(&b, "api_key = %q\n", up.APIKey)
	}
	if len(up.ModelMap) > 0 {
		keys := make([]string, 0, len(up.ModelMap))
		for k := range up.ModelMap {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		b.WriteString("model_map = { ")
		for i, k := range keys {
			if i > 0 {
				b.WriteString(", ")
			}
			fmt.Fprintf(&b, "%q = %q", k, up.ModelMap[k])
		}
		b.WriteString(" }\n")
	}
	if up.Dialect != "" {
		fmt.Fprintf(&b, "dialect = %q\n", up.Dialect)
	}
	return os.WriteFile(filepath.Join(s.Root, "config.toml"), []byte(b.String()), 0o644)
}

// sandboxProfilePatch 沙箱 web profile 的 cordis.patch.yml（e2e write_sandbox_
// patch 形状＋票面两差异：服务面腿合成组成 insert；转录明文 insert——探针要
// 直读转录断压缩链执行痕）。ios-control（钉生产口 3081）与 MCP 注入（npx 闪窗
// ＋网络双风险）整体剔除——e2e 同款隔离纪律。
const sandboxProfilePatch = `# Ferryman verify-dsh L2 sandbox profile patch - generated (do not hand-edit).
# Diffs vs production profiles/web/cordis.patch.yml:
#   - phone-remote bundle entry removed (its config pins a production port)
#   - MCP client inserts removed (npx spawns: console-flash + network)
#   - no production secrets carried
- insert:
    - id: ferryman-dsh
      name: ./ferryman-dsh/index.mjs
    # 服务面腿合成组成（票05）：真机 web 宿主把 compaction 服务隔离在 preset
    # 组内、插件域恒不可达（2026-10-07 E2E 实锚）——profile 层 insert 一份
    # compaction-basic 使服务面对插件可见；cordis 会话的 /compact 命令照走
    # 组内实例（内层遮蔽），本份只服务插件服务面腿。
    - id: compaction-profile
      name: '@deepseek-ai/dsh-compaction-basic'
    # 会话转录落明文（e2e suite 同款）：探针直读转录断压缩链执行痕
    # （command/run 生命周期/compaction 事件）。id 定向 patch 是整段 config
    # 替换——root 用与 base bundle 相同的 dshHomePath 表达式原样重述。
    - id: session-persistence-jsonl
      name: '@deepseek-ai/dsh-session-persistence-jsonl'
      config:
        root: !!js dshHomePath('sessions')
        compression: none
`

// writeProfilePatch 重写沙箱 web profile patch。
func writeProfilePatch(s *Stack) error {
	p := filepath.Join(s.Home, "profiles", "web", "cordis.patch.yml")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, []byte(sandboxProfilePatch), 0o644)
}

// writeProfilePackageJSON 沙箱 profile package.json：生产形状派生（镜像
// installed 生产面，不硬编码清单）——摘 dsh-ios-control（其 config 钉生产口
// 3081；运行时决议只看 bundles 表，dependencies 仅为 pnpm 元数据留档——沙箱
// 从不起 pnpm install）。
func writeProfilePackageJSON(s *Stack, homeSrc string) error {
	raw, err := os.ReadFile(filepath.Join(homeSrc, "profiles", "web", "package.json"))
	if err != nil {
		return fmt.Errorf("生产 package.json 不可读: %w", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return fmt.Errorf("生产 package.json 解析失败: %w", err)
	}
	if deps, ok := doc["dependencies"].(map[string]any); ok {
		delete(deps, "dsh-ios-control")
	}
	if dsh, ok := doc["dsh"].(map[string]any); ok {
		if prof, ok := dsh["profile"].(map[string]any); ok {
			if bundles, ok := prof["bundles"].([]any); ok {
				kept := make([]any, 0, len(bundles))
				for _, b := range bundles {
					if bs, _ := b.(string); bs == "dsh-ios-control" {
						continue
					}
					kept = append(kept, b)
				}
				prof["bundles"] = kept
			}
		}
	}
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	p := filepath.Join(s.Home, "profiles", "web", "package.json")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, append(out, '\n'), 0o644)
}

// rewriteHomePatch home 层 cordis.patch.yml：生产拷贝的 llm-deepseek 渡口地址
// 改指沙箱渡口（生产 15722 → 沙箱 25xxx；行级替换——写入器 baseURL 行形状
// 钉死「baseURL: http://127.0.0.1:15722」）。其余条目原样（含 disabled 遥测）。
func rewriteHomePatch(s *Stack, homeSrc string) error {
	raw, err := os.ReadFile(filepath.Join(homeSrc, "cordis.patch.yml"))
	if err != nil {
		return fmt.Errorf("生产 home patch 不可读: %w", err)
	}
	old := "baseURL: http://127.0.0.1:15722"
	new := fmt.Sprintf("baseURL: http://127.0.0.1:%d", s.Ports.Dock)
	text := strings.ReplaceAll(string(raw), old, new)
	return os.WriteFile(filepath.Join(s.Home, "cordis.patch.yml"), []byte(text), 0o644)
}

// isolationScan 生产端口残迹扫描（生成面四件：config/home patch/profile
// patch/package.json；任一命中即拒跑——e2e isolation_scan 同款铁闸）。
func isolationScan(s *Stack) error {
	files := []string{
		filepath.Join(s.Root, "config.toml"),
		filepath.Join(s.Home, "cordis.patch.yml"),
		filepath.Join(s.Home, "profiles", "web", "cordis.patch.yml"),
		filepath.Join(s.Home, "profiles", "web", "package.json"),
	}
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			return fmt.Errorf("隔离扫描读失败: %s: %w", f, err)
		}
		for _, mark := range prodPortMarks {
			if strings.Contains(string(raw), mark) {
				return fmt.Errorf("隔离扫描失败：%s 含生产端口残迹 %s", f, mark)
			}
		}
	}
	return nil
}
