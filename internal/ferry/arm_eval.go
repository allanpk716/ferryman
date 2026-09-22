// arm_eval.go — 票04:追加重放实跳臂的评估逻辑(ADR-0015 决定一【启用硬门槛】)。
//
// 四条成功标准的判定与证据形状,纯逻辑无 I/O(真实发送在
// experiments/append-replay-arm/ 工具里,经 internal/beat 发送器):
//
//	① 追加后仍命中:该跳缓存读占比 ≥ 同前缀心跳基线。占比口径与
//	  beat.Classify 单源(cache_read/(cache_read+input)),不另立公式;
//	② 字节差异仅末尾追加段+max_tokens 数值子区间:独立 span 校验器,
//	  不信任构造方自证(评审口径:快照 32000→封顶 4096 属允许的数值子区间,
//	  非缓存键、无碍命中);
//	③ stop_reason 非 tool_use(决定三:防工具调用循环烧输出价);
//	④ 输出可解析为交接 MD:直接复用票03 ParseSameModelOutput,不要第二份解析。
//
// 判定三态:pass / fail / not_evaluatable。任一 fail → 未过(failed,回写状态);
// 无 fail 但有 not_evaluatable(如发送失败/缺 stop_reason)→ 不可判定
// (inconclusive,不回写,状态保持待实跳);全 pass → 通过(passed,回写启用)。
package ferry

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
)

// 四条标准的稳定名(评估结果与状态文件里 criteria 的键)。
const (
	ArmCritRatio     = "c1_cache_ratio"      // ① 追加后缓存读占比 ≥ 心跳基线
	ArmCritDiff      = "c2_append_only_diff" // ② 字节差异仅末尾追加段
	ArmCritNoToolUse = "c3_no_tool_use"      // ③ stop_reason 非 tool_use
	ArmCritHandoffMD = "c4_handoff_md"       // ④ 输出可解析为交接 MD
)

// 单条标准判定三态。
const (
	ArmStatusPass = "pass"
	ArmStatusFail = "fail"
	ArmStatusNA   = "not_evaluatable"
)

// 实跳臂总判定三态。inconclusive 不回写状态文件(无结论不落账)。
const (
	ArmVerdictPassed       = "passed"       // 四条全过 → 启用
	ArmVerdictFailed       = "failed"       // 任一条判否 → 保持未启用
	ArmVerdictInconclusive = "inconclusive" // 有标准悬而未决 → 不落状态
)

// ArmHopResult 单跳(基线心跳重放 / 追加重放)的事实记录:发送方只报事实,
// 判定归 EvaluateArm(与 beat 发送器同纪律)。CacheWrite 为发送器未暴露列
// (beat 的 usage 解析不含 cache_creation_input_tokens),留档为 null 如实
// 标注——占比口径三列自足,不受影响。
type ArmHopResult struct {
	Hop        string `json:"hop"`                   // "baseline" | "append"
	Sent       bool   `json:"sent"`                  // false = 未真发(dry-run)
	OK         bool   `json:"ok"`                    // 2xx 且 SSE 完整读到 message_delta
	StopReason string `json:"stop_reason,omitempty"` // 仅追加跳有意义
	Text       string `json:"text,omitempty"`        // 仅追加跳:输出全文(证据留档)
	Err        string `json:"err,omitempty"`         // 错误类别(不含消息内容)
	Input      int    `json:"input_tokens"`
	CacheRead  int    `json:"cache_read_tokens"`
	Output     int    `json:"output_tokens"`
	CacheWrite *int   `json:"cache_write_tokens"` // 发送器未暴露 → null(如实)
	Model      string `json:"model,omitempty"`
}

// ArmCriterion 单条标准的判定行。
type ArmCriterion struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail"`
}

