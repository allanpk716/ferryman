package update

// 升级监督者状态机(票05,规格 §C 全序列):换装目标解析(seam E)→ 锁
// (seam B)→ journal → staging(票03 下载+SHA256)→ 静默门(票02 W1:等流量
// 空闲才动手)→ 停旧(/shutdown 票04 + 身份校验兜底 kill)→ swap(seam A 单次
// 原子替换)→ 拉起+校验(seam C 看门抢跑复停重拉)→ 回滚 → 崩溃恢复。CLI
// `ferryman update` 与内部旗标 --supervise 收敛到同一 Run(规格 §C 统一监督者)。

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// 监督者缺省参数。defaultPortWait 240s：v0.2.4 起守护优雅关停先排水（在途流
// 最长 [dock].drain_timeout_s=180s，期间渡口 15722 仍被旧进程占着、进程未退），
// 停旧预算必须覆盖排水窗+余量——2026-09-29 复盘（docs/20260929_守护重启事故
// 复盘.md）：30s 会让监督者在排水中抢跑换装，新守护渡口绑定失败进半死形态。
const (
	// DefaultDaemonPort 守护口(与 installer.DefaultDaemonPort 同值;不 import
	// installer,避免 update→installer 的面拉宽)。2026-09-29 由 7311 改 15700。
	DefaultDaemonPort = 15700
	defaultPortWait   = 240 * time.Second
	defaultPollWait   = 90 * time.Second
	defaultPollEvery  = 1 * time.Second
	// 拉起验证探针窗(W2/spec Implementation Decisions 3):launch 后 2s 起
	// 首测(刚 spawn 的守护尚在引导,立刻拨只是白敲连接拒绝),10s(自拉起
	// 计)截止。
	defaultProbeDelay   = 2 * time.Second
	defaultProbeTimeout = 10 * time.Second
	// SupervisorLaunchEnv 监督者自拉起标记(票04 集成缺陷修):监督者事务拉起
	// (launchTxCmdImpl)给子进程注入本环境变量,daemon 侧让路判定见此标记即
	// 豁免——否则监督者全程持锁,它自己拉起的新守护会按票04让路退出,升级
	// 必败回滚(泳道04 评审发现;W4 彩排台真两版演练本会炸出)。
	SupervisorLaunchEnv = "FERRYMAN_LAUNCHED_BY_SUPERVISOR"
	// startCmdName 点火脚本名(installer.LauncherName 同名同位)。
	startCmdName = "start-daemon.cmd"
	// 静默门参数(W1/spec Implementation Decisions 2):判据 = 在途 0 且距最后
	// 请求 ≥ defaultQuietRequired(last_request_ts==0 视为静默成立);判据不满足
	// 默认轮询等待 defaultQuietWait,每 quietLogEvery 记一行进度日志。
	defaultQuietRequired = 10 * time.Second
	defaultQuietWait     = 60 * time.Second
	quietLogEvery        = 10 * time.Second
)

// 静默门交互三选词表(askQuiet/Config.QuietAsk 的返回契约;"" = 非交互/
// 无法判定/无效输入 → 调用方告警后硬切兜底)。
const (
	quietChoiceWait   = "wait"   // 继续等一个预算窗再判
	quietChoiceSwitch = "switch" // 现在切换(硬切)
	quietChoiceAbort  = "abort"  // 放弃本次升级
)

// Config 监督者装配面:全部路径/口/时限可注入——测试世界零生产面触碰。
type Config struct {
	DataDir      string // ~/ferryman:锁/journal/token/pid;空 = 回落 ~/ferryman
	Port         int    // 守护口;0 = 15700
	Endpoints    Endpoints
	Current      string                      // 当前版本(main.version)
	Spec         string                      // 显式目标版本(可空;含降级)
	Prerelease   bool                        // 纳入预发布
	StartCmd     string                      // 点火脚本;空 = <DataDir>/start-daemon.cmd
	HTTP         *http.Client                // 守护面客户端;空 = 3s 超时内建
	PortWait     time.Duration               // 停旧等端口释放+进程退场上限;0 = 240s（覆盖 v0.2.4+ 排水窗）
	PollTimeout  time.Duration               // 拉起校验轮询上限;0 = 90s
	PollInterval time.Duration               // 轮询间隔;0 = 1s
	ProbeDelay   time.Duration               // 拉起验证探针首测延迟(自拉起计);0 = 2s
	ProbeTimeout time.Duration               // 拉起验证探针截止(自拉起计);0 = 10s
	Alert func(title, message string) // 事务告警通道(cmd 侧装配 notify.NotifyEvent(EventUpgrade));nil = 只落 Logf
	// AlertHardCut 静默门硬切兜底告警通道(票03 通知分级:硬切是独立事件
	// hard_cut,与事务告警 upgrade 的三值开关分列——拆两条缝而非给 Alert 加
	// 事件参数,update 包不 import notify、不感知事件名,cmd 装配侧各归各事件;
	// cmd 侧装配 notify.NotifyEvent(EventHardCut));nil = 只落 Logf。
	AlertHardCut func(title, message string)
	// WaitQuiet 静默门等待预算(判据不满足时轮询等待的上限):0 = 缺省 60s;
	// 负值 = 不等待(CLI --wait-quiet=0 的映射哨兵:判据不满足立即进兜底)。
	WaitQuiet time.Duration
	// Force 跳过静默门直接停旧(CLI --force 脚本态)。
	Force bool
	// QuietAsk 静默门交互三选问询注入缝:参数为情境提示与三选项文本,返回
	// quietChoiceWait/quietChoiceSwitch/quietChoiceAbort 之一;返回 ""(或 nil
	// 缝下缺省实现判定非交互)= 告警后硬切。缺省实现看 stdin 是否字符设备。
	QuietAsk func(prompt string, options []string) string
	Logf     func(format string, args ...any)
}

// Result 升级结论(seam F 结果通知与 CLI stdout 的单源)。v0.5.2(票02)删
// 自中继副本机制起,Relayed 语义退役——`ferryman update` 恒同步跑完全程。
type Result struct {
	Success     bool
	From, To    string
	RolledBack  bool   // 失败但已回滚恢复旧版服务
	RollbackErr string // 回滚也失败(服务可能中断,需人工介入)
	Err         error
}

