package qwatch

// 规格：tests/test_qwatch.py 全部 11 例 1:1（T51 票01 提问潮检测器）
// + tests/test_qwatch_e2e.py 的漏检关联纯计数 2 例（票06）
// + 全角样本用例（票面钉子：期望值以 uv run python 实测
// ferryman.qwatch._classify 钉死，2026-09-19）。
// 隐私铁律：Verdict 只有计数与布尔，任何字段不携带消息内容。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ferryman/internal/beat"
)

const tsStr = "2026-09-18T12:00:00.000Z"

// surge8 正例语料：❓ **Qn** 家族格式、8 个问题单元（spec 决策 1 的靶消息形态）。
var surge8 = strings.Join([]string{
	"好，先把口径一次对齐，逐条答我：",
	"❓ **Q1** - **TTL 口径**：保温间隔按实测 600s 还是配置值？",
	"❓ **Q2** - **预算封顶**：单会话每日上限多少，超了先停谁？",
	"❓ **Q3** - **熔断语义**：连续 MISS 两次降级后，要不要人工拨回？",
	"❓ **Q4** - **窗口互斥**：与停车窗同时命中，先开者赢还是检测器优先？",
	"❓ **Q5** - **摆渡死线**：强制入队提前量 480s 够不够，要不要留骨架？",
	"❓ **Q6** - **费用科目**：心跳花费记独立科目还是并入摆渡账？",
	"❓ **Q7** - **observe 时长**：跑满一周还是攒够 10 次命中就复核？",
	"❓ **Q8** - **开关归属**：一键停改 mode 配置，还是另设 kill 开关？",
}, "\n")

