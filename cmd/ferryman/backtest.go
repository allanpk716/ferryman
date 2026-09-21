// backtest.go — 票04：`ferryman backtest` 离线子命令（等待窗扫参端到端装配）。
//
// 用户/agent 敲一条命令：读自己的账本（config data_dir 下 accounts/*.jsonl）
// 与价格表（同一份 config 的 [prices.*]），装配票01 装载 → 票02 网格引擎 →
// 票03 报告三包，markdown 实验报告落 --out（缺省 docs/ 实验报告惯例文件名），
// --json 时 stdout 另给结构化结果（BuildView 投影，退出码承载结论）。
//
// 纪律（票面/规格「通用性」）：全程只读账本与 config，唯一写目标是报告文件；
// 无任何本机路径/项目名硬编码——默认全量，profile 只经 --projects/--exclude
// glob 进来；config 解析复用既有优先级（显式 --config > FERRYMAN_CONFIG >
// ~/ferryman/config.toml，同 daemon）。
//
// 如实语义（不崩溃不造数）：
//   - 账本/config 读不动 → 硬错误退 1，不落报告；
//   - 引擎未产出（ttl_s 未配置 ErrTTLUnset、价格不可算等）→ 空态报告照落
//     （票03「（引擎未产出）」占位）+ stderr 说明 + 退 1；
//   - 零窗/过滤后零窗是数据状态非错误 → 空态标注 + 退 0。
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"ferryman/internal/backtest"
	"ferryman/internal/config"
	"ferryman/internal/prices"
)

// globList 可多次的 glob 旗标（flag.Value：每次 Set 追加一段，空段丢弃）。
type globList []string

func (g *globList) String() string { return strings.Join(*g, ",") }

func (g *globList) Set(v string) error {
	if v != "" {
		*g = append(*g, v)
	}
	return nil
}

// backtestOpts cmdBacktest 解析产物（runBacktest 可测核心的输入形）。
type backtestOpts struct {
	ConfigPath string   // 显式 config 路径（空 = FERRYMAN_CONFIG 或默认）
	Projects   []string // 项目正集 glob（空 = 全量）
	Exclude    []string // 排除 glob（恒压过正集）
	Out        string   // markdown 报告落点（空 = docs/ 惯例文件名）
	JSON       bool     // stdout 结构化结果
}

// cmdBacktest `ferryman backtest` 入口（流注入，cmdVersion 同款可测形）。
func cmdBacktest(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("backtest", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var projects, excludes globList
	cfgPath := fs.String("config", "", "配置文件路径（缺省 = FERRYMAN_CONFIG 环境变量或 ~/ferryman/config.toml）")
	fs.Var(&projects, "projects", "项目正集 glob（fnmatch 语义；可多次；缺省全量）")
	fs.Var(&excludes, "exclude", "排除项目 glob（可多次；排除恒压过正集；实验/压测项目靠它剔）")
	out := fs.String("out", "", "markdown 报告落点（缺省 docs/<当天日期>_等待窗扫参_实验报告.md）")
	asJSON := fs.Bool("json", false, "stdout 输出结构化结果（JSON）+ 退出码；markdown 报告照落 --out")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "未知参数: %q（backtest 不收位置参数）\n", fs.Arg(0))
		return 2
	}
	return runBacktest(backtestOpts{
		ConfigPath: *cfgPath, Projects: projects, Exclude: excludes,
		Out: *out, JSON: *asJSON,
	}, stdout, stderr)
}

// runBacktest 装配主链：config/价格装载 → 票01 Load → 票02 RunSweep →
// 票03 双投影。返回进程退出码。
func runBacktest(o backtestOpts, stdout, stderr io.Writer) int {
	// config 优先级同 daemon：显式 > FERRYMAN_CONFIG > 默认。先归一路径再同时
	// 喂 config.Load 与 prices.LoadPrices——后者缺省不认 FERRYMAN_CONFIG，归一
	// 保证价格表与 config 恒为同一份文件（夹具/沙箱才有单源真义）。
	cfgPath := resolveConfigPath(o.ConfigPath)
	cfg, err := config.Load(cfgPath, false)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	books := prices.LoadPrices(cfgPath)
	econKey := cfg.FerryProvider // report.Run 同款：缺省 = ferry provider 键

	ds, err := backtest.Load(backtest.LoadOptions{
		DataDir: cfg.DataDir(), Projects: o.Projects, Exclude: o.Exclude,
		Books: books, EconKey: econKey,
	})
	if err != nil { // 账本读不到是硬错误（读端不碰盘面）
		fmt.Fprintln(stderr, err)
		return 1
	}

	// 引擎未产出（ttl 未配置/价格不可算/网格构造失败）→ 如实空态：报告照落
	// （BuildView 零值优雅呈现），退出码 1 承载「未得出结论」。
	res, sweepErr := backtest.RunSweep(ds, backtest.SweepOptions{
		Books: books, EconKey: econKey, TTLS: cfg.Heartbeat.TTLS,
	})
	if sweepErr != nil {
		fmt.Fprintln(stderr, "扫参未执行（空态报告照常落盘）:", sweepErr)
		res = nil
	}

	outPath := o.Out
	if outPath == "" {
		outPath = defaultReportPath()
	}
	if dir := filepath.Dir(outPath); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	}
	if err := os.WriteFile(outPath, []byte(backtest.RenderMarkdown(res, ds)), 0o644); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	if o.JSON {
		b, jerr := backtest.RenderJSON(res, ds)
		if jerr != nil { // SweepView 全可序列化，理论不可达；护底线
			fmt.Fprintln(stderr, jerr)
			return 1
		}
		_, _ = stdout.Write(b)
	} else {
		c := ds.Counts
		fmt.Fprintf(stdout, "报告已写入 %s\n", outPath)
		fmt.Fprintf(stdout, "窗计数：全部 %d / 过滤后 %d / 可重放 %d / unknown %d / 不可算 %d\n",
			c.TotalWindows, c.AfterFilterWindows, c.ReplayWindows,
			c.UnknownCount, c.UncomputableCount)
		if c.TotalWindows == 0 {
			fmt.Fprintln(stdout, "空态：账本无 window 行（零窗数据集，网格未重放）")
		} else if c.ReplayWindows == 0 {
			fmt.Fprintln(stdout, "空态：过滤后无可重放窗（全部落入 unknown/不可算桶，网格未重放）")
		}
	}
	if sweepErr != nil {
		return 1
	}
	return 0
}

// resolveConfigPath 显式参数 > FERRYMAN_CONFIG > 默认路径（config.Load 同链；
// 显式归一使价格表与 config 读同一份文件）。
func resolveConfigPath(explicit string) string {
	if explicit != "" {
		return explicit
	}
	if v := strings.TrimSpace(os.Getenv("FERRYMAN_CONFIG")); v != "" {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil {
		home = ""
	}
	return filepath.Join(home, "ferryman", "config.toml")
}

// defaultReportPath docs 实验报告惯例：docs/<当天日期>_等待窗扫参_实验报告.md
// （相对 cwd——仓根运行即落 docs/；日期实跑现取，无硬编码）。
func defaultReportPath() string {
	return filepath.Join("docs", time.Now().Format("20060102")+"_等待窗扫参_实验报告.md")
}
