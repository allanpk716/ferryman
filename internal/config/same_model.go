// same_model.go — 票01（同模型摆渡八张竖切 · 配置面地基）：[ferry.same_model]
// 与 [tuning] 两节的类型、解析辅助、钳位校验与 doctor 四检查判定。
//
// 决策依据：ADR-0015 + 夜链 decisions.md：
//   - D2  触发时机：冷启动种子 20 分钟，计算器钳位 [10min, 总结阈值]；不变量链
//         同模型 ≤ 总结 ≤ 拦截（总结 < 拦截由 Validate 既有分支保证，本文件只
//         补同模型一环）。
//   - D5  默认 off（E1 评测候选，出结果前不当默认）。
//   - D6  上游白名单按 [dock.upstreams] 条目键登记；白名单预置 ≠ 启用——
//         每上游须先过「追加重放实跳臂」四条成功标准才置启用。
//   - D10 调参三态 manual|recommend|auto，默认 recommend，永不自动升档；
//         护栏④样本门槛 = 滚动 30 天 <30 个摆渡事件。
//   - D13 配置值语义：manual 档配置值=生效值；recommend/auto 档为生效值上限
//         （CeilingFor 即生效上限单源，计算器票取用）。
package config

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"ferryman/internal/clock"
	"ferryman/internal/prices"
)

// TuningModes 调参三态合法值（D10；枚举键 UI 友好：扁平、可校验）。
var TuningModes = [...]string{"manual", "recommend", "auto"}

// 同模型阈值钳位常量与调参缺省（D2/D10）。
const (
	SameModelMinCeilMin = 10.0 // 钳位下限（分钟）
	SameModelSeedMin    = 20.0 // 冷启动种子（分钟；E0a 实测）
	TuningWindowDays    = 30   // 滚动观察窗（天；护栏④）
	TuningMinEvents     = 30   // 样本不足门槛（摆渡事件数；护栏④）
)

// SameModelCfg [ferry.same_model] 同模型摆渡节（票01）。threshold 是全局
// 上限/种子，每上游 ceiling 覆盖（review_blocks F4：ceiling 全局单值+按上游
// 覆盖的 schema 在本票钉死——三层供给的「手编上限」层，计算器只能在其内现算）。
type SameModelCfg struct {
	Enabled      bool
	Upstreams    []string           // 白名单（= [dock.upstreams] 条目键）；预置 ≠ 启用
	ThresholdMin float64            // 触发阈值（分钟）：全局上限/冷启动种子
	CeilingMin   map[string]float64 // 每上游覆盖（分钟）；缺省回落 ThresholdMin
}

// CeilingFor 该上游的生效阈值上限（分钟，D13 语义缝）：manual 档配置值即
// 生效值；recommend/auto 档为上限、计算器在其内现算。每上游覆盖优先，
// 缺省回落全局种子值。
func (sm *SameModelCfg) CeilingFor(upstream string) float64 {
	if v, ok := sm.CeilingMin[upstream]; ok {
		return v
	}
	return sm.ThresholdMin
}

// TuningCfg [tuning] 调参三态节（票01，D10）。
type TuningCfg struct {
	Mode       string // manual | recommend | auto
	WindowDays int    // 滚动观察窗（天）
	MinEvents  int    // 样本不足门槛（窗内摆渡事件数；不足则 auto 收敛只提醒）
}

// parseSameModelSection [ferry.same_model] 子节解析（[ferry] 节处理器内调）。
// 节内缺字段回落默认（off/种子 20min/空表）；类型不对上抛（Load 拒启）。
func parseSameModelSection(sm map[string]any) (SameModelCfg, error) {
	out := SameModelCfg{
		Enabled:      pyBool(get(sm, "enabled", false)),
		Upstreams:    []string{},
		ThresholdMin: SameModelSeedMin,
		CeilingMin:   map[string]float64{},
	}
	thr, err := pyFloat(get(sm, "threshold_min", SameModelSeedMin))
	if err != nil {
		return out, err
	}
	out.ThresholdMin = thr
	if rawUps, ok := sm["upstreams"]; ok {
		arr, ok := rawUps.([]any)
		if !ok {
			return out, errors.New("config: ferry.same_model.upstreams 不是数组")
		}
		for _, u := range arr {
			out.Upstreams = append(out.Upstreams, pyStr(u))
		}
	}
	if rawCeil, ok := sm["ceiling"]; ok {
		ct, err := asTable(rawCeil, "ferry.same_model.ceiling")
		if err != nil {
			return out, err
		}
		for k, v := range ct {
			fv, err := pyFloat(v)
			if err != nil {
				return out, fmt.Errorf("config: ferry.same_model.ceiling.%s: %w", k, err)
			}
			out.CeilingMin[k] = fv
		}
	}
	return out, nil
}

