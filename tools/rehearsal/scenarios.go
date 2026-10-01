package main

// scenarios.go — 三场景（D10 口径，票05 What-to-build 第3条）：
//
//   S1 静默路径：间歇流量（相邻请求完成间隙 12s ≥ 10s 硬判据）→ 门放行 →
//      断言零截断、拒连窗 ≤10s、连续 N 次事务升级成功（-all N=10）。
//      N 次事务在两版之间往复（升/降级交替）——同源两构建下最真的形态：
//      每次事务的 From/To、备份名、校验目标都不同，还顺带覆盖降级。
//   S2 强制路径：--force + 1rps 探针全程打 → 拒连窗与窗内行为如实记录
//      （不判 ≤10s），断言事务完成。
//   S3 长流场景：在途流 >60s（桩上游慢速 SSE 70s）→ 断言零截断（流自然
//      跑完：message_stop 到达）+ 硬切边界行为有记录（黑窗时长、渡口排水
//      等待——这正是 2026-09-29 复盘修的停旧预算/绑定重试链路）。

import (
	"fmt"
	"strings"
	"time"
)

// rehearsalDeps 场景共享面。
type rehearsalDeps struct {
	assets   map[string]tagAsset
	logf     func(string, ...any)
	portsReg *[]int
	deadline time.Time
	quick    bool
}

// expired 全局截止判定（长跑终结纪律：到点即收，不再起新阶段）。
func (d rehearsalDeps) expired() bool { return !time.Now().Before(d.deadline) }

// defaultTxOpts 场景通用事务参数（时限收紧到彩排可跑的量级；停旧预算仍覆盖
// 排水窗——长流场景另调）。
func defaultTxOpts(name, target string, force bool) txOpts {
	return txOpts{
		Name: name, TargetTag: target, Force: force,
		WaitQuietS:     30,
		PollTimeoutS:   20,
		PortWaitS:      90,
		PollIntervalMs: 300,
		ProbeDelayMs:   1000,
		ProbeTimeoutMs: 8000,
		TxTimeout:      120 * time.Second,
	}
}

// verifyTx 事务成功 + 版本翻到位 的合成断言。
func verifyTx(out txOutcome, wantTag string) (checkRec, checkRec) {
	c1 := ck("事务成功", out.Res != nil && out.Res.Success, txDetail(out))
	c2 := ck(fmt.Sprintf("守护版本翻到 %s", wantTag),
		out.VerifyErr == nil && out.VersionAfter == wantTag,
		fmt.Sprintf("版本 %q（核对错: %v）", out.VersionAfter, out.VerifyErr))
	return c1, c2
}

func txDetail(out txOutcome) string {
	if out.Res == nil {
		if out.TimedOut {
			return "等待事务结果超时（副本未写 result-out）"
		}
		return "事务未执行"
	}
	d := fmt.Sprintf("from=%s to=%s rolled_back=%v", out.Res.From, out.Res.To, out.Res.RolledBack)
	if out.Res.Err != "" {
		d += " err=" + out.Res.Err
	}
	if out.Res.RollbackErr != "" {
		d += " rollback_err=" + out.Res.RollbackErr
	}
	return d
}

