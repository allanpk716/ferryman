// engine.go — 票07:三态判定(Consider)与执行(AutoApply/Accept/Reject/
// Rollback)及生效值出口(EffectiveThreshold)。
//
// 三态行为(D10):
//
//	manual     只出报告——零应用零提醒(配置值即生效值,人手改 config);
//	recommend  只提醒——待审气泡,人工经 CLI apply 应用(默认档);
//	auto       五护栏判定:全过自动应用+事后通报,任一不过收敛只提醒。
//
// 五护栏(auto 档;①为全包结构面约束,②③④在此判定,⑤在执行侧):
//   - ①只改公式输入不旁路计算器:应用只写 Calibration(TTL 观测替代集),
//     生效值一律 EffectiveThreshold → 票05 policy.SameModelEffectiveThreshold
//     现算;本包不存在直接产出运行阈值的函数。
//   - ②建议值钳 [10, 总结阈值]:越界收敛只提醒(sweep 已钳,此处防御复核)。
//   - ③每上游每周至多一次生效:7 天内有生效(或回滚)记录 → 只提醒。
//   - ④样本不足收敛只提醒:滚动窗摆渡事件 < 门槛(票06 SameModelSampleGate
//     同口径数字)→ 只提醒。
//   - ⑤生效后通报+一键回滚:AutoApplied 通报必发;Rollback 恢复流水里
//     prev_calibration 快照(undo 交换,可再回滚),回滚亦计入频控窗。
//
// 永不自动升档:mode 是各函数只读入参,无任何返回/改写档位的出口;流水
// 事件类型全集固定(无 mode 事件)。升档只认 config.toml [tuning].mode 手改。
package tuning

import (
	"fmt"

	"ferryman/internal/backtest"
	"ferryman/internal/config"
	"ferryman/internal/policy"
	"ferryman/internal/prices"
)

// Decision 三态判定产物(动作+人话依据;无档位字段——判定不改档位)。
type Decision struct {
	Action string // ActReportOnly | ActRemind | ActApply
	Reason string
}

// Notifier 通知缝(CLI 装配 internal/notify 两类托盘气泡;nil 成员=静默)。
// 引擎不 import notify——依赖方向保持 tuning → (CLI) → notify。
type Notifier struct {
	// SuggestionPending 有新建议待审(recommend 提醒 / auto 护栏收敛提醒)。
	SuggestionPending func(sug *Suggestion, reason string)
	// AutoApplied auto 已应用(护栏⑤生效后通报;prev=生效前校准,可 nil)。
	AutoApplied func(sug *Suggestion, prev *Calibration)
}

// Suggestable 扫参产物是否含可应用的建议:样本门槛充足(护栏④同口径)+
// 建议值出口非空(计算器未拒算)。不可建议时报告照落,由调用方决定提醒。
func Suggestable(res *backtest.SameModelSweepResult) bool {
	return res != nil && res.Sample.Sufficient && res.Derived != nil
}

// SuggestionFromSweep 票06 扫参产物 → 建议(ID=装载时点戳+上游,确定性;
// TTLObsMin 原样带过——应用时成为公式输入校准)。不可建议返回 nil。
func SuggestionFromSweep(res *backtest.SameModelSweepResult, upstream, reportPath string,
	currentMin float64, hasCurrent bool, ttlObs []float64, now float64) *Suggestion {
	if !Suggestable(res) {
		return nil
	}
	return &Suggestion{
		ID:          fmt.Sprintf("s%d-%s", int64(res.Counts.LoadedAt), upstream),
		Upstream:    upstream,
		SuggestMin:  res.Derived.SuggestMin,
		CurrentMin:  currentMin,
		HasCurrent:  hasCurrent,
		BestMin:     bestMin(res),
		NetSavings:  bestNet(res),
		FerryEvents: res.Sample.FerryEvents,
		MinEvents:   res.Sample.MinEvents,
		TTLObsMin:   append([]float64(nil), ttlObs...),
		ReportPath:  reportPath,
		CreatedAt:   now,
	}
}

// bestMin/bestNet 扫参最优档与净额(依据列;无 Best=0,如实)。
func bestMin(res *backtest.SameModelSweepResult) float64 {
	if res.Best != nil {
		return res.Best.TMin
	}
	return 0
}

func bestNet(res *backtest.SameModelSweepResult) float64 {
	if res.Best != nil {
		return res.Best.NetSavings
	}
	return 0
}

