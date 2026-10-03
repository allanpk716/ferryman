package main

// faults.go — 故障注入集（票05 What-to-build 第4条，三场景通用口径各跑一遍，
// 逐项断言按设计恢复——回滚/复原路径真跑通）：
//
//   F1 拉起即杀：StartCmd 换秒退假脚本 → 拉起验证探针必须报「拉起验证失败
//      （相位错误）」并告警（W2 核心：拉起失败不再静默）；版本校验裁决回滚，
//      盘上恢复旧版（rollback 的 copyFile 复原真发生）；修复点火脚本后服务
//      可人工恢复。
//   F2 蜂群抢拉旧版：停旧信号（管理口转暗）一出现立刻用影子 exe 抢绑管理口
//      （不带监督者自拉起标记）→ 必须被守护层锁让路挡下（退出码 0 + 让路
//      行），没有第二个实例绑上管理口；正主事务照常完成。
//   F3 陈旧锁：预写 PID=1 的假 update.lock → 监督者判不持有、原子接管续跑。
//   F4 备份位被占：预放一个运行中映像到本事务备份位 → 让位失败后占据者被
//      挪 stale 名让路（v0.2.1 修复路径），事务成功，占位进程存活不受扰。
//   F5 排水中强断：长流排水等待中 kill 桩上游在途连接 → 排水契约收尾（截断
//      落账 dock 行 truncated=true，流被切断无 message_stop），旧守护按约退场，
//      事务完成。

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// faultLaunchDie F1 拉起即杀。
func faultLaunchDie(d rehearsalDeps) phaseRec {
	ph := phaseRec{Name: "F1 拉起即杀（StartCmd 秒退假脚本）", Metrics: map[string]string{}}
	t0 := time.Now()
	env, err := newShadowEnv(d.assets, "v0.9.0", envOpts{}, d.logf, d.portsReg)
	if err != nil {
		ph.Err = err.Error()
		ph.DurSec = time.Since(t0).Seconds()
		return ph
	}
	defer env.teardown()

	// 换秒退假脚本：保留引号 exe 行（监督者的换装目标解析要消费它），但
	// exit /b 1 让 cmd 永远走不到启动行——「拉起了但守护从未起来」的形态。
	broken := "@echo off\r\n" +
		"rem broken launcher for fault injection (rehearsal F1)\r\n" +
		"exit /b 1\r\n" +
		"\"" + env.ExePath + "\" serve >> \"" + slash(filepath.Join(env.Root, "serve.out.log")) +
		"\" 2>> \"" + slash(filepath.Join(env.Root, "serve.err.log")) + "\"\r\n"
	if err := os.WriteFile(env.CmdPath, []byte(broken), 0o644); err != nil {
		ph.Err = err.Error()
		ph.DurSec = time.Since(t0).Seconds()
		return ph
	}

	o := defaultTxOpts("f1-launch-die", "v0.9.1", false)
	o.PollTimeoutS = 8 // 拉不起时版本校验尽快裁决进回滚（彩排时限收紧）
	o.ProbeTimeoutMs = 4000
	o.TxTimeout = 90 * time.Second
	out := env.runTransaction(o)

	// ① 拉起失败立刻告警（W2 的存在意义）——alert 文本含「拉起验证失败」。
	alerted := false
	var alertLine string
	if out.Res != nil {
		for _, a := range out.Res.Alerts {
			if strings.Contains(a, "拉起验证失败") {
				alerted = true
				alertLine = a
			}
		}
	}
	ph.Checks = append(ph.Checks, ck("拉起验证失败已告警（不静默）", alerted, alertLine))

	// ② 事务如实失败（不 Success；回滚报告在场）。
	honest := out.Res != nil && !out.Res.Success
	ph.Checks = append(ph.Checks, ck("事务如实报告失败", honest, txDetail(out)))

	// ③ 回滚/复原路径真跑通：盘上恢复旧版（rollback 的 copyFile(备份→目标)）。
	gotSHA, serr := sha256File(env.ExePath)
	restored := serr == nil && gotSHA == d.assets["v0.9.0"].SHA
	ph.Checks = append(ph.Checks, ck("盘上已恢复旧版（rollback copyFile 复原）", restored,
		fmt.Sprintf("sha=%s…", shortSHA(gotSHA))))

	// ④ 修复点火脚本后服务可恢复（人工介入面真跑通）。
	if err := writeStartCmd(env.CmdPath, env.ExePath,
		filepath.Join(env.Root, "config.toml"), env.HomeDir,
		filepath.Join(env.Root, "serve.out.log"), filepath.Join(env.Root, "serve.err.log")); err != nil {
		ph.Checks = append(ph.Checks, ck("修复点火脚本", false, err.Error()))
	} else if err := env.startDaemon("v0.9.0"); err != nil {
		ph.Checks = append(ph.Checks, ck("修复后旧版服务恢复", false, err.Error()))
	} else {
		ph.Checks = append(ph.Checks, ck("修复后旧版服务恢复", true, ""))
	}

	ph.Metrics["tx_dur"] = fmtDur(out.End.Sub(out.Start))
	ph.Pass = alerted && honest && restored
	for _, c := range ph.Checks {
		if !c.Pass && strings.HasPrefix(c.Name, "修复") {
			ph.Pass = false
		}
	}
	ph.DurSec = time.Since(t0).Seconds()
	return ph
}

