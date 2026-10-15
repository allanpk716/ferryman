package main

// restore.go —— 票39（spec#34，ADR-0025 信号②）：`ferryman restore [--cwd 目录]`
// 显式恢复薄客户端。守护 /restore?explicit=1 端点的命令行面：把注入文本打到
// stdout（单份注入最新／多份列清单／无候选明说——语义全在守护端点，
// 接缝 A 见 internal/daemon/restore_explicit_test.go），本文件只管参数解析、
// 端点调用与可读报错（不可达/鉴权失败给人话 + 处置提示，绝无堆栈）。
//
// 用户级斜杠命令 /ferry-restore（install-cc 写入）的命令体就是本命令——
// 会话内 `!` 执行把 stdout 带回上下文，与终端手跑同一条链路。
//
// 可测性（status.go 同范式）：HTTP 收在 daemonRestoreGet（httptest 直打钉
// 请求形态），决策树收在 restoreRun（deps 注桩零网络），装配缝
// realRestoreDeps 从 config 派生 port/token（与守护同源）。

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"time"

	"ferryman/internal/config"
	"ferryman/internal/daemon"
	"ferryman/internal/installer"
)

// restoreUsage 用法面（-h/--help 与用法错共用一份常量，票01 契约）。
const restoreUsage string = `用法:
  ferryman restore [--cwd 目录]  # 显式取回本目录交接（ADR-0025 信号②）：
                                 #   单份注入最新／多份列清单／无候选明说
                                 #   （输出到标准输出；守护 /restore?explicit=1
                                 #   的薄客户端）
                                 #   --cwd 缺省 = 当前目录
`

// cmdRestore `ferryman restore` 入口（help 安全契约：-h/--help 打印 usage
// 退 0；用法错退 2）。配置载不动不挡恢复：按缺省口发（token 留空 → 401
// 如实报，status 同纪律）。
func cmdRestore(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 && (args[0] == "-h" || args[0] == "--help") {
		fmt.Fprint(stdout, restoreUsage)
		return 0
	}
	fs := flag.NewFlagSet("restore", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cwd := fs.String("cwd", "", "目标项目目录（缺省 = 当前目录）")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) { // 迟到的 -h（如 `restore --cwd X -h`）
			return 0
		}
		fmt.Fprint(stderr, restoreUsage)
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "未知参数 %q——ferryman restore 只接受 --cwd\n%s", fs.Arg(0), restoreUsage)
		return 2
	}
	cfg, err := config.Load("", false)
	if err != nil {
		fmt.Fprintf(stderr, "配置加载失败（按缺省口 %d 发请求）: %v\n", installer.DefaultDaemonPort, err)
		return restoreRun(stdout, stderr, restoreCwdOrDefault(*cwd, os.Getwd), realRestoreDeps(nil))
	}
	return restoreRun(stdout, stderr, restoreCwdOrDefault(*cwd, os.Getwd), realRestoreDeps(cfg))
}

// restoreCwdOrDefault --cwd 缺省 = 当前目录（纯函数；getwd 失败空串照传——
// 守护对空 cwd 返回空候选，CLI 输出“无可用交接”明说，不静默猜别的目录）。
func restoreCwdOrDefault(cwd string, getwd func() (string, error)) string {
	if cwd != "" {
		return cwd
	}
	wd, err := getwd()
	if err != nil {
		return ""
	}
	return wd
}

// restoreDeps 可注入面（statusDeps 同范式）：真装配 realRestoreDeps，单测
// 注桩 Get 钉决策树（零网络）。
type restoreDeps struct {
	Port  int
	Token string
	Get   func(port int, token, cwd, sessionID string) (map[string]any, error)
}

// realRestoreDeps 真装配：port/token 与守护同源（Server.Port 缺省 15700 与
// 数据目录 daemon.token——realStatusDeps 同纪律）。cfg 不可得（nil）时按
// 缺省口、留空 token（端点 401 如实报）。
func realRestoreDeps(cfg *config.Config) *restoreDeps {
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
	return &restoreDeps{Port: port, Token: token, Get: daemonRestoreGet}
}

// daemonRestoreGet GET /restore?agent=cc&explicit=1（daemonGet 复用：Bearer、
// 2s 超时、网络错 → errDaemonOffline / 401·403 → errAuth）。sessionID 由
// 调用方合成（cli-restore-*：inject 账与 mark_injected 的登记主体）。
func daemonRestoreGet(port int, token, cwd, sessionID string) (map[string]any, error) {
	q := url.Values{}
	q.Set("agent", "cc") // CLI 是 CC 显式入口（codex 入口不在票39）
	q.Set("cwd", cwd)
	q.Set("session_id", sessionID)
	q.Set("explicit", "1")
	body, err := daemonGet(port, token, "/restore?"+q.Encode())
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// restoreSessionID 合成取回会话 id：cli-restore-<unix 秒>-<6 位随机 hex>（store
// 交接 id 的 randHex6 同款随机源；Windows 时钟精度粗，秒+随机比纳秒更稳）。
// 每次 CLI 调用各自成 id——inject 账与 mark_injected 登记的是“这一次显式取回”。
func restoreSessionID() string {
	suffix := ""
	b := make([]byte, 3)
	if _, err := rand.Read(b); err == nil {
		suffix = "-" + hex.EncodeToString(b)
	}
	return fmt.Sprintf("cli-restore-%d%s", time.Now().Unix(), suffix)
}

// restoreRun 可测核心：合成会话 id → 端点取回 → context 打到 stdout。失败面
// 全部可读报错退 1（守护不在线/鉴权失败/HTTP 异常——绝无堆栈）；context 为
// null/空串时零输出退 0（守护端点对显式请求恒有明说文案，null 只是协议兼容）。
func restoreRun(stdout, stderr io.Writer, cwd string, deps *restoreDeps) int {
	r, err := deps.Get(deps.Port, deps.Token, cwd, restoreSessionID())
	if err != nil {
		switch {
		case errors.Is(err, errDaemonOffline):
			fmt.Fprintf(stderr, "Ferryman 守护不在线（127.0.0.1:%d 无应答）——显式恢复需要守护在线；"+
				"请先启动守护（ferryman serve，或 %s）后重试\n", deps.Port,
				"%USERPROFILE%\\ferryman\\start-daemon.cmd")
		case errors.Is(err, errAuth):
			fmt.Fprintln(stderr, "恢复被拒：鉴权失败（401）——本机 daemon.token 与守护不一致")
		default:
			fmt.Fprintf(stderr, "恢复失败: %v\n", err)
		}
		return 1
	}
	if ctx, _ := r["context"].(string); ctx != "" {
		fmt.Fprintln(stdout, ctx)
	}
	return 0
}
