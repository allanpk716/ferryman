package main

// env.go — 影子实例装配（票05 What-to-build 第1条）：独立 DataDir（临时目录）、
// 独立双端口（管理/渡口，高位随机分配）、独立 start-daemon.cmd、本地桩发布
// 端点与桩上游。一切端口/目录从装配注入——代码里没有 15700/15722 字面量，
// 分配器还有生产口黑名单护栏（util.go）。
//
// 隔离面（不碰生产 ~/ferryman 与真实会话目录）：
//   - config 走 FERRYMAN_CONFIG 指向影子 config.toml（[server].data_dir=影子目录）；
//   - USERPROFILE 重定向到影子 home——daemon 内部 ferry.LoadProviders("") 等
//     以家目录推缺省路径的读取面全部落空目录，杜绝读生产配置；
//   - [watch] 的 cc_projects_dir/codex_sessions_dir 指空目录——彩排守护绝不扫
//     真实会话转录。

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// tagAsset 一版影子 exe 的资产位形（tag == exe 烤进的 main.version）。
type tagAsset struct {
	Path string
	SHA  string
	Size int64
}

// envPorts 影子四口（全高位随机）。
type envPorts struct {
	Mgmt int // 影子守护管理口（supervisor 注入 Port）
	Dock int // 影子渡口代理面（探针打这里）
	Up   int // 桩上游
	Rel  int // 桩发布端点
}

// envOpts 影子配置面。
type envOpts struct {
	DrainS     float64 // [dock].drain_timeout_s（长流场景须覆盖流全程）
	BindRetryS float64 // [dock].bind_retry_s（升级排水窗内渡口口被旧守护占）
	StreamDur  time.Duration
	ChunkEvery time.Duration
}

func (o envOpts) withDefaults() envOpts {
	if o.DrainS == 0 {
		o.DrainS = 120
	}
	if o.BindRetryS == 0 {
		o.BindRetryS = 150
	}
	if o.StreamDur == 0 {
		o.StreamDur = 70 * time.Second // 长流场景定义：在途流 >60s
	}
	if o.ChunkEvery == 0 {
		o.ChunkEvery = time.Second
	}
	return o
}

// shadowEnv 一个影子世界：目录、端口、桩、守护。
type shadowEnv struct {
	Root    string
	DataDir string // 影子 DataDir（锁/journal/token/pid/update.log 都在这）
	HomeDir string // USERPROFILE 重定向目标
	CmdPath string // 影子 start-daemon.cmd
	ExePath string // <Root>/ferryman.exe（换装目标；名字必须是 ferryman.exe——
	// .new/.old-* 残余与备份的清扫域按本名展开）
	Ports    envPorts
	Token    string
	assets   map[string]tagAsset
	rel      *releaseStub
	up       *upstreamStub
	logf     func(string, ...any)
	portsReg *[]int
}

