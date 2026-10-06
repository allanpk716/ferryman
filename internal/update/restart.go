package update

// restart.go — 票07 安全重启帮手编排核心（RunRestart）。端点
// POST /settings/restart（internal/daemon/settings_restart.go）只做预检/静默门/
// 拉帮手/hold；停旧拉新在帮手进程 `ferryman restart --from N --to M`
// （cmd/ferryman/restart.go）里跑，最终收敛到本函数。
//
// 序列（票面钉死）：
//   - grace 睡（缺省 1s）——端点响应出网余量（帮手被拉起后先别急着杀旧，
//     让端点的 200 与帮手拉起日志先落地）；
//   - StopOld（缺省=NewSupervisor(...).stopDaemon(TargetExe,"重启停旧")——
//     /shutdown 优雅停+端口释放/进程退场硬门+身份校验兜底 kill，supervisor
//     同款）。失败=Result{Err}，配置未动，如实报（不告警——票面终局组才
//     告警；旧守护多半还在，人工可见状态）；
//   - Launch（缺省=sup.launch(StartCmd)，不经 launchTx——verifyLaunch 探的
//     是 FromPort，换端口场景必误报）→ 轮询 Probe(ToPort) 至 PollTimeout
//     （缺省 90s/1s）→ 活=Success；
//   - 死且有回滚源（RollbackPort≠0 且 LastHealthy 非空）→ Restore（缺省=
//     copyFile(LastHealthy, ConfigPath+".restart-restore")+moveFileReplace
//     原子替换）→ Launch → 轮询 Probe(RollbackPort) → 活=Success+RolledBack
//     （F2 定案 A：还原「上次健康运行配置」=守护启动成功点盖章的
//     last-healthy.toml，daemon 侧 stampLastHealthyConfig）；
//   - 死/无回滚源/Restore 失败 → Alert("Ferryman 安全重启失败", 人工指引)+
//     Result{Success:false}。每阶段 logf 一行。
//
// 缝纪律（对齐 supervisor）：全部 var/字段形注入；update 包不 import
// config/notify/daemon——Alert/Logf 由 cmd 侧装配（Alert 缺省 nil=只 Logf）。
// Logf 缺省追加 <DataDir>/restart.log（fileTeeLogf 同款独立文件；帮手是
// detached 后台进程，stdout 无人看，但保留 fmt.Print 便于手工前台跑）。
// 端口/路径全部显式传入（CLI 解析），零值默认在 RunRestart 开头补齐。

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// RestartOpts 帮手编排装配面：路径/端口/时限/四缝全注入——测试世界零生产
// 面触碰（StopOld/Launch/Probe/Restore/Alert/Logf 缺省即真实现）。
type RestartOpts struct {
	DataDir, ConfigPath, StartCmd, LastHealthy, TargetExe string
	FromPort, ToPort, RollbackPort int // RollbackPort=0 或 LastHealthy 空 = 无回滚源
	GraceBeforeStop, PollTimeout, PollInterval time.Duration // 缺省 1s / 90s / 1s
	StopOld func() error        // 缺省: NewSupervisor(Config{DataDir,Port:FromPort,StartCmd,Logf}) 的 sup.stopDaemon(TargetExe,"重启停旧")
	Launch  func() error        // 缺省: sup.launch(StartCmd)（不经 launchTx——verifyLaunch 探 FromPort,换端口场景会误报）
	Probe   func(port int) bool // 缺省: GET http://127.0.0.1:port/stats 任意 HTTP 应答(含 401)=活,3s 超时
	Restore func() error        // 缺省: copyFile(LastHealthy, ConfigPath+".restart-restore") + moveFileReplace(tmp, ConfigPath)
	Alert   func(title, msg string) // 缺省 nil=只 Logf
	Logf    func(string, ...any)    // 缺省: 追加 <DataDir>/restart.log
}

// RestartResult 帮手结论（CLI stdout 与告警正文的单源）。
type RestartResult struct {
	Success, RolledBack bool
	Err                 error
	Detail              string
}

