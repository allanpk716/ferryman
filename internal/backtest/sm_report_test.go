// 票06 测试：同模型扫参报告的四要素（曲线/样本量/预期差价/现值 diff）、
// 样本不足标注、落盘与确定性。
package backtest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// goldenReport 金样本报告（复用 sm_sweep_test 夹具）。
func goldenReport(t *testing.T, opts SameModelSweepOptions) (string, *SameModelSweepResult) {
	t.Helper()
	res, err := SameModelSweep(smGoldenDS(), opts)
	if err != nil {
		t.Fatal(err)
	}
	return RenderSameModelMarkdown(res, smGoldenDS()), res
}

func TestSameModelReportFourElements(t *testing.T) {
	md, _ := goldenReport(t, smOpts())
	// 四要素齐备。
	for _, marker := range []string{"曲线", "样本量", "预期差价", "现值"} {
		if !strings.Contains(md, marker) {
			t.Fatalf("报告缺四要素之一 %q", marker)
		}
	}
	// 曲线节含网格行（t 与净节省数字）。
	if !strings.Contains(md, "t=16") || !strings.Contains(md, "151.20") {
		t.Fatal("曲线节缺网格数据行（t=16 / 151.20）")
	}
	// 样本量：窗内摆渡事件数与门槛在案。
	if !strings.Contains(md, "2") || !strings.Contains(md, "30") {
		t.Fatal("样本量节缺门槛数字（30）")
	}
	// 现值 diff：现值 20、最优 16 → diff 在案。
	if !strings.Contains(md, "20.0") || !strings.Contains(md, "-4.0") {
		t.Fatal("diff 节缺现值 20.0 与差 -4.0")
	}
	// 三线对比表头：阈值=t / 实际发生 / 什么都不做。
	for _, marker := range []string{"若当时阈值", "实际发生", "什么都不做"} {
		if !strings.Contains(md, marker) {
			t.Fatalf("三线对比缺 %q 线", marker)
		}
	}
	// 建议值出口在案（公式单源）。
	if !strings.Contains(md, "20") || !strings.Contains(md, "建议") {
		t.Fatal("报告缺建议值节")
	}
	// 证据等级硬标（反事实推断）。
	if !strings.Contains(md, "反事实推断") {
		t.Fatal("报告缺证据等级硬标")
	}
}

func TestSameModelReportInsufficient(t *testing.T) {
	opts := smOpts()
	opts.MinEvents = 30
	md, res := goldenReport(t, opts)
	if !res.Sample.Sufficient {
		if !strings.Contains(md, "样本不足") {
			t.Fatal("样本不足未标注")
		}
		if strings.Contains(md, "建议值：") {
			t.Fatal("样本不足不得出现建议值")
		}
		// diff 节照登但如实标注无建议。
		if !strings.Contains(md, "现值") {
			t.Fatal("样本不足报告仍须登现值节")
		}
	} else {
		t.Fatal("夹具应判不足")
	}
}

func TestSameModelReportNoCurrentAndDerivedError(t *testing.T) {
	// 现值未供给：diff 列「—」占位，不炸。
	opts := smOpts()
	opts.HasCurrent = false
	md, _ := goldenReport(t, opts)
	if !strings.Contains(md, "现值") {
		t.Fatal("无现值也须有现值节（占位说明）")
	}
	// 拒算（no_gap）：建议值节如实登拒算分类。
	ng := noGapBooks()
	opts2 := smOpts()
	opts2.Books = ng
	res, err := SameModelSweep(smGoldenDS(), opts2)
	if err != nil {
		t.Fatal(err)
	}
	md2 := RenderSameModelMarkdown(res, smGoldenDS())
	if !strings.Contains(md2, "no_gap") {
		t.Fatal("拒算报告须登拒算分类 no_gap")
	}
}

func TestSameModelReportDeterministic(t *testing.T) {
	a, _ := goldenReport(t, smOpts())
	b, _ := goldenReport(t, smOpts())
	if a != b {
		t.Fatal("两次渲染不一致")
	}
}

func TestWriteSameModelReport(t *testing.T) {
	res, err := SameModelSweep(smGoldenDS(), smOpts())
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path, err := WriteSameModelReport(dir, res, smGoldenDS())
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(path) != "same-model-sweep-report.md" {
		t.Fatalf("落盘文件名 = %q", filepath.Base(path))
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := RenderSameModelMarkdown(res, smGoldenDS())
	if string(raw) != want {
		t.Fatal("落盘内容与渲染不一致")
	}
}