// Supervisor 监督者。proc* 为进程面 seam(测试注桩);launch 为拉起 seam
// (缺省注入 txLauncher 的事务形态——W2 起固定 cmd.exe 直拉,事务调用点经
// launchTx 附带拉起验证探针;测试可换桩)。
// v0.5.2(票02)删自中继副本机制:selfExe/spawnRelay/selfDelete 三 seam 随
// selfRelayIfNeeded/relaySelfDelete 一并退场(自身映像==换装目标时直接跑
// 两步换装——改名让位对运行映像放行,单监督者全程无副本)。
type Supervisor struct {
	cfg         Config
	procAlive   func(pid int) bool
	procImage   func(pid int) (string, error)
	killPID     func(pid int) error
	launch      func(cmdPath string) error
}

// txLauncher 事务拉起缺省注点(平台面注入):Windows 侧由 proc_windows.go 的
// init 固定为 launchTxCmdImpl(cmd.exe /c 直拉 + CREATE_NO_WINDOW——W2 弃
// wscript/VBS,ADR-0015 2026-09-29 补记);非 Windows 世界无该文件,恒 nil,
// 回落 launchCmdImpl 的新会话直执行(unix 上本无 wscript/cmd 通道之分,
// 直执行即直连形态)。
var txLauncher func(cmdPath string) error

// NewSupervisor 装配(平台真实现见 proc_*.go / swap_*.go)。
func NewSupervisor(cfg Config) *Supervisor {
	if cfg.DataDir == "" {
		if home, err := os.UserHomeDir(); err == nil {
			cfg.DataDir = filepath.Join(home, "ferryman")
		}
	}
	if cfg.Port == 0 {
		cfg.Port = DefaultDaemonPort
	}
	if cfg.StartCmd == "" && cfg.DataDir != "" {
		cfg.StartCmd = filepath.Join(cfg.DataDir, startCmdName)
	}
	if cfg.PortWait == 0 {
		cfg.PortWait = defaultPortWait
	}
	if cfg.PollTimeout == 0 {
		cfg.PollTimeout = defaultPollWait
	}
	if cfg.PollInterval == 0 {
		cfg.PollInterval = defaultPollEvery
	}
	if cfg.ProbeDelay == 0 {
		cfg.ProbeDelay = defaultProbeDelay
	}
	if cfg.ProbeTimeout == 0 {
		cfg.ProbeTimeout = defaultProbeTimeout
	}
	if cfg.WaitQuiet == 0 {
		cfg.WaitQuiet = defaultQuietWait // 负值 = 不等待,保留原样
	}
	if cfg.Logf == nil {
		cfg.Logf = fileTeeLogf(cfg.DataDir)
	}
	launchFn := launchCmdImpl
	if txLauncher != nil {
		launchFn = txLauncher
	}
	return &Supervisor{
		cfg:        cfg,
		procAlive:  procAliveImpl,
		procImage:  procImageImpl,
		killPID:    killImpl,
		launch:     launchFn,
	}
}

func (s *Supervisor) logf(format string, a ...any) { s.cfg.Logf(format, a...) }

// fileTeeLogf 缺省日志:stdout + 追加 <DataDir>/update.log。落盘是留证——
// 托盘/脚本 detached 派生场景 stdout 无人看,而部分失败路径清 journal 即失现场
// (2026-09-22 v0.1.4 失败即因此无痕,只剩备份文件的时间戳可推)。逐行开合
// (升级全程日志量寥寥,不值得留常开句柄——还挡测试 TempDir 清理)。
func fileTeeLogf(dataDir string) func(format string, a ...any) {
	var mu sync.Mutex
	return func(format string, a ...any) {
		msg := fmt.Sprintf("[ferryman-update] "+format+"\n", a...)
		mu.Lock()
		defer mu.Unlock()
		fmt.Print(msg)
		if dataDir == "" {
			return
		}
		if f, err := os.OpenFile(filepath.Join(dataDir, "update.log"),
			os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); err == nil {
			_, _ = f.WriteString(time.Now().Format("2006-01-02 15:04:05 ") + msg)
			_ = f.Close()
		}
	}
}

