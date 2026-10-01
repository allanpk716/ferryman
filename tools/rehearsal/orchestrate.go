package main

// orchestrate.go — 彩排编排：真构建两版影子 exe → 逐阶段跑三场景+五故障注入
// → 结果落盘。全程带截止（长跑终结纪律：到点即收，不起新阶段，正在跑的
// 阶段自有 TxTimeout 兜底，收尾必清场）。

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// rehearsalOpts 编排入口参数。
type rehearsalOpts struct {
	Quick    bool          // 静默路径 3 次事务（CI 形态；-all 才是完整验收）
	Deadline time.Duration // 全局截止
	RepoRoot string        // 空 = 自动探测
	OutDir   string        // 空 = <repoRoot>/.scratch/upgrade-reliability/rehearsal
	Only     []string      // 只跑指定阶段（调试用）：quiet|force|longstream|launch-die|swarm|stale-lock|backup-occupied|drain-kill
}

// buildRouteNote 两版路线说明（票面要求在结果文件注明选型与原因）。
const buildRouteNote = "两版 exe = 同一源码（tools/rehearsal）两次 go build，" +
	"-ldflags \"-X main.version=<tag>\" 注入版本：/stats version 必须等于桩 release 的 tag，" +
	"而版本随换装翻版（环境变量/文件标记过不了换装——版本必须烤进 exe），故取两次构建形态。" +
	"多轮事务在两版间往复（升/降级交替），桩 release 按事务切换 tag 与资产。"

// runRehearsal 编排主流程。基础设施失败（构建不起来）返回 error；阶段断言
// 失败记入报告（OverallPass=false），报告照常落盘。
func runRehearsal(o rehearsalOpts) (*rehearsalReport, error) {
	started := time.Now()
	if o.Deadline <= 0 {
		o.Deadline = 15 * time.Minute
	}
	repoRoot := o.RepoRoot
	if repoRoot == "" {
		var err error
		repoRoot, err = findRepoRoot()
		if err != nil {
			return nil, err
		}
	}
	outDir := o.OutDir
	if outDir == "" {
		outDir = filepath.Join(repoRoot, ".scratch", "upgrade-reliability", "rehearsal")
	}
	logf := func(f string, a ...any) { fmt.Printf("[rehearsal] "+f+"\n", a...) }

	mode := "all"
	if o.Quick {
		mode = "quick"
	}
	rep := &rehearsalReport{
		Mode:      mode,
		StartedAt: started.Format("2006-01-02 15:04:05"),
		RepoRoot:  repoRoot,
		PortsUsed: []int{},
	}

	// —— 构建两版 ——
	logf("构建两版影子 exe（%s）", repoRoot)
	bRec, assets, err := buildShadowExes(repoRoot, outDir)
	if err != nil {
		return nil, fmt.Errorf("影子 exe 构建失败: %w", err)
	}
	bRec.Route = buildRouteNote
	rep.Builds = bRec
	logf("构建完成: %s(%dB) / %s(%dB) 用时 %.0fs",
		bRec.OldTag, bRec.OldSize, bRec.NewTag, bRec.NewSize, bRec.DurSec)

	d := rehearsalDeps{assets: assets, logf: logf, portsReg: &rep.PortsUsed,
		deadline: started.Add(o.Deadline), quick: o.Quick}

	type staged struct {
		key string
		fn  func(rehearsalDeps) phaseRec
	}
	stages := []staged{
		{"quiet", scenarioQuiet},
		{"force", scenarioForce},
		{"longstream", scenarioLongStream},
		{"launch-die", faultLaunchDie},
		{"swarm", faultSwarm},
		{"stale-lock", faultStaleLock},
		{"backup-occupied", faultBackupOccupied},
		{"drain-kill", faultDrainKill},
	}
	for _, st := range stages {
		if len(o.Only) > 0 && !containsStr(o.Only, st.key) {
			continue
		}
		if d.expired() {
			ph := phaseRec{Name: st.key, Err: "全局截止到点，阶段未跑"}
			rep.Phases = append(rep.Phases, ph)
			continue
		}
		logf("阶段 %s …", st.key)
		ph := st.fn(d)
		rep.Phases = append(rep.Phases, ph)
		logf("阶段 %s: %s（%.0fs）", st.key, passMark(ph.Pass), ph.DurSec)
	}

	rep.TotalDurSec = time.Since(started).Seconds()
	rep.OverallPass = true
	for _, ph := range rep.Phases {
		if !ph.Pass {
			rep.OverallPass = false
		}
	}
	jsonPath, mdPath, werr := writeReports(rep, outDir)
	if werr != nil {
		return rep, fmt.Errorf("结果落盘失败: %w", werr)
	}
	logf("结果: %s / %s", jsonPath, mdPath)
	return rep, nil
}

// buildShadowExes 构建两版影子 exe（产物留 outDir/build-<ts>/ 便于排查）。
func buildShadowExes(repoRoot, outDir string) (buildRec, map[string]tagAsset, error) {
	var rec buildRec
	buildDir := filepath.Join(outDir, "build-"+time.Now().Format("20060102-150405"))
	if err := os.MkdirAll(buildDir, 0o755); err != nil {
		return rec, nil, err
	}
	t0 := time.Now()
	rec.OldTag, rec.NewTag = "v0.9.0", "v0.9.1"
	oldOut := filepath.Join(buildDir, "ferryman-v0.9.0.exe")
	newOut := filepath.Join(buildDir, "ferryman-v0.9.1.exe")
	if err := runGoBuild(repoRoot, oldOut, rec.OldTag); err != nil {
		return rec, nil, err
	}
	if err := runGoBuild(repoRoot, newOut, rec.NewTag); err != nil {
		return rec, nil, err
	}
	var err error
	if rec.OldSHA, err = sha256File(oldOut); err != nil {
		return rec, nil, err
	}
	if rec.NewSHA, err = sha256File(newOut); err != nil {
		return rec, nil, err
	}
	rec.OldSize, rec.NewSize = fileSize(oldOut), fileSize(newOut)
	rec.DurSec = time.Since(t0).Seconds()
	assets := map[string]tagAsset{
		rec.OldTag: {Path: oldOut, SHA: rec.OldSHA, Size: rec.OldSize},
		rec.NewTag: {Path: newOut, SHA: rec.NewSHA, Size: rec.NewSize},
	}
	return rec, assets, nil
}

// runGoBuild go build -ldflags 版本注入（隐藏窗口防闪黑窗；失败回显构建输出）。
func runGoBuild(repoRoot, out, tag string) error {
	var buf bytes.Buffer
	c := newHiddenCmd("go", []string{"build", "-ldflags", "-X main.version=" + tag,
		"-o", out, "./tools/rehearsal"}, repoRoot, filterEnv(), &buf, &buf)
	if err := c.Run(); err != nil {
		tail := buf.String()
		if len(tail) > 4000 {
			tail = tail[len(tail)-4000:]
		}
		return fmt.Errorf("go build %s: %v\n%s", tag, err, tail)
	}
	return nil
}

func fileSize(p string) int64 {
	st, err := os.Stat(p)
	if err != nil {
		return 0
	}
	return st.Size()
}

func containsStr(vs []string, s string) bool {
	for _, v := range vs {
		if v == s || strings.EqualFold(v, s) {
			return true
		}
	}
	return false
}
