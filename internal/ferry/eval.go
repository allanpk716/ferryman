// eval.go — 票04:盲评生成工具(`ferryman eval-ferry` 子命令的入口逻辑)。
//
// 职责:取最近 N 份交接(生产在 ~/ferryman/handoffs/,命名
// YYYYMMDD_HHMMSS_<短ID>.md;样本目录可注入,测试给临时目录),把每份的
// 骨架素材经指定供应商生成模型叙事,产物=一样本一文件对(骨架/叙事并排,
// 文件名含时间戳与会话短 ID,可溯源到源交接),供人工盲评对照——人工评分
// 环节不在本票。
//
// 骨架素材口径:仓内骨架抽取(extract.Extract)面向会话转录 jsonl,对交接
// MD 不适用;交接 MD 本身=头部+注入层+模型叙事,不含骨架段。按票面兜底
// 条款,素材退化为交接 MD 全文,产物头注明「全文模式」。
//
// 供应商走票02 供应商表(LoadProviders/Provider);经 chainCall(0, pr) 按
// Provider.Protocol 分派——openai 兼容走既有 Chat、anthropic 走票03 适配器
// (SystemPrompt 原文为 system,素材为 user,max_tokens 同生产 L1 口径=
// PromptReserve),与生产链同形（终局修复2：盲评门对 anthropic 档可用）。
//
// 报错契约:供应商缺名/表缺名/无可用样本 → 硬错误(不静默空跑、
// 不发上游调用);单样本生成失败 → 记账跳过不炸整批,由调用方按结果分流。
package ferry

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// evalFullTextMode 全文模式注明(票面兜底条款:骨架抽取现状不可复用于交接
// MD 时,素材=交接 MD 全文并在产物里注明)。
const evalFullTextMode = "全文模式（骨架抽取器面向会话转录 jsonl,对交接 MD 不适用;素材=交接 MD 全文）"

// EvalSample 一份盲评样本:一份交接文件及其素材。
type EvalSample struct {
	FileName  string    // 交接文件名(含 .md,溯源用)
	SessionID string    // 会话短 ID(文件名末段)
	Stamp     string    // 文件名时间戳段(YYYYMMDD_HHMMSS;文件名不合契约时取 mtime 兜底)
	Time      time.Time // 排序键(文件名时间戳优先,兜底 mtime)
	Path      string    // 绝对路径
	Material  string    // 骨架素材(现状=交接 MD 全文,全文模式)
}

// EvalPair 一个样本的产物文件对(骨架/叙事并排,同 stem)。
type EvalPair struct {
	Sample        EvalSample
	SkeletonPath  string // <stamp>_<短ID>_骨架.md
	NarrativePath string // <stamp>_<短ID>_叙事.md
}

// EvalFailure 单样本生成失败记账(样本 + 一句原因)。
type EvalFailure struct {
	Sample EvalSample
	Err    string
}

// EvalResult 一次盲评生成的账:请求/可用/实取计数 + 产物与失败清单。
type EvalResult struct {
	Requested int
	Total     int // 样本目录可用交接总数
	Taken     int
	Truncated bool // 可用 < 请求(不足 N 已如实全取)
	Pairs     []EvalPair
	Failures  []EvalFailure
}

// EvalOptions 盲评生成入参。Providers=票02 供应商表(生产由 cmd 侧
// LoadProviders 装配,测试注入假上游指向);TimeoutS<=0 取 600s;MaxTokens<=0
// 取 PromptReserve(与生产 L1 单发同口径)。
type EvalOptions struct {
	ProviderName string
	Providers    map[string]Provider
	N            int
	HandoffDir   string
	OutDir       string
	TimeoutS     float64
	MaxTokens    int
}

// PickRecentHandoffs 样本目录取最近 N 份交接(按文件名时间戳倒序,文件名不
// 合契约的退 mtime 排序)。返回(样本按序, 可用总数, error):不足 N 时如实
// 全取,总数上抛由调用方注明;目录无 .md 交接 → error(不静默空跑)。
func PickRecentHandoffs(dir string, n int) ([]EvalSample, int, error) {
	if n < 1 {
		return nil, 0, fmt.Errorf("样本数须 ≥ 1,得 %d", n)
	}
	des, err := os.ReadDir(dir)
	if err != nil {
		return nil, 0, fmt.Errorf("样本目录不可读: %w", err)
	}
	all := make([]EvalSample, 0, len(des))
	for _, de := range des {
		name := de.Name()
		if de.IsDir() || !strings.HasSuffix(name, ".md") {
			continue // index.json 等非交接不进样本
		}
		path := filepath.Join(dir, name)
		s := EvalSample{FileName: name, Path: path}
		base := strings.TrimSuffix(name, ".md")
		parts := strings.Split(base, "_")
		if len(parts) >= 3 { // 生产契约 YYYYMMDD_HHMMSS_<短ID>
			stamp := parts[0] + "_" + parts[1]
			if ts, perr := time.ParseInLocation("20060102_150405", stamp, time.Local); perr == nil {
				s.Stamp = stamp
				s.Time = ts
				s.SessionID = strings.Join(parts[2:], "_")
			}
		}
		if s.SessionID == "" { // 文件名不合契约:mtime 兜底,短 ID=去后缀全名
			fi, ferr := de.Info()
			if ferr != nil {
				return nil, 0, fmt.Errorf("样本信息不可读(%s): %w", name, ferr)
			}
			s.Time = fi.ModTime()
			s.Stamp = s.Time.Format("20060102_150405")
			s.SessionID = base
		}
		raw, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil, 0, fmt.Errorf("样本不可读(%s): %w", name, rerr)
		}
		s.Material = string(raw)
		all = append(all, s)
	}
	if len(all) == 0 {
		return nil, 0, fmt.Errorf("样本目录无可用交接(目录 %s 下无 .md 文件)", dir)
	}
	sort.Slice(all, func(i, j int) bool { // 时间倒序
		if !all[i].Time.Equal(all[j].Time) {
			return all[i].Time.After(all[j].Time)
		}
		return all[i].FileName > all[j].FileName // 同刻稳定:名大者前
	})
	taken := all
	if len(taken) > n {
		taken = taken[:n]
	}
	return taken, len(all), nil
}