// newShadowEnv 装配并拉起一个影子世界（curTag = 初始磁盘版本）。
func newShadowEnv(assets map[string]tagAsset, curTag string, opts envOpts,
	logf func(string, ...any), portsReg *[]int) (*shadowEnv, error) {
	opts = opts.withDefaults()
	root, err := os.MkdirTemp("", "ferryman-rehearsal-")
	if err != nil {
		return nil, err
	}
	e := &shadowEnv{
		Root:     root,
		DataDir:  filepath.Join(root, "data"),
		HomeDir:  filepath.Join(root, "home"),
		CmdPath:  filepath.Join(root, "start-daemon.cmd"),
		ExePath:  filepath.Join(root, "ferryman.exe"),
		assets:   assets,
		logf:     logf,
		portsReg: portsReg,
	}
	for _, d := range []string{e.DataDir, e.HomeDir,
		filepath.Join(root, "watch-cc"), filepath.Join(root, "watch-codex")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, err
		}
	}
	// 点火脚本全 ASCII 铁律——路径非 ASCII 时早失败（不静默写坏脚本）。
	for _, p := range []string{root, e.ExePath} {
		if !isASCII(p) {
			return nil, fmt.Errorf("影子路径含非 ASCII（点火脚本铁律）: %s", p)
		}
	}
	// 四口全高位随机。
	for i := 0; i < 4; i++ {
		p, err := allocPort()
		if err != nil {
			return nil, err
		}
		switch i {
		case 0:
			e.Ports.Mgmt = p
		case 1:
			e.Ports.Dock = p
		case 2:
			e.Ports.Up = p
		case 3:
			e.Ports.Rel = p
		}
	}
	*e.portsReg = append(*e.portsReg, e.Ports.Mgmt, e.Ports.Dock, e.Ports.Up, e.Ports.Rel)

	// 影子 config.toml（TOML 字符串统一正斜杠——Windows API 两斜杠通吃，
	// 免反斜杠转义噪音）。
	cfg := fmt.Sprintf(`# 彩排影子配置（装配生成，勿手编）
[server]
port = %d
data_dir = "%s"

[watch]
cc_projects_dir = "%s"
codex_sessions_dir = "%s"

[dock]
listen = "127.0.0.1:%d"
active = "stub"
drain_timeout_s = %s
bind_retry_s = %s

[dock.upstreams.stub]
base_url = "http://127.0.0.1:%d"
api_key = "rehearsal-stub-key"

[dock.upstreams.stub.model_map]
default = "rehearsal-model"
`,
		e.Ports.Mgmt, slash(e.DataDir),
		slash(filepath.Join(root, "watch-cc")), slash(filepath.Join(root, "watch-codex")),
		e.Ports.Dock, strconv.FormatFloat(opts.DrainS, 'f', -1, 64),
		strconv.FormatFloat(opts.BindRetryS, 'f', -1, 64),
		e.Ports.Up)
	cfgPath := filepath.Join(root, "config.toml")
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o644); err != nil {
		return nil, err
	}
	if err := writeStartCmd(e.CmdPath, e.ExePath, cfgPath, e.HomeDir,
		filepath.Join(root, "serve.out.log"), filepath.Join(root, "serve.err.log")); err != nil {
		return nil, err
	}
	// 初始磁盘版本 = curTag 构建（此后随事务换装翻版）。
	if err := copyFileSteno(assets[curTag].Path, e.ExePath); err != nil {
		return nil, err
	}
	e.up, err = newUpstreamStub(e.Ports.Up, opts.StreamDur, opts.ChunkEvery)
	if err != nil {
		return nil, err
	}
	e.rel, err = newReleaseStub(e.Ports.Rel)
	if err != nil {
		return nil, err
	}
	if err := e.startDaemon(curTag); err != nil {
		e.teardown()
		return nil, err
	}
	e.logf("影子世界就绪: root=%s mgmt=%d dock=%d up=%d rel=%s",
		e.Root, e.Ports.Mgmt, e.Ports.Dock, e.Ports.Up, e.rel.addr)
	return e, nil
}

// writeStartCmd 影子点火脚本：形态与 installer.EnsureLauncher 同款（全 ASCII、
// CRLF、引号 exe 路径——update.ParseStartDaemonExe 的消费契约），另注入
// FERRYMAN_CONFIG 与 USERPROFILE 重定向（隔离面见文件头）。
func writeStartCmd(path, exePath, configPath, homeDir, outLog, errLog string) error {
	body := "@echo off\r\n" +
		"rem Ferryman rehearsal shadow launcher (auto-generated, do not edit)\r\n" +
		"set \"FERRYMAN_CONFIG=" + slash(configPath) + "\"\r\n" +
		"set \"USERPROFILE=" + slash(homeDir) + "\"\r\n" +
		"\"" + exePath + "\" serve >> \"" + slash(outLog) + "\" 2>> \"" + slash(errLog) + "\"\r\n"
	if !isASCII(body) {
		return fmt.Errorf("点火脚本内容含非 ASCII（代码页脆弱）")
	}
	return os.WriteFile(path, []byte(body), 0o644)
}

// slash 路径正斜杠化（TOML 值与 cmd set 值通吃；exe 引号路径保留反斜杠原样
// ——与生产脚本同形）。
func slash(p string) string { return strings.ReplaceAll(p, "\\", "/") }

// daemonEnv 守护子进程环境：剥自拉起标记 + 影子隔离注入。
func (e *shadowEnv) daemonEnv() []string {
	return append(filterEnv(),
		"FERRYMAN_CONFIG="+filepath.Join(e.Root, "config.toml"),
		"USERPROFILE="+e.HomeDir)
}