// faultSwarm F2 蜂群抢拉旧版。
func faultSwarm(d rehearsalDeps) phaseRec {
	ph := phaseRec{Name: "F2 蜂群抢拉旧版（锁让路挡下，无第二实例绑管理口）", Metrics: map[string]string{}}
	t0 := time.Now()
	env, err := newShadowEnv(d.assets, "v0.9.0", envOpts{}, d.logf, d.portsReg)
	if err != nil {
		ph.Err = err.Error()
		ph.DurSec = time.Since(t0).Seconds()
		return ph
	}
	defer env.teardown()

	supStart := time.Now()
	// 蜂群守望：管理口一转暗（停旧信号）立刻抢拉。影子事务全程仅 1-3s、
	// 黑窗 ≈ 停旧→新守护绑定（约 0.5-1.5s）——守望要密（40ms 拨测），
	// 且本事务刻意拉长监督者停旧→拉起的轮询节奏（PollIntervalMs=1200：
	// waitPortFree/waitProcessExit 两次轮询即把黑窗撑到 2-4s），保证蜂群
	// 守护有时间在场并被锁让路判定看到。
	type swarmRec struct {
		exitOK bool
		log    string
		err    error
	}
	swarmCh := make(chan swarmRec, 1)
	go func() {
		if !waitAddrDark(env.mgmtAddr(), supStart.Add(300*time.Millisecond), 90*time.Second) {
			swarmCh <- swarmRec{err: fmt.Errorf("未观察到停旧暗窗")}
			return
		}
		logPath := filepath.Join(env.Root, "swarm.log")
		f, ferr := os.Create(logPath)
		if ferr != nil {
			swarmCh <- swarmRec{err: ferr}
			return
		}
		defer f.Close()
		// 抢拉：影子 exe 本尊（与换装目标同路径——锁让路判持有者映像的
		// 前提），env 刻意不带自拉起标记。
		// CreateProcess 与换装 rename 撞上是毫秒级竞窗——失败隔 150ms 重试
		// 一次（黑暗窗 1-3s，重试仍在窗内）。
		var cmd *exec.Cmd
		var serr error
		for attempt := 0; attempt < 2; attempt++ {
			cmd, serr = spawnHidden(env.ExePath, []string{"serve"}, env.daemonEnv(), f, f)
			if serr == nil {
				break
			}
			time.Sleep(150 * time.Millisecond)
		}
		if serr != nil {
			swarmCh <- swarmRec{err: serr}
			return
		}
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case werr := <-done:
			b, _ := os.ReadFile(logPath)
			swarmCh <- swarmRec{exitOK: werr == nil, log: string(b), err: werr}
		case <-time.After(30 * time.Second):
			_ = killByPID(cmd.Process.Pid)
			swarmCh <- swarmRec{err: fmt.Errorf("蜂群实例 30s 未退（疑似真绑上了管理口）")}
		}
	}()

	o := defaultTxOpts("f2-swarm", "v0.9.1", false)
	o.PollIntervalMs = 1200 // 拉长停旧→拉起间隙：撑大黑窗给蜂群留出场时间
	out := env.runTransaction(o)
	c1, c2 := verifyTx(out, "v0.9.1")
	ph.Checks = append(ph.Checks, c1, c2)

	var sw swarmRec
	select {
	case sw = <-swarmCh:
	case <-time.After(35 * time.Second):
		sw = swarmRec{err: fmt.Errorf("蜂群守望超时")}
	}
	blocked := sw.err == nil && sw.exitOK &&
		(strings.Contains(sw.log, "静默让路") || strings.Contains(sw.log, "唯一化"))
	ph.Checks = append(ph.Checks, ck("蜂群实例被锁让路挡下（或撞唯一化），未绑管理口", blocked,
		swarmDetail(sw.exitOK, sw.log, sw.err)))
	ph.Metrics["swarm_exit_ok"] = fmt.Sprint(sw.exitOK)
	ph.Metrics["swarm_yield_kind"] = yieldKind(sw.log)
	ph.Pass = c1.Pass && c2.Pass && blocked
	ph.DurSec = time.Since(t0).Seconds()
	return ph
}

