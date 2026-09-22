package daemon

// watcher_same_model_effective_test.go — 票08:票07 遗留接线钉子——同模型触发
// 阈值读取改经 tuning.Store.EffectiveThreshold(三态生效值唯一出口,票05 计算
// 器单源),不再直读 CeilingFor。
//
// 验收对照(票08「票07 遗留接线」):
//   - recommend 档无校准:生效值=计算器种子路径现算值(min(20,总结阈值) 钳位),
//     不再等于配置值——闲置越过生效值即进门序,遥测 threshold_min 记生效值;
//   - 现算失败(校准并进 TTL 观测但无闲置观测 → bad_obs 等):回落配置值
//     CeilingFor 并告警一次——宁可保守回落,绝不阻断守望(异常吞掉总纪律);
//   - manual 档:出口语义=配置值即生效值,行为与配置值读取一致(钉回归)。
//
// 确定性说明:测试配置的总结阈值取 25 分钟(钳位合法域),价格本/调参库全部
// 指向临时目录——不读真实 ~/ferryman,机器状态零依赖。

import (
	"testing"

	"ferryman/internal/config"
	"ferryman/internal/ledger"
	"ferryman/internal/prices"
	"ferryman/internal/tuning"
)

// smEffCfg 生效值接线测试配置:总结阈值 25 分钟(钳位合法域)、同模型配置值
// 30 分钟(高于种子 20,使"生效值≠配置值"可观测)、recommend 档。
func smEffCfg() *config.Config {
	cfg := smGateCfg()
	cfg.Thresholds.SummarizeS = 25 * 60
	cfg.SameModel.ThresholdMin = 30
	cfg.Tuning.Mode = "recommend"
	return cfg
}

// smZhipuBooks 单本 zhipu 价格表(键=上游条目键;p_cache 在场,计算器可算)。
func smZhipuBooks() map[string]prices.PriceBook {
	pc := 1.7
	return map[string]prices.PriceBook{"zhipu": {Key: "zhipu", Unit: "智谱积分",
		Per: 10000,
		Versions: []prices.PriceVersion{{EffectiveFrom: "2026-09-01",
			PIn: 6.9, PCache: &pc, POut: 24}}}}
}

// newSmEffWatcher 生效值接线测试守望:调参库/价格本全指临时目录,ArmVerdict
// 全启用(只差判热一门),跳过遥测进 sink。
func newSmEffWatcher(t *testing.T, cfg *config.Config) (*Watcher, *skipSink) {
	t.Helper()
	led := ledger.New()
	sink := newSkipSink()
	w := newTestWatcherW(cfg, led, nil, nil, nil, nil)
	w.TuningStore = tuning.NewStore(t.TempDir())
	w.waitBooks = smZhipuBooks()
	w.bookSameModelSkipFn = sink.record
	w.ArmVerdict = func(string) (bool, bool) { return true, true }
	return w, sink
}

// smEffThresholdRow 读该会话跳过遥测行的 threshold_min(无行时 t.Fatal)。
func smEffThresholdRow(t *testing.T, sink *skipSink, sid string) float64 {
	t.Helper()
	sink.mu.Lock()
	defer sink.mu.Unlock()
	ffs := sink.fields[sid]
	if len(ffs) == 0 {
		t.Fatalf("%s 应有一条跳过遥测(携 threshold_min)", sid)
	}
	v, ok := ffs[len(ffs)-1]["threshold_min"]
	if !ok {
		t.Fatalf("遥测行缺 threshold_min: %v", ffs[len(ffs)-1])
	}
	fv, _ := v.(float64)
	return fv
}

// TestSameModelTriggerReadsEffectiveThreshold recommend 档无校准:触发阈值=
// 计算器种子路径生效值 20 分钟(配置值 30),不再直读配置——闲置 21 分钟越过
// 生效值即进判热门,遥测记 20。
func TestSameModelTriggerReadsEffectiveThreshold(t *testing.T) {
	now := freezeClock(t, smBaseT)
	tmp := t.TempDir()
	w, sink := newSmEffWatcher(t, smEffCfg())
	st, _ := bareSession(t, w.Ledger, tmp, "sm-eff1")
	setLastWrite(w.Ledger, st, *now-21*60) // 闲置 21min > 生效值 20min(< 配置值 30min)
	w.maybeSameModel(st)
	if got := smEffThresholdRow(t, sink, "sm-eff1"); got != 20 {
		t.Fatalf("触发阈值应取生效值 20 分钟(计算器种子路径), got %v(配置值 30)", got)
	}
	if r := sink.last("sm-eff1"); r == "" {
		t.Fatal("越过生效值应进门序并留遥测(无判热观测 → cold)")
	}
}

// TestSameModelEffectiveFallsBackToConfigOnCalcError 现算失败回落:校准把
// TTL 观测并进公式输入但闲置观测缺席(bad_obs)→ 回落配置值 30 分钟,
// 闲置 31 分钟照常进门序,遥测记 30;守望绝不被现算失败阻断。
func TestSameModelEffectiveFallsBackToConfigOnCalcError(t *testing.T) {
	now := freezeClock(t, smBaseT)
	tmp := t.TempDir()
	w, sink := newSmEffWatcher(t, smEffCfg())
	// 校准在案:TTL 观测 [20] 替代集(无闲置观测 → 票05 出口 bad_obs 拒算)。
	if err := w.TuningStore.Append(&tuning.Event{TS: *now,
		Type: tuning.EvAccepted, ID: "sx-zhipu", Upstream: "zhipu",
		Calibration: &tuning.Calibration{Upstream: "zhipu",
			TTLObsMin: []float64{20}, SuggestMin: 16, SourceID: "sx-zhipu",
			AppliedAt: *now}}); err != nil {
		t.Fatal(err)
	}
	st, _ := bareSession(t, w.Ledger, tmp, "sm-eff2")
	setLastWrite(w.Ledger, st, *now-31*60) // 闲置 31min > 回落配置值 30min
	w.maybeSameModel(st)
	if got := smEffThresholdRow(t, sink, "sm-eff2"); got != 30 {
		t.Fatalf("现算失败应回落配置值 30 分钟, got %v", got)
	}
}

// TestSameModelTriggerManualModeUsesConfigValue manual 档:票05 出口语义=
// 配置值即生效值,触发行为与配置值读取一致(钉回归,防接线改变 manual 档)。
func TestSameModelTriggerManualModeUsesConfigValue(t *testing.T) {
	now := freezeClock(t, smBaseT)
	tmp := t.TempDir()
	cfg := smEffCfg()
	cfg.Tuning.Mode = "manual"
	w, sink := newSmEffWatcher(t, cfg)
	st, _ := bareSession(t, w.Ledger, tmp, "sm-eff3")
	setLastWrite(w.Ledger, st, *now-31*60) // 闲置 31min ≥ 配置值 30min
	w.maybeSameModel(st)
	if got := smEffThresholdRow(t, sink, "sm-eff3"); got != 30 {
		t.Fatalf("manual 档生效值应即配置值 30 分钟, got %v", got)
	}
}