// RunEvalFerry 盲评生成主流程:供应商解析 → 样本选取 → 逐样本按协议分派生成
// 叙事并落文件对（终局修复2：经 chainCall 分派，anthropic 档走适配器，与
// 生产链同形——含本地级拨号限时语义）。硬错误(供应商缺名/表缺名/无样本)
// → error 且不发任何上游调用;单样本失败 → 记账进 Failures,整批继续。
func RunEvalFerry(opts EvalOptions) (*EvalResult, error) {
	if strings.TrimSpace(opts.ProviderName) == "" {
		return nil, fmt.Errorf("缺少供应商名(--provider 必填,[providers.*] 键)")
	}
	pr, ok := opts.Providers[opts.ProviderName]
	if !ok {
		if len(opts.Providers) == 0 {
			return nil, fmt.Errorf("供应商表为空(配置无 [providers.*] 条目),供应商 %q 无法解析", opts.ProviderName)
		}
		names := make([]string, 0, len(opts.Providers))
		for k := range opts.Providers {
			names = append(names, k)
		}
		sort.Strings(names)
		return nil, fmt.Errorf("供应商 %q 不在供应商表中(可用: %s)", opts.ProviderName, strings.Join(names, ", "))
	}

	samples, total, err := PickRecentHandoffs(opts.HandoffDir, opts.N)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(opts.OutDir, 0o755); err != nil {
		return nil, fmt.Errorf("输出目录不可建: %w", err)
	}

	timeoutS := opts.TimeoutS
	if timeoutS <= 0 {
		timeoutS = 600
	}
	maxTokens := opts.MaxTokens
	if maxTokens <= 0 {
		maxTokens = PromptReserve
	}
	call := chainCall(0, pr) // 协议分派+拨号语义与生产链同形（终局修复2）

	res := &EvalResult{Requested: opts.N, Total: total, Taken: len(samples),
		Truncated: total < opts.N}
	truncNote := ""
	if res.Truncated {
		truncNote = fmt.Sprintf("- 注: 样本不足——请求 %d 份,实取 %d 份（已如实全取）", opts.N, len(samples))
	}
	for _, s := range samples {
		narrative, _, cerr := call(pr, SystemPrompt, s.Material, timeoutS, maxTokens)
		if cerr != nil {
			res.Failures = append(res.Failures, EvalFailure{Sample: s, Err: cerr.Error()})
			continue
		}
		if strings.TrimSpace(narrative) == "" {
			res.Failures = append(res.Failures, EvalFailure{Sample: s, Err: "模型返回空叙事——不落空文件"})
			continue
		}
		stem := filepath.Join(opts.OutDir, s.Stamp+"_"+s.SessionID)
		skPath := stem + "_骨架.md"
		nrPath := stem + "_叙事.md"
		skBody := evalFileHeader("素材", s.SessionID, s.FileName,
			[]string{evalFullTextMode, truncNote}) + s.Material + "\n"
		nrBody := evalFileHeader("叙事", s.SessionID, s.FileName,
			[]string{"- 供应商: " + pr.Name + " · 模型: " + pr.Model, evalFullTextMode, truncNote}) +
			narrative + "\n"
		if werr := os.WriteFile(skPath, []byte(skBody), 0o644); werr != nil {
			res.Failures = append(res.Failures, EvalFailure{Sample: s, Err: "骨架件落盘失败: " + werr.Error()})
			continue
		}
		if werr := os.WriteFile(nrPath, []byte(nrBody), 0o644); werr != nil {
			res.Failures = append(res.Failures, EvalFailure{Sample: s, Err: "叙事件落盘失败: " + werr.Error()})
			continue
		}
		res.Pairs = append(res.Pairs, EvalPair{Sample: s, SkeletonPath: skPath, NarrativePath: nrPath})
	}
	return res, nil
}

// evalFileHeader 产物头:种类行(素材/叙事)+ 来源 + extra 行(模式/供应商/
// 截断注;空串行跳过)+ 生成时刻。头后空一行接正文。
func evalFileHeader(kind, sid, source string, extra []string) string {
	lines := []string{
		"[Ferryman 盲评" + kind + " · 会话 " + sid + "]",
		"- 来源: " + source,
	}
	for _, e := range extra {
		if e != "" {
			lines = append(lines, e)
		}
	}
	lines = append(lines, "- 生成: "+time.Now().Format("2006-01-02 15:04"))
	return strings.Join(lines, "\n") + "\n\n"
}
