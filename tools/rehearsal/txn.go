package main

// txn.go — 彩排事务跑法（编排侧）：把一次升级事务交给「影子 exe 自己」以
// supervisor 子命令形态执行。为什么监督者必须是 <Root>/ferryman.exe 本尊：
//
//   - 守护层锁让路判「锁持有者映像 ∈ {自己, 自己.supervisor-copy}」——监督者
//     与换装目标同路径（生产 `ferryman update` 的真实形态），蜂群注入才能被
//     让路挡下（换任何别的映像路径都会让该判定失真）；
//   - 自中继（自身映像==换装目标）会真实发生：监督者转交 .supervisor-copy
//     副本接手，副本经「update --supervise --self-relay …」argv + 环境变量
//     重建装配（relayArgs 只透传版本意图/force/wait-quiet，端口/端点/时限
//     走 REHEARSAL_SUP_* 环境）。
//
// 事务结果由副本原子写 result-out JSON，编排进程轮询读取。

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// txOpts 一次事务的注入面（全部经 Config/环境注入，零生产缺省生效）。
type txOpts struct {
	Name           string
	TargetTag      string // 桩发布的 tag（== 新版 exe 烤进的 main.version）
	Force          bool   // --force：跳静默门直接停旧（强制路径/长流场景）
	WaitQuietS     int    // 静默门等待预算（秒）
	PollTimeoutS   int    // 版本校验轮询上限（故障注入缩短用）
	PortWaitS      int    // 停旧等端口/进程退场上限（长流排水窗覆盖）
	PollIntervalMs int
	ProbeDelayMs   int           // 拉起验证探针首测延迟
	ProbeTimeoutMs int           // 拉起验证探针截止
	TxTimeout      time.Duration // 编排侧等待 result-out 的上限
}

// txResult 监督者事务结论（影子 exe 写盘的 JSON 形态）。
type txResult struct {
	Success     bool     `json:"success"`
	Relayed     bool     `json:"relayed"`
	From        string   `json:"from"`
	To          string   `json:"to"`
	RolledBack  bool     `json:"rolled_back"`
	RollbackErr string   `json:"rollback_err,omitempty"`
	Err         string   `json:"err,omitempty"`
	Alerts      []string `json:"alerts,omitempty"`
}

// txOutcome 编排侧事务观测。
type txOutcome struct {
	Res          *txResult // nil = 等结果超时
	Start, End   time.Time
	VersionAfter string
	VerifyErr    error
	TimedOut     bool
}

// runTransaction 跑一次升级事务：桩切目标版本 → 拉起监督者（影子 exe 本尊）
// → 等副本写 result-out → 核对守护版本。
func (e *shadowEnv) runTransaction(o txOpts) txOutcome {
	out := txOutcome{Start: time.Now()}
	asset, ok := e.assets[o.TargetTag]
	if !ok {
		out.VerifyErr = fmt.Errorf("资产表缺 %s", o.TargetTag)
		out.End = time.Now()
		return out
	}
	e.rel.setRelease(o.TargetTag, asset.Path, asset.SHA)
	resultPath := filepath.Join(e.Root, "tx-"+o.Name+".json")
	_ = os.Remove(resultPath)

	args := []string{"supervisor",
		"--data-dir", e.DataDir,
		"--port", strconv.Itoa(e.Ports.Mgmt),
		"--api-base", "http://" + e.rel.addr,
		"--dl-base", "http://" + e.rel.addr,
		"--start-cmd", e.CmdPath,
		"--spec", o.TargetTag,
		"--result-out", resultPath,
		"--wait-quiet", strconv.Itoa(o.WaitQuietS),
		"--poll-timeout-s", strconv.Itoa(o.PollTimeoutS),
		"--port-wait-s", strconv.Itoa(o.PortWaitS),
		"--poll-interval-ms", strconv.Itoa(o.PollIntervalMs),
		"--probe-delay-ms", strconv.Itoa(o.ProbeDelayMs),
		"--probe-timeout-ms", strconv.Itoa(o.ProbeTimeoutMs),
	}
	if o.Force {
		args = append(args, "--force")
	}
	// 环境带 REHEARSAL_SUP_*（副本接力重建装配用）；USERPROFILE 重定向同守护。
	env := append(e.daemonEnv(),
		"REHEARSAL_SUP_DATA_DIR="+e.DataDir,
		"REHEARSAL_SUP_PORT="+strconv.Itoa(e.Ports.Mgmt),
		"REHEARSAL_SUP_API_BASE=http://"+e.rel.addr,
		"REHEARSAL_SUP_DL_BASE=http://"+e.rel.addr,
		"REHEARSAL_SUP_START_CMD="+e.CmdPath,
		"REHEARSAL_SUP_RESULT_OUT="+resultPath,
		"REHEARSAL_SUP_POLL_TIMEOUT_S="+strconv.Itoa(o.PollTimeoutS),
		"REHEARSAL_SUP_PORT_WAIT_S="+strconv.Itoa(o.PortWaitS),
		"REHEARSAL_SUP_POLL_INTERVAL_MS="+strconv.Itoa(o.PollIntervalMs),
		"REHEARSAL_SUP_PROBE_DELAY_MS="+strconv.Itoa(o.ProbeDelayMs),
		"REHEARSAL_SUP_PROBE_TIMEOUT_MS="+strconv.Itoa(o.ProbeTimeoutMs),
	)
	logf, lerr := os.Create(filepath.Join(e.Root, "sup-"+o.Name+".log"))
	if lerr != nil {
		out.VerifyErr = lerr
		out.End = time.Now()
		return out
	}
	cmd, err := spawnHidden(e.ExePath, args, env, logf, logf)
	if err != nil {
		logf.Close()
		out.VerifyErr = err
		out.End = time.Now()
		return out
	}
	go func() { _ = cmd.Wait(); logf.Close() }() // 监督者本尊转交副本后即退

	deadline := out.Start.Add(o.TxTimeout)
	for time.Now().Before(deadline) {
		if b, rerr := os.ReadFile(resultPath); rerr == nil {
			var res txResult
			if json.Unmarshal(b, &res) == nil {
				out.Res = &res
				break
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	out.End = time.Now()
	if out.Res == nil {
		out.TimedOut = true
	}
	out.VersionAfter, out.VerifyErr = e.statsVersion()
	return out
}