// Run 监督者主序列。返回 Result;过程日志走 Logf。
// 自身映像 == 换装目标时直接进入主序列——两步换装(改名让位)对运行映像放行,
// 监督者自己的镜像被改名成 .old-<版本> 备份即设计保留件;并发第二监督者的
// 防线只剩 update.lock 的两族持活判据(lock.go holderAlive)。
func (s *Supervisor) Run() Result {
	// 0. 换装目标解析(seam E)
	targetExe, err := s.resolveTargetExe()
	if err != nil {
		return Result{Err: err}
	}
	exeDir := filepath.Dir(targetExe)

	// 1. 锁(seam B:持有者活 = PID 活且映像∈两族{目标, .old-* 备份族})
	lk, err := acquireUpdateLock(s.cfg.DataDir, targetExe, s.procAlive, s.procImage, s.logf)
	if err != nil {
		return Result{Err: err}
	}
	defer lk.release()

	// 2. 目标版本解析(票03)
	tgt, err := ResolveTarget(s.cfg.Endpoints, s.cfg.Spec, s.cfg.Prerelease)
	if err != nil {
		return Result{Err: err}
	}
	res := Result{From: s.cfg.Current, To: tgt.Tag}
	s.logf("升级 %s → %s(换装目标 %s)", s.cfg.Current, tgt.Tag, targetExe)

	// 3. 崩溃恢复(journal;锁后进行——活监督者持锁时无人动它的账)
	recovered, rerr := s.recoverJournal(targetExe)
	if rerr != nil {
		res.Err = rerr
		return res
	}
	if recovered != "" && recovered == tgt.Tag {
		s.logf("上次升级 %s 已生效(账目已清),本次无事可做", recovered)
		res.Success = true
		return res
	}

	j := journal{Phase: PhaseStaging, From: s.cfg.Current, To: tgt.Tag,
		TargetExe: targetExe, NewExe: newExePath(targetExe),
		Backup: backupPath(exeDir, s.cfg.Current), StartCmd: s.cfg.StartCmd}
	// 一切退出路径清 .new/.part/.swap-tmp 残留(成功路径 .new 已被换装消费)
	defer cleanSwapResidues(exeDir)

	// 4. staging:先写后动(票03 下载 + SHA256;失败 = 拒绝替换现场原样)
	if err := saveJournal(s.cfg.DataDir, j); err != nil {
		res.Err = err
		return res
	}
	if sha, err := s.cfg.Endpoints.FetchSHA256(tgt.ShaURL); err != nil {
		_ = clearJournal(s.cfg.DataDir)
		res.Err = err
		return res
	} else if err := DownloadToFile(s.cfg.Endpoints.client(), tgt.ExeURL, sha, j.NewExe); err != nil {
		_ = clearJournal(s.cfg.DataDir)
		res.Err = err
		return res
	}

	// 5. 静默门(票02/W1):staging 完成后、停旧前——下载完新 exe 再等静默,
	// 等待期不占下载时间;门通过(或兜底放行)后立即进停旧(postShutdown 紧随,
	// 门与 shutdown 之间不插其他等待)。
	if err := s.quietGate(); err != nil {
		_ = clearJournal(s.cfg.DataDir)
		res.Err = err
		return res
	}

	// 6. 停旧(票04 /shutdown;端点不可达且守护在跑 → 身份校验兜底 kill)
	if err := s.stopDaemon(j.TargetExe, "停旧"); err != nil {
		_ = clearJournal(s.cfg.DataDir)
		res.Err = fmt.Errorf("停旧失败: %w", err)
		return res
	}

	// 7. swap(seam A:备份 → 单次原子替换;备份只留 2 份)
	j.Phase = PhaseSwap
	if err := saveJournal(s.cfg.DataDir, j); err != nil {
		res.Err = err
		return res
	}
	if err := s.swapFiles(j); err != nil {
		_ = clearJournal(s.cfg.DataDir)
		res.Err = err
		// 正常失败现场盘上仍是旧版(让位未成或已复原)——监督者自己拉起
		// 恢复服务;极端双败(目标缺位)swapFiles 已报警,人工介入。
		s.restoreServiceQuiet(j)
		return res
	}

	// 8. 拉起 + 校验(seam C:失败先查持有者,旧版抢跑 → 复停重拉再校验一轮)
	j.Phase = PhaseVerify
	if err := saveJournal(s.cfg.DataDir, j); err != nil {
		res.Err = err
		return res
	}
	seen, ok := s.launchAndVerify(j, tgt.Tag)
	if ok {
		_ = clearJournal(s.cfg.DataDir)
		res.Success = true
		s.logf("升级完成: %s → %s(备份 %s)", j.From, j.To, j.Backup)
		return res
	}

	// 9. 回滚
	return s.rollback(j, seen)
}

// resolveTargetExe seam E:点火脚本引号 exe 优先;失败回落本进程映像。
func (s *Supervisor) resolveTargetExe() (string, error) {
	if p, err := ParseStartDaemonExe(s.cfg.StartCmd); err == nil {
		s.logf("换装目标(点火脚本): %s", p)
		return p, nil
	} else {
		s.logf("点火脚本解析失败(%v)——换装目标回落本进程映像", err)
	}
	p, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("换装目标不可定: %w", err)
	}
	s.logf("换装目标(本进程映像): %s", p)
	return p, nil
}

// swapFiles seam A:两步换装(改名让位)。ADR-0010 的前提「停旧后无运行锁」
// 被同映像多进程击穿——agent 面 mcp 实例(ADR-0009,用户级注册,CC 会话常驻
// 拉起)以同一 exe 为映像,停旧守护管不到它们;Windows 对运行中映像拒绝
// 覆盖(MOVEFILE_REPLACE_EXISTING 必 Access denied——2026-09-22 v0.1.4
// 生产实测,备份已拷、替换被拒、升级必败),但允许改名(同卷;运行映像可
// rename 是 Windows 固有语义)。故:① 旧 exe 改名到备份位(让位即备份,
// 比特相同免拷贝;REPLACE 覆盖同号陈旧备份——陈旧备份非运行映像);② 新
// exe 落位原名。① 若被拒且备份位在场,多为先前换装已把存活让位者改到该
// 名上(同 from 版本第二次升级必撞——2026-09-22 v0.2.0 部署两次实测),
// 先把占据者挪到 old-* 域内 stale 名(改名活映像恒可行)再重试。两步之间
// 存在毫秒级缺位窗口(ADR-0010 曾以此否决双 rename,但单次替换在 mcp 共
// 存下永不可行,权衡翻转——见 ADR-0015):看门/自举在窗口内拉空只会失败,
// 下一轮重试即恢复。② 败则把备份改回原名尽力复原,复原也败时如实报警
// (目标缺位,人工介入)。
func (s *Supervisor) swapFiles(j journal) error {
	if err := moveFileReplace(j.TargetExe, j.Backup); err != nil {
		if !fileExists(j.Backup) {
			return fmt.Errorf("旧版让位失败(改名到备份位): %w", err)
		}
		aside := staleAsidePath(j.Backup)
		if rerr := os.Rename(j.Backup, aside); rerr != nil {
			return fmt.Errorf("旧版让位失败(改名到备份位): %w(备份位被占,挪窝亦败: %v)", err, rerr)
		}
		s.logf("备份位被先前让位者占据——已挪到 %s,重试让位", filepath.Base(aside))
		if rerr := moveFileReplace(j.TargetExe, j.Backup); rerr != nil {
			return fmt.Errorf("旧版让位失败(改名到备份位;占据者已挪 %s): %w", aside, rerr)
		}
	}
	if err := moveFileReplace(j.NewExe, j.TargetExe); err != nil {
		if rbErr := moveFileReplace(j.Backup, j.TargetExe); rbErr != nil {
			s.logf("警示:两步换装落位与复原均败——目标缺位!备份滞留 %s,请人工介入(落位 %v / 复原 %v)",
				j.Backup, err, rbErr)
		}
		return fmt.Errorf("新版落位失败: %w", err)
	}
	pruneBackups(filepath.Dir(j.TargetExe), 2)
	s.logf("换装完成(改名让位): %s ← %s(备份 %s)", j.TargetExe, filepath.Base(j.NewExe), j.Backup)
	return nil
}