// Consider 三态判定(纯决策,不落盘不通知):建议在某档位下的处置。
func Consider(mode string, sug *Suggestion, st *State, summarizeMin, now float64) Decision {
	switch mode {
	case ModeManual:
		return Decision{ActReportOnly,
			"manual 档:只出报告,人工改配置(manual 档配置值即生效值)"}
	case ModeRecommend:
		return Decision{ActRemind, fmt.Sprintf(
			"recommend 档:待人工审阅(ferryman tuning apply %s)", sug.ID)}
	case ModeAuto:
		// 护栏④样本不足收敛只提醒(票06 SameModelSampleGate 同口径)。
		if sug.MinEvents <= 0 || sug.FerryEvents < sug.MinEvents {
			return Decision{ActRemind, fmt.Sprintf(
				"护栏④样本不足:滚动窗摆渡事件 %d < 门槛 %d,收敛为只提醒",
				sug.FerryEvents, sug.MinEvents)}
		}
		// 护栏②建议值钳 [10, 总结阈值](sweep 已钳,防御复核)。
		if sug.SuggestMin < config.SameModelMinCeilMin || sug.SuggestMin > summarizeMin {
			verdict := "低于下限"
			if sug.SuggestMin > summarizeMin {
				verdict = "超出总结阈值"
			}
			return Decision{ActRemind, fmt.Sprintf(
				"护栏②建议值 %.1f 分钟%s(合法域 [%.0f, %.0f] 分钟),收敛为只提醒",
				sug.SuggestMin, verdict, config.SameModelMinCeilMin, summarizeMin)}
		}
		// 护栏③每上游每周至多一次生效(回滚也计入,防来回翻转)。
		if last := st.LastEffective[sug.Upstream]; last > 0 && now-last < FrequencyWindowS {
			return Decision{ActRemind, fmt.Sprintf(
				"护栏③频控:%s 上游 %.1f 天内已有生效记录(每上游每周至多一次生效),收敛为只提醒",
				sug.Upstream, (now-last)/86400)}
		}
		return Decision{ActApply, fmt.Sprintf(
			"护栏全过:建议值 %.1f 分钟(现值 %.1f)、净节省 %.2f、窗内摆渡事件 %d/%d——护栏内自动应用",
			sug.SuggestMin, sug.CurrentMin, sug.NetSavings, sug.FerryEvents, sug.MinEvents)}
	default:
		// 未知档位 = 最保守:只出报告,绝不应用(config.Validate 应已拦截,防御)。
		return Decision{ActReportOnly, fmt.Sprintf(
			"未知调参档位 %q:按最保守处理,只出报告不应用", mode)}
	}
}

// AutoApply 引擎自动处置主入口(manual/recommend/auto 统一走此判定):
// manual → 零动作;recommend → 待审提醒;auto → 护栏通过则落 auto_applied
// 事件+写校准投影+通报,不过则收敛只提醒。永不改写档位(只读入参)。
func (s *Store) AutoApply(sug *Suggestion, mode string, summarizeMin, now float64,
	n *Notifier) (Decision, error) {
	st, err := s.Replay()
	if err != nil {
		return Decision{}, err
	}
	d := Consider(mode, sug, st, summarizeMin, now)
	switch d.Action {
	case ActRemind:
		if n != nil && n.SuggestionPending != nil {
			n.SuggestionPending(sug, d.Reason)
		}
		return d, nil
	case ActApply:
		prev := st.Calib[sug.Upstream]
		cal := &Calibration{Upstream: sug.Upstream,
			TTLObsMin:  append([]float64(nil), sug.TTLObsMin...),
			SuggestMin: sug.SuggestMin, SourceID: sug.ID, AppliedAt: now}
		if err := s.Append(&Event{TS: now, Type: EvAutoApplied, ID: sug.ID,
			Upstream: sug.Upstream, Mode: mode, Suggestion: sug,
			Calibration: cal, PrevCalibration: prev}); err != nil {
			return d, err
		}
		if err := s.writeCalibFile(cal); err != nil {
			return d, err
		}
		if n != nil && n.AutoApplied != nil { // 护栏⑤:生效后必须通报
			n.AutoApplied(sug, prev)
		}
		return d, nil
	default: // ActReportOnly(manual/未知档位):零应用零提醒
		return d, nil
	}
}

