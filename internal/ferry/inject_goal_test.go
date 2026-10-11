package ferry

import (
	"strings"
	"testing"
)

// d0b927 实锚形状：注入层有末段定格/当前状态/下一步/关键文件/续接，缺目标。
func TestEnsureInjectGoalBackfillsBeforeState(t *testing.T) {
	inject := "## 末段定格（最后一轮对话，程序化保留）\n[user] 你把反馈提交过去了吗？\n[assistant] 提交过去了。\n\n" +
		"**当前状态**：第三轮过程评审反馈已完成并推送。\n\n**下一步**：等待用户拍板。"
	full := "# 目标\n作为评审方，对另一台电脑上开发方产出的开发与实验过程进行第三轮过程评审。\n\n# 已完成与关键结论\n- 略"
	got := EnsureInjectGoal(inject, full)
	gi := strings.Index(got, "**目标**（程序补自全文「目标」节）：作为评审方")
	si := strings.Index(got, "**当前状态**")
	fi := strings.Index(got, "[assistant] 提交过去了。")
	if gi < 0 || si < 0 || !(fi < gi && gi < si) {
		t.Fatalf("目标应补在末段定格之后、状态段之前:\n%s", got)
	}
	if strings.Contains(got, "已完成与关键结论") {
		t.Fatalf("只截目标节, 不得越到下一节:\n%s", got)
	}
}

func TestEnsureInjectGoalNoopCases(t *testing.T) {
	full := "# 目标\n做 A\n# 续接第一句话\n接 B"
	for _, inj := range []string{
		"**目标**：已写了\n**最新状态**：x",
		"【目标】已写了",
		"# 目标\n已写了",
		"目标：已写了",
	} {
		if got := EnsureInjectGoal(inj, full); got != inj {
			t.Fatalf("已有目标应原样: %q → %q", inj, got)
		}
	}
	if got := EnsureInjectGoal("只有状态", "# 已完成\n无目标节"); got != "只有状态" {
		t.Fatalf("全文无目标节应原样: %q", got)
	}
	if got := EnsureInjectGoal("只有状态", "# 目标\n   \n# 下一节\nx"); got != "只有状态" {
		t.Fatalf("目标节空应原样: %q", got)
	}
}

func TestEnsureInjectGoalAppendsAndCaps(t *testing.T) {
	long := strings.Repeat("长", InjectGoalCap+50)
	got := EnsureInjectGoal("无状态段的注入层", "# 目标\n"+long)
	if !strings.HasPrefix(got, "无状态段的注入层\n\n**目标**") {
		t.Fatalf("无状态段应尾附: %q", got)
	}
	if !strings.HasSuffix(got, "…") || strings.Count(got, "长") != InjectGoalCap {
		t.Fatalf("目标应截到 %d 码点加省略号", InjectGoalCap)
	}
}