// restoreServiceQuiet swap 失败后恢复服务:盘上仍是旧版,拉起它(已有服务
// 则不动)。尽力而为——失败只记日志,看门/自举三件套仍是兜底。
func (s *Supervisor) restoreServiceQuiet(j journal) {
	if _, ok := s.queryVersion(); ok {
		return
	}
	if err := s.launchTx(j.StartCmd); err != nil {
		s.logf("swap 失败后拉起旧版失败(看门/自举会兜底): %v", err)
		return
	}
	if v, ok := s.pollAnyVersion(s.cfg.PollTimeout); ok {
		s.logf("swap 失败后旧版 %s 已恢复服务", v)
	}
}

// launchTx 事务拉起(launchAndVerify/rollback/restoreServiceQuiet 同缝):经
// launch 缺省注入(launchTxCmdImpl,固定 cmd.exe /c + CREATE_NO_WINDOW——W2
// 起不再探测 wscript/VBS)拉起,随后做拉起验证活性探针。launch 本体失败照旧
// 返回错误(探针无意义);探针结论只报告不裁决——launchTx 不因探针失败返回
// 错误,事务走向仍由既有 PollTimeout(90s)版本校验裁决。探针是相位可见性,
// 不是第二裁判。
func (s *Supervisor) launchTx(cmdPath string) error {
	if err := s.launch(cmdPath); err != nil {
		return err
	}
	s.verifyLaunch()
	return nil
}

// verifyLaunch 拉起验证(W2/ADR-0015 2026-09-29 补记):launch 后 ProbeDelay
// (缺省 2s)起对本事务配置的管理端点(cfg.Port 经 daemonAddr 拼接——不硬
// 编码 15700)做活性探针,成功谓词=任意 HTTP 应答(含 401/404,probeDaemonAny
// 同语义:守护起来即应答,鉴权与路由不构成前提),重试至 ProbeTimeout(缺省
// 10s,自拉起计)截止。端点始终无应答(含拉起进程秒退——口永远不会响,cmd.exe
// 转手下无法跟踪守护 PID,以端点静默为准)→ 报「拉起验证失败（相位错误）」+
// 告警;但只报告,不触发回滚——回滚仍由既有版本校验裁决。动机(07:31 事故):
// VBS 三级转手吞 stderr 且成败不验,拉起静默失败无处追。
func (s *Supervisor) verifyLaunch() {
	time.Sleep(s.cfg.ProbeDelay) // 首测自 2s 起:引导期内拨号只是白敲
	deadline := time.Now().Add(s.cfg.ProbeTimeout - s.cfg.ProbeDelay)
	for {
		if s.probeDaemonAny() {
			s.logf("拉起验证通过: 端点 %s 已应答", s.daemonAddr())
			return
		}
		if !time.Now().Before(deadline) {
			msg := fmt.Sprintf("拉起验证失败（相位错误）: 端点 %s 于 %v 内无应答——"+
				"拉起进程可能已退出或未监听;事务继续,以版本校验为准",
				s.daemonAddr(), s.cfg.ProbeTimeout)
			s.logf("%s", msg)
			s.alert("Ferryman 升级", msg)
			return
		}
		time.Sleep(s.cfg.PollInterval)
	}
}

// alert 事务告警注入缝(cmd 侧装配 notify.NotifyEvent(EventUpgrade);nil = 只落
// Logf,update 包不 import notify,依赖面不拉宽)。尽力而为的旁路(notify 包同
// 纪律):通道任何故障 recover 吞掉只记日志,绝不影响事务走向。
func (s *Supervisor) alert(title, message string) {
	if s.cfg.Alert == nil {
		return // 未接通道:正文已由调用方落 Logf/update.log,不重复
	}
	defer func() {
		if r := recover(); r != nil {
			s.logf("告警通道故障(忽略): %v", r)
		}
	}()
	s.cfg.Alert(title, message)
}

// alertHardCut 静默门硬切兜底告警缝(票03:独立于事务告警的 hard_cut 事件缝,
// 见 Config.AlertHardCut 注)。nil = 只落 Logf(正文已由调用方落 Logf)。
func (s *Supervisor) alertHardCut(title, message string) {
	if s.cfg.AlertHardCut == nil {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			s.logf("告警通道故障(忽略): %v", r)
		}
	}()
	s.cfg.AlertHardCut(title, message)
}

// launchAndVerify 拉起并校验 want 版本;seam C:失败判定前先查 15700 持有者,
// 旧版本(看门/自举抢跑重拉)→ 复停一次 → 重拉起 → 重校验一轮。
func (s *Supervisor) launchAndVerify(j journal, want string) (string, bool) {
	if err := s.launchTx(j.StartCmd); err != nil {
		s.logf("拉起失败: %v", err)
	}
	seen, ok := s.pollVersionMatch(want, s.cfg.PollTimeout)
	if ok {
		return want, true
	}
	if holder, held := s.queryVersion(); held && holder != "" && holder != want {
		s.logf("校验未过:%s 持有者为旧版 %s(看门/自举抢跑)——复停重拉再校验一轮",
			s.daemonAddr(), holder)
		if err := s.stopDaemon(j.TargetExe, "复停"); err != nil {
			s.logf("复停失败: %v", err)
			return nonEmpty(seen, holder), false
		}
		if err := s.launchTx(j.StartCmd); err != nil {
			s.logf("重拉起失败: %v", err)
		}
		seen2, ok2 := s.pollVersionMatch(want, s.cfg.PollTimeout)
		if ok2 {
			return want, true
		}
		return nonEmpty(seen2, seen, holder), false
	}
	return seen, false
}

