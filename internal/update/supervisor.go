package update

// 升级监督者状态机(票05,规格 §C 全序列):换装目标解析(seam E)→ 锁
// (seam B)→ journal → staging(票03 下载+SHA256)→ 停旧(/shutdown 票04 +
// 身份校验兜底 kill)→ swap(seam A 单次原子替换)→ 拉起+校验(seam C 看门
// 抢跑复停重拉)→ 回滚 → 崩溃恢复。CLI `ferryman update` 与内部旗标
// --supervise 收敛到同一 Run(规格 §C 统一监督者)。

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// 监督者缺省参数(规格 §C 第5/7条:等端口释放 ≤30s、轮询 /stats ≤90s)。
const (
	// DefaultDaemonPort 守护口(与 installer.DefaultDaemonPort 同值;不 import
	// installer,避免 update→installer 的面拉宽)。
	DefaultDaemonPort = 7311
	defaultPortWait   = 30 * time.Second
	defaultPollWait   = 90 * time.Second
	defaultPollEvery  = 1 * time.Second
	// startCmdName 点火脚本名(installer.LauncherName 同名同位)。
	startCmdName = "start-daemon.cmd"
)

// Config 监督者装配面:全部路径/口/时限可注入——测试世界零生产面触碰。
type Config struct {
	DataDir      string // ~/ferryman:锁/journal/token/pid;空 = 回落 ~/ferryman
	Port         int    // 守护口;0 = 7311
	Endpoints    Endpoints
	Current      string        // 当前版本(main.version)
	Spec         string        // 显式目标版本(可空;含降级)
	Prerelease   bool          // 纳入预发布
	SelfRelay    bool          // 本进程已是自中继副本,不再自中继(内部旗标)
	StartCmd     string        // 点火脚本;空 = <DataDir>/start-daemon.cmd
	HTTP         *http.Client  // 守护面客户端;空 = 3s 超时内建
	PortWait     time.Duration // 停旧等端口释放上限;0 = 30s
	PollTimeout  time.Duration // 拉起校验轮询上限;0 = 90s
	PollInterval time.Duration // 轮询间隔;0 = 1s
	Logf         func(format string, args ...any)
}

// Result 升级结论(seam F 结果通知与 CLI stdout 的单源)。
type Result struct {
	Success     bool
	Relayed     bool  // 已转交自中继副本接手,本进程只负责退出(v0.1.0 首发实测补)
	From, To    string
	RolledBack  bool   // 失败但已回滚恢复旧版服务
	RollbackErr string // 回滚也失败(服务可能中断,需人工介入)
	Err         error
}

// Supervisor 监督者。proc* 为进程面 seam(测试注桩);launch 为拉起 seam;
// selfExe/spawnRelay 为自中继 seam(同上)。
type Supervisor struct {
	cfg        Config
	procAlive  func(pid int) bool
	procImage  func(pid int) (string, error)
	killPID    func(pid int) error
	launch     func(cmdPath string) error
	selfExe    func() (string, error)
	spawnRelay func(exe string, args []string) error
}

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
	if cfg.Logf == nil {
		cfg.Logf = func(format string, a ...any) {
			fmt.Printf("[ferryman-update] "+format+"\n", a...)
		}
	}
	return &Supervisor{
		cfg:        cfg,
		procAlive:  procAliveImpl,
		procImage:  procImageImpl,
		killPID:    killImpl,
		launch:     launchCmdImpl,
		selfExe:    os.Executable,
		spawnRelay: spawnRelayImpl,
	}
}

func (s *Supervisor) logf(format string, a ...any) { s.cfg.Logf(format, a...) }

// Run 监督者主序列。返回 Result;过程日志走 Logf。
func (s *Supervisor) Run() Result {
	// 0. 换装目标解析(seam E)
	targetExe, err := s.resolveTargetExe()
	if err != nil {
		return Result{Err: err}
	}
	exeDir := filepath.Dir(targetExe)

	// 0.5 自中继(v0.1.0 首发实测补):监督者自身映像 == 换装目标时,单次原子
	// 替换会被自己的运行镜像锁死(实测 Access is denied——停旧只停了守护,
	// 没停监督者本人)。复制自身为副本、detached 拉起副本接手(副本与目标不同
	// 文件,seam A 恢复可行),本进程交棒退出。
	if relayed, rerr := s.selfRelayIfNeeded(targetExe); rerr != nil {
		return Result{Err: rerr}
	} else if relayed {
		return Result{Relayed: true, From: s.cfg.Current}
	}

	// 1. 锁(seam B:持有者活 = PID 活且映像==换装目标)
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

	// 5. 停旧(票04 /shutdown;端点不可达且守护在跑 → 身份校验兜底 kill)
	if err := s.stopDaemon(j.TargetExe, "停旧"); err != nil {
		_ = clearJournal(s.cfg.DataDir)
		res.Err = fmt.Errorf("停旧失败: %w", err)
		return res
	}

	// 6. swap(seam A:备份 → 单次原子替换;备份只留 2 份)
	j.Phase = PhaseSwap
	if err := saveJournal(s.cfg.DataDir, j); err != nil {
		res.Err = err
		return res
	}
	if err := s.swapFiles(j); err != nil {
		_ = clearJournal(s.cfg.DataDir)
		res.Err = err
		// exe 未被原子替换触及;盘上仍是旧版——监督者自己拉起恢复服务
		s.restoreServiceQuiet(j)
		return res
	}

	// 7. 拉起 + 校验(seam C:失败先查持有者,旧版抢跑 → 复停重拉再校验一轮)
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

	// 8. 回滚
	return s.rollback(j, seen)
}