// ArmEvaluation 四条标准 + 总判定。占比双值随评估留档(证据链)。
type ArmEvaluation struct {
	Criteria      []ArmCriterion `json:"criteria"`
	Verdict       string         `json:"verdict"`
	BaselineRatio float64        `json:"baseline_ratio,omitempty"`
	AppendRatio   float64        `json:"append_ratio,omitempty"`
}

// CacheReadRatio 缓存读占比(beat.Classify 同口径):cache_read/(cache_read+input)。
// 总数为零 = 无信号,第二返回值 false(调用方按不可判定处理,绝不伪造 0 或 1)。
func CacheReadRatio(input, cacheRead int) (float64, bool) {
	total := input + cacheRead
	if total <= 0 {
		return 0, false
	}
	return float64(cacheRead) / float64(total), true
}

// EvaluateArm 四条标准评估(纯函数)。snapBody=捕获快照体原字节;
// appendBody=实际发送的追加重放体原字节;wantMaxTokens=构造时的封顶值。
// 基线跳=同前缀心跳式原样重放(max_tokens=1),只参与标准①。
func EvaluateArm(snapBody, appendBody []byte, wantMaxTokens int, baseline, appendHop ArmHopResult) ArmEvaluation {
	var crits []ArmCriterion

	// ② 字节差异仅末尾追加段(与网络无关,dry-run 也照判)。
	if err := VerifyAppendOnlyDiff(snapBody, appendBody, wantMaxTokens); err != nil {
		crits = append(crits, ArmCriterion{ArmCritDiff, ArmStatusFail,
			fmt.Sprintf("与捕获快照的字节差异超出允许范围(仅允许末尾追加段+max_tokens 数值子区间): %v", err)})
	} else {
		crits = append(crits, ArmCriterion{ArmCritDiff, ArmStatusPass,
			"与捕获快照字节一致,差异仅末尾追加段+max_tokens 数值子区间"})
	}

	// ① 追加后缓存读占比 ≥ 同前缀心跳基线(两跳都要真发且成功才有信号)。
	baseRatio, baseSig := CacheReadRatio(baseline.Input, baseline.CacheRead)
	appRatio, appSig := CacheReadRatio(appendHop.Input, appendHop.CacheRead)
	switch {
	case !baseline.Sent || !baseline.OK:
		crits = append(crits, ArmCriterion{ArmCritRatio, ArmStatusNA,
			hopNAReason("基线跳", &baseline)})
	case !appendHop.Sent || !appendHop.OK:
		crits = append(crits, ArmCriterion{ArmCritRatio, ArmStatusNA,
			hopNAReason("追加跳", &appendHop)})
	case !baseSig || !appSig:
		crits = append(crits, ArmCriterion{ArmCritRatio, ArmStatusNA,
			"usage 三列全零,无缓存信号可算占比"})
	case appRatio >= baseRatio:
		crits = append(crits, ArmCriterion{ArmCritRatio, ArmStatusPass,
			fmt.Sprintf("追加跳缓存读占比 %.4f ≥ 心跳基线 %.4f", appRatio, baseRatio)})
	default:
		crits = append(crits, ArmCriterion{ArmCritRatio, ArmStatusFail,
			fmt.Sprintf("追加跳缓存读占比 %.4f < 心跳基线 %.4f——追加后前缀命中退化", appRatio, baseRatio)})
	}

	// ③ stop_reason 非 tool_use(决定三)。
	switch {
	case !appendHop.Sent || !appendHop.OK:
		crits = append(crits, ArmCriterion{ArmCritNoToolUse, ArmStatusNA,
			hopNAReason("追加跳", &appendHop)})
	case appendHop.StopReason == "":
		crits = append(crits, ArmCriterion{ArmCritNoToolUse, ArmStatusNA,
			"响应缺 stop_reason,无法判定"})
	case appendHop.StopReason == "tool_use":
		crits = append(crits, ArmCriterion{ArmCritNoToolUse, ArmStatusFail,
			"stop_reason=tool_use——同模型档失败(防工具调用循环,决定三)"})
	default:
		crits = append(crits, ArmCriterion{ArmCritNoToolUse, ArmStatusPass,
			"stop_reason=" + appendHop.StopReason})
	}

	// ④ 输出可解析为交接 MD(票03 严格解析单源)。
	switch {
	case !appendHop.Sent || !appendHop.OK:
		crits = append(crits, ArmCriterion{ArmCritHandoffMD, ArmStatusNA,
			hopNAReason("追加跳", &appendHop)})
	default:
		if _, _, err := ParseSameModelOutput(appendHop.Text); err != nil {
			crits = append(crits, ArmCriterion{ArmCritHandoffMD, ArmStatusFail,
				fmt.Sprintf("输出不合交接 MD 结构: %v", err)})
		} else {
			crits = append(crits, ArmCriterion{ArmCritHandoffMD, ArmStatusPass,
				"输出可解析为交接 MD 结构(注入层+全文两层齐)"})
		}
	}

	e := ArmEvaluation{Criteria: crits, Verdict: ArmVerdictPassed}
	if baseSig && baseRatio > 0 || !baseSig {
		e.BaselineRatio = baseRatio
	}
	if appSig && appRatio > 0 || !appSig {
		e.AppendRatio = appRatio
	}
	for _, c := range crits {
		switch c.Status {
		case ArmStatusFail:
			e.Verdict = ArmVerdictFailed
		case ArmStatusNA:
			if e.Verdict != ArmVerdictFailed {
				e.Verdict = ArmVerdictInconclusive
			}
		}
	}
	return e
}