// marshalLine 对应 Python 的 _line(d)（json.dumps ensure_ascii=False）。
func marshalLine(t *testing.T, d map[string]any) string {
	t.Helper()
	b, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// assistantMsg 对应 Python 的 _assistant(text=None, tools=(), mid="msg_1")。
func assistantMsg(text any, tools [][2]string, mid string) map[string]any {
	blocks := []any{}
	if text != nil {
		blocks = append(blocks, map[string]any{"type": "text", "text": text})
	}
	for _, tl := range tools {
		blocks = append(blocks, map[string]any{
			"type": "tool_use", "id": tl[0], "name": tl[1], "input": map[string]any{}})
	}
	return map[string]any{
		"type": "assistant", "timestamp": tsStr,
		"message": map[string]any{"id": mid, "role": "assistant", "content": blocks,
			"usage": map[string]any{"input_tokens": 10, "cache_read_input_tokens": 100,
				"cache_creation_input_tokens": 0, "output_tokens": 5}},
	}
}

// resultMsg 对应 Python 的 _result(*tool_ids)。
func resultMsg(toolIDs ...string) map[string]any {
	blocks := make([]any, 0, len(toolIDs))
	for _, tid := range toolIDs {
		blocks = append(blocks, map[string]any{
			"type": "tool_result", "tool_use_id": tid, "content": "ok"})
	}
	return map[string]any{
		"type": "user", "timestamp": tsStr,
		"message": map[string]any{"role": "user", "content": blocks},
	}
}

// writeFile 对应 Python 的 _write(tmp_path, lines, name="s.jsonl")。
func writeFile(t *testing.T, dir, name string, lines []string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestT51SurgePositiveFixture(t *testing.T) {
	// 已知正例：❓ **Qn** 家族 8 单元 → is_surge=True，判据构成如实。
	f := writeFile(t, t.TempDir(), "s.jsonl",
		[]string{marshalLine(t, assistantMsg(surge8, nil, "msg_1"))})
	v := Detect(f, DefaultMinQuestions)
	if !v.IsSurge {
		t.Errorf("IsSurge = false, want true")
	}
	if v.UnitCount != 8 {
		t.Errorf("UnitCount = %d, want 8", v.UnitCount)
	}
	want := Breakdown{MarkerLines: 8, QmarkLines: 0, QualifiedNumberedLines: 0}
	if v.BD != want {
		t.Errorf("BD = %+v, want %+v", v.BD, want)
	}
	if !v.AskUserQuestionDangling {
		t.Errorf("AskUserQuestionDangling = false, want true（无 tool_use：空集 ⊆ {AQ}，真空真口径）")
	}
}

func TestT51NegativeStatusReportNotSurge(t *testing.T) {
	// 反例：状态汇报/步骤清单/票台账（编号行无疑问词无疑问号）＋散落 2 问 → 不触发。
	report := strings.Join([]string{
		"任务完成，汇报如下：",
		"1. 读取配置文件，确认阈值生效。",
		"2. 重跑测试套件，204 用例全绿。",
		"3. 更新台账，写入费用科目。",
		"4. 生成报告，输出对比表格。",
		"- 检测器票：绿。",
		"- 调度器票：绿。",
		"下一步要不要我继续优化？",
		"另外，文档是否同步？",
		"缓存策略沿用既有口径，无变更。",
	}, "\n")
	f := writeFile(t, t.TempDir(), "s.jsonl",
		[]string{marshalLine(t, assistantMsg(report, nil, "msg_1"))})
	v := Detect(f, DefaultMinQuestions)
	if v.IsSurge {
		t.Errorf("IsSurge = true, want false")
	}
	if v.UnitCount != 2 {
		t.Errorf("UnitCount = %d, want 2", v.UnitCount)
	}
	if v.BD.QualifiedNumberedLines != 0 {
		t.Errorf("QualifiedNumberedLines = %d, want 0", v.BD.QualifiedNumberedLines)
	}
	if v.BD.QmarkLines != 2 {
		t.Errorf("QmarkLines = %d, want 2", v.BD.QmarkLines)
	}
	if v.BD.MarkerLines != 0 {
		t.Errorf("MarkerLines = %d, want 0", v.BD.MarkerLines)
	}
}

func TestT51CodeBlockStripped(t *testing.T) {
	// 代码块内的问号/编号不计数（先剥块再判定）。
	msg := strings.Join([]string{
		"两个口径问题：",
		"❓ **Q1** - **费率**：按哪个档位算？",
		"❓ **Q2** - **窗口**：多长合适？",
		"示例查询：",
		"```sql",
		"SELECT * FROM t WHERE a = ? AND b = ?;",
		"-- 1. 这行是问句吗？算不算编号？",
		"```",
		"以上。",
	}, "\n")
	f := writeFile(t, t.TempDir(), "s.jsonl",
		[]string{marshalLine(t, assistantMsg(msg, nil, "msg_1"))})
	v := Detect(f, DefaultMinQuestions)
	if v.UnitCount != 2 {
		t.Errorf("UnitCount = %d, want 2", v.UnitCount)
	}
	want := Breakdown{MarkerLines: 2, QmarkLines: 0, QualifiedNumberedLines: 0}
	if v.BD != want {
		t.Errorf("BD = %+v, want %+v", v.BD, want)
	}
	if v.IsSurge {
		t.Errorf("IsSurge = true, want false")
	}
}

func TestT51SingleQuestionNotSurge(t *testing.T) {
	// 一次一问（1 单元）不触发；min_questions 是参数。
	f := writeFile(t, t.TempDir(), "s.jsonl",
		[]string{marshalLine(t, assistantMsg("这个配置要不要保留？", nil, "msg_1"))})
	v := Detect(f, DefaultMinQuestions)
	if v.UnitCount != 1 {
		t.Errorf("UnitCount = %d, want 1", v.UnitCount)
	}
	if v.IsSurge {
		t.Errorf("IsSurge = true, want false")
	}
	if !Detect(f, 1).IsSurge {
		t.Errorf("min_questions=1: IsSurge = false, want true")
	}
}

func TestT51NumberedLineNeedsQuestionSignal(t *testing.T) {
	// 紧档核心：编号/列表行含问号或疑问词才计入，纯步骤行不计。
	msg := strings.Join([]string{
		"1. 你想要什么口径？",
		"2、哪个先做？",
		"3) Tell me HOW it works", // 疑问词（英文，大小写不敏感）无问号也计
		"4. 这条是纯步骤，直接执行。",
		"5. 这条也是纯步骤。",
	}, "\n")
	f := writeFile(t, t.TempDir(), "s.jsonl",
		[]string{marshalLine(t, assistantMsg(msg, nil, "msg_1"))})
	v := Detect(f, DefaultMinQuestions)
	if v.UnitCount != 3 {
		t.Errorf("UnitCount = %d, want 3", v.UnitCount)
	}
	want := Breakdown{MarkerLines: 0, QmarkLines: 0, QualifiedNumberedLines: 3}
	if v.BD != want {
		t.Errorf("BD = %+v, want %+v", v.BD, want)
	}
	if v.IsSurge {
		t.Errorf("IsSurge = true, want false")
	}
}

func TestT51AskUserQuestionDanglingTrueSurgeUnaffected(t *testing.T) {
	// 悬空 tool_use 仅 AskUserQuestion：aq=True 且不影响 is_surge（靶场景豁免）。
	f := writeFile(t, t.TempDir(), "s.jsonl", []string{marshalLine(t, assistantMsg(surge8,
		[][2]string{{"t1", "AskUserQuestion"}}, "msg_1"))})
	v := Detect(f, DefaultMinQuestions)
	if !v.AskUserQuestionDangling {
		t.Errorf("AskUserQuestionDangling = false, want true")
	}
	if !v.IsSurge {
		t.Errorf("IsSurge = false, want true")
	}
	if v.UnitCount != 8 {
		t.Errorf("UnitCount = %d, want 8", v.UnitCount)
	}
}

func TestT51DanglingOtherToolReportedFalse(t *testing.T) {
	// 悬空含其他工具 → 如实 False；{AskUserQuestion, 其他} 混合也 False。
	f1 := writeFile(t, t.TempDir(), "s.jsonl", []string{marshalLine(t, assistantMsg(surge8,
		[][2]string{{"t1", "Bash"}}, "msg_1"))})
	v1 := Detect(f1, DefaultMinQuestions)
	if v1.AskUserQuestionDangling {
		t.Errorf("AskUserQuestionDangling = true, want false")
	}
	if !v1.IsSurge {
		t.Errorf("IsSurge = false, want true（悬空与否不参与 is_surge）")
	}
	f2 := writeFile(t, t.TempDir(), "s2.jsonl", []string{marshalLine(t, assistantMsg(surge8,
		[][2]string{{"t1", "AskUserQuestion"}, {"t2", "Task"}}, "msg_1"))})
	if Detect(f2, DefaultMinQuestions).AskUserQuestionDangling {
		t.Errorf("mixed dangling: AskUserQuestionDangling = true, want false")
	}
}

func TestT51DanglingEmptySetIsVacuousTrue(t *testing.T) {
	// 无悬空（tool_use 已全部回包 / 纯文本无工具）→ 空集 ⊆ {AQ} 记 True。
	// 字段真实语义＝「悬空集不含 AskUserQuestion 之外的工具」，谓词②可直接用。
	f1 := writeFile(t, t.TempDir(), "s.jsonl", []string{
		marshalLine(t, assistantMsg("就一个问题。", [][2]string{{"t1", "AskUserQuestion"}}, "msg_1")),
		marshalLine(t, resultMsg("t1")),
	})
	if !Detect(f1, DefaultMinQuestions).AskUserQuestionDangling {
		t.Errorf("all served: AskUserQuestionDangling = false, want true")
	}
	f2 := writeFile(t, t.TempDir(), "s2.jsonl",
		[]string{marshalLine(t, assistantMsg("纯文本，无工具调用。", nil, "msg_1"))})
	if !Detect(f2, DefaultMinQuestions).AskUserQuestionDangling {
		t.Errorf("pure text: AskUserQuestionDangling = false, want true")
	}
}

func TestT51UsesLastTextAssistantMessage(t *testing.T) {
	// 只判末条带文本的 assistant 消息：前一条提问潮被后一条普通答复覆盖。
	f := writeFile(t, t.TempDir(), "s.jsonl", []string{
		marshalLine(t, assistantMsg(surge8, nil, "msg_1")),
		marshalLine(t, assistantMsg("干完了，无问题。", nil, "msg_2")),
	})
	v := Detect(f, DefaultMinQuestions)
	if v.IsSurge {
		t.Errorf("IsSurge = true, want false")
	}
	if v.UnitCount != 0 {
		t.Errorf("UnitCount = %d, want 0", v.UnitCount)
	}
}

func TestT51StreamingChunksOfSameMessageMerged(t *testing.T) {
	// 同 message id 的流式分片（多行各带部分文本块）合并后再判定。
	half1 := strings.Join([]string{
		"❓ **Q1** - **标题1**：要哪个？",
		"❓ **Q2** - **标题2**：要哪个？",
		"❓ **Q3** - **标题3**：要哪个？",
		"❓ **Q4** - **标题4**：要哪个？",
	}, "\n")
	half2 := strings.Join([]string{
		"❓ **Q5** - **标题5**：要哪个？",
		"❓ **Q6** - **标题6**：要哪个？",
		"❓ **Q7** - **标题7**：要哪个？",
		"❓ **Q8** - **标题8**：要哪个？",
	}, "\n")
	f := writeFile(t, t.TempDir(), "s.jsonl", []string{
		marshalLine(t, assistantMsg(half1, nil, "msg_1")),
		marshalLine(t, assistantMsg(half2, nil, "msg_1")),
	})
	v := Detect(f, DefaultMinQuestions)
	if !v.IsSurge {
		t.Errorf("IsSurge = false, want true")
	}
	if v.UnitCount != 8 {
		t.Errorf("UnitCount = %d, want 8", v.UnitCount)
	}
}

func TestT51BadLinesAndEmptyFileSafe(t *testing.T) {
	// 坏行/空文件/缺文件安全跳过：不抛错，零计数兜底（沿 transcripts 风格）。
	f := writeFile(t, t.TempDir(), "s.jsonl", []string{
		"这不是json", "{broken", "", marshalLine(t, assistantMsg(surge8, nil, "msg_1")),
	})
	v := Detect(f, DefaultMinQuestions)
	if !v.IsSurge {
		t.Errorf("IsSurge = false, want true")
	}
	if v.UnitCount != 8 {
		t.Errorf("UnitCount = %d, want 8", v.UnitCount)
	}

	empty := filepath.Join(t.TempDir(), "empty.jsonl")
	if err := os.WriteFile(empty, []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	ve := Detect(empty, DefaultMinQuestions)
	if ve.IsSurge {
		t.Errorf("empty: IsSurge = true, want false")
	}
	if ve.UnitCount != 0 {
		t.Errorf("empty: UnitCount = %d, want 0", ve.UnitCount)
	}
	want := Breakdown{MarkerLines: 0, QmarkLines: 0, QualifiedNumberedLines: 0}
	if ve.BD != want {
		t.Errorf("empty: BD = %+v, want %+v", ve.BD, want)
	}
	if !ve.AskUserQuestionDangling {
		t.Errorf("empty: AskUserQuestionDangling = false, want true（空集口径）")
	}

	missing := Detect(filepath.Join(t.TempDir(), "nope.jsonl"), DefaultMinQuestions)
	if missing.IsSurge {
		t.Errorf("missing: IsSurge = true, want false")
	}
	if missing.UnitCount != 0 {
		t.Errorf("missing: UnitCount = %d, want 0", missing.UnitCount)
	}
	if !missing.AskUserQuestionDangling {
		t.Errorf("missing: AskUserQuestionDangling = false, want true（读失败真空真）")
	}
}

// ---------- 全角样本（票面钉子；期望值 = uv run python 实测 _classify） ----------

func TestFullWidthSamples(t *testing.T) {
	cases := []struct {
		name string
		text string
		want Breakdown
	}{
		// **Q １**（全角数字+可选全角空格）三态全认标记桶
		{"marker全角数字无空格", "**Q１** - **标题**：要哪个？",
			Breakdown{MarkerLines: 1, QmarkLines: 0, QualifiedNumberedLines: 0}},
		{"marker全角数字ASCII空格", "**Q １** - **标题**：要哪个？",
			Breakdown{MarkerLines: 1, QmarkLines: 0, QualifiedNumberedLines: 0}},
		{"marker全角数字全角空格", "**Q　１** - **标题**：要哪个？",
			Breakdown{MarkerLines: 1, QmarkLines: 0, QualifiedNumberedLines: 0}},
		// 全角Ｑ不认标记（\*\*Q 不匹配 Ｑ）；行首 * 进列表桶＋疑问号计 1
		{"全角Q不认标记_行首星进列表桶", "**Ｑ１** - **标题**：要哪个？",
			Breakdown{MarkerLines: 0, QmarkLines: 0, QualifiedNumberedLines: 1}},
		// 全角数字编号行：前导全角空格（U+3000 ∈ \s）+ 全角数字（\p{Nd}）
		{"全角编号前导全角空格", "　１、你想要什么口径？",
			Breakdown{MarkerLines: 0, QmarkLines: 0, QualifiedNumberedLines: 1}},
		{"全角编号无前导空格", "２、哪个先做？",
			Breakdown{MarkerLines: 0, QmarkLines: 0, QualifiedNumberedLines: 1}},
		{"全角编号无疑问信号不计", "　３. 这条是纯步骤，直接执行。",
			Breakdown{MarkerLines: 0, QmarkLines: 0, QualifiedNumberedLines: 0}},
		// 全角右括号不在 [.、)] → 不进编号桶，落问号桶
		{"全角右括号非编号桶", "５）这个怎么算？",
			Breakdown{MarkerLines: 0, QmarkLines: 1, QualifiedNumberedLines: 0}},
		// 混合：标记 1 + 全角编号 1（纯步骤行不计）
		{"混合", "**Q　１** - **TTL**：保温按实测 600s 还是配置值？\n　１、预算封顶多少？\n　２、这条是纯步骤。",
			Breakdown{MarkerLines: 1, QmarkLines: 0, QualifiedNumberedLines: 1}},
	}
	for _, c := range cases {
		f := writeFile(t, t.TempDir(), "fw.jsonl",
			[]string{marshalLine(t, assistantMsg(c.text, nil, "msg_1"))})
		v := Detect(f, DefaultMinQuestions)
		if v.BD != c.want {
			t.Errorf("%s: BD = %+v, want %+v", c.name, v.BD, c.want)
		}
	}
}

// ---------- 漏检关联纯计数（票06，test_qwatch_e2e.py 2 例 1:1） ----------

const t0 = 1_800_000_000.0 // 纯函数不吃系统钟：任意固定锚点

func hitRow(sid string, ts float64) map[string]any {
	return map[string]any{"kind": "qwatch_hit", "session_id": sid, "ts": ts}
}

func usageRow(sid string, ts float64, cacheRead float64) map[string]any {
	return map[string]any{"kind": "usage", "session_id": sid, "ts": ts,
		"input_tokens": 30_000.0, "cache_read_tokens": cacheRead,
		"cache_creation_tokens": 0.0, "output_tokens": 5.0}
}

func beatRow(sid string, ts float64, outcome string) map[string]any {
	return map[string]any{"kind": "beat", "session_id": sid, "ts": ts, "outcome": outcome}
}

func TestMissSignalCountsSurgeThenFullRepayRevival(t *testing.T) {
	// 正例：末条疑似提问命中 → observe 演练零保温 → ≥TTL 闲置后复活请求
	// 缓存零命中（全量重付）→ 计 1；两会话各自独立计。
	rows := []map[string]any{
		hitRow("s1", t0),
		usageRow("s1", t0, 100),                // 提问潮落盘（缓存刚写）
		beatRow("s1", t0+420, beat.OutObserve), // 演练跳：未真发，不保温
		usageRow("s1", t0+1200, 0),             // 20min 后复活：全量重付
	}
	if got := CorrelateMissSignals(rows); got != 1 {
		t.Errorf("CorrelateMissSignals = %d, want 1", got)
	}
	rows = append(rows,
		hitRow("s2", t0+5),
		usageRow("s2", t0+5, 100),
		usageRow("s2", t0+5+900, 0))
	if got := CorrelateMissSignals(rows); got != 2 {
		t.Errorf("CorrelateMissSignals = %d, want 2（会话间独立）", got)
	}
}

func TestMissSignalNegativeControls(t *testing.T) {
	// 反例逐条不计：无命中／缓存命中／间隙不足／首条用量行／其间有真跳／
	// 命中超出 30 分钟回看窗。
	gap := MissIdleS + 60
	// ① 无命中可对照（检测没见过疑似提问）
	if got := CorrelateMissSignals([]map[string]any{
		usageRow("a", t0, 100),
		usageRow("a", t0+gap, 0),
	}); got != 0 {
		t.Errorf("①无命中: got %d, want 0", got)
	}
	// ② 复活但缓存命中（没全量重付）
	if got := CorrelateMissSignals([]map[string]any{
		hitRow("b", t0), usageRow("b", t0, 100),
		usageRow("b", t0+gap, 28_000),
	}); got != 0 {
		t.Errorf("②缓存命中: got %d, want 0", got)
	}
	// ③ 间隙不足 MISS_IDLE_S（TTL 内密集交互，非闲置复活）
	if got := CorrelateMissSignals([]map[string]any{
		hitRow("c", t0), usageRow("c", t0, 100),
		usageRow("c", t0+60, 0),
	}); got != 0 {
		t.Errorf("③间隙不足: got %d, want 0", got)
	}
	// ④ 会话首条用量行（冷启动，无前驱间隙可言）
	if got := CorrelateMissSignals([]map[string]any{
		hitRow("d", t0), usageRow("d", t0+gap, 0),
	}); got != 0 {
		t.Errorf("④首条用量行: got %d, want 0", got)
	}
	// ⑤ 命中与重付之间有真跳保温（检测与执行已尽职——其后重付归
	//    TTL 漂移/死区取舍，由熔断与 D4 遥测管辖，不计漏检）
	if got := CorrelateMissSignals([]map[string]any{
		hitRow("e", t0), usageRow("e", t0, 100),
		beatRow("e", t0+420, beat.OutHit),
		usageRow("e", t0+gap, 0),
	}); got != 0 {
		t.Errorf("⑤真跳保温: got %d, want 0", got)
	}
	// ⑥ 命中超出 30 分钟回看窗（与本次重付无关联）
	if got := CorrelateMissSignals([]map[string]any{
		hitRow("f", t0-MissLookbackS-1),
		usageRow("f", t0-MissLookbackS-1, 100),
		usageRow("f", t0, 0),
	}); got != 0 {
		t.Errorf("⑥回看窗外: got %d, want 0", got)
	}
}