// rollback 规格 §C 第8条:杀新进程(身份校验同停旧)→ copy 恢复备份 →
// 重拉旧版并校验 → 报错退出。回滚不成功时 journal 保留(下次启动再恢复)。
func (s *Supervisor) rollback(j journal, seen string) Result {
	res := Result{From: j.From, To: j.To,
		Err: fmt.Errorf("新版 %s 校验失败(最后所见版本 %q)", j.To, seen)}
	if err := s.stopDaemon(j.TargetExe, "回滚停新"); err != nil {
		res.RollbackErr = "停新守护失败: " + err.Error()
		s.logf("回滚:%s(继续尝试恢复文件)", res.RollbackErr)
	}
	if err := copyFile(j.Backup, j.TargetExe); err != nil {
		if res.RollbackErr == "" {
			res.RollbackErr = "恢复备份失败: " + err.Error()
		}
		s.logf("回滚:%v(现场保持,请人工检查)", err)
		return res
	}
	if err := s.launchTx(j.StartCmd); err != nil {
		s.logf("回滚拉起失败: %v", err)
	}
	v, ok := s.pollVersionMatch(j.From, s.cfg.PollTimeout)
	if !ok {
		res.RollbackErr = fmt.Sprintf("回滚后旧版 %s 校验失败(所见 %q)", j.From, v)
		s.logf("%s", res.RollbackErr)
		return res
	}
	_ = clearJournal(s.cfg.DataDir)
	res.RolledBack = true
	s.logf("已回滚到 %s 并恢复服务(备份保留: %s)", j.From, j.Backup)
	return res
}

// recoverJournal 崩溃恢复(规格 §C 第9条):任何 update 启动先读 journal。
// 返回「上次升级实际已落地的版本」(非空且 == 本次目标 → 短路无事可做)。
func (s *Supervisor) recoverJournal(targetExe string) (string, error) {
	j, exists, err := loadJournal(s.cfg.DataDir)
	if err != nil {
		// 半写坏账:视作 staging 残留清理后续跑
		s.logf("journal 不可读(%v)——按 staging 残留清理", err)
		s.cleanStagingResidue(journal{}, targetExe)
		return "", nil
	}
	if !exists {
		return "", nil
	}
	switch j.Phase {
	case PhaseStaging:
		s.logf("检出 staging 中断残留——清理后续跑")
		s.cleanStagingResidue(j, targetExe)
		return "", nil
	case PhaseSwap, PhaseVerify:
		return s.recoverPostSwap(j, targetExe)
	default:
		s.logf("journal 阶段未知(%q)——按 staging 残留清理", j.Phase)
		s.cleanStagingResidue(j, targetExe)
		return "", nil
	}
}

// cleanStagingResidue staging 清残留:journal 记录目录 + 当前目标目录的
// .new/.part/.swap-tmp(挪窝场景两处都清),清账续跑。
func (s *Supervisor) cleanStagingResidue(j journal, targetExe string) {
	dirs := map[string]bool{filepath.Dir(targetExe): true}
	if j.TargetExe != "" {
		dirs[filepath.Dir(j.TargetExe)] = true
	}
	for d := range dirs {
		cleanSwapResidues(d)
	}
	_ = clearJournal(s.cfg.DataDir)
}

// recoverPostSwap swap/verify 中断:核对当前 exe 与运行版本——健康清账,
// 不健康按备份回滚。
func (s *Supervisor) recoverPostSwap(j journal, targetExe string) (string, error) {
	s.logf("检出 %s 中断——核对当前运行版本", j.Phase)
	if v, ok := s.queryVersion(); ok {
		switch v {
		case j.To: // 上次升级其实已完成:清账,本次短路
			s.logf("运行版本已是目标 %s——清账", v)
			cleanSwapResidues(filepath.Dir(targetExe))
			_ = clearJournal(s.cfg.DataDir)
			return j.To, nil
		case j.From: // swap 从未发生(旧版照常服务):健康,清账续跑
			s.logf("运行版本仍是旧版 %s(swap 未发生)——清账续跑", v)
			cleanSwapResidues(filepath.Dir(targetExe))
			_ = clearJournal(s.cfg.DataDir)
			return "", nil
		default:
			return "", fmt.Errorf("中断恢复:端口持有者版本 %q 既非旧版 %s 也非目标 %s,请人工确认",
				v, j.From, j.To)
		}
	}
	// 无服务:swap 可能已发生也可能没有——拉起盘上 exe,报什么版本就是什么
	if err := s.launch(j.StartCmd); err != nil {
		s.logf("恢复期拉起失败: %v(按备份回滚路径处理)", err)
	}
	v, ok := s.pollAnyVersion(s.cfg.PollTimeout)
	if ok {
		switch v {
		case j.To:
			s.logf("盘上已是新版 %s——上次升级实际完成,清账", v)
			cleanSwapResidues(filepath.Dir(targetExe))
			_ = clearJournal(s.cfg.DataDir)
			return j.To, nil
		case j.From:
			s.logf("盘上仍是旧版 %s——清账续跑", v)
			cleanSwapResidues(filepath.Dir(targetExe))
			_ = clearJournal(s.cfg.DataDir)
			return "", nil
		}
	}
	// 不健康(拉不起/陌生版本)→ 按备份回滚
	if j.Backup == "" || !fileExists(j.Backup) {
		return "", fmt.Errorf("升级中断于 %s 且无备份可回滚(人工检查 %s)", j.Phase, j.TargetExe)
	}
	s.logf("服务不健康——按备份 %s 回滚", j.Backup)
	rb := s.rollback(j, v)
	if rb.RolledBack {
		_ = clearJournal(s.cfg.DataDir)
		s.logf("中断现场已回滚到 %s,续跑本次升级", j.From)
		return "", nil
	}
	return "", fmt.Errorf("中断恢复回滚失败: %s(%v)", rb.RollbackErr, rb.Err)
}

// ---- 静默门(票02/W1,spec Implementation Decisions 2) ----