// hopNAReason 不可判定的原因一句(指名哪跳、为何)。
func hopNAReason(name string, h *ArmHopResult) string {
	switch {
	case !h.Sent:
		return name + "未真发(dry-run)——无 usage 可判"
	case h.Err != "":
		return name + "发送失败(" + h.Err + ")——无 usage 可判"
	default:
		return name + "未成功——无 usage 可判"
	}
}

// ---- 标准②:独立 span 校验器 ----

// keySpan 顶层键的值字节区间([valStart,valEnd) 于 body 内)。
type keySpan struct {
	key              string
	valStart, valEnd int
}

// scanTopLevelObject 扫顶层对象的键序与值字节区间(定点定位,绝不反序列化
// 重组——校验器必须看见原始字节,序列化怪癖原样参与比对)。
func scanTopLevelObject(src []byte) ([]keySpan, error) {
	dec := json.NewDecoder(bytes.NewReader(src))
	dec.UseNumber()
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, errors.New("顶层非 JSON 对象")
	}
	var spans []keySpan
	for dec.More() {
		ktok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, ok := ktok.(string)
		if !ok {
			return nil, errors.New("对象键非字符串")
		}
		before := dec.InputOffset()
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, err
		}
		after := dec.InputOffset()
		rel := bytes.Index(src[before:after], raw)
		if rel < 0 {
			return nil, errors.New("值区间定位失败")
		}
		spans = append(spans, keySpan{key, int(before) + rel, int(before) + rel + len(raw)})
	}
	if _, err := dec.Token(); err != nil { // 收 '}'
		return nil, err
	}
	return spans, nil
}

const armMaxTokensKey = "max_tokens"
const armMessagesKey = "messages"

