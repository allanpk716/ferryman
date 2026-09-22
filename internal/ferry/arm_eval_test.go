// arm_eval_test.go — 票04:实跳臂四条成功标准评估单测(先红后绿)。
//
// 覆盖(ADR-0015 决定一【启用硬门槛】):
//
//	① 追加后缓存读占比 ≥ 同前缀心跳基线(占比口径 = beat.Classify 单源:
//	  cache_read/(cache_read+input));两跳任一未成 → 不可判定;
//	② 与捕获快照字节差异仅末尾追加段+max_tokens 数值子区间(独立 span 校验,
//	  不信任构造方自证);
//	③ stop_reason 非 tool_use;
//	④ 输出可解析为交接 MD(复用票03 ParseSameModelOutput,不要第二份解析)。
package ferry

import (
	"bytes"
	"strings"
	"testing"

	"ferryman/internal/beat"
)

const armEvalInstr = SameModelInstruction

// armGoodReply 合格的交接 MD 输出(两标记齐、两层非空)。
const armGoodReply = "开场白\n<<<INJECT>>>\n目标:测完即走\n<<</INJECT>>>\n# 目标\n测完即走\n# 续接第一句话\n接着测。"

// armSrcBody 标准捕获快照体(max_tokens=32000,典型 CC 形态)。
const armSrcBody = `{"model":"glm-5.3","max_tokens":32000,"messages":[` +
	`{"role":"user","content":"你好,帮我看看这个会话"},{"role":"assistant","content":"好的"}]}`

// armHop 构造一跳结果:input=100*(1-r) 不整除时改用显式对。测试里直接给定
// input/cacheRead 整数对更诚实——比值断言用浮点精确对(10,90)→0.9 等。
func TestCacheReadRatio(t *testing.T) {
	if _, ok := CacheReadRatio(0, 0); ok {
		t.Fatal("全零 usage 无信号,应不可判定")
	}
	if r, ok := CacheReadRatio(10, 90); !ok || r != 0.9 {
		t.Fatalf("ratio(10,90) = %v,%v; want 0.9,true", r, ok)
	}
	if r, _ := CacheReadRatio(90, 10); r != 0.1 {
		t.Fatalf("ratio(90,10) = %v; want 0.1", r)
	}
	if r, _ := CacheReadRatio(0, 5); r != 1.0 {
		t.Fatalf("纯缓存读 = %v; want 1.0", r)
	}
}

// ---- 标准②:字节差异仅末尾追加段 + max_tokens 数值子区间 ----

func TestVerifyDiffAppendOnlyPass(t *testing.T) {
	cases := []struct {
		name   string
		src    string
		maxTok int
	}{
		{"纯追加(max_tokens 恰等零编辑)", `{"model":"glm-5.3","max_tokens":4096,"messages":[{"role":"user","content":"hi"}]}`, 4096},
		{"追加+max_tokens 改写(32000→4096)", armSrcBody, 4096},
		{"快照缺 max_tokens(补键)", `{"model":"glm-5.3","messages":[{"role":"user","content":"hi"}]}`, 4096},
		{"空 messages 数组(首元素)", `{"max_tokens":32000,"messages":[]}`, 4096},
		{"messages 值尾随空白保留", `{"max_tokens":32000,"messages":[{"role":"user","content":"hi"}]   }`, 4096},
		{"转义形态原样保留", `{"max_tokens":32000,"messages":[{"role":"user","content":"a\"b\n你好"}]}`, 4096},
		{"max_tokens 位于 messages 之后", `{"messages":[{"role":"user","content":"hi"}],"max_tokens":32000,"model":"glm-5.3"}`, 4096},
	}
	for _, c := range cases {
		out, _, err := beat.AppendReplayBody([]byte(c.src), c.maxTok, armEvalInstr)
		if err != nil {
			t.Fatalf("%s: 构造失败: %v", c.name, err)
		}
		if err := VerifyAppendOnlyDiff([]byte(c.src), out, c.maxTok); err != nil {
			t.Fatalf("%s: 应判仅末尾追加: %v", c.name, err)
		}
	}
}