// scenarioQuiet S1 静默路径。
func scenarioQuiet(d rehearsalDeps) phaseRec {
	ph := phaseRec{Name: "S1 静默路径（间歇流量，门放行，连续事务零截断）", Metrics: map[string]string{}}
	t0 := time.Now()
	env, err := newShadowEnv(d.assets, "v0.9.0", envOpts{}, d.logf, d.portsReg)
	if err != nil {
		ph.Err = err.Error()
		ph.DurSec = time.Since(t0).Seconds()
		return ph
	}
	defer env.teardown()

	// 间歇流量：完成间隙 12s（≥10s 是门判据的硬口径，不可缩）；黑窗内 250ms
	// 密重试收口拒连窗测量——失败从不达守护，不污染静默节律。
	probe := newTrafficProbe(env.dockAddr(), "S1", 12*time.Second, 250*time.Millisecond)
	probe.start()
	defer probe.stop()
	time.Sleep(1500 * time.Millisecond) // 首请求完成落账，门有基线

	n := 10
	if d.quick {
		n = 3
	}
	allOK := true
	var windows []string
	cur := "v0.9.0"
	for i := 1; i <= n; i++ {
		if d.expired() {
			ph.Checks = append(ph.Checks, ck(fmt.Sprintf("事务 %d/%d", i, n), false, "全局截止到点"))
			allOK = false
			break
		}
		target := "v0.9.1"
		if i%2 == 0 {
			target = "v0.9.0" // 往复：升/降级交替，From/To 与备份名逐次不同
		}
		o := defaultTxOpts(fmt.Sprintf("quiet-%d", i), target, false)
		out := env.runTransaction(o)
		c1, c2 := verifyTx(out, target)
		c1.Name = fmt.Sprintf("事务 %d/%d 成功（%s → %s）", i, n, cur, target)
		c2.Name = fmt.Sprintf("事务 %d/%d 后版本 == %s", i, n, target)
		ph.Checks = append(ph.Checks, c1, c2)
		if !c1.Pass || !c2.Pass {
			allOK = false
		}
		ph.Metrics[fmt.Sprintf("tx%d_dur", i)] = fmtDur(out.End.Sub(out.Start))
		if w, obs, rec := measureRefusalWindow(probe.snapshot(), out.Start); obs && rec {
			windows = append(windows, fmtDur(w))
			if w > 10*time.Second {
				ph.Checks = append(ph.Checks, ck(fmt.Sprintf("事务 %d 拒连窗 ≤10s", i), false, fmtDur(w)))
				allOK = false
			}
		}
		cur = target
	}

	// 聚合断言：零截断（Cut 类事件为 0——响应已开始后失败才是截断；黑窗内
	// 拨号拒绝不计）；门真放行（update.log 见「静默门放行」且全程无「硬切兜底」）。
	cuts := probe.truncationCount(t0)
	ph.Checks = append(ph.Checks, ck(fmt.Sprintf("零截断（截断类事件 = %d）", cuts), cuts == 0, ""))
	if cuts != 0 {
		allOK = false
	}
	logText := env.updateLogText()
	gateOK := strings.Contains(logText, "静默门放行") && !strings.Contains(logText, "硬切兜底")
	ph.Checks = append(ph.Checks, ck("静默门放行且未走硬切兜底", gateOK,
		gateLine(logText)))
	if !gateOK {
		allOK = false
	}
	ph.Metrics["refusal_windows"] = strings.Join(windows, ", ")
	ph.Metrics["refusal_windows_note"] = "未列出的事务未观测到拒连（12s 探针间隙大于黑窗——合法形态，窗为 0）"
	ph.Pass = allOK
	ph.DurSec = time.Since(t0).Seconds()
	return ph
}

// gateLine 从 update.log 抽门行为证据行（人话定位）。
func gateLine(logText string) string {
	for _, l := range strings.Split(logText, "\n") {
		if strings.Contains(l, "静默门") {
			return strings.TrimSpace(l)
		}
	}
	return "update.log 未见静默门行"
}

// scenarioForce S2 强制路径。
func scenarioForce(d rehearsalDeps) phaseRec {
	ph := phaseRec{Name: "S2 强制路径（--force + 1rps 探针，如实记录不判 ≤10s）", Metrics: map[string]string{}}
	t0 := time.Now()
	env, err := newShadowEnv(d.assets, "v0.9.0", envOpts{}, d.logf, d.portsReg)
	if err != nil {
		ph.Err = err.Error()
		ph.DurSec = time.Since(t0).Seconds()
		return ph
	}
	defer env.teardown()

	probe := newTrafficProbe(env.dockAddr(), "S2", time.Second, 250*time.Millisecond)
	probe.start()
	defer probe.stop()
	time.Sleep(1500 * time.Millisecond)

	out := env.runTransaction(defaultTxOpts("force-1", "v0.9.1", true))
	c1, c2 := verifyTx(out, "v0.9.1")
	ph.Checks = append(ph.Checks, c1, c2)

	logText := env.updateLogText()
	forced := strings.Contains(logText, "静默门跳过(--force)")
	ph.Checks = append(ph.Checks, ck("门被 --force 跳过（update.log 留证）", forced, gateLine(logText)))

	// 窗内行为如实记录（不判）：拒连窗、窗内截断类事件数、1rps 完成量。
	if w, obs, rec := measureRefusalWindow(probe.snapshot(), out.Start); obs && rec {
		ph.Metrics["refusal_window"] = fmtDur(w)
	} else {
		ph.Metrics["refusal_window"] = "未观测到（探针间隙踩空黑窗）"
	}
	ph.Metrics["cut_events"] = fmt.Sprint(probe.truncationCount(out.Start))
	ph.Metrics["tx_dur"] = fmtDur(out.End.Sub(out.Start))
	ph.Pass = c1.Pass && c2.Pass && forced
	ph.DurSec = time.Since(t0).Seconds()
	return ph
}

