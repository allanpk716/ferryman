// arm_verdict_test.go — 票04:启用门状态机单测(先红后绿)。
//
// 覆盖:预置(无记录)= 待实跳;实跳通过 → 启用;未过 → 保持未启用;
// last-wins 状态转移;append/幂等持久化;缝形状与 config.ArmVerdictResolver 同形。
package ferry

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ferryman/internal/clock"
	"ferryman/internal/config"
)

func armRec(upstream, verdict string) ArmRecord {
	return ArmRecord{
		V: 1, Kind: ArmRecordKind,
		Upstream: upstream, Verdict: verdict,
		Criteria: map[string]string{
			ArmCritRatio: ArmStatusPass, ArmCritDiff: ArmStatusPass,
			ArmCritNoToolUse: ArmStatusPass, ArmCritHandoffMD: ArmStatusPass,
		},
		Note: "单测",
	}
}

func TestArmVerdictMissingFileMeansPending(t *testing.T) {
	path := filepath.Join(t.TempDir(), "arm_verdict.jsonl")
	recs, err := LoadArmVerdicts(path)
	if err != nil {
		t.Fatalf("状态文件不存在应视为空而非错: %v", err)
	}
	if len(recs) != 0 {
		t.Fatalf("空状态应零记录, got %d", len(recs))
	}
	if st := ArmState(recs, "zhipu"); st != ArmStatePending {
		t.Fatalf("无记录 = 待实跳, got %q", st)
	}
	has, enabled := ArmVerdictResolverFor(path)("zhipu")
	if has || enabled {
		t.Fatalf("无记录: has=%v enabled=%v, want false false", has, enabled)
	}
}

func TestArmVerdictPassedEnables(t *testing.T) {
	path := filepath.Join(t.TempDir(), "arm_verdict.jsonl")
	if err := RecordArmVerdict(path, armRec("zhipu", ArmVerdictPassed)); err != nil {
		t.Fatalf("回写失败: %v", err)
	}
	recs, err := LoadArmVerdicts(path)
	if err != nil {
		t.Fatal(err)
	}
	if st := ArmState(recs, "zhipu"); st != ArmStatePassed {
		t.Fatalf("passed 记录后状态 = %q, want %q", st, ArmStatePassed)
	}
	has, enabled := ArmVerdictResolverFor(path)("zhipu")
	if !has || !enabled {
		t.Fatalf("实跳通过应启用: has=%v enabled=%v", has, enabled)
	}
	// 白名单其他上游不受牵连:预置 ≠ 启用。
	has, enabled = ArmVerdictResolverFor(path)("deepseek")
	if has || enabled {
		t.Fatalf("未实跳上游应保持待实跳: has=%v enabled=%v", has, enabled)
	}
}

func TestArmVerdictFailedStaysDisabled(t *testing.T) {
	path := filepath.Join(t.TempDir(), "arm_verdict.jsonl")
	if err := RecordArmVerdict(path, armRec("zhipu", ArmVerdictFailed)); err != nil {
		t.Fatal(err)
	}
	recs, _ := LoadArmVerdicts(path)
	if st := ArmState(recs, "zhipu"); st != ArmStateFailed {
		t.Fatalf("failed 记录后状态 = %q, want %q", st, ArmStateFailed)
	}
	has, enabled := ArmVerdictResolverFor(path)("zhipu")
	if !has || enabled {
		t.Fatalf("未过应有结论但不启用: has=%v enabled=%v", has, enabled)
	}
}

