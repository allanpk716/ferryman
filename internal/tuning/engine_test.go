// engine_test.go — 票07:三态行为、五护栏、永不自动升档、生效值走计算器。
//
// 验收对映:
//   - 「三态行为单测:manual 零应用、recommend 只提醒、auto 护栏内应用」
//     → TestTriStateBehaviors。
//   - 「五护栏各自单测(频控按上游按周、钳位、样本门槛、回滚)」
//     → TestGuardrailFrequency / TestGuardrailClamp / TestGuardrailSampleGate /
//       TestGuardrailRollback(护栏①=结构性:TestEffectiveThresholdUsesCalculator
//       断言生效值只出自票05 计算器、校准只改公式输入)。
//   - 「永不自动升档:代码路径不存在自动改 mode 的分支,单测断言」
//     → TestNeverAutoUpgrade。
package tuning

import (
	"os"
	"strings"
	"testing"

	"ferryman/internal/config"
	"ferryman/internal/policy"
	"ferryman/internal/prices"
)

const testNow = 1758500000.0

// glmBooks 单本 GLM 价格表(policy 同款口径:Per=10000,p_in 6.9/p_cache 1.7/p_out 24)。
func glmBooks() map[string]prices.PriceBook {
	pc := 1.7
	return map[string]prices.PriceBook{"glm": {Key: "glm", Unit: "智谱积分", Per: 10000,
		Versions: []prices.PriceVersion{{EffectiveFrom: "2026-09-01", PIn: 6.9, PCache: &pc, POut: 24}}}}
}

// goldenIdle 黄金闲置样本(policy 黄金用例同款,n=20,F(25)=0.95)。
func goldenIdle() []float64 {
	idle := []float64{40, 20, 15, 15, 15, 10, 10, 10, 10, 10,
		5, 5, 5, 5, 5, 5, 5, 5, 5, 5}
	return idle
}

// testCfg 沙箱配置:总结阈值 25 分钟、同模型上限 25(=总结)。
func testCfg(mode string) *config.Config {
	c := config.Default()
	c.Tuning.Mode = mode
	c.Thresholds.SummarizeS = 25 * 60
	c.SameModel = config.SameModelCfg{Enabled: true, Upstreams: []string{"glm"}, ThresholdMin: 25}
	return c
}

// testSug 合法建议:样本充足、值在 [10,25] 内。
func testSug(id, up string, suggest float64, ferry, minEv int) *Suggestion {
	return &Suggestion{ID: id, Upstream: up, SuggestMin: suggest, CurrentMin: 25,
		HasCurrent: true, BestMin: suggest, NetSavings: 12.5, FerryEvents: ferry,
		MinEvents: minEv, TTLObsMin: []float64{26, 30, 34}, CreatedAt: testNow}
}

// newTestStore 临时目录库(Store.Dir = <tmp>/tuning)。
func newTestStore(t *testing.T) *Store {
	t.Helper()
	return NewStore(t.TempDir())
}

// recorder 通知桩:记录两类气泡的触发。
type recorder struct {
	pending []string
	applied []string
}

func (r *recorder) notifier() *Notifier {
	return &Notifier{
		SuggestionPending: func(s *Suggestion, reason string) {
			r.pending = append(r.pending, s.ID+": "+reason)
		},
		AutoApplied: func(s *Suggestion, _ *Calibration) {
			r.applied = append(r.applied, s.ID)
		},
	}
}