// startDaemon 经点火脚本拉起影子守护并等健康（版本须等于 wantTag——/stats
// version 来自 exe 烤进的 main.version）。
func (e *shadowEnv) startDaemon(wantTag string) error {
	log, err := os.Create(filepath.Join(e.Root, "daemon-boot.log"))
	if err != nil {
		return err
	}
	defer log.Close()
	if _, err := spawnHidden("cmd.exe", []string{"/c", e.CmdPath}, e.daemonEnv(), log, log); err != nil {
		return fmt.Errorf("影子守护拉起失败: %w", err)
	}
	if err := e.waitHealthy(wantTag, 30*time.Second); err != nil {
		return err
	}
	return e.waitDockReady(15 * time.Second)
}

// mgmtAddr 管理面地址。
func (e *shadowEnv) mgmtAddr() string { return fmt.Sprintf("127.0.0.1:%d", e.Ports.Mgmt) }

// dockAddr 代理面地址。
func (e *shadowEnv) dockAddr() string { return fmt.Sprintf("127.0.0.1:%d", e.Ports.Dock) }

// statsVersion GET /stats（Bearer 影子 token）取 version。
func (e *shadowEnv) statsVersion() (string, error) {
	req, err := http.NewRequest(http.MethodGet, "http://"+e.mgmtAddr()+"/stats", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+e.Token)
	c := &http.Client{Timeout: 3 * time.Second}
	resp, err := c.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var out struct {
		Version string `json:"version"`
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("/stats HTTP %d", resp.StatusCode)
	}
	if err := jsonDecodeBody(resp.Body, &out); err != nil {
		return "", err
	}
	return out.Version, nil
}

// waitHealthy 等守护健康（token 落盘 + /stats 200 + version==want）。
func (e *shadowEnv) waitHealthy(wantTag string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		if e.Token == "" {
			if b, err := os.ReadFile(filepath.Join(e.DataDir, "daemon.token")); err == nil {
				e.Token = strings.TrimSpace(string(b))
			}
		}
		if e.Token != "" {
			if v, err := e.statsVersion(); err == nil {
				if v == wantTag {
					return nil
				}
				lastErr = fmt.Errorf("版本 %q != 期望 %q", v, wantTag)
			} else {
				lastErr = err
			}
		} else {
			lastErr = fmt.Errorf("daemon.token 未落盘")
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("影子守护健康等待超时: %v", lastErr)
}

// waitDockReady 等渡口监听就绪（拨得通即认为 listener 在）。
func (e *shadowEnv) waitDockReady(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", e.dockAddr(), 500*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("影子渡口就绪等待超时 %s", e.dockAddr())
}

// postShutdown 优雅停影子守护（清场用；事务内停旧由监督者自己做）。
func (e *shadowEnv) postShutdown() {
	req, err := http.NewRequest(http.MethodPost, "http://"+e.mgmtAddr()+"/shutdown", nil)
	if err != nil {
		return
	}
	req.Header.Set("Authorization", "Bearer "+e.Token)
	c := &http.Client{Timeout: 3 * time.Second}
	resp, err := c.Do(req)
	if err == nil {
		_ = resp.Body.Close()
	}
}

// updateLogText 影子 update.log 全文（门行为/锁接管/备份挪窝的断言证据）。
func (e *shadowEnv) updateLogText() string {
	b, err := os.ReadFile(filepath.Join(e.DataDir, "update.log"))
	if err != nil {
		return ""
	}
	return string(b)
}

// teardown 清场：优雅停 → 硬杀兜底（daemon.pid 与 update.lock 持有者）→
// 关桩 → 删临时目录（运行映像改名可能短暂锁文件，重试；删不净只记日志——
// 留给系统临时区清理，绝不留孤儿进程）。
func (e *shadowEnv) teardown() {
	e.postShutdown()
	if pid := readDaemonPIDFile(e.DataDir); pid > 0 {
		if !waitProcExit(pid, 10*time.Second) {
			e.logf("影子守护 PID %d 未退场——硬杀", pid)
			_ = killByPID(pid)
			waitProcExit(pid, 5*time.Second)
		}
	}
	if pid := readUpdateLockPID(e.DataDir); pid > 0 && pidAliveLocal(pid) {
		e.logf("update.lock 持有者 PID %d 仍在——硬杀", pid)
		_ = killByPID(pid)
		waitProcExit(pid, 5*time.Second)
	}
	if e.up != nil {
		e.up.close()
	}
	if e.rel != nil {
		e.rel.close()
	}
	for i := 0; i < 3; i++ {
		if err := os.RemoveAll(e.Root); err == nil {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	e.logf("影子目录清理未净（残留 %s，无进程占用后系统临时区会收走）", e.Root)
}