// VerifyAppendOnlyDiff 标准②的独立校验:out(追加重放体)相对 src(捕获快照
// 体)的字节差异必须仅为——
//   - messages 数组末尾追加段(前缀字节一个不动,追加段为一条 user 消息);
//   - max_tokens 数值子区间(恰等于封顶时零编辑);
//   - 快照缺 max_tokens 时,对象末尾补键。
//
// 其余任何字节(中段正文、其他键、键序、空白结构)不一致即报错。本函数是
// 校验器而非构造的复述——不信任构造方自证,构造回归在此现形。
func VerifyAppendOnlyDiff(src, out []byte, wantMaxTokens int) error {
	ss, err := scanTopLevelObject(src)
	if err != nil {
		return fmt.Errorf("快照体不可扫: %w", err)
	}
	os_, err := scanTopLevelObject(out)
	if err != nil {
		return fmt.Errorf("追加体不可扫: %w", err)
	}
	find := func(spans []keySpan, key string) (keySpan, bool) {
		for _, s := range spans {
			if s.key == key {
				return s, true
			}
		}
		return keySpan{}, false
	}
	if _, ok := find(os_, armMessagesKey); !ok {
		return errors.New("追加体缺 messages")
	}
	if _, ok := find(os_, armMaxTokensKey); !ok {
		return errors.New("追加体缺 max_tokens(构造应放开至封顶值)")
	}
	srcHasMT := false
	for _, s := range ss {
		if s.key == armMaxTokensKey {
			srcHasMT = true
			break
		}
	}

	// 键序列对齐:同序逐段比;快照缺 max_tokens 时,追加体恰多出末尾一个
	// max_tokens 键(补键形态),其余必须全同序。
	var aligned []armPair
	if srcHasMT {
		if len(ss) != len(os_) {
			return fmt.Errorf("顶层键数不同: 快照 %d vs 追加体 %d(键集必须一致)", len(ss), len(os_))
		}
		for i := range ss {
			if ss[i].key != os_[i].key {
				return fmt.Errorf("顶层键序不一致: 第 %d 个键 %q vs %q", i+1, ss[i].key, os_[i].key)
			}
			aligned = append(aligned, armPair{ss[i], os_[i]})
		}
	} else {
		if len(os_) != len(ss)+1 || os_[len(os_)-1].key != armMaxTokensKey {
			return fmt.Errorf("快照缺 max_tokens 时,追加体应恰在末尾补该键(快照 %d 键,追加体 %d 键)", len(ss), len(os_))
		}
		for i := range ss {
			if ss[i].key != os_[i].key {
				return fmt.Errorf("顶层键序不一致: 第 %d 个键 %q vs %q", i+1, ss[i].key, os_[i].key)
			}
			aligned = append(aligned, armPair{ss[i], os_[i]})
		}
	}

	// 补键形态:追加体尾段必须是 `,\"max_tokens\":<封顶值>` 插在快照尾段之前。
	if !srcHasMT {
		last := aligned[len(aligned)-1]
		srcTail := src[last.s.valEnd:]
		outTail := out[last.o.valEnd:]
		ins := []byte(`,"` + armMaxTokensKey + `":` + strconv.Itoa(wantMaxTokens))
		if len(outTail) < len(ins)+len(srcTail) ||
			!bytes.Equal(outTail[:len(ins)], ins) ||
			!bytes.Equal(outTail[len(ins):], srcTail) {
			return errors.New("补键形态不符: 追加体尾段应为快照尾段前插 ,\"max_tokens\":" + strconv.Itoa(wantMaxTokens))
		}
		// 补键形态下,公共键段已比完,收口。
		return verifyAligned(src, out, aligned[:len(aligned)-1], wantMaxTokens)
	}

	// 同序形态:尾段(最后值之后到 EOF)必须逐字节一致。
	last := aligned[len(aligned)-1]
	if !bytes.Equal(src[last.s.valEnd:], out[last.o.valEnd:]) {
		return errors.New("尾段字节不一致(最后值之后的内容被改动)")
	}
	return verifyAligned(src, out, aligned, wantMaxTokens)
}

// armPair 快照键与追加体键的对齐对(同序形态下同下标)。
type armPair struct{ s, o keySpan }

