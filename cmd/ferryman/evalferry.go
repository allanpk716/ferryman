// evalferry.go — 票04:盲评生成工具 CLI(`ferryman eval-ferry`)。
//
// 用法:
//
//	ferryman eval-ferry --provider <名> --n <样本数> --out <目录>
//	                    [--handoffs 目录] [--config 路径] [--timeout 秒]
//
// 取最近 N 份交接的骨架素材(现状=交接 MD 全文,产物注明「全文模式」),
// 逐样本经指定供应商(票02 供应商表,openai 兼容协议走既有 Chat)生成模型
// 叙事,一样本一文件对(骨架/叙事并排,文件名含时间戳与会话短 ID)落
// --out,供人工盲评对照——人工评分环节不在本票。核心逻辑在
// internal/ferry.RunEvalFerry(含样本选取与报错契约),本文件只做旗标装配
// 与结果分流。
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"ferryman/internal/ferry"
)

// defaultHandoffDir 交接目录缺省(生产数据根下的 handoffs/)。
func defaultHandoffDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join("ferryman", "handoffs")
	}
	return filepath.Join(home, "ferryman", "handoffs")
}

// cmdEvalFerry eval-ferry 子命令入口:旗标解析 → 供应商表装配(票02
// LoadProviders,config 优先级同 tuning/backtest:显式 --config >
// FERRYMAN_CONFIG > ~/ferryman/config.toml)→ RunEvalFerry → 摘要分流。
// 退出码:0=全成;1=硬错误或存在失败样本;2=用法错。
func cmdEvalFerry(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("eval-ferry", flag.ExitOnError)
	provider := fs.String("provider", "", "供应商名（[providers.*] 键；必填）")
	n := fs.Int("n", 10, "样本数：取最近 N 份交接（按时间倒序；不足 N 如实全取并注明）")
	out := fs.String("out", "", "输出目录（必填；一样本一文件对：骨架/叙事并排）")
	handoffs := fs.String("handoffs", defaultHandoffDir(), "交接目录（缺省 = ~/ferryman/handoffs）")
	cfgPath := fs.String("config", "", "配置文件路径（缺省 = FERRYMAN_CONFIG 或 ~/ferryman/config.toml）")
	timeout := fs.Float64("timeout", 600, "单样本生成超时（秒）")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *out == "" {
		fmt.Fprintln(stderr, "用法: ferryman eval-ferry --provider <名> --n <样本数> --out <目录>"+
			" [--handoffs 目录] [--config 路径] [--timeout 秒]")
		return 2
	}
	providers, err := ferry.LoadProviders(resolveConfigPath(*cfgPath))
	if err != nil {
		fmt.Fprintln(stderr, "供应商表加载失败:", err)
		return 1
	}
	res, err := ferry.RunEvalFerry(ferry.EvalOptions{
		ProviderName: *provider,
		Providers:    providers,
		N:            *n,
		HandoffDir:   *handoffs,
		OutDir:       *out,
		TimeoutS:     *timeout,
	})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintf(stdout, "盲评生成完成:请求 %d 份,可用 %d 份,实取 %d 份;文件对 %d,失败 %d\n",
		res.Requested, res.Total, res.Taken, len(res.Pairs), len(res.Failures))
	if res.Truncated {
		fmt.Fprintf(stdout, "注: 样本不足——请求 %d 份,实取 %d 份（已如实全取）\n", res.Requested, res.Taken)
	}
	for _, p := range res.Pairs {
		fmt.Fprintf(stdout, "  %s\n    骨架: %s\n    叙事: %s\n",
			p.Sample.FileName, p.SkeletonPath, p.NarrativePath)
	}
	for _, f := range res.Failures {
		fmt.Fprintf(stdout, "  失败 %s: %s\n", f.Sample.FileName, f.Err)
	}
	if len(res.Failures) > 0 {
		return 1
	}
	return 0
}
