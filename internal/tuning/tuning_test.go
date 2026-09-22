// tuning_test.go — 票07:调参流水层单测(append-only 落盘可回放 / 建议投影)。
//
// 验收对映:
//   - 「调参流水 append-only,接受/拒绝/自动应用三事件落盘可回放」
//     → TestLogAppendReplay(含坏行跳过、投影非事实源、重放幂等)。
//   - 「建议输入来自票06」→ TestSuggestionFromSweep(SameModelSweep 产物投影)。
package tuning

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ferryman/internal/backtest"
	"ferryman/internal/policy"
)

// sweepFixture 构造一条票06 扫参产物(纯结构,不跑引擎)。
func sweepFixture(loadedAt float64) *backtest.SameModelSweepResult {
	return &backtest.SameModelSweepResult{
		Counts: backtest.IdleLoadCounts{LoadedAt: loadedAt, WindowDays: 30,
			FerryEventsInWindow: 42},
		Sample: backtest.SameModelSampleGate{WindowDays: 30, MinEvents: 30,
			FerryEvents: 42, Sufficient: true},
		Best:   &backtest.SameModelPoint{TMin: 21, NetSavings: 12.5},
		Derived: &policy.SameModelResult{Upstream: "glm", SuggestMin: 22},
		CurrentThresholdMin: 25, HasCurrent: true,
	}
}

// TestSuggestionFromSweep 票06 扫参产物 → 建议的字段投影与可建议性判定。
func TestSuggestionFromSweep(t *testing.T) {
	res := sweepFixture(1758500000)
	sug := SuggestionFromSweep(res, "glm", "r/report.md", 25, true,
		[]float64{26, 30, 34}, 1758500001)
	if sug == nil {
		t.Fatal("样本充足且有建议值,应产出建议")
	}
	if sug.ID != "s1758500000-glm" {
		t.Fatalf("ID = %q, want s1758500000-glm(装载时点戳+上游,确定性)", sug.ID)
	}
	if sug.Upstream != "glm" || sug.SuggestMin != 22 || sug.CurrentMin != 25 ||
		!sug.HasCurrent || sug.BestMin != 21 || sug.NetSavings != 12.5 ||
		sug.FerryEvents != 42 || sug.MinEvents != 30 || sug.CreatedAt != 1758500001 {
		t.Fatalf("投影字段不符: %+v", sug)
	}
	if sug.ReportPath != "r/report.md" {
		t.Fatalf("ReportPath = %q, want r/report.md(证据报告落点)", sug.ReportPath)
	}
	if len(sug.TTLObsMin) != 3 || sug.TTLObsMin[0] != 26 {
		t.Fatalf("TTLObsMin 应原样带过(公式输入校准的来源), got %v", sug.TTLObsMin)
	}

	// 不可建议三态:样本不足 / 建议值拒算 / 空产物。
	bad := sweepFixture(1)
	bad.Sample.Sufficient = false
	if Suggestable(bad) {
		t.Fatal("样本不足不可建议(护栏④)")
	}
	if sug := SuggestionFromSweep(bad, "glm", "", 25, true, nil, 2); sug != nil {
		t.Fatal("样本不足不应产出建议")
	}
	noDerive := sweepFixture(1)
	noDerive.Derived = nil
	if Suggestable(noDerive) {
		t.Fatal("建议值拒算(Derived 空)不可建议")
	}
	if Suggestable(nil) {
		t.Fatal("空产物不可建议")
	}
}