// verifyAligned 对齐键段的值级校验:非 messages/max_tokens 键值逐字节一致;
// messages 走「前缀不动+末尾追加」性质检查;max_tokens 数值须等于封顶值。
// 各键值之前的段(逗号+键+冒号+空白)逐字节一致——尾段一致性由调用方负责。
func verifyAligned(src, out []byte, aligned []armPair, wantMaxTokens int) error {
	prevS, prevO := 0, 0
	for _, p := range aligned {
		// 值之前的段(逗号+键+冒号+空白)必须逐字节一致。
		if !bytes.Equal(src[prevS:p.s.valStart], out[prevO:p.o.valStart]) {
			return fmt.Errorf("键 %q 之前的字节不一致(键序/空白/键名被改动)", p.s.key)
		}
		switch p.s.key {
		case armMessagesKey:
			if err := verifyAppendOnlyMessages(
				src[p.s.valStart:p.s.valEnd], out[p.o.valStart:p.o.valEnd]); err != nil {
				return err
			}
		case armMaxTokensKey:
			var n json.Number
			if err := json.Unmarshal(bytes.TrimSpace(out[p.o.valStart:p.o.valEnd]), &n); err != nil {
				return fmt.Errorf("追加体 max_tokens 非数值: %w", err)
			}
			got, err := n.Int64()
			if err != nil || int(got) != wantMaxTokens {
				return fmt.Errorf("追加体 max_tokens=%s, 应为封顶值 %d", string(bytes.TrimSpace(out[p.o.valStart:p.o.valEnd])), wantMaxTokens)
			}
		default:
			if !bytes.Equal(src[p.s.valStart:p.s.valEnd], out[p.o.valStart:p.o.valEnd]) {
				return fmt.Errorf("键 %q 的值字节被改动(差异只允许在 messages 末尾与 max_tokens)", p.s.key)
			}
		}
		prevS, prevO = p.s.valEnd, p.o.valEnd
	}
	return nil
}

// verifyAppendOnlyMessages messages 值的性质检查:trim 尾空白后,追加体的
// messages 必须=快照 messages 的全部字节 + 末尾插入一段(且插入段为一条
// user 消息)。前缀一个字节不动是「非缓存键、无碍命中」的根基。
func verifyAppendOnlyMessages(srcVal, outVal []byte) error {
	sm := bytes.TrimRight(srcVal, " \t\r\n")
	om := bytes.TrimRight(outVal, " \t\r\n")
	if len(sm) < 2 || sm[0] != '[' || sm[len(sm)-1] != ']' {
		return errors.New("快照 messages 非 JSON 数组")
	}
	if len(om) < 2 || om[0] != '[' || om[len(om)-1] != ']' {
		return errors.New("追加体 messages 非 JSON 数组")
	}
	coreS := sm[:len(sm)-1] // 去 ']'
	coreO := om[:len(om)-1]
	if !bytes.HasPrefix(coreO, coreS) {
		return errors.New("messages 存在末尾追加段之外的字节差异(前缀被改动)")
	}
	if len(coreO) == len(coreS) {
		return errors.New("未检出末尾追加段(追加体与快照全等)")
	}
	inserted := coreO[len(coreS):]
	// 插入段必须是数组收尾处的一条 user 消息:空数组形态为首元素,否则以
	// 逗号起。包一层方括号还原成单元素数组解析验证。
	var payload []byte
	trimmedS := bytes.TrimRight(coreS, " \t\r\n")
	if len(trimmedS) > 0 && trimmedS[len(trimmedS)-1] == '[' {
		payload = append(append([]byte{'['}, inserted...), ']')
	} else {
		if len(inserted) == 0 || inserted[0] != ',' {
			return errors.New("末尾追加段应以逗号起(非末尾插入形态)")
		}
		payload = append(append([]byte{'['}, inserted[1:]...), ']')
	}
	var probe []map[string]json.RawMessage
	if err := json.Unmarshal(payload, &probe); err != nil || len(probe) != 1 {
		return fmt.Errorf("末尾追加段不是单条消息: %v", err)
	}
	var role string
	if err := json.Unmarshal(probe[0]["role"], &role); err != nil || role != "user" {
		return errors.New("末尾追加段不是 user 消息(摆渡指令须为 user 角色)")
	}
	return nil
}