func TestArmVerdictLastWinsTransitions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "arm_verdict.jsonl")
	// 通过 → 未过:吊销启用。
	if err := RecordArmVerdict(path, armRec("zhipu", ArmVerdictPassed)); err != nil {
		t.Fatal(err)
	}
	if err := RecordArmVerdict(path, armRec("zhipu", ArmVerdictFailed)); err != nil {
		t.Fatal(err)
	}
	_, enabled := ArmVerdictResolverFor(path)("zhipu")
	if enabled {
		t.Fatal("后写未过应吊销启用")
	}
	// 未过 → 通过:复跑翻案。
	if err := RecordArmVerdict(path, armRec("zhipu", ArmVerdictPassed)); err != nil {
		t.Fatal(err)
	}
	_, enabled = ArmVerdictResolverFor(path)("zhipu")
	if !enabled {
		t.Fatal("后写通过应重新启用")
	}
	// 多上游互不干扰。
	if err := RecordArmVerdict(path, armRec("deepseek", ArmVerdictFailed)); err != nil {
		t.Fatal(err)
	}
	_, zEn := ArmVerdictResolverFor(path)("zhipu")
	_, dEn := ArmVerdictResolverFor(path)("deepseek")
	if !zEn || dEn {
		t.Fatalf("多上游应分列: zhipu=%v deepseek=%v", zEn, dEn)
	}
}

func TestArmVerdictRecordAppendsHumanReadable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "arm_verdict.jsonl")
	fixed := 1789000000.0
	oldNow := clock.Now
	clock.Now = func() float64 { return fixed }
	defer func() { clock.Now = oldNow }()

	if err := RecordArmVerdict(path, armRec("zhipu", ArmVerdictPassed)); err != nil {
		t.Fatal(err)
	}
	if err := RecordArmVerdict(path, armRec("zhipu", ArmVerdictFailed)); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("append 语义:应两行记录, got %d 行", len(lines))
	}
	for i, ln := range lines {
		if !strings.Contains(ln, `"kind":"arm_verdict"`) || !strings.Contains(ln, `"upstream":"zhipu"`) {
			t.Fatalf("第 %d 行缺 kind/upstream 键(账本同风格): %s", i+1, ln)
		}
		if !strings.Contains(ln, `"ts":1789000000`) {
			t.Fatalf("第 %d 行缺注入时钟的 ts: %s", i+1, ln)
		}
	}
}

func TestArmVerdictSkipsMalformedLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "arm_verdict.jsonl")
	if err := RecordArmVerdict(path, armRec("zhipu", ArmVerdictFailed)); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// 首尾插坏行与空行:人手编辑痕迹不应炸读取。
	content := "这不是JSON\n" + string(raw) + "{坏行\n\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	recs, err := LoadArmVerdicts(path)
	if err != nil {
		t.Fatalf("混入坏行应跳过而非报错: %v", err)
	}
	if st := ArmState(recs, "zhipu"); st != ArmStateFailed {
		t.Fatalf("坏行应被跳过、有效行照常生效: %q", st)
	}
}

func TestArmVerdictRejectsBadVerdict(t *testing.T) {
	path := filepath.Join(t.TempDir(), "arm_verdict.jsonl")
	if err := RecordArmVerdict(path, armRec("zhipu", "inconclusive")); err == nil {
		t.Fatal("非 passed/failed 的结论必须拒写——无结论不落状态")
	}
}

// TestArmVerdictResolverSeamShape 钉死缝形状:可直接赋给票01 预留的
// config.ArmVerdictResolver(watcher/doctor 的注入点就用这一形)。
func TestArmVerdictResolverSeamShape(t *testing.T) {
	path := filepath.Join(t.TempDir(), "arm_verdict.jsonl")
	var seam config.ArmVerdictResolver = ArmVerdictResolverFor(path)
	if seam == nil {
		t.Fatal("resolver 不应为 nil")
	}
	has, _ := seam("zhipu")
	if has {
		t.Fatal("空状态应无结论")
	}
}

func TestDefaultArmVerdictPath(t *testing.T) {
	p := DefaultArmVerdictPath()
	if !strings.Contains(p, "ferryman") || !strings.Contains(p, "arm_verdict.jsonl") {
		t.Fatalf("默认状态文件应落在 ~/ferryman/arm_verdict.jsonl, got %q", p)
	}
}