func TestVerifyDiffViolationsFail(t *testing.T) {
	appended, _, err := beat.AppendReplayBody([]byte(armSrcBody), 4096, armEvalInstr)
	if err != nil {
		t.Fatal(err)
	}
	mk := func(mutate func(out []byte) []byte) []byte {
		return mutate(bytes.Clone(appended))
	}
	cases := []struct {
		name string
		src  string
		out  []byte
	}{
		{"全等无追加(未追加即非追加形态)", armSrcBody, []byte(armSrcBody)},
		{"中段正文被改", armSrcBody, mk(func(o []byte) []byte {
			return bytes.Replace(o, []byte("好的"), []byte("被改"), 1)
		})},
		{"max_tokens 数值不符", armSrcBody, mk(func(o []byte) []byte {
			return bytes.Replace(o, []byte(`"max_tokens":4096`), []byte(`"max_tokens":8192`), 1)
		})},
		{"多出顶层键", armSrcBody, mk(func(o []byte) []byte {
			return bytes.Replace(o, []byte(`{"model"`), []byte(`{"temperature":0.5,"model"`), 1)
		})},
		{"顶层键序被重排", armSrcBody, []byte(
			`{"max_tokens":4096,"model":"glm-5.3","messages":[{"role":"user","content":"你好,帮我看看这个会话"},{"role":"assistant","content":"好的"},{"role":"user","content":"X"}]}`)},
		{"少了一条原消息", armSrcBody, []byte(
			`{"model":"glm-5.3","max_tokens":4096,"messages":[{"role":"user","content":"你好,帮我看看这个会话"},{"role":"user","content":"追加"}]}`)},
		{"追加插在中间而非末尾", armSrcBody, []byte(
			`{"model":"glm-5.3","max_tokens":4096,"messages":[{"role":"user","content":"你好,帮我看看这个会话"},{"role":"user","content":"追加"},{"role":"assistant","content":"好的"}]}`)},
		{"追加进了 system 而非 messages", `{"model":"m","max_tokens":32000,"system":"sys","messages":[{"role":"user","content":"hi"}]}`,
			[]byte(`{"model":"m","max_tokens":4096,"system":"sys追加","messages":[{"role":"user","content":"hi"}]}`)},
	}
	for _, c := range cases {
		if err := VerifyAppendOnlyDiff([]byte(c.src), c.out, 4096); err == nil {
			t.Fatalf("%s: 应判违规, 却通过", c.name)
		}
	}
	// 非对象体与坏 JSON:显式报错。
	if err := VerifyAppendOnlyDiff([]byte(`[1,2]`), []byte(`[1,2]`), 4096); err == nil {
		t.Fatal("非对象快照应报错")
	}
	if err := VerifyAppendOnlyDiff([]byte(`{`), []byte(`{`), 4096); err == nil {
		t.Fatal("坏 JSON 应报错")
	}
}

// TestVerifyDiffAppendKeyFormTamperFails 补键形态(快照缺 max_tokens)的收口
// 盲区回归:收口曾传 aligned[:len(aligned)-1],最后一个公共键的值级校验整段
// 被跳过——评审探针:messages 中段正文被改仍判 pass。回归在此。
func TestVerifyDiffAppendKeyFormTamperFails(t *testing.T) {
	// 探针一:最后一个公共键恰是 messages,中段正文被改必须判违规。
	src := `{"model":"glm-5.3","messages":[{"role":"user","content":"你好,帮我看看这个会话"}]}`
	out, _, err := beat.AppendReplayBody([]byte(src), 4096, armEvalInstr)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyAppendOnlyDiff([]byte(src), out, 4096); err != nil {
		t.Fatalf("合法补键形态应照过: %v", err)
	}
	tampered := bytes.Replace(out, []byte("你好,帮我看看这个会话"), []byte("中段正文被改"), 1)
	if bytes.Equal(tampered, out) {
		t.Fatal("探针未生效(快照正文未在追加体中原样出现)")
	}
	if err := VerifyAppendOnlyDiff([]byte(src), tampered, 4096); err == nil {
		t.Fatal("补键形态下 messages 中段被改应判违规, 却通过")
	}
	// 探针二:最后一个公共键非 messages(值为尾键),同一盲区同验。
	src2 := `{"messages":[{"role":"user","content":"hi"}],"model":"glm-5.3"}`
	out2, _, err := beat.AppendReplayBody([]byte(src2), 4096, armEvalInstr)
	if err != nil {
		t.Fatal(err)
	}
	tampered2 := bytes.Replace(out2, []byte("glm-5.3"), []byte("glm-4.5"), 1)
	if bytes.Equal(tampered2, out2) {
		t.Fatal("探针未生效(model 值未在追加体中出现)")
	}
	if err := VerifyAppendOnlyDiff([]byte(src2), tampered2, 4096); err == nil {
		t.Fatal("补键形态下尾键值被改应判违规, 却通过")
	}
}