func swarmDetail(exitOK bool, log string, err error) string {
	if err != nil {
		return err.Error()
	}
	if !exitOK {
		return "蜂群实例退出码非 0"
	}
	first := ""
	for _, l := range strings.Split(log, "\n") {
		if strings.Contains(l, "让路") || strings.Contains(l, "唯一化") {
			first = strings.TrimSpace(l)
			break
		}
	}
	return first
}

func yieldKind(log string) string {
	if strings.Contains(log, "静默让路") {
		return "lock-yield"
	}
	if strings.Contains(log, "唯一化") {
		return "singleton-skip"
	}
	return "none-observed"
}

// waitAddrDark 等地址转暗（拨号拒绝）——停旧信号探测。notBefore 之前的暗
// 不算（跳过拉起前抖动）；拨测密（40ms 超时 + 15ms 间隔）以踩中亚秒级黑窗。
func waitAddrDark(addr string, notBefore time.Time, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 40*time.Millisecond)
		if err != nil {
			if time.Now().After(notBefore) {
				return true
			}
		} else {
			_ = conn.Close()
		}
		time.Sleep(15 * time.Millisecond)
	}
	return false
}

// faultStaleLock F3 陈旧锁。
func faultStaleLock(d rehearsalDeps) phaseRec {
	ph := phaseRec{Name: "F3 陈旧锁（PID=1 假锁 → 原子接管续跑）", Metrics: map[string]string{}}
	t0 := time.Now()
	env, err := newShadowEnv(d.assets, "v0.9.0", envOpts{}, d.logf, d.portsReg)
	if err != nil {
		ph.Err = err.Error()
		ph.DurSec = time.Since(t0).Seconds()
		return ph
	}
	defer env.teardown()

	// PID=1：Windows 上恒打不开 → 判不活 → 陈旧 → 接管（映像字段也不匹配，
	// 双保险）。generation=7 验证接管递增链路。
	stale := map[string]any{"pid": 1, "image": `C:\nonexistent\ferryman.exe`, "generation": 7,
		"started_at": "2026-01-01T00:00:00Z"}
	b, _ := json.Marshal(stale)
	if err := os.WriteFile(filepath.Join(env.DataDir, "update.lock"), b, 0o644); err != nil {
		ph.Err = err.Error()
		ph.DurSec = time.Since(t0).Seconds()
		return ph
	}

	out := env.runTransaction(defaultTxOpts("f3-stale-lock", "v0.9.1", false))
	c1, c2 := verifyTx(out, "v0.9.1")
	ph.Checks = append(ph.Checks, c1, c2)
	tookOver := strings.Contains(env.updateLogText(), "接管")
	ph.Checks = append(ph.Checks, ck("陈旧锁被判定并接管（update.log 留证）", tookOver,
		lockLine(env.updateLogText())))
	ph.Metrics["tx_dur"] = fmtDur(out.End.Sub(out.Start))
	ph.Pass = c1.Pass && c2.Pass && tookOver
	ph.DurSec = time.Since(t0).Seconds()
	return ph
}

