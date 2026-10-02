package main

// txn.go — 彩排事务跑法（编排侧）：把一次升级事务交给「影子 exe 自己」以
// supervisor 子命令形态执行。为什么监督者必须是 <Root>/ferryman.exe 本尊：
//
//   - 守护层锁让路判「锁持有者映像 ∈ {目标本体,
//     同目录 .old-* 备份族}」——监督者与换装目标同路径（生产 `ferryman update`
//     的真实形态；自中继副本机制已删，监督者直接跑两步换装，自身映像在让位
//     后即 .old-* 形态），蜂群注入才能被让路挡下（换任何别的映像路径都会让
//     该判定失真）；
//   - supervisor 子命令的全部装配经旗标注入（--data-dir/--port/--api-base/
//     --start-cmd/--spec/--result-out 等，零生产缺省生效）。
//
// 事务结果由监督者原子写 result-out JSON，编排进程轮询读取。

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
	// 监督者装配全走旗标（上表）；env 只做 USERPROFILE 重定向同守护（旧
	// REHEARSAL_SUP_* 环境通道随自中继副本机制删除，无消费方）。
	env := e.daemonEnv()
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
	go func() { _ = cmd.Wait(); logf.Close() }() // 监督者同步跑完全程（v0.5.2 起无副本转交）

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