// ---- 标准①③④与总判定 ----

func armHop(input, cacheRead, output int, stop, text string) ArmHopResult {
	return ArmHopResult{
		Sent: true, OK: true,
		StopReason: stop, Text: text,
		Input: input, CacheRead: cacheRead, Output: output,
		Model: "glm-5.3",
	}
}

func armEval(t *testing.T, base, app ArmHopResult) ArmEvaluation {
	t.Helper()
	out, _, err := beat.AppendReplayBody([]byte(armSrcBody), 4096, armEvalInstr)
	if err != nil {
		t.Fatal(err)
	}
	return EvaluateArm([]byte(armSrcBody), out, 4096, base, app)
}

func armCrit(t *testing.T, e ArmEvaluation, name string) ArmCriterion {
	for _, c := range e.Criteria {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("缺标准 %s", name)
	return ArmCriterion{}
}

func TestEvaluateAllPassVerdictPassed(t *testing.T) {
	e := armEval(t,
		armHop(10, 1990, 1, "", ""),                     // 心跳基线:0.995
		armHop(10, 1990, 800, "end_turn", armGoodReply)) // 追加跳:同占比
	if e.Verdict != ArmVerdictPassed {
		t.Fatalf("四条全过应 passed, got %q(%+v)", e.Verdict, e.Criteria)
	}
	for _, c := range e.Criteria {
		if c.Status != ArmStatusPass {
			t.Fatalf("%s 应 pass, got %q: %s", c.Name, c.Status, c.Detail)
		}
	}
}

func TestEvaluateRatioBelowBaselineFails(t *testing.T) {
	e := armEval(t,
		armHop(10, 1990, 1, "", ""),                      // 基线 0.995
		armHop(1500, 500, 800, "end_turn", armGoodReply)) // 追加跳 0.25
	if c := armCrit(t, e, ArmCritRatio); c.Status != ArmStatusFail {
		t.Fatalf("占比退化应 fail, got %q", c.Status)
	}
	if e.Verdict != ArmVerdictFailed {
		t.Fatalf("任一未过即 failed, got %q", e.Verdict)
	}
	if e.BaselineRatio != 0.995 || e.AppendRatio != 0.25 {
		t.Fatalf("占比应留档: base=%v append=%v", e.BaselineRatio, e.AppendRatio)
	}
}

func TestEvaluateRatioEqualPasses(t *testing.T) {
	// 硬口径是 ≥:恰好相等仍过。
	e := armEval(t,
		armHop(100, 100, 1, "", ""),
		armHop(100, 100, 800, "end_turn", armGoodReply))
	if c := armCrit(t, e, ArmCritRatio); c.Status != ArmStatusPass {
		t.Fatalf("占比相等应 pass(≥), got %q", c.Status)
	}
}

func TestEvaluateRatioNotEvaluatable(t *testing.T) {
	// 基线跳发送失败 → ① 不可判定 → 总判定 inconclusive(不落状态)。
	base := armHop(10, 1990, 1, "", "")
	base.OK = false
	base.Err = "http_5xx"
	e := armEval(t, base, armHop(10, 1990, 800, "end_turn", armGoodReply))
	if c := armCrit(t, e, ArmCritRatio); c.Status != ArmStatusNA {
		t.Fatalf("基线跳失败应 not_evaluatable, got %q", c.Status)
	}
	if e.Verdict != ArmVerdictInconclusive {
		t.Fatalf("有未判定无未过 → inconclusive, got %q", e.Verdict)
	}
	// 追加跳全零 usage 同理不可判定。
	app := armHop(0, 0, 800, "end_turn", armGoodReply)
	e = armEval(t, armHop(10, 1990, 1, "", ""), app)
	if c := armCrit(t, e, ArmCritRatio); c.Status != ArmStatusNA {
		t.Fatalf("全零 usage 应 not_evaluatable, got %q", c.Status)
	}
}

func TestEvaluateToolUseFails(t *testing.T) {
	e := armEval(t,
		armHop(10, 1990, 1, "", ""),
		armHop(10, 1990, 800, "tool_use", armGoodReply))
	if c := armCrit(t, e, ArmCritNoToolUse); c.Status != ArmStatusFail {
		t.Fatalf("tool_use 应 fail, got %q", c.Status)
	}
	if e.Verdict != ArmVerdictFailed {
		t.Fatalf("got %q", e.Verdict)
	}
}

func TestEvaluateBadHandoffMDFails(t *testing.T) {
	for name, text := range map[string]string{
		"无标记散文": "没有标记的普通输出",
		"只有开标记": "<<<INJECT>>>\n半截",
		"顺序颠倒":  "<<</INJECT>>>前<<<INJECT>>>后",
		"注入层为空": "<<<INJECT>>>\n<<</INJECT>>>\n# 目标\n正文",
	} {
		e := armEval(t,
			armHop(10, 1990, 1, "", ""),
			armHop(10, 1990, 800, "end_turn", text))
		if c := armCrit(t, e, ArmCritHandoffMD); c.Status != ArmStatusFail {
			t.Fatalf("%s: 应 fail, got %q", name, c.Status)
		}
		if e.Verdict != ArmVerdictFailed {
			t.Fatalf("%s: got %q", name, e.Verdict)
		}
	}
}

func TestEvaluateAppendHopErrorInconclusive(t *testing.T) {
	app := ArmHopResult{Sent: true, OK: false, Err: "timeout"}
	e := armEval(t, armHop(10, 1990, 1, "", ""), app)
	// ② 与字节有关,仍可判;①③④ 都悬在追加跳上 → 不可判定。
	if c := armCrit(t, e, ArmCritDiff); c.Status != ArmStatusPass {
		t.Fatalf("②不依赖网络应照常判: %q", c.Status)
	}
	for _, name := range []string{ArmCritRatio, ArmCritNoToolUse, ArmCritHandoffMD} {
		if c := armCrit(t, e, name); c.Status != ArmStatusNA {
			t.Fatalf("%s 追加跳失败应 not_evaluatable, got %q", name, c.Status)
		}
	}
	if e.Verdict != ArmVerdictInconclusive {
		t.Fatalf("got %q", e.Verdict)
	}
}

func TestEvaluateDryRunZeroHops(t *testing.T) {
	// dry-run:两跳零值(Sent=false)。②照判;其余不可判定;总判定 inconclusive。
	out, _, err := beat.AppendReplayBody([]byte(armSrcBody), 4096, armEvalInstr)
	if err != nil {
		t.Fatal(err)
	}
	e := EvaluateArm([]byte(armSrcBody), out, 4096, ArmHopResult{}, ArmHopResult{})
	if c := armCrit(t, e, ArmCritDiff); c.Status != ArmStatusPass {
		t.Fatalf("② dry-run 应照判: %q", c.Status)
	}
	if e.Verdict != ArmVerdictInconclusive {
		t.Fatalf("got %q", e.Verdict)
	}
}

func TestEvaluateDiffViolationFailsEvenWithGoodHops(t *testing.T) {
	// ②独立否决:usage 再漂亮,字节面被污染即未过。
	e := armEval(t,
		armHop(10, 1990, 1, "", ""),
		armHop(10, 1990, 800, "end_turn", armGoodReply))
	bad := bytes.Replace(bytes.Clone([]byte(armSrcBody)), []byte("好的"), []byte("被改"), 1)
	e = EvaluateArm(bad, bad, 4096,
		armHop(10, 1990, 1, "", ""),
		armHop(10, 1990, 800, "end_turn", armGoodReply))
	if c := armCrit(t, e, ArmCritDiff); c.Status != ArmStatusFail {
		t.Fatalf("字节面违规应 fail, got %q", c.Status)
	}
	if e.Verdict != ArmVerdictFailed {
		t.Fatalf("got %q", e.Verdict)
	}
	if !strings.Contains(armCrit(t, e, ArmCritDiff).Detail, "追加") {
		t.Fatalf("违规明细应说明差异位置: %s", armCrit(t, e, ArmCritDiff).Detail)
	}
}
