// proc.go — 本包唯一进程创建工厂（零闪窗铁律：Windows 分支恒 HideWindow；
// 进程树击杀记 PID 经 taskkill /T /F——Windows 不杀子进程链，工厂集中一处）。
//
// 一切 exec.Command 的构造集中在本文件 hiddenCmd 一处（平台 SysProcAttr 单
// 点＝newHiddenSysProcAttr）；两种消费形态共用它：
//   - spawnProc：长跑栈进程（沙箱 daemon / DSH web 宿主），返回 managedProc
//     （PID＋杀树闭包）。var 形＝测试注入假件（票面「起栈/收尾/断言全部可
//     注入假件」）——单测断言 env/参数/全停，不起真进程。
//   - runHidden：短命辅助进程（mklink 批 / taskkill），同步等退出收输出。
package dshsandbox

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// procSpec 一次进程创建的完整描述（栈进程与辅助进程共用）。
type procSpec struct {
	Path string
	Args []string
	// Env 完整环境（调用方负责「-u 清单＋沙箱钉值」；本工厂不合并不猜）。
	Env []string
	Dir string
	// StdoutFile / StderrFile 非空＝输出重定向到该文件（日志逐次可读，e2e
	// daemon.log/web.log 同形状）；空＝丢弃。
	StdoutFile string
	StderrFile string
}

// hiddenCmd 进程创建单点：构造 exec.Cmd 并挂平台隐藏属性（Windows 分支
// HideWindow——零闪窗铁律；非 Windows 无该字段，见 proc_other.go）。
func hiddenCmd(spec procSpec) *exec.Cmd {
	cmd := exec.Command(spec.Path, spec.Args...)
	cmd.Env = spec.Env
	cmd.Dir = spec.Dir
	cmd.SysProcAttr = newHiddenSysProcAttr()
	return cmd
}

// managedProc 一个被管理的栈进程：PID 记录在案（进程树击杀的锚点）＋杀树。
type managedProc struct {
	PID    int
	killFn func() error
}

// KillTree 进程树击杀（幂等：二次调用 nil）。Windows 走 taskkill /F /T /PID
// （树形，孙进程一并收——dsh.cmd 起 node 起宿主 exe 的多层链）；失败回错由
// 调用方记录，不阻断后续清理（收尾尽力而为，e2e stop.sh 同纪律）。
func (p *managedProc) KillTree() error {
	if p == nil || p.killFn == nil {
		return nil
	}
	fn := p.killFn
	p.killFn = nil
	return fn()
}

// spawnProc 长跑栈进程创建工厂（var 形＝测试注入假件；真进程只在真机冒烟走）。
var spawnProc = defaultSpawnProc

func defaultSpawnProc(spec procSpec) (*managedProc, error) {
	cmd := hiddenCmd(spec)
	// 输出文件句柄：Start 成功后子进程已继承句柄，父侧随即关闭自己的引用
	//（defer 在 Start 之后注册——打开失败时不误关）。
	var closers []*os.File
	defer func() {
		for _, f := range closers {
			_ = f.Close()
		}
	}()
	open := func(file string) (*os.File, error) {
		f, err := os.OpenFile(file, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return nil, err
		}
		closers = append(closers, f)
		return f, nil
	}
	if spec.StdoutFile != "" {
		f, err := open(spec.StdoutFile)
		if err != nil {
			return nil, fmt.Errorf("stdout 打不开 %s: %w", spec.StdoutFile, err)
		}
		cmd.Stdout = f
	}
	if spec.StderrFile != "" {
		f, err := open(spec.StderrFile)
		if err != nil {
			return nil, fmt.Errorf("stderr 打不开 %s: %w", spec.StderrFile, err)
		}
		cmd.Stderr = f
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	pid := cmd.Process.Pid
	mp := &managedProc{PID: pid, killFn: func() error { return killTree(pid) }}
	go func() { _ = cmd.Wait() }() // 后台收尸防僵尸（杀树对已退 PID 是 no-op）
	return mp, nil
}

// runHidden 短命辅助进程：同步等退出，回合并输出（mklink 批/taskkill 用）。
func runHidden(spec procSpec) (string, error) {
	cmd := hiddenCmd(spec)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// killTree 平台分派（Windows＝taskkill 树形；非 Windows＝单进程 Kill）。
func killTree(pid int) error { return killTreeImpl(pid) }

// makeJunctions 批量造 junction（验收标准钉死 mklink /J；一次 cmd 调多连
// 命令，行长度有界按段分批）。var 形＝测试注入（单测不真起 cmd）。
var makeJunctions = defaultMakeJunctions

// defaultMakeJunctions mklink /J 批量造链（Windows 形态；非 Windows 调用方
// 不走 junction 备料——stage 侧 GOOS 守卫）。悬空目标允许（mklink /J 语义，
// 生产 profiles/node_modules 本就大量悬垂）。
func defaultMakeJunctions(pairs [][2]string) error {
	const maxLen = 6000 // cmd 命令行安全上限内（8191 减头部余量）
	var batch []string
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		script := strings.Join(batch, " & ")
		batch = nil
		if out, err := runHidden(procSpec{Path: "cmd", Args: []string{"/c", script}}); err != nil {
			return fmt.Errorf("mklink 批失败: %v: %s", err, strings.TrimSpace(out))
		}
		return nil
	}
	cur := 0
	for _, p := range pairs {
		seg := fmt.Sprintf("mklink /J %s %s", quoteWin(p[0]), quoteWin(p[1]))
		if cur+len(seg)+3 > maxLen {
			if err := flush(); err != nil {
				return err
			}
			cur = 0
		}
		batch = append(batch, seg)
		cur += len(seg) + 3
	}
	return flush()
}

// quoteWin 路径含空格时加引号（mklink 参数；无空格裸传——cmd 引号规则从简）。
func quoteWin(p string) string {
	if strings.ContainsAny(p, " \t") && !strings.HasPrefix(p, "\"") {
		return "\"" + p + "\""
	}
	return p
}