func lockLine(logText string) string {
	for _, l := range strings.Split(logText, "\n") {
		if strings.Contains(l, "接管") {
			return strings.TrimSpace(l)
		}
	}
	return "未见接管行"
}

// faultBackupOccupied F4 备份位被占（占据者=运行中映像）。
func faultBackupOccupied(d rehearsalDeps) phaseRec {
	ph := phaseRec{Name: "F4 备份位被占（运行映像占位 → 挪 stale 让路）", Metrics: map[string]string{}}
	t0 := time.Now()
	env, err := newShadowEnv(d.assets, "v0.9.0", envOpts{}, d.logf, d.portsReg)
	if err != nil {
		ph.Err = err.Error()
		ph.DurSec = time.Since(t0).Seconds()
		return ph
	}
	defer env.teardown()

	// 占位者 = 运行中的旧版副本，落在本次事务的备份位（ferryman.exe.old-
	// v0.9.0）。REPLACE 对运行映像必 Access denied → 触发「占据者挪 stale」
	// 修复路径。占位进程起一个可探活的小 HTTP（sleeper 模式），断言它全程
	// 存活（改名运行映像不杀进程）。
	backup := filepath.Join(env.Root, "ferryman.exe.old-v0.9.0")
	if err := copyFileSteno(d.assets["v0.9.0"].Path, backup); err != nil {
		ph.Err = err.Error()
		ph.DurSec = time.Since(t0).Seconds()
		return ph
	}
	pingPort, err := allocPort()
	if err != nil {
		ph.Err = err.Error()
		ph.DurSec = time.Since(t0).Seconds()
		return ph
	}
	*d.portsReg = append(*d.portsReg, pingPort)
	pidOut := filepath.Join(env.Root, "sleeper.pid")
	slog, _ := os.Create(filepath.Join(env.Root, "sleeper.log"))
	cmd, serr := spawnHidden(backup, []string{"sleeper", "--ping-port", fmt.Sprint(pingPort),
		"--pid-out", pidOut}, env.daemonEnv(), slog, slog)
	if serr != nil {
		ph.Err = serr.Error()
		ph.DurSec = time.Since(t0).Seconds()
		return ph
	}
	go func() { _ = cmd.Wait(); slog.Close() }()
	if !waitPingOK(pingPort, 10*time.Second) {
		ph.Err = "占位进程未就绪"
		ph.DurSec = time.Since(t0).Seconds()
		return ph
	}
	defer stopSleeper(pingPort)

	out := env.runTransaction(defaultTxOpts("f4-backup-occupied", "v0.9.1", false))
	c1, c2 := verifyTx(out, "v0.9.1")
	ph.Checks = append(ph.Checks, c1, c2)

	logText := env.updateLogText()
	moved := strings.Contains(logText, "备份位被先前让位者占据——已挪到")
	ph.Checks = append(ph.Checks, ck("占据者被挪 stale 让路（update.log 留证）", moved, asideLine(logText)))
	stales, _ := filepath.Glob(filepath.Join(env.Root, "ferryman.exe.old-v0.9.0.stale-*"))
	ph.Checks = append(ph.Checks, ck("stale 挪窝名在盘", len(stales) > 0, fmt.Sprint(stales)))
	alive := pingOK(pingPort)
	ph.Checks = append(ph.Checks, ck("占位进程存活不受扰（改名运行映像不杀进程）", alive, ""))

	ph.Metrics["tx_dur"] = fmtDur(out.End.Sub(out.Start))
	ph.Pass = c1.Pass && c2.Pass && moved && len(stales) > 0 && alive
	ph.DurSec = time.Since(t0).Seconds()
	return ph
}

func asideLine(logText string) string {
	for _, l := range strings.Split(logText, "\n") {
		if strings.Contains(l, "备份位") {
			return strings.TrimSpace(l)
		}
	}
	return "未见备份位行"
}