// TestTriStateBehaviors manual 零应用 / recommend 只提醒 / auto 护栏内应用。
func TestTriStateBehaviors(t *testing.T) {
	sumMin := 25.0

	// manual:零应用零提醒——无校准文件、无应用事件、无通知。
	st := newTestStore(t)
	rec := &recorder{}
	d, err := st.AutoApply(testSug("s1-glm", "glm", 22, 42, 30), ModeManual, sumMin, testNow, rec.notifier())
	if err != nil {
		t.Fatal(err)
	}
	if d.Action != ActReportOnly {
		t.Fatalf("manual 档动作 = %q, want report_only", d.Action)
	}
	if _, err := os.Stat(st.calibFile("glm")); !os.IsNotExist(err) {
		t.Fatal("manual 档不得写校准文件(零应用)")
	}
	if st2, _ := st.Replay(); len(st2.Calib) != 0 || len(st2.Status) != 0 {
		t.Fatalf("manual 档不得产生任何生效事件: %+v", st2)
	}
	if len(rec.pending) != 0 || len(rec.applied) != 0 {
		t.Fatalf("manual 档只出报告,不应有通知: %v %v", rec.pending, rec.applied)
	}

	// recommend:只提醒(待审气泡),零应用。
	rec = &recorder{}
	d, err = st.AutoApply(testSug("s2-glm", "glm", 22, 42, 30), ModeRecommend, sumMin, testNow, rec.notifier())
	if err != nil {
		t.Fatal(err)
	}
	if d.Action != ActRemind {
		t.Fatalf("recommend 档动作 = %q, want remind", d.Action)
	}
	if len(rec.pending) != 1 || !strings.Contains(rec.pending[0], "s2-glm") {
		t.Fatalf("recommend 档应发一次待审提醒: %v", rec.pending)
	}
	if len(rec.applied) != 0 {
		t.Fatal("recommend 档不得自动应用")
	}
	if st2, _ := st.Replay(); len(st2.Calib) != 0 {
		t.Fatalf("recommend 档零应用: %+v", st2.Calib)
	}

	// auto:护栏全过 → 应用(校准落盘+事件+已应用通报)。
	rec = &recorder{}
	sug := testSug("s3-glm", "glm", 22, 42, 30)
	d, err = st.AutoApply(sug, ModeAuto, sumMin, testNow, rec.notifier())
	if err != nil {
		t.Fatal(err)
	}
	if d.Action != ActApply {
		t.Fatalf("auto 档护栏全过应应用: %s(%s)", d.Action, d.Reason)
	}
	cal, err := os.ReadFile(st.calibFile("glm"))
	if err != nil {
		t.Fatalf("auto 应用应写校准投影: %v", err)
	}
	if !strings.Contains(string(cal), `"s3-glm"`) {
		t.Fatalf("校准投影应记来源建议: %s", cal)
	}
	st2, _ := st.Replay()
	if st2.Status["s3-glm"] != StAutoApplied {
		t.Fatalf("auto 应用应落 auto_applied 事件: %v", st2.Status)
	}
	if c := st2.Calib["glm"]; c == nil || c.SuggestMin != 22 {
		t.Fatalf("auto 应用应落校准: %+v", st2.Calib["glm"])
	}
	if len(rec.applied) != 1 {
		t.Fatalf("护栏⑤:生效后必须通报, got %v", rec.applied)
	}
}

// TestGuardrailFrequency 护栏③:同一上游 7 天内已有生效记录则只提醒;
// 过窗可再应用;按上游隔离。
func TestGuardrailFrequency(t *testing.T) {
	st := newTestStore(t)
	rec := &recorder{}
	n := rec.notifier()

	if _, err := st.AutoApply(testSug("s1-glm", "glm", 22, 42, 30), ModeAuto, 25, testNow, n); err != nil {
		t.Fatal(err)
	}
	// 1 天后同上游:只提醒,不应用。
	d, err := st.AutoApply(testSug("s2-glm", "glm", 18, 42, 30), ModeAuto, 25, testNow+86400, n)
	if err != nil {
		t.Fatal(err)
	}
	if d.Action != ActRemind || !strings.Contains(d.Reason, "频控") {
		t.Fatalf("7 天内再应用应被频控收敛: %s(%s)", d.Action, d.Reason)
	}
	if st2, _ := st.Replay(); st2.Calib["glm"].SourceID != "s1-glm" {
		t.Fatalf("频控期内校准不得变更: %+v", st2.Calib["glm"])
	}
	// 按上游隔离:另一上游不受 glm 频控影响。
	d, err = st.AutoApply(testSug("s3-ds", "ds", 20, 42, 30), ModeAuto, 25, testNow+86400, n)
	if err != nil {
		t.Fatal(err)
	}
	if d.Action != ActApply {
		t.Fatalf("频控按上游隔离: %s(%s)", d.Action, d.Reason)
	}
	// 第 8 天:过窗可再应用。
	d, err = st.AutoApply(testSug("s4-glm", "glm", 17, 42, 30), ModeAuto, 25, testNow+8*86400, n)
	if err != nil {
		t.Fatal(err)
	}
	if d.Action != ActApply {
		t.Fatalf("过窗后应可再应用: %s(%s)", d.Action, d.Reason)
	}
}

// TestGuardrailClamp 护栏②:建议值越出 [10, 总结阈值] 收敛只提醒。
func TestGuardrailClamp(t *testing.T) {
	st := newTestStore(t)
	for _, tc := range []struct {
		suggest float64
		want    string
	}{
		{5, "低于下限"},
		{30, "超出总结阈值"},
	} {
		d, err := st.AutoApply(testSug("sc-glm", "glm", tc.suggest, 42, 30), ModeAuto, 25, testNow, nil)
		if err != nil {
			t.Fatal(err)
		}
		if d.Action != ActRemind || !strings.Contains(d.Reason, tc.want) {
			t.Fatalf("建议值 %.0f 应被钳位护栏收敛(%s): %s(%s)",
				tc.suggest, tc.want, d.Action, d.Reason)
		}
	}
	if st2, _ := st.Replay(); len(st2.Calib) != 0 {
		t.Fatalf("钳位不过不得应用: %+v", st2.Calib)
	}
}