// TestLogAppendReplay 流水 append-only:接受/拒绝/自动应用逐条落盘,重开重放
// 全量还原;投影文件删除后重放不变(流水是唯一事实源);坏行跳过不毁重放。
func TestLogAppendReplay(t *testing.T) {
	dir := t.TempDir()
	s1 := NewStore(dir)
	now := 1758500000.0
	sA := testSug("sA-glm", "glm", 22, 42, 30)
	sB := testSug("sB-glm", "glm", 18, 42, 30)
	sC := testSug("sC-ds", "ds", 20, 42, 30)
	sD := testSug("sD-glm", "glm", 19, 42, 30)

	must(t, s1.RecordSuggestion(sA, now))
	must(t, s1.RecordSuggestion(sB, now+1))
	must(t, s1.Reject("sB-glm", "净额不足", now+2))
	must(t, s1.Accept("sA-glm", "recommend", 25, now+3))
	must(t, s1.RecordSuggestion(sC, now+4))
	if _, err := s1.AutoApply(sC, "auto", 25, now+5, nil); err != nil {
		t.Fatal(err)
	}
	must(t, s1.RecordSuggestion(sD, now+6))
	must(t, s1.Accept("sD-glm", "recommend", 25, now+7)) // 第二次 glm 生效:留 prev 快照

	// append-only:行数 = 事件数(8),无改写。
	b, err := os.ReadFile(filepath.Join(s1.Dir, LogFileName))
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(b), "\n"); n != 8 {
		t.Fatalf("流水行数 = %d, want 8(8 个事件逐条 append)", n)
	}

	// 重开重放:三事件+created 全部可回放。
	s2 := NewStore(dir)
	st, err := s2.Replay()
	if err != nil {
		t.Fatal(err)
	}
	if st.Status["sA-glm"] != StAccepted || st.Status["sB-glm"] != StRejected ||
		st.Status["sC-ds"] != StAutoApplied || st.Status["sD-glm"] != StAccepted {
		t.Fatalf("状态回放不符: %v", st.Status)
	}
	if len(st.Suggestions) != 4 {
		t.Fatalf("建议条数 = %d, want 4", len(st.Suggestions))
	}
	if c := st.Calib["glm"]; c == nil || c.SuggestMin != 19 || c.SourceID != "sD-glm" {
		t.Fatalf("glm 校准应为最近一次生效(sD): %+v", st.Calib["glm"])
	}
	if c := st.Calib["ds"]; c == nil || c.SourceID != "sC-ds" {
		t.Fatalf("ds 校准不符: %+v", st.Calib["ds"])
	}
	if p := st.PrevCalib["glm"]; p == nil || p.SourceID != "sA-glm" {
		t.Fatalf("glm 生效前快照应为 sA(回滚源): %+v", st.PrevCalib["glm"])
	}
	if st.LastEffective["glm"] != now+7 || st.LastEffective["ds"] != now+5 {
		t.Fatalf("最近生效时点不符(频控输入): %v", st.LastEffective)
	}
	if st.Events != 8 {
		t.Fatalf("回放事件数 = %d, want 8", st.Events)
	}

	// 投影非事实源:删掉校准投影文件,重放结果不变。
	for _, up := range []string{"glm", "ds"} {
		if err := os.Remove(s2.calibFile(up)); err != nil {
			t.Fatal(err)
		}
	}
	st3, err := NewStore(dir).Replay()
	if err != nil {
		t.Fatal(err)
	}
	if c := st3.Calib["glm"]; c == nil || c.SourceID != "sD-glm" {
		t.Fatalf("删投影后重放应不变(流水唯一事实源): %+v", st3.Calib["glm"])
	}

	// 坏行跳过:注入垃圾行后重放照常(告警不致命)。
	f, err := os.OpenFile(filepath.Join(s1.Dir, LogFileName),
		os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("这不是 json\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()
	st4, err := NewStore(dir).Replay()
	if err != nil {
		t.Fatalf("坏行不应使重放失败: %v", err)
	}
	if st4.Events != 8 || len(st4.Suggestions) != 4 {
		t.Fatalf("坏行后重放结果应不变: events=%d sugs=%d", st4.Events, len(st4.Suggestions))
	}

	// 重放幂等:同库两次重放逐字段一致。
	st5, err := NewStore(dir).Replay()
	if err != nil {
		t.Fatal(err)
	}
	if len(st5.Status) != len(st4.Status) || len(st5.Calib) != len(st4.Calib) ||
		len(st5.LastEffective) != len(st4.LastEffective) {
		t.Fatal("两次重放结果不一致")
	}
}

// TestAcceptRejectValidation 人工写路径的防御:未在案报错、终态不可逆、
// 钳位越界拒接受、已自动应用指向回滚。
func TestAcceptRejectValidation(t *testing.T) {
	st := newTestStore(t)
	now := 1758500000.0

	if err := st.Accept("nope", "recommend", 25, now); err == nil {
		t.Fatal("未在案建议不可接受")
	}
	if err := st.Reject("nope", "", now); err == nil {
		t.Fatal("未在案建议不可拒绝")
	}

	sug := testSug("s1-glm", "glm", 22, 42, 30)
	must(t, st.RecordSuggestion(sug, now))
	must(t, st.Reject("s1-glm", "人工拒绝", now))
	if err := st.Accept("s1-glm", "recommend", 25, now+1); err == nil {
		t.Fatal("已拒绝的建议不可再接受(等下次扫参)")
	}

	bad := testSug("s2-glm", "glm", 40, 42, 30) // 越界:总结阈值 25
	must(t, st.RecordSuggestion(bad, now))
	err := st.Accept("s2-glm", "recommend", 25, now+1)
	if err == nil || !strings.Contains(err.Error(), "护栏②") {
		t.Fatalf("越界建议应被护栏②拒绝: %v", err)
	}

	ok := testSug("s3-glm", "glm", 22, 42, 30)
	must(t, st.RecordSuggestion(ok, now))
	if _, err := st.AutoApply(ok, "auto", 25, now+2, nil); err != nil {
		t.Fatal(err)
	}
	err = st.Reject("s3-glm", "反悔", now+3)
	if err == nil || !strings.Contains(err.Error(), "回滚") {
		t.Fatalf("已自动应用的建议不可拒绝,应指向回滚: %v", err)
	}
}

// must 测试辅助:err 非 nil 即 Fatal。
func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