// faultDrainKill F5 排水中强断。
func faultDrainKill(d rehearsalDeps) phaseRec {
	ph := phaseRec{Name: "F5 排水中强断（kill 在途上游连接）", Metrics: map[string]string{}}
	t0 := time.Now()
	// 长流口径的短版：30s 流——足够跨过停旧时刻，又把彩排时长压住。
	env, err := newShadowEnv(d.assets, "v0.9.0",
		envOpts{DrainS: 120, BindRetryS: 150, StreamDur: 30 * time.Second, ChunkEvery: time.Second},
		d.logf, d.portsReg)
	if err != nil {
		ph.Err = err.Error()
		ph.DurSec = time.Since(t0).Seconds()
		return ph
	}
	defer env.teardown()

	sse := newSSEProbe(env.dockAddr())
	sse.start()
	if err := sse.waitFirstChunk(15 * time.Second); err != nil {
		ph.Err = "长流未确立: " + err.Error()
		ph.DurSec = time.Since(t0).Seconds()
		return ph
	}

	supStart := time.Now()
	killed := make(chan int, 1)
	go func() {
		// 停旧暗窗一出现（管理口已关、渡口在排水等待）即杀上游在途连接。
		if waitAddrDark(env.mgmtAddr(), supStart.Add(2*time.Second), 90*time.Second) {
			killed <- env.up.killStreams()
		} else {
			killed <- -1
		}
	}()

	o := defaultTxOpts("f5-drain-kill", "v0.9.1", true)
	o.PortWaitS = 150
	o.PollTimeoutS = 60
	o.TxTimeout = 200 * time.Second
	out := env.runTransaction(o)
	c1, c2 := verifyTx(out, "v0.9.1")
	ph.Checks = append(ph.Checks, c1, c2)

	res := sse.wait(90 * time.Second)
	// 排水契约收尾：上游强断 → 流被切断（无 message_stop）、渡口侧截断落账。
	cut := !res.SawStop && res.Deltas > 0 && res.Deltas < 28 // 计划 ~28 delta，中途断
	ph.Checks = append(ph.Checks, ck("在途流被切断且截断可观测（无 message_stop）", cut,
		fmt.Sprintf("deltas=%d sawStop=%v err=%q", res.Deltas, res.SawStop, res.Err)))

	// 服务侧截断记录：dock 科目行 truncated=true（渡口记账面）。
	truncRec := false
	rows, _ := filepath.Glob(filepath.Join(env.DataDir, "accounts", "*.jsonl"))
	for _, p := range rows {
		if b, rerr := os.ReadFile(p); rerr == nil && strings.Contains(string(b), `"truncated":true`) {
			truncRec = true
			break
		}
	}
	ph.Checks = append(ph.Checks, ck("截断落账（accounts dock 行 truncated=true）", truncRec, ""))

	kn := -2
	select {
	case kn = <-killed:
	case <-time.After(5 * time.Second):
	}
	ph.Metrics["killed_streams"] = fmt.Sprint(kn)
	ph.Metrics["stream_deltas"] = fmt.Sprint(res.Deltas)
	ph.Metrics["tx_dur"] = fmtDur(out.End.Sub(out.Start))
	ph.Pass = c1.Pass && c2.Pass && cut && truncRec
	ph.DurSec = time.Since(t0).Seconds()
	return ph
}

// waitPingOK 等 sleeper 的探活口就绪。
func waitPingOK(port int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if pingOK(port) {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}

func pingOK(port int) bool {
	c := &http.Client{Timeout: 2 * time.Second}
	resp, err := c.Get(fmt.Sprintf("http://127.0.0.1:%d/ping", port))
	if err != nil {
		return false
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// stopSleeper 让 sleeper 自行退出（POST /stop）。
func stopSleeper(port int) {
	c := &http.Client{Timeout: 2 * time.Second}
	resp, err := c.Post(fmt.Sprintf("http://127.0.0.1:%d/stop", port), "", nil)
	if err == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}
}