// quietGate 停旧前的静默门:升级动手前等流量空闲(没有在途请求、10 秒没新
// 请求)。门前置三分支——①已验证旧守护不存在(cfg.Port 端口探测空且
// daemon.pid 无活进程)→ 跳过门直接进既有停旧/拉新流程;②守护在但 /stats
// 不可达 → 直接进入非静默兜底路径(含交互问/告警/硬切,不干等无法观测的
// 静默窗);③可达 → 严格执行判据(dockQuiet)。判据不满足:轮询等待
// WaitQuiet(缺省 60s,每 10s 一行进度),到期交互终端在场则三选问询(继续等/
// 现在切/放弃),非交互 notify 告警一条后按既有语义硬切(D10:拒连窗与截断
// 如实记录进 update.log)。--force 跳过门直接停旧。
//
// 返回 nil = 放行(调用方立即停旧,门与 shutdown 之间无其他等待);非 nil =
// 放弃升级(调用方清账中止)。判据满足即放行,不额外等待。
func (s *Supervisor) quietGate() error {
	if s.cfg.Force {
		s.logf("静默门跳过(--force):直接停旧")
		return nil
	}
	if s.daemonAbsent() {
		s.logf("静默门跳过:旧守护不在场(端口空且 daemon.pid 无活进程),直接进停旧/拉新流程")
		return nil
	}
	inflight, lastTS, ok := s.fetchDockStats()
	if ok && dockQuiet(inflight, lastTS, time.Now()) {
		s.logf("静默门放行:在途 0/距最后请求 %s,判据成立", lastReqAge(lastTS))
		return nil
	}
	// 判据不满足(或 /stats 不可达):可达且预算为正 → 先轮询一个等待窗;
	// 不可达(分支②)或预算 ≤0(不等)→ 不干等,直接进非静默兜底。
	var waited time.Duration
	lastInflight, lastSeenTS := inflight, lastTS
	if ok && s.cfg.WaitQuiet > 0 {
		quiet, inf, ts, el := s.waitQuietWindow()
		waited += el
		lastInflight, lastSeenTS = inf, ts
		if quiet {
			s.logf("静默门放行:在途 0/距最后请求 %s,判据成立(等了 %v)", lastReqAge(ts), el.Round(time.Second))
			return nil
		}
	}
	return s.quietFallback(lastInflight, lastSeenTS, waited, ok)
}

// waitQuietWindow 轮询等待一个静默窗(预算 cfg.WaitQuiet,≤0 视为 0=只查
// 一次):判据满足即放行(quiet=true),到期仍不满足 quiet=false。返回窗内
// 末次观测的渡口统计与实际等待时长。每 quietLogEvery 记一行进度日志。
func (s *Supervisor) waitQuietWindow() (quiet bool, inflight int, lastTS int64, waited time.Duration) {
	start := time.Now()
	budget := s.cfg.WaitQuiet
	if budget < 0 {
		budget = 0
	}
	deadline := start.Add(budget)
	lastLog := start
	reported := false
	for {
		inf, ts, ok := s.fetchDockStats()
		if ok {
			inflight, lastTS = inf, ts
			if dockQuiet(inf, ts, time.Now()) {
				return true, inf, ts, time.Since(start)
			}
		}
		if !time.Now().Before(deadline) {
			return false, inflight, lastTS, time.Since(start)
		}
		if !reported || time.Since(lastLog) >= quietLogEvery {
			s.logf("静默门等待中(已等 %v/预算 %v):末次在途 %d/距最后请求 %s",
				time.Since(start).Round(time.Second), budget, inflight, lastReqAge(lastTS))
			lastLog = time.Now()
			reported = true
		}
		time.Sleep(s.cfg.PollInterval)
	}
}

// quietFallback 非静默兜底路径(W1/D10):交互终端在场 → 三选问询(继续等
// 一个预算窗/现在切/放弃);非交互或无法判定 → notify 告警一条(「静默门未
// 达成,硬切兜底」)后按既有语义直接走停旧。告警是尽力而为的旁路(alert 内
// 已护,失败不阻塞事务);硬切路径在 update.log 记一行留证。statsOK=false
// (门前置分支②,/stats 不可达)时本路径被直接进入、无等待窗。
func (s *Supervisor) quietFallback(lastInflight int, lastTS int64, waited time.Duration, statsOK bool) error {
	for {
		switch s.askQuiet(waited) {
		case quietChoiceWait:
			quiet, inf, ts, el := s.waitQuietWindow()
			waited += el
			lastInflight, lastTS = inf, ts
			if quiet {
				s.logf("静默门放行:在途 0/距最后请求 %s,判据成立(等了 %v)", lastReqAge(ts), el.Round(time.Second))
				return nil
			}
			continue // 仍不静默:再问一轮
		case quietChoiceSwitch:
			s.logf("静默门未达成——用户选择立即切换(硬切,已等 %v)", waited.Round(time.Second))
			return nil
		case quietChoiceAbort:
			return fmt.Errorf("静默门未达成,用户选择放弃升级(已等 %v)", waited.Round(time.Second))
		default: // 非交互/无法判定/无效输入 → 告警后硬切兜底
			s.logf("%s", hardCutLine(lastInflight, lastTS, waited, statsOK))
			s.alertHardCut("Ferryman 升级", "静默门未达成，硬切兜底（"+hardCutDetail(lastInflight, lastTS, statsOK)+"）")
			return nil
		}
	}
}

// hardCutLine 硬切兜底留证行(票02:硬切路径在 update.log 记一行——等待
// 时长与末次观测;statsOK=false 时注明 /stats 不可达)。
func hardCutLine(lastInflight int, lastTS int64, waited time.Duration, statsOK bool) string {
	return fmt.Sprintf("硬切兜底：门未达成（等待 %ds，%s）",
		int(waited.Seconds()), hardCutDetail(lastInflight, lastTS, statsOK))
}

// hardCutDetail 硬切情境明细(留证行与告警共用)。
func hardCutDetail(lastInflight int, lastTS int64, statsOK bool) string {
	if !statsOK {
		return "/stats 不可达"
	}
	return fmt.Sprintf("末次在途 %d/距最后请求 %s", lastInflight, lastReqAge(lastTS))
}

// lastReqAge 距最后请求的人话时长(last_request_ts==0 = 从未有请求)。
func lastReqAge(lastTS int64) string {
	if lastTS == 0 {
		return "从未有请求"
	}
	return fmt.Sprintf("%ds", int(time.Since(time.Unix(lastTS, 0)).Seconds()))
}

// dockQuiet 静默判据(W1):在途 = 0 且距最后请求 ≥ defaultQuietRequired;
// last_request_ts == 0(从未有请求)视为静默成立。
func dockQuiet(inflight int, lastTS int64, now time.Time) bool {
	if inflight != 0 {
		return false
	}
	if lastTS == 0 {
		return true
	}
	return now.Sub(time.Unix(lastTS, 0)) >= defaultQuietRequired
}