// Accept 人工接受(CLI apply;人工写路径):不做频控/样本门槛(人的显式
// 动作不拦),但校验在案与状态、钳位合法(防御)。落 accepted 事件+写校准投影。
func (s *Store) Accept(id, mode string, summarizeMin, now float64) error {
	st, err := s.Replay()
	if err != nil {
		return err
	}
	sug := st.Suggestions[id]
	if sug == nil {
		return fmt.Errorf("建议 %s 不在调参流水中(先 ferryman tuning sweep 产出或 status 查看)", id)
	}
	switch st.Status[id] {
	case StAccepted:
		return fmt.Errorf("建议 %s 已在接受态,无需重复接受", id)
	case StRejected:
		return fmt.Errorf("建议 %s 已被拒绝;如要采纳请等下次扫参产出新建议", id)
	case StAutoApplied:
		return fmt.Errorf("建议 %s 已由 auto 档自动应用;如要撤销请回滚:ferryman tuning rollback %s", id, sug.Upstream)
	}
	if sug.SuggestMin < config.SameModelMinCeilMin || sug.SuggestMin > summarizeMin {
		return fmt.Errorf("护栏②:建议值 %.1f 分钟越界 [%.0f, %.0f] 分钟,拒绝接受",
			sug.SuggestMin, config.SameModelMinCeilMin, summarizeMin)
	}
	prev := st.Calib[sug.Upstream]
	cal := &Calibration{Upstream: sug.Upstream,
		TTLObsMin: append([]float64(nil), sug.TTLObsMin...),
		SuggestMin: sug.SuggestMin, SourceID: sug.ID, AppliedAt: now}
	if err := s.Append(&Event{TS: now, Type: EvAccepted, ID: id,
		Upstream: sug.Upstream, Mode: mode, Suggestion: sug,
		Calibration: cal, PrevCalibration: prev}); err != nil {
		return err
	}
	return s.writeCalibFile(cal)
}

// Reject 人工拒绝(CLI reject):落 rejected 事件;已生效的建议不可拒绝
// (撤销走回滚)。
func (s *Store) Reject(id, reason string, now float64) error {
	st, err := s.Replay()
	if err != nil {
		return err
	}
	sug := st.Suggestions[id]
	if sug == nil {
		return fmt.Errorf("建议 %s 不在调参流水中(先 ferryman tuning status 查看)", id)
	}
	switch st.Status[id] {
	case StRejected:
		return fmt.Errorf("建议 %s 已是拒绝态", id)
	case StAccepted, StAutoApplied:
		return fmt.Errorf("建议 %s 已生效,不可拒绝;如要撤销请回滚:ferryman tuning rollback %s", id, sug.Upstream)
	}
	return s.Append(&Event{TS: now, Type: EvRejected, ID: id,
		Upstream: sug.Upstream, Reason: reason, Suggestion: sug})
}

// Rollback 一键回滚(护栏⑤):恢复该上游最近一次生效前的公式输入快照,
// 落 rollback_applied 事件并同步校准投影;返回还原的快照(nil=还原到无校准)。
// 回滚计入频控窗;undo 交换——回滚前值成为新回滚源,可再回滚还原。
func (s *Store) Rollback(upstream, mode string, now float64) (*Calibration, error) {
	st, err := s.Replay()
	if err != nil {
		return nil, err
	}
	cur, hasCur := st.Calib[upstream]
	prev := st.PrevCalib[upstream]
	if !hasCur && prev == nil {
		return nil, fmt.Errorf("%s 上游没有可回滚的生效记录(调参流水中无接受/自动应用事件)", upstream)
	}
	if err := s.Append(&Event{TS: now, Type: EvRollbackApplied, Upstream: upstream,
		Mode: mode, Calibration: prev, PrevCalibration: cur}); err != nil {
		return nil, err
	}
	if prev != nil {
		if err := s.writeCalibFile(prev); err != nil {
			return nil, err
		}
	} else if err := s.deleteCalibFile(upstream); err != nil { // 还原无校准态:显式按上游删投影
		return nil, err
	}
	return prev, nil
}

// EffectiveThreshold 三列之"生效值"(护栏①执行面):当前公式输入校准并进
// 观测后调票05 SameModelEffectiveThreshold 现算——生效值只出自计算器,本包
// 不存在第二份实现;manual 档校准自然不参与(配置值即生效值,票05 出口语义)。
func (s *Store) EffectiveThreshold(c *config.Config, books map[string]prices.PriceBook,
	upstream string, obs policy.SameModelObs) (policy.SameModelResult, error) {
	st, err := s.Replay()
	if err != nil {
		return policy.SameModelResult{}, err
	}
	if cal := st.Calib[upstream]; cal != nil && len(cal.TTLObsMin) > 0 {
		obs.TTLObsMin = append([]float64(nil), cal.TTLObsMin...)
	}
	return policy.SameModelEffectiveThreshold(c, books, upstream, obs)
}
