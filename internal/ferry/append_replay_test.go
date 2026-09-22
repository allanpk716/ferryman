package ferry

// append_replay_test.go — 票03:同模型摆渡执行档的纯逻辑钉子(ADR-0015
// 决定一/决定三/决定六)。
//
// 验收对照:
//   - 指令模板含禁工具与结构要求(硬约束:只输出交接 MD 结构、禁止调用工具;
//     两层标记+六节结构);
//   - max_tokens 封顶默认保守值 4096、经包级 var 可配;
//   - 同模型输出严格解析:标记缺失/顺序错/层空 = 不合交接 MD 结构(与第三方
//     ParseOutput 的兜底语义相反——判该档失败);
//   - 产物合成:模型叙事+程序骨架 → 两层交接 MD,结构与第三方档同构
//     (HandoffMarkdown 单源);
//   - lane 三档标注互异稳定。

import (
	"strings"
	"testing"
)

func TestSameModelInstructionContract(t *testing.T) {
	// 硬约束一:明令禁止调用工具(防工具调用循环烧输出价——决定三)。
	for _, want := range []string{"禁止调用", "tool_use"} {
		if !strings.Contains(SameModelInstruction, want) {
			t.Fatalf("指令模板须包含 %q(禁工具硬约束), got: %.80s…", want, SameModelInstruction)
		}
	}
	// 硬约束二:结构要求——两层标记 + 六节。
	for _, want := range []string{InjectOpen, InjectClose,
		"# 目标", "# 已完成与关键结论", "# 未完成与下一步",
		"# 关键文件与改动", "# 踩过的坑与决策", "# 续接第一句话"} {
		if !strings.Contains(SameModelInstruction, want) {
			t.Fatalf("指令模板须包含结构要求 %q", want)
		}
	}
	// 只输出结构、不开场白。
	if !strings.Contains(SameModelInstruction, "只输出交接 MD 结构") {
		t.Fatal("指令模板须明令只输出交接 MD 结构")
	}
}

func TestSameModelMaxTokensDefaultConservative(t *testing.T) {
	if SameModelMaxTokens != 4096 {
		t.Fatalf("max_tokens 封顶默认须为保守值 4096, got %d", SameModelMaxTokens)
	}
	// 可配:包级 var 是缝(注入后须还原)。
	old := SameModelMaxTokens
	SameModelMaxTokens = 2048
	defer func() { SameModelMaxTokens = old }()
	if SameModelMaxTokens != 2048 {
		t.Fatalf("可配缝失效: %d", SameModelMaxTokens)
	}
}

func TestHandoffLaneConstantsDistinct(t *testing.T) {
	lanes := []string{HandoffLaneSameModel, HandoffLaneThirdParty, HandoffLaneSkeleton}
	seen := map[string]bool{}
	for _, l := range lanes {
		if l == "" || seen[l] {
			t.Fatalf("lane 三档须非空互异, got %v", lanes)
		}
		seen[l] = true
	}
}

func TestParseSameModelOutputStrict(t *testing.T) {
	valid := "开场白不要\n" + InjectOpen + "\n注入层内容\n" + InjectClose + "\n# 目标\n做某事\n# 续接第一句话\n下一句"
	inject, full, err := ParseSameModelOutput(valid)
	if err != nil {
		t.Fatalf("合法输出应通过: %v", err)
	}
	if inject != "注入层内容" {
		t.Fatalf("inject = %q", inject)
	}
	if !strings.HasPrefix(full, "# 目标") || !strings.Contains(full, "下一句") {
		t.Fatalf("full = %q", full)
	}
	cases := []struct {
		name string
		in   string
	}{
		{"缺开标记", "只有正文 " + InjectClose + " 全文"},
		{"缺闭标记", InjectOpen + " 注入层 没有闭标记"},
		{"闭在开前", InjectClose + " 反了 " + InjectOpen + " 注入"},
		{"注入层空", InjectOpen + "   \n" + InjectClose + "\n# 目标\n正文"},
		{"全文空", InjectOpen + " 注入层 " + InjectClose + "\n  \n"},
		{"空串", ""},
	}
	for _, tc := range cases {
		if _, _, err := ParseSameModelOutput(tc.in); err == nil {
			t.Fatalf("%s: 应判不合交接 MD 结构(该档失败)", tc.name)
		}
	}
}

func TestParseSameModelOutputTrimsOversizeInject(t *testing.T) {
	huge := strings.Repeat("字", InjectBudget+500)
	reply := InjectOpen + "\n" + huge + "\n" + InjectClose + "\n# 目标\n正文"
	inject, _, err := ParseSameModelOutput(reply)
	if err != nil {
		t.Fatal(err)
	}
	if l := len([]rune(inject)); l > InjectBudget+10 { // 截断标记的少量余量
		t.Fatalf("超预算注入层应硬截断, got %d 字", l)
	}
}

func TestSameModelMarkdownComposes(t *testing.T) {
	narrative := InjectOpen + "\n注入层:目标过半\n" + InjectClose + "\n# 目标\n做完 A\n# 续接第一句话\n接 B"
	skeleton := "## 末段定格（最后一轮对话，程序化保留）\n[user] 问\n## 确定性骨架（程序化抽取，未经模型）\n- 标题: T"
	meta := map[string]any{"model": "glm-5.3", "mode": "same_model", "wall_s": 3.2}
	md, inject, err := SameModelMarkdown("标题T", narrative, skeleton, meta)
	if err != nil {
		t.Fatal(err)
	}
	if inject != "注入层:目标过半" {
		t.Fatalf("inject = %q", inject)
	}
	// 产物结构与第三方档同构:头部 + INJECT 层 + 分隔 + 全文(尾部拼程序骨架)。
	for _, want := range []string{"[Ferryman 交接 · 会话 标题T]", InjectOpen, InjectClose,
		"做完 A", "确定性骨架（程序化抽取，未经模型）"} {
		if !strings.Contains(md, want) {
			t.Fatalf("产物缺 %q:\n%s", want, md)
		}
	}
	if !strings.HasSuffix(strings.TrimSpace(md), strings.TrimSpace(skeleton)) {
		t.Fatalf("程序骨架须拼在全文尾(确定性地面真值):\n%.200s", md[len(md)-300:])
	}
	if idx := strings.Index(md, "做完 A"); idx == -1 || idx > strings.Index(md, "确定性骨架") {
		t.Fatal("叙事须在骨架之前")
	}
	// 单源证明:与 HandoffMarkdown(同输入)逐字相等。
	inj, full, _ := ParseSameModelOutput(narrative)
	want := HandoffMarkdown("标题T", inj, full+"\n\n"+skeleton, meta)
	if md != want {
		t.Fatal("SameModelMarkdown 与 HandoffMarkdown 单源分叉")
	}
}
