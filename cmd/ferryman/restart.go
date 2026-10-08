package main

// restart.go — 票07 `ferryman restart [--from N] [--to M]`：安全重启帮手 CLI。
// 端点 POST /settings/restart 拉起的就是它（detached 隐藏，--from/--to 由端点
// 注入旧口/新口）；同时也是用户可手敲的独立 CLI（帮手语义=能独立复跑）。
//
// 装配：缺省从盘上 config.Load("", false) 取 port/data_dir（--from/--to 覆盖）；
// 回滚源=<dataDir>/backups/config/last-healthy.toml（daemon 启动成功点盖章），
// 在场且可整装 Load → RollbackPort=其 [server].port；缺席/不通过 → RollbackPort=0
// 并打一行「无有效回滚源」。Alert 装配照 runUpdateExecute 先例
// （notify.NotifyEvent(tray_reply) 旁路尽力而为——票03 收编事件分派，缺省
// toast）。结果人话打印，成功 exit 0 / 失败 exit 1。

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"ferryman/internal/config"
	"ferryman/internal/notify"
	"ferryman/internal/update"
)

// restartUsage restart 的用法面（用法错共用）。
const restartUsage string = `用法:
  ferryman restart [--from N] [--to M]   # 安全重启守护：停旧→按盘上配置拉新；
                                        #   健康失败自动还原上次健康配置再拉一次
`

// parseRestartArgs 旗标解析（纯函数，测试直测；parseEvents 同先例）：
// --from/--to 缺省 0=未给（调用方回落盘上配置口）；未知旗标/多余位置参数/
// 负端口 = 用法错。
func parseRestartArgs(args []string) (from, to int, err error) {
	fs := flag.NewFlagSet("restart", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.IntVar(&from, "from", 0, "旧守护端口（缺省=盘上配置口）")
	fs.IntVar(&to, "to", 0, "新守护端口（缺省=盘上配置口）")
	if err := fs.Parse(args); err != nil {
		return 0, 0, err
	}
	if fs.NArg() > 0 {
		return 0, 0, fmt.Errorf("多余参数 %q", fs.Arg(0))
	}
	if from < 0 || to < 0 {
		return 0, 0, fmt.Errorf("端口必须为正整数（--from %d --to %d）", from, to)
	}
	return from, to, nil
}

// cmdRestart restart 子命令：CLI 解析+装配 → update.RunRestart（帮手编排核心，
// 见其头注释）。自身不做停旧/拉起——那是 RunRestart 的序列。
func cmdRestart(args []string, w io.Writer) int {
	fromFlag, toFlag, err := parseRestartArgs(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n%s", err, restartUsage)
		return 2
	}
	cfg, err := config.Load("", false)
	if err != nil {
		fmt.Fprintf(w, "restart: 盘上配置读不出，无从重启: %v\n", err)
		return 1
	}
	dataDir := cfg.DataDir()
	fromPort, toPort := cfg.Server.Port, cfg.Server.Port // 手敲缺省=原口重启
	if fromFlag != 0 {
		fromPort = fromFlag
	}
	if toFlag != 0 {
		toPort = toFlag
	}

	// 回滚源（F2 定案 A）：last-healthy.toml 在场且可整装 Load 才算——config.Load
	// 对缺席文件静默回落默认值，故先 Stat 判在场再 Load 判可过。
	lastHealthy := filepath.Join(dataDir, "backups", "config", "last-healthy.toml")
	rollbackPort := 0
	if _, serr := os.Stat(lastHealthy); serr == nil {
		if hc, lerr := config.Load(lastHealthy, false); lerr == nil && hc.Server.Port != 0 {
			rollbackPort = hc.Server.Port
		}
	}
	if rollbackPort == 0 {
		fmt.Fprintln(w, "无有效回滚源（last-healthy.toml 缺席或不可用）——健康失败将停在现场并告警")
	}

	// 换装目标（supervisor.resolveTargetExe 同序）：点火脚本引号 exe 优先，
	// 回落本进程映像（stopDaemon 身份校验兜底 kill 的比对标的）。
	startCmd := filepath.Join(dataDir, "start-daemon.cmd")
	targetExe := ""
	if p, perr := update.ParseStartDaemonExe(startCmd); perr == nil {
		targetExe = p
	} else if exe, eerr := os.Executable(); eerr == nil {
		targetExe = exe
	}

	res := update.RunRestart(update.RestartOpts{
		DataDir:      dataDir,
		ConfigPath:   config.ResolveConfigPath(""),
		StartCmd:     startCmd,
		LastHealthy:  lastHealthy,
		TargetExe:    targetExe,
		FromPort:     fromPort,
		ToPort:       toPort,
		RollbackPort: rollbackPort,
		Alert: func(title, msg string) {
			notify.NotifyEvent(notify.EventTrayReply, title, msg, cfg) // 旁路尽力而为（NotifyEvent 内已护）
		},
	})
	switch {
	case res.Success && !res.RolledBack:
		fmt.Fprintf(w, "重启完成：%s\n", res.Detail)
		return 0
	case res.Success && res.RolledBack:
		fmt.Fprintf(w, "重启以回滚形态完成：%s\n", res.Detail)
		return 0
	default:
		fmt.Fprintf(w, "重启失败：%s（%v）——详见 %s\n", res.Detail, res.Err, filepath.Join(dataDir, "restart.log"))
		return 1
	}
}