// validateSameModelClamp 同模型阈值钳位校验（票01，D2）：全局与每上游覆盖
// 均须落在 [SameModelMinCeilMin, 总结阈值]。人话文案带三值与不变量链；
// 越界/倒挂即拒启（调用方仅在 enabled 时调用——休眠键不拦，doctor 兜底提示）。
func (c *Config) validateSameModelClamp() []string {
	var problems []string
	sumMin := c.Thresholds.SummarizeS / 60.0
	check := func(key string, v float64) {
		name := "ferry.same_model.threshold_min"
		if key != "" {
			name = "ferry.same_model.ceiling[\"" + key + "\"]"
		}
		if v < SameModelMinCeilMin {
			problems = append(problems, fmt.Sprintf(
				"%s=%s 分钟低于钳位下限 %s 分钟（不变量链:同模型 ≤ 总结 ≤ 拦截）",
				name, pyFloatStr(v), pyFloatStr(SameModelMinCeilMin)))
		}
		if v > sumMin {
			problems = append(problems, fmt.Sprintf(
				"%s=%s 分钟超出钳位上限（总结阈值 %s 分钟=%ss；不变量链:同模型 ≤ 总结 ≤ 拦截）",
				name, pyFloatStr(v), pyFloatStr(sumMin), pyFloatStr(c.Thresholds.SummarizeS)))
		}
	}
	check("", c.SameModel.ThresholdMin)
	names := make([]string, 0, len(c.SameModel.CeilingMin))
	for k := range c.SameModel.CeilingMin {
		names = append(names, k)
	}
	slices.Sort(names) // 覆盖项按名字排序：文案顺序确定
	for _, k := range names {
		check(k, c.SameModel.CeilingMin[k])
	}
	return problems
}

// DoctorCheck doctor 单检查项判定（票01）：判定与文案单源本包；人面/agent 面
// 换装 CheckResult 在 internal/installer（公式单源，照 dock 检查组先例）。
type DoctorCheck struct {
	Name   string // 稳定检查项名（doctor 打印序 = 返回序）
	OK     bool
	Detail string
	Skip   bool // true = 检查目标未装配 → not_checked（如实标注，绝不伪造）
}

// ArmVerdictResolver 实跳臂结论查询缝（票04 启用门实施后由 doctor 装配注入）：
// 该上游是否已有追加重放实跳臂结论、结论是否为「已启用」。nil = 状态源未装配
// → 按无结论对待（启用硬门槛：无结论 = 未启用，D6/ADR-0015）。
type ArmVerdictResolver func(upstream string) (hasVerdict, enabled bool)

// bookFor 价格本选法（report.bookFor / watcher 同口径）：key 精确命中 → 取之；
// 仅一本 → 取唯一本；再否则不可算。本包本地副本——config 不 import report
//（config 保持叶子包）。
func bookFor(books map[string]prices.PriceBook, key string) *prices.PriceBook {
	if len(books) == 0 {
		return nil
	}
	if key != "" {
		if b, ok := books[key]; ok {
			return &b
		}
	}
	if len(books) == 1 {
		for _, b := range books {
			return &b
		}
	}
	return nil
}