// TestGuardrailSampleGate 护栏④:滚动窗摆渡事件不足收敛只提醒。
func TestGuardrailSampleGate(t *testing.T) {
	st := newTestStore(t)
	d, err := st.AutoApply(testSug("s1-glm", "glm", 22, 29, 30), ModeAuto, 25, testNow, nil)
	if err != nil {
		t.Fatal(err)
	}
	if d.Action != ActRemind || !strings.Contains(d.Reason, "样本不足") {
		t.Fatalf("样本不足应只提醒: %s(%s)", d.Action, d.Reason)
	}
	d, err = st.AutoApply(testSug("s2-glm", "glm", 22, 30, 30), ModeAuto, 25, testNow, nil)
	if err != nil {
		t.Fatal(err)
	}
	if d.Action != ActApply {
		t.Fatalf("样本恰达门槛应可应用: %s(%s)", d.Action, d.Reason)
	}
}

// TestGuardrailRollback 护栏⑤:一键回滚还原最近一次生效前快照;
// 再回滚按 undo 交换还原上一次值;无生效记录报错不落事件。
func TestGuardrailRollback(t *testing.T) {
	st := newTestStore(t)
	sugA := testSug("sA-glm", "glm", 22, 42, 30) // TTL 校准 [26 30 34]
	sugB := testSug("sB-glm", "glm", 18, 42, 30)
	sugB.TTLObsMin = []float64{18, 20, 22}
	if _, err := st.AutoApply(sugA, ModeAuto, 25, testNow, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AutoApply(sugB, ModeAuto, 25, testNow+8*86400, nil); err != nil {
		t.Fatal(err)
	}

	// 回滚 → 还原 A(最近一次生效前快照)。
	prev, err := st.Rollback("glm", ModeAuto, testNow+9*86400)
	if err != nil {
		t.Fatal(err)
	}
	if prev == nil || prev.SourceID != "sA-glm" {
		t.Fatalf("回滚应还原 sA 生效前快照: %+v", prev)
	}
	b, err := os.ReadFile(st.calibFile("glm"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"sA-glm"`) {
		t.Fatalf("校准投影应还原为 sA: %s", b)
	}
	st2, _ := st.Replay()
	if st2.Calib["glm"].SourceID != "sA-glm" {
		t.Fatalf("回滚后当前校准应为 sA: %+v", st2.Calib["glm"])
	}
	if st2.LastEffective["glm"] != testNow+9*86400 {
		t.Fatalf("回滚计入频控窗口(防自动应用来回翻转): %v", st2.LastEffective)
	}

	// 再回滚 → undo 交换还原 B。
	prev2, err := st.Rollback("glm", ModeAuto, testNow+10*86400)
	if err != nil {
		t.Fatal(err)
	}
	if prev2 == nil || prev2.SourceID != "sB-glm" {
		t.Fatalf("二次回滚应还原回滚前的值(sB): %+v", prev2)
	}

	// 无生效记录 → 报错且不落事件。
	if _, err := st.Rollback("nope", ModeAuto, testNow); err == nil {
		t.Fatal("无生效记录的上游无可回滚")
	}
	st3, _ := st.Replay()
	if _, ok := st3.Calib["nope"]; ok {
		t.Fatal("失败回滚不得写校准")
	}
}

// TestEffectiveThresholdUsesCalculator 护栏①执行面:生效值只出自票05 计算器
// (校准并进公式输入后调 SameModelEffectiveThreshold,与直调逐位一致);
// manual 档校准不参与(配置值即生效值)。
func TestEffectiveThresholdUsesCalculator(t *testing.T) {
	st := newTestStore(t)
	books := glmBooks()
	obs := policy.SameModelObs{PrefixTokens: 150000, OutTokens: 1000,
		TTLObsMin: []float64{26, 30, 34}, IdleObsMin: goldenIdle()}
	cfg := testCfg(ModeRecommend) // 上限 25 = 总结阈值

	// 未校准:计算器按观测现算 → 0.8×median(30)=24。
	res0, err := st.EffectiveThreshold(cfg, books, "glm", obs)
	if err != nil {
		t.Fatal(err)
	}
	if res0.ThresholdMin != 24 {
		t.Fatalf("未校准生效值 = %v, want 24(公式出口)", res0.ThresholdMin)
	}

	// 应用校准(TTL 观测替换为扫参校准集 [20] → 0.8×20=16)。
	sug := testSug("s1-glm", "glm", 16, 42, 30)
	sug.TTLObsMin = []float64{20}
	if _, err := st.AutoApply(sug, ModeAuto, 25, testNow, nil); err != nil {
		t.Fatal(err)
	}
	res1, err := st.EffectiveThreshold(cfg, books, "glm", obs)
	if err != nil {
		t.Fatal(err)
	}
	if res1.ThresholdMin != 16 {
		t.Fatalf("校准后生效值 = %v, want 16(公式吃校准输入)", res1.ThresholdMin)
	}
	// 与直调计算器(手工并进口)逐位一致——不存在第二份生效值实现。
	direct, err := policy.SameModelEffectiveThreshold(cfg, books, "glm",
		policy.SameModelObs{PrefixTokens: 150000, OutTokens: 1000,
			TTLObsMin: []float64{20}, IdleObsMin: goldenIdle()})
	if err != nil {
		t.Fatal(err)
	}
	if direct.ThresholdMin != res1.ThresholdMin {
		t.Fatalf("生效值与直调计算器不一致: %v vs %v", res1.ThresholdMin, direct.ThresholdMin)
	}

	// manual 档:校准不参与,配置值即生效值(计算器出口语义,票05 D13)。
	cfgM := testCfg(ModeManual)
	resM, err := st.EffectiveThreshold(cfgM, books, "glm", obs)
	if err != nil {
		t.Fatal(err)
	}
	if resM.ThresholdMin != 25 {
		t.Fatalf("manual 档生效值 = %v, want 25(CeilingFor)", resM.ThresholdMin)
	}
}

// TestNeverAutoUpgrade 永不自动升档:全流程(记录/自动应用/人工接受/回滚/
// 生效值)走完,档位纹丝不动;事件类型与决策动作全集固定,不存在任何
// 改档位出口——升档只认 config.toml [tuning].mode 手改。
func TestNeverAutoUpgrade(t *testing.T) {
	st := newTestStore(t)
	cfg := testCfg(ModeRecommend)

	sug := testSug("s1-glm", "glm", 22, 42, 30)
	must(t, st.RecordSuggestion(sug, testNow))
	if _, err := st.AutoApply(sug, cfg.Tuning.Mode, 25, testNow, nil); err != nil {
		t.Fatal(err)
	}
	must(t, st.Accept("s1-glm", cfg.Tuning.Mode, 25, testNow+1))
	if _, err := st.Rollback("glm", cfg.Tuning.Mode, testNow+2); err != nil {
		t.Fatal(err)
	}
	if _, err := st.EffectiveThreshold(cfg, glmBooks(), "glm",
		policy.SameModelObs{PrefixTokens: 150000, OutTokens: 1000,
			TTLObsMin: []float64{26, 30, 34}, IdleObsMin: goldenIdle()}); err != nil {
		t.Fatal(err)
	}
	if cfg.Tuning.Mode != ModeRecommend {
		t.Fatalf("引擎全流程后档位被改: %q——永不自动升档被破坏", cfg.Tuning.Mode)
	}

	// 事件类型全集固定:无 mode 类事件(升档无从落流水)。
	for _, e := range []string{EvSuggestionCreated, EvAccepted, EvRejected,
		EvAutoApplied, EvRollbackApplied} {
		if strings.Contains(e, "mode") || strings.Contains(e, "upgrade") {
			t.Fatalf("流水事件类型含改档位语义: %q", e)
		}
	}

	// 决策动作全集固定:任何档位×建议只产出 report_only/remind/apply。
	stFull, _ := st.Replay()
	for _, mode := range []string{ModeManual, ModeRecommend, ModeAuto, "future-mode"} {
		d := Consider(mode, sug, stFull, 25, testNow)
		switch d.Action {
		case ActReportOnly, ActRemind, ActApply:
		default:
			t.Fatalf("未知决策动作 %q(档位 %s)", d.Action, mode)
		}
	}
}

// TestUnknownModeConservative 未知档位按最保守处理:只出报告,绝不应用。
func TestUnknownModeConservative(t *testing.T) {
	st := newTestStore(t)
	d, err := st.AutoApply(testSug("s1-glm", "glm", 22, 42, 30), "future-mode", 25, testNow, nil)
	if err != nil {
		t.Fatal(err)
	}
	if d.Action != ActReportOnly {
		t.Fatalf("未知档位应只出报告: %s", d.Action)
	}
	if st2, _ := st.Replay(); len(st2.Calib) != 0 {
		t.Fatalf("未知档位不得应用: %+v", st2.Calib)
	}
}