// daemonAbsent 门前置分支①的「已验证旧守护不存在」:cfg.Port 端口探测空
// (拨不通)且 daemon.pid 无活进程(文件缺失/坏值/PID 已死)。
func (s *Supervisor) daemonAbsent() bool {
	if !s.portIdle() {
		return false // 端口有人
	}
	if pid, err := s.readDaemonPID(); err == nil && s.procAlive(pid) {
		return false // pid 活着
	}
	return true
}

// portIdle 守护口探测:拨不通(拒绝/无监听)= 空。
func (s *Supervisor) portIdle() bool {
	conn, err := net.DialTimeout("tcp", s.daemonAddr(), 500*time.Millisecond)
	if err != nil {
		return true
	}
	_ = conn.Close()
	return false
}

// fetchDockStats GET /stats(Bearer)取渡口统计(票01 字段,dock_inflight/
// last_request_ts);任何失败(网络错/非 200/解码败)= 不可达(false → 门前置
// 分支②)。
func (s *Supervisor) fetchDockStats() (inflight int, lastTS int64, ok bool) {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet,
		"http://"+s.daemonAddr()+"/stats", nil)
	if err != nil {
		return 0, 0, false
	}
	req.Header.Set("Authorization", "Bearer "+s.daemonToken())
	resp, err := s.httpClient().Do(req)
	if err != nil {
		return 0, 0, false
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 读一次:先排水后解码会扑空
	if err != nil {
		return 0, 0, false
	}
	if resp.StatusCode != http.StatusOK {
		return 0, 0, false
	}
	var out struct {
		DockInflight  int   `json:"dock_inflight"`
		LastRequestTs int64 `json:"last_request_ts"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return 0, 0, false
	}
	return out.DockInflight, out.LastRequestTs, true
}

// askQuiet 三选问询:cfg.QuietAsk 注入缝优先(测试桩保可测);nil 走缺省
// 实现(stdin 字符设备判定,非交互返回 "" → 调用方告警硬切)。
func (s *Supervisor) askQuiet(waited time.Duration) string {
	prompt := fmt.Sprintf("静默门未达成(已等 %v):升级动手前流量未空闲,继续会打扰在途会话。",
		waited.Round(time.Second))
	options := []string{
		fmt.Sprintf("  1) 继续等 %v 再判", s.quietWaitBudget()),
		"  2) 现在切换(硬切,可能打断在途请求)",
		"  3) 放弃本次升级",
	}
	if s.cfg.QuietAsk != nil {
		return s.cfg.QuietAsk(prompt, options)
	}
	return defaultQuietAsk(prompt, options)
}

// quietWaitBudget 「继续等」选项展示用的预算(负值哨兵 = 不等,展示缺省)。
func (s *Supervisor) quietWaitBudget() time.Duration {
	if s.cfg.WaitQuiet > 0 {
		return s.cfg.WaitQuiet
	}
	return defaultQuietWait
}

// stdinInteractive 判 stdin 是否交互终端(var 缝,测试注桩)。ModeCharDevice
// 是启发式:/dev/null 与 Windows NUL 也是字符设备(计划任务/go test 的 stdin
// 常是它们)——误判成终端也不悬死:defaultQuietAsk 的读行立即 EOF 返回 "",
// 调用方照样走告警硬切。
var stdinInteractive = func() bool {
	st, err := os.Stdin.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

// defaultQuietAsk 缺省问询:stdin 为交互终端才提示三选并读一行(trim);
// 非交互/无法判定/读失败/无法识别的输入返回 ""(调用方告警硬切)。
// 输入映射:1/等 → wait,2/切 → switch,3/放弃 → abort。
func defaultQuietAsk(prompt string, options []string) string {
	if !stdinInteractive() {
		return "" // 非交互/无法判定
	}
	fmt.Println(prompt)
	for _, o := range options {
		fmt.Println(o)
	}
	fmt.Print("> ")
	line, rerr := bufio.NewReader(os.Stdin).ReadString('\n')
	input := strings.TrimSpace(line)
	if rerr != nil && input == "" {
		return ""
	}
	switch input {
	case "1", "等", "wait":
		return quietChoiceWait
	case "2", "切", "现在切", "switch":
		return quietChoiceSwitch
	case "3", "放弃", "abort":
		return quietChoiceAbort
	}
	return ""
}

// ---- 停旧与守护面探活 ----

// stopDaemon 停守护(规格 §C 第5条):① 优雅 POST /shutdown(票04:loopback
// + Bearer)→ ② 等端口释放(≤PortWait)→ ②b 等旧进程真正退出(端口先释、
// 进程后出:swap 撞上还活着的旧镜像会 Access denied——v0.1.1 演练实证)→
// ③ 端口仍被占且是本守护在跑 → 兜底 kill:读 daemon.pid,验证 PID 映像路径
// == 换装目标(seam B),不匹配/不可查一律拒杀并报错。
func (s *Supervisor) stopDaemon(targetExe, what string) error {
	// 先记旧 PID(shutdown 过程会删 pid 文件,读晚了就没了)。
	oldPID := 0
	if pid, err := s.readDaemonPID(); err == nil {
		oldPID = pid
	}
	if err := s.postShutdown(); err != nil {
		s.logf("%s:/shutdown 端点未应(%v)——走兜底判定", what, err)
	}
	if s.waitPortFree(s.cfg.PortWait) {
		if !s.waitProcessExit(oldPID, s.cfg.PortWait) {
			return fmt.Errorf("%s:端口已释但 PID %d 仍活(排水/退出卡住,等满 %v)——"+
				"拒绝在旧进程在场时换装,人工介入或稍后重试", what, oldPID, s.cfg.PortWait)
		}
		return nil
	}
	if !s.probeDaemonAny() {
		return fmt.Errorf("%s:端口 %d 仍被占用且无守护应答(非 Ferryman 占口,拒动)",
			what, s.cfg.Port)
	}
	pid, err := s.readDaemonPID()
	if err != nil {
		return fmt.Errorf("%s:兜底 kill 前读 daemon.pid 失败: %w", what, err)
	}
	img, ierr := s.procImage(pid)
	if ierr != nil {
		return fmt.Errorf("%s:拒杀 PID %d——映像路径不可查(%v;seam B),请人工处理",
			what, pid, ierr)
	}
	if !samePath(img, targetExe) {
		return fmt.Errorf("%s:拒杀 PID %d——映像 %q 与换装目标 %q 不匹配(seam B)",
			what, pid, img, targetExe)
	}
	if err := s.killPID(pid); err != nil {
		return fmt.Errorf("%s:kill PID %d 失败: %w", what, pid, err)
	}
	if !s.waitPortFree(s.cfg.PortWait) {
		return fmt.Errorf("%s:kill 后端口 %d 仍未释放", what, s.cfg.Port)
	}
	if !s.waitProcessExit(pid, s.cfg.PortWait) {
		return fmt.Errorf("%s:kill PID %d 后进程仍未退出(等满 %v)", what, pid, s.cfg.PortWait)
	}
	s.logf("%s:兜底 kill PID %d(映像已核 == 换装目标)", what, pid)
	return nil
}

// waitProcessExit 等进程真正退出(≤budget)。/shutdown 优雅停机里监听口先关、
// 进程后走——端口释放 ≠ 镜像解锁,swap 若抢跑会 Access denied(v0.1.1 演练
// 实证);v0.2.4 排水落地后更是事故源:渡口 15722 在排水窗内仍被旧进程占着,
// 抢跑换装会让新守护渡口绑定失败进半死形态(2026-09-29 复盘)。故自该日起
// 软等待(超时放弃前进)改硬门:超时仍活返回 false,调用方报错中止。
// pid≤0(pid 文件缺失的罕见形态)视为放行——无从跟踪,保持旧行为。
func (s *Supervisor) waitProcessExit(pid int, budget time.Duration) bool {
	if pid <= 0 || budget <= 0 {
		return true
	}
	deadline := time.Now().Add(budget)
	for time.Now().Before(deadline) {
		if !s.procAlive(pid) {
			return true
		}
		time.Sleep(200 * time.Millisecond)
	}
	if s.procAlive(pid) {
		s.logf("PID %d 端口已释但进程未退(等满 %v,硬门:中止本次停旧)", pid, budget)
		return false
	}
	return true
}

// postShutdown POST /shutdown(票04 端点);非 200/网络错都算未应。
func (s *Supervisor) postShutdown() error {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost,
		"http://"+s.daemonAddr()+"/shutdown", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+s.daemonToken())
	resp, err := s.httpClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20)) // 排水(RST 纪律)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}

// queryVersion GET /stats(Bearer)取 version;任何失败 = 未得。
func (s *Supervisor) queryVersion() (string, bool) {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet,
		"http://"+s.daemonAddr()+"/stats", nil)
	if err != nil {
		return "", false
	}
	req.Header.Set("Authorization", "Bearer "+s.daemonToken())
	resp, err := s.httpClient().Do(req)
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 读一次:先排水后解码会扑空
	if err != nil {
		return "", false
	}
	if resp.StatusCode != http.StatusOK {
		return "", false
	}
	var out struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", false
	}
	return out.Version, true
}

// probeDaemonAny 任何 HTTP 应答(含 401/404)都算「有守护在听」(看门
// 分支①同语义);网络错 = 无。
func (s *Supervisor) probeDaemonAny() bool {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet,
		"http://"+s.daemonAddr()+"/stats", nil)
	if err != nil {
		return false
	}
	resp, err := s.httpClient().Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	return true
}

// waitPortFree 等端口释放:拨不通(拒绝/无监听)= 空。
func (s *Supervisor) waitPortFree(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		conn, err := net.DialTimeout("tcp", s.daemonAddr(), 500*time.Millisecond)
		if err != nil {
			return true
		}
		_ = conn.Close()
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(s.cfg.PollInterval)
	}
}

// pollVersionMatch 轮询直到 version==want 或超时;返回最后所见版本与是否命中。
func (s *Supervisor) pollVersionMatch(want string, timeout time.Duration) (string, bool) {
	deadline := time.Now().Add(timeout)
	var last string
	for {
		if v, ok := s.queryVersion(); ok {
			last = v
			if v == want {
				return v, true
			}
		}
		if !time.Now().Before(deadline) {
			return last, false
		}
		time.Sleep(s.cfg.PollInterval)
	}
}

// pollAnyVersion 轮询直到任一健康版本应答(恢复判定:盘上 exe 报什么版本
// 就是什么)。
func (s *Supervisor) pollAnyVersion(timeout time.Duration) (string, bool) {
	deadline := time.Now().Add(timeout)
	for {
		if v, ok := s.queryVersion(); ok {
			return v, true
		}
		if !time.Now().Before(deadline) {
			return "", false
		}
		time.Sleep(s.cfg.PollInterval)
	}
}

// daemonAddr 守护面地址(只环回)。
func (s *Supervisor) daemonAddr() string {
	return fmt.Sprintf("127.0.0.1:%d", s.cfg.Port)
}

// daemonToken 读 daemon.token(daemon 侧 EnsureToken 同位);读不到 = 空
// (鉴权必败,自然走兜底判定)。
func (s *Supervisor) daemonToken() string {
	b, err := os.ReadFile(filepath.Join(s.cfg.DataDir, "daemon.token"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// readDaemonPID 读 daemon.pid(daemon serve 写;字段见 internal/daemon)。
func (s *Supervisor) readDaemonPID() (int, error) {
	b, err := os.ReadFile(filepath.Join(s.cfg.DataDir, "daemon.pid"))
	if err != nil {
		return 0, err
	}
	var pj struct {
		PID int `json:"pid"`
	}
	if err := json.Unmarshal(b, &pj); err != nil {
		return 0, fmt.Errorf("daemon.pid 坏: %w", err)
	}
	if pj.PID <= 0 {
		return 0, fmt.Errorf("daemon.pid 无合法 PID: %d", pj.PID)
	}
	return pj.PID, nil
}

// httpClient 守护面客户端(3s——本机环回,拖长无意义)。
func (s *Supervisor) httpClient() *http.Client {
	if s.cfg.HTTP != nil {
		return s.cfg.HTTP
	}
	return &http.Client{Timeout: 3 * time.Second}
}

// ---- 小助手 ----

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

// nonEmpty 第一个非空串(所见版本择优汇报)。
func nonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