// SameModelDoctorChecks 同模型/调参配置矛盾四查（票01，ADR-0015 Consequences）：
//
//	same_model_whitelist   same_model 开而白名单空或全未启用（enabled 才出）；
//	same_model_arm_verdict 白名单条目无实跳臂结论（白名单非空才出）；
//	tuning_price_pcache    tuning=auto 而价格表缺 p_cache（auto 才出）；
//	same_model_clamp       同模型阈值与总结阈值钳位冲突（特性面被碰才出）。
//
// 默认配置（off/recommend/空白名单）零新增检查行——存量用户 doctor 输出零漂移。
// books 仅 #3 使用；books 为 nil = 价格表未装配 → #3 显式 Skip（not_checked，
// 不伪造）。行序固定：whitelist → arm_verdict → price_pcache → clamp。
func SameModelDoctorChecks(c *Config, books map[string]prices.PriceBook, arm ArmVerdictResolver) []DoctorCheck {
	var out []DoctorCheck
	sm := &c.SameModel
	smTouched := sm.Enabled || len(sm.Upstreams) > 0
	if !smTouched && c.Tuning.Mode != "auto" {
		return out
	}

	// #1 开而白名单空或全未启用（enabled 才出——关着不可能触发，不制造噪音）
	if sm.Enabled {
		switch {
		case len(sm.Upstreams) == 0:
			out = append(out, DoctorCheck{Name: "same_model_whitelist", OK: false,
				Detail: "same_model 已开启但白名单为空——同模型档不会触发任何上游" +
					"（[ferry.same_model].upstreams 登记 [dock.upstreams] 条目键）"})
		default:
			enabledN := 0
			for _, u := range sm.Upstreams {
				if arm != nil {
					if _, en := arm(u); en {
						enabledN++
					}
				}
			}
			if enabledN == 0 {
				out = append(out, DoctorCheck{Name: "same_model_whitelist", OK: false,
					Detail: "same_model 已开启但白名单条目均未启用——白名单预置≠启用" +
						"（追加重放实跳臂四条成功标准全过才置启用，ADR-0015 硬门槛）"})
			} else {
				out = append(out, DoctorCheck{Name: "same_model_whitelist", OK: true,
					Detail: fmt.Sprintf("same_model 白名单 %d 条、其中 %d 条已启用",
						len(sm.Upstreams), enabledN)})
			}
		}
	}

	// #2 白名单条目无实跳臂结论（白名单非空才出；arm 缝未装配按无结论对待）
	if len(sm.Upstreams) > 0 {
		var noVerdict []string
		for _, u := range sm.Upstreams {
			if arm == nil {
				noVerdict = append(noVerdict, u)
				continue
			}
			if has, _ := arm(u); !has {
				noVerdict = append(noVerdict, u)
			}
		}
		if len(noVerdict) > 0 {
			out = append(out, DoctorCheck{Name: "same_model_arm_verdict", OK: false,
				Detail: "白名单条目无追加重放实跳臂结论（预置≠启用）: " +
					strings.Join(noVerdict, "、") + "——实跳臂结论回写后才可置启用（ADR-0015）"})
		} else {
			out = append(out, DoctorCheck{Name: "same_model_arm_verdict", OK: true,
				Detail: fmt.Sprintf("白名单 %d 条均有实跳臂结论", len(sm.Upstreams))})
		}
	}

	// #3 tuning=auto 而价格表缺 p_cache（auto 才出；计算器拒算 = 无缓存经济红线）
	if c.Tuning.Mode == "auto" {
		switch {
		case len(sm.Upstreams) == 0:
			// 白名单空 = 无需现算：先于 books 装配判定（与价格表在不在无关）
			out = append(out, DoctorCheck{Name: "tuning_price_pcache", OK: true,
				Detail: "tuning=auto 但同模型白名单为空——无同模型阈值需现算"})
		case books == nil:
			out = append(out, DoctorCheck{Name: "tuning_price_pcache", OK: true, Skip: true,
				Detail: "价格表未装配——tuning=auto 的 p_cache 检查未执行（如实标注不伪造）"})
		default:
			var bad []string
			for _, u := range sm.Upstreams {
				book := bookFor(books, u)
				if book == nil || len(book.Versions) == 0 {
					bad = append(bad, u+"（无法定位 [prices.*] 价格表）")
					continue
				}
				pv := book.At(clock.Now())
				if pv == nil { // 早于一切版本 → 回落末版（watcher/report 同款）
					pv = &book.Versions[len(book.Versions)-1]
				}
				if pv.PCache == nil {
					bad = append(bad, u+"（现价无 p_cache）")
				}
			}
			if len(bad) > 0 {
				out = append(out, DoctorCheck{Name: "tuning_price_pcache", OK: false,
					Detail: "tuning=auto 而价格表缺 p_cache——计算器将拒算同模型阈值" +
						"（无缓存经济红线）: " + strings.Join(bad, "、")})
			} else {
				out = append(out, DoctorCheck{Name: "tuning_price_pcache", OK: true,
					Detail: fmt.Sprintf("tuning=auto：白名单 %d 条价格表现价均有 p_cache",
						len(sm.Upstreams))})
			}
		}
	}

	// #4 同模型阈值与总结阈值钳位冲突（特性面被碰才出）。enabled 时 Validate
	// 已拒启，此处是 agent 面兜底；关闭态为休眠值——启用前须修。
	if smTouched {
		if probs := c.validateSameModelClamp(); len(probs) > 0 {
			suffix := "（当前关闭——休眠值，启用前须修）"
			if sm.Enabled {
				suffix = "——enabled 时将拒绝启动"
			}
			out = append(out, DoctorCheck{Name: "same_model_clamp", OK: false,
				Detail: strings.Join(probs, "；") + suffix})
		} else {
			out = append(out, DoctorCheck{Name: "same_model_clamp", OK: true,
				Detail: fmt.Sprintf("同模型阈值钳位无冲突：%s 分钟 ∈ [%s, %s]（总结阈值）",
					pyFloatStr(sm.ThresholdMin),
					pyFloatStr(SameModelMinCeilMin),
					pyFloatStr(c.Thresholds.SummarizeS/60.0))})
		}
	}
	return out
}