// scenarioLongStream S3 长流场景。
func scenarioLongStream(d rehearsalDeps) phaseRec {
	ph := phaseRec{Name: "S3 长流场景（在途流 >60s，零截断 + 硬切边界记录）", Metrics: map[string]string{}}
	t0 := time.Now()
	// 影子按长流口径配：桩上游 70s/1s 慢速 SSE；排水上限 120s 覆盖流全程；
	// 渡口绑定重试 150s 覆盖排水窗（旧守护排水期间渡口口仍被占）。
	env, err := newShadowEnv(d.assets, "v0.9.0",
		envOpts{DrainS: 120, BindRetryS: 150, StreamDur: 70 * time.Second, ChunkEvery: time.Second},
		d.logf, d.portsReg)
	if err != nil {
		ph.Err = err.Error()
		ph.DurSec = time.Since(t0).Seconds()
		return ph
	}
	defer env.teardown()

	// 黑窗观测探针（1rps 快速请求）：长流的硬切边界（黑窗 ≈ 排水+启动全程）
	// 只记录不判。它同时给 last_request_ts 持续供血——force 路径本就不看门。
	probe := newTrafficProbe(env.dockAddr(), "S3-obs", time.Second, 250*time.Millisecond)
	probe.start()
	defer probe.stop()

	sse := newSSEProbe(env.dockAddr())
	sse.start()
	if err := sse.waitFirstChunk(15 * time.Second); err != nil {
		ph.Err = "长流未确立: " + err.Error()
		ph.DurSec = time.Since(t0).Seconds()
		return ph
	}

	o := defaultTxOpts("longstream-1", "v0.9.1", true)
	o.PortWaitS = 150   // 停旧预算覆盖排水窗（70s 流 + 余量）
	o.PollTimeoutS = 60 // 新版校验余量（渡口绑定重试在管理面应答之后）
	o.TxTimeout = 220 * time.Second
	out := env.runTransaction(o)
	c1, c2 := verifyTx(out, "v0.9.1")
	ph.Checks = append(ph.Checks, c1, c2)

	res := sse.wait(140 * time.Second)
	// 零截断：message_stop 到达 + 事件量够（70s/1s ≈ 68 个 delta，容差 5）。
	noCut := res.SawStop && res.CompletedOK && res.Deltas >= 63
	ph.Checks = append(ph.Checks, ck("零截断（message_stop 到达，流自然跑完）", noCut,
		fmt.Sprintf("deltas=%d sawStop=%v err=%q dur=%s", res.Deltas, res.SawStop, res.Err, fmtDur(res.Dur))))

	// 硬切边界记录：黑窗（新连接拒连跨度）与流收尾时序。
	if w, obs, rec := measureRefusalWindow(probe.snapshot(), out.Start); obs && rec {
		ph.Metrics["dark_window"] = fmtDur(w)
	} else {
		ph.Metrics["dark_window"] = "未观测到"
	}
	ph.Metrics["stream_dur"] = fmtDur(res.Dur)
	ph.Metrics["tx_dur"] = fmtDur(out.End.Sub(out.Start))
	ph.Metrics["boundary_note"] = "硬切边界=排水等待（旧守护渡口在途流跑完才退场）+新守护渡口绑定重试；黑窗如实记录不判"
	ph.Pass = c1.Pass && c2.Pass && noCut
	ph.DurSec = time.Since(t0).Seconds()
	return ph
}