// selfRelayIfNeeded 自中继判定:自身映像 == 换装目标且未携中继标记时,复制
// 自身为 <目标>.supervisor-copy 并 detached 拉起副本接手(副本携 --self-relay,
// 不会再次自中继),本进程交棒。复制失败/拉起失败如实报错中止——此时硬走
// swap 必然 Access denied(自己的镜像锁着目标),早失败比晚失败诚实。
func (s *Supervisor) selfRelayIfNeeded(targetExe string) (bool, error) {
	if s.cfg.SelfRelay {
		return false, nil
	}
	self, err := s.selfExe()
	if err != nil {
		return false, nil // 拿不到自身映像(几乎不可能):不拦,让后续按原样走
	}
	if !samePath(self, targetExe) {
		return false, nil
	}
	copyPath := relayCopyPath(targetExe)
	if err := copyFile(self, copyPath); err != nil {
		return false, fmt.Errorf("自中继副本落盘失败: %w", err)
	}
	if err := s.spawnRelay(copyPath, relayArgs(s.cfg.Spec, s.cfg.Prerelease)); err != nil {
		_ = os.Remove(copyPath)
		return false, fmt.Errorf("自中继副本拉起失败: %w", err)
	}
	s.logf("监督者自身即换装目标——已转交副本接手升级,本进程退出(副本 %s)", copyPath)
	return true, nil
}

// relayCopyPath 自中继副本落点(在 cleanSwapResidues 清扫域内:副本退出后
// 无法删除自身运行镜像,留待下次 update/doctor 驱动的清扫收走)。
func relayCopyPath(targetExe string) string {
	return targetExe + ".supervisor-copy"
}

// relayArgs 副本接手参数:监督者旗标 + 自中继标记 + 原样的版本意图。
func relayArgs(spec string, prerelease bool) []string {
	args := []string{"update", "--supervise", "--self-relay"}
	if spec != "" {
		args = append(args, spec)
	}
	if prerelease {
		args = append(args, "--prerelease")
	}
	return args
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

// swapFiles seam A:copy 当前 exe → 备份 → 单次 MoveFileEx 原子替换
// (REPLACE_EXISTING;停旧后无运行锁,要么旧要么新,无缺位窗口)。
func (s *Supervisor) swapFiles(j journal) error {
	if err := copyFile(j.TargetExe, j.Backup); err != nil {
		return fmt.Errorf("备份旧版失败: %w", err)
	}
	pruneBackups(filepath.Dir(j.TargetExe), 2)
	if err := moveFileReplace(j.NewExe, j.TargetExe); err != nil {
		return fmt.Errorf("原子替换失败: %w", err)
	}
	s.logf("换装完成: %s ← %s(备份 %s)", j.TargetExe, filepath.Base(j.NewExe), j.Backup)
	return nil
}

// restoreServiceQuiet swap 失败后恢复服务:盘上仍是旧版,拉起它(已有服务
// 则不动)。尽力而为——失败只记日志,看门/自举三件套仍是兜底。
func (s *Supervisor) restoreServiceQuiet(j journal) {
	if _, ok := s.queryVersion(); ok {
		return
	}
	if err := s.launch(j.StartCmd); err != nil {
		s.logf("swap 失败后拉起旧版失败(看门/自举会兜底): %v", err)
		return
	}
	if v, ok := s.pollAnyVersion(s.cfg.PollTimeout); ok {
		s.logf("swap 失败后旧版 %s 已恢复服务", v)
	}
}

// launchAndVerify 拉起并校验 want 版本;seam C:失败判定前先查 7311 持有者,
// 旧版本(看门/自举抢跑重拉)→ 复停一次 → 重拉起 → 重校验一轮。
func (s *Supervisor) launchAndVerify(j journal, want string) (string, bool) {
	if err := s.launch(j.StartCmd); err != nil {
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
		if err := s.launch(j.StartCmd); err != nil {
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
	if err := s.launch(j.StartCmd); err != nil {
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
		s.waitProcessExit(oldPID, s.cfg.PortWait)
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
	s.waitProcessExit(pid, s.cfg.PortWait)
	s.logf("%s:兜底 kill PID %d(映像已核 == 换装目标)", what, pid)
	return nil
}

// waitProcessExit 等进程真正退出(≤budget)。/shutdown 优雅停机里监听口先关、
// 进程后走——端口释放 ≠ 镜像解锁,swap 若抢跑会 Access denied(v0.1.1 演练
// 实证)。超时如实放弃并留日志:不静默假装等过,后续步骤撞锁会再如实报错。
func (s *Supervisor) waitProcessExit(pid int, budget time.Duration) {
	if pid <= 0 || budget <= 0 {
		return
	}
	deadline := time.Now().Add(budget)
	for time.Now().Before(deadline) {
		if !s.procAlive(pid) {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	if s.procAlive(pid) {
		s.logf("PID %d 端口已释但进程未退(等满 %v 放弃;若后续撞锁将如实报错)", pid, budget)
	}
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