// RunRestart 安全重启序列。返回 Result；过程日志走 Logf（每阶段一行）。
func RunRestart(o RestartOpts) RestartResult {
	// 零值默认补齐（对齐 NewSupervisor 缺省面）。
	if o.GraceBeforeStop == 0 {
		o.GraceBeforeStop = time.Second
	}
	if o.PollTimeout == 0 {
		o.PollTimeout = defaultPollWait // 90s
	}
	if o.PollInterval == 0 {
		o.PollInterval = defaultPollEvery // 1s
	}
	if o.DataDir == "" {
		if home, err := os.UserHomeDir(); err == nil {
			o.DataDir = filepath.Join(home, "ferryman")
		}
	}
	if o.StartCmd == "" && o.DataDir != "" {
		o.StartCmd = filepath.Join(o.DataDir, startCmdName)
	}
	if o.Logf == nil {
		o.Logf = restartFileLogf(o.DataDir)
	}
	// 停旧/拉起缺省走监督者平台面（stopDaemon 的身份校验兜底 kill 与 launch
	// 的 cmd.exe 隐藏拉起都复用 supervisor，不另立进程知识）。
	sup := NewSupervisor(Config{DataDir: o.DataDir, Port: o.FromPort, StartCmd: o.StartCmd, Logf: o.Logf})
	if o.StopOld == nil {
		o.StopOld = func() error { return sup.stopDaemon(o.TargetExe, "重启停旧") }
	}
	if o.Launch == nil {
		o.Launch = func() error { return sup.launch(o.StartCmd) }
	}
	if o.Probe == nil {
		o.Probe = restartProbeAny
	}
	if o.Restore == nil {
		o.Restore = func() error {
			tmp := o.ConfigPath + ".restart-restore"
			if err := copyFile(o.LastHealthy, tmp); err != nil {
				return err
			}
			return moveFileReplace(tmp, o.ConfigPath)
		}
	}

	o.Logf("安全重启: 停旧口 %d → 新口 %d（回滚口 %d，回滚源 %q）", o.FromPort, o.ToPort, o.RollbackPort, o.LastHealthy)
	time.Sleep(o.GraceBeforeStop) // 端点响应出网余量
	o.Logf("grace %v 已过，开始停旧（端口 %d）", o.GraceBeforeStop, o.FromPort)
	if err := o.StopOld(); err != nil {
		o.Logf("停旧失败: %v（配置未动，如实退出）", err)
		return RestartResult{Err: fmt.Errorf("停旧失败: %w", err),
			Detail: "停旧失败，配置未动；旧守护状态见 restart.log"}
	}
	o.Logf("旧守护已停，拉起 %s", o.StartCmd)
	if err := o.Launch(); err != nil {
		o.Logf("拉起报错: %v（以端口轮询裁决）", err) // supervisor.launchAndVerify 同纪律：拉起报错不早退
	}
	if restartProbeUntil(o, o.ToPort) {
		o.Logf("新守护已在端口 %d 应答——重启成功", o.ToPort)
		return RestartResult{Success: true, Detail: fmt.Sprintf("新守护已在端口 %d 应答", o.ToPort)}
	}
	o.Logf("新口 %d 于 %v 内无应答", o.ToPort, o.PollTimeout)
	if o.RollbackPort == 0 || o.LastHealthy == "" {
		return o.failRestart(fmt.Sprintf("新守护于端口 %d 无应答，且无有效回滚源", o.ToPort),
			fmt.Errorf("新守护于端口 %d 无应答（等满 %v），且无有效回滚源", o.ToPort, o.PollTimeout),
			"无有效回滚源")
	}
	o.Logf("健康失败——还原上次健康配置 %s 后重拉", o.LastHealthy)
	if err := o.Restore(); err != nil {
		o.Logf("还原上次健康配置失败: %v", err)
		return o.failRestart("新守护无应答，还原上次健康配置亦失败",
			fmt.Errorf("还原上次健康配置失败: %w", err), "无有效回滚源")
	}
	if err := o.Launch(); err != nil {
		o.Logf("回滚拉起报错: %v（以端口轮询裁决）", err)
	}
	if restartProbeUntil(o, o.RollbackPort) {
		o.Logf("回滚守护已在端口 %d 应答——重启以回滚形态成功", o.RollbackPort)
		return RestartResult{Success: true, RolledBack: true,
			Detail: fmt.Sprintf("新守护（端口 %d）未活，已还原上次健康配置并在端口 %d 恢复服务", o.ToPort, o.RollbackPort)}
	}
	return o.failRestart(fmt.Sprintf("已还原上次健康配置，但回滚口 %d 仍无应答", o.RollbackPort),
		fmt.Errorf("回滚后端口 %d 于 %v 内无应答", o.RollbackPort, o.PollTimeout),
		"已还原上次健康配置")
}

// failRestart 终局失败收口：完整人话告警落 Logf +（缝在位时）Alert 推送；
// restoredNote 为人工指引里的回滚态说明（已还原/无有效回滚源）。
func (o RestartOpts) failRestart(detail string, err error, restoredNote string) RestartResult {
	msg := "Ferryman 安全重启失败：" + detail + "。" + restoredNote +
		"；可手动运行 " + o.StartCmd + " 拉起；详见 " + filepath.Join(o.DataDir, "restart.log")
	o.Logf("%s", msg)
	if o.Alert != nil {
		defer func() {
			if r := recover(); r != nil {
				o.Logf("告警通道故障(忽略): %v", r) // notify 旁路纪律：通道故障绝不影响编排
			}
		}()
		o.Alert("Ferryman 安全重启失败", msg)
	}
	return RestartResult{Err: err, Detail: detail}
}

// restartProbeUntil 轮询活探针至 PollTimeout；活=true 即返。
func restartProbeUntil(o RestartOpts, port int) bool {
	deadline := time.Now().Add(o.PollTimeout)
	for {
		if o.Probe(port) {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(o.PollInterval)
	}
}

// restartProbeAny 端口活性探针缺省：GET /stats 任意 HTTP 应答（含 401/404）=
// 有守护在听（supervisor.probeDaemonAny 同语义——守护起来即应答，鉴权与路由
// 不构成前提）；网络错/超时=无应答。3s 超时（本机环回，拖长无意义）。
func restartProbeAny(port int) bool {
	resp, err := (&http.Client{Timeout: 3 * time.Second}).
		Get(fmt.Sprintf("http://127.0.0.1:%d/stats", port))
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20)) // 排水礼节
	return true
}

// restartFileLogf 缺省日志：stdout + 追加 <DataDir>/restart.log（fileTeeLogf
// 同款逐行开合——帮手日志量寥寥，不值得留常开句柄）。stdout 保留：用户手敲
// `ferryman restart` 前台跑时看得见；detached 帮手形态下无人看亦无害。
func restartFileLogf(dataDir string) func(format string, args ...any) {
	var mu sync.Mutex
	return func(format string, args ...any) {
		msg := fmt.Sprintf("[ferryman-restart] "+format+"\n", args...)
		mu.Lock()
		defer mu.Unlock()
		fmt.Print(msg)
		if dataDir == "" {
			return
		}
		if f, err := os.OpenFile(filepath.Join(dataDir, "restart.log"),
			os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); err == nil {
			_, _ = f.WriteString(time.Now().Format("2006-01-02 15:04:05 ") + msg)
			_ = f.Close()
		}
	}
}
