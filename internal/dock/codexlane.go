// codexlane.go — 票03：responses 翻译车道接线（对照表 §0 总流程 + §3 + §4
// + §5 + 差异点 D1-D10）。
//
// 三分支（spec 决策 1/3）：
//   - 翻译分支（dialect=anthropic）：model 改写（responses 体上、翻译前）
//     → responses→Anthropic 翻译 → [1M] 剥离 → GLM thinking:disabled →
//     cache 断点注入 → 私有字段剥除+键序规范 → 出站；响应向三分支（SSE 转
//     换 / JSON 合成 SSE / 整体 JSON 转换，§0）；
//   - 原生透传分支（dialect=openai_responses）：同车道不译，仅鉴权注入+
//     model 改写，未知字段逐字节达上游（差异点 D2 的"透传不丢弃"完整落地）；
//   - 不支持分支（F11）：codex="unsupported" 到站即刻带原因显式错误，不挂
//     起不静默超时。
//
// 鉴权（§3/T39）：入站占位凭证剥除，出站 Bearer 真钥单形态（D5）；真钥只
// 进出站头，永不入日志/账本/错误文本。记账＝dock 事件+账本四列（agent=
// codex、mode 带 lane 标注，D9）；不接快照/摆渡/漂移（spec 决策 3/D7）。
// 身份模拟默认关（§1.6，无配置面＝关）。
package dock

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"ferryman/internal/accounts"
	"ferryman/internal/config"
)

// codex 车道 mode 列三值（lane 标注，D9）。
const (
	modeCodexTranslate   = "codex_translate"
	modeCodexNative      = "codex_native"
	modeCodexUnsupported = "codex_unsupported"
)

// anthropicVersion 出站必带（客户端没带时补；§3.3）。
const anthropicVersion = "2023-06-01"

// context1MBeta [1M] 模型标记的 beta 旗标（§1.6/§3.3）。
const context1MBeta = "context-1m-2025-08-07"

// codexStripExact / codexStripPrefixes 出站剥除清单（§3.3：追踪/CDN 类 +
// codex/OpenAI 指纹头——后者泄身份且破坏网关指纹校验，永不转发到上游）。
var (
	codexStripExact = map[string]bool{
		// 追踪/CDN
		"forwarded": true, "true-client-ip": true, "x-real-ip": true,
		"x-request-id": true, "x-correlation-id": true, "x-trace-id": true,
		"x-amzn-trace-id": true, "traceparent": true, "tracestate": true,
		"baggage": true, "sentry-trace": true,
		HeaderFerrymanReplay: true,
		// codex/OpenAI 指纹
		"originator": true, "session_id": true, "session-id": true,
		"thread-id": true, "conversation_id": true, "chatgpt-account-id": true,
		"x-openai-subagent": true, "x-client-request-id": true,
		"openai-beta": true, "openai-organization": true, "openai-project": true,
	}
	codexStripPrefixes = []string{
		"x-forwarded-", "cdn-", "cf-", "x-azure-", "akamai-",
		"x-stainless-", "x-codex-", "x-b3-",
	}
)

// canonicalResponsesPath 入口路径归一化（§4.1 变体全表 → 规范端点；全 POST）。
// GET /responses/{id} 不实现（差异点 D1：codex 无状态运行从不回查，未匹配
// 路径走渡口既有通用行为——上游自答，渡口不臆造回查语义）。
func canonicalResponsesPath(path string) (string, bool) {
	switch path {
	case "/responses", "/v1/responses", "/v1/v1/responses", "/codex/v1/responses":
		return "/responses", true
	case "/responses/compact", "/v1/responses/compact", "/v1/v1/responses/compact", "/codex/v1/responses/compact":
		return "/responses/compact", true
	}
	return "", false
}

// codexLaneEvent codex 车道事件（票 7 记账之"事件"半边）：一次一值去重（同
// 值不刷屏——codex 每请求同形态），日志一行 + 最近事件环供测试/诊断读取。
type codexLaneEvent struct {
	mu     sync.Mutex
	seen   map[string]bool
	recent []string
}

func newCodexLaneEvent() *codexLaneEvent {
	return &codexLaneEvent{seen: map[string]bool{}}
}

// fire 记一次事件（key 相同只记一次）；nil 安全。
func (l *codexLaneEvent) fire(key, msg string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.seen[key] {
		l.seen[key] = true
		l.recent = append(l.recent, msg)
		if len(l.recent) > 64 {
			l.recent = l.recent[len(l.recent)-64:]
		}
		logger.Printf("[codex] %s", msg)
	}
}

// snapshot 最近事件拷贝（测试断言面）；nil 安全。
func (l *codexLaneEvent) snapshot() []string {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.recent...)
}

// codexLaneMeta 单请求记账元数据（与 reqMeta 同族但独立——两车道字段语义
// 不同：codex 无首包闸门/排水注册，session 取 codex 的 session_id 头）。
type codexLaneMeta struct {
	start    time.Time
	session  string
	mode     string
	modelIn  string
	modelOut string
	usage    laneUsage
	status   int
}

// laneUsage 账本四列（Anthropic 原生字段口径；native 分支由 responses usage
// 反推，§2.3）。
type laneUsage struct {
	input, cacheRead, cacheCreation, output int
}

// usageFromAnthropicMap 翻译分支：直取 Anthropic 原生字段（§2.3 记账口径：
// 不从合成后的 responses usage 反推）。
func usageFromAnthropicMap(m map[string]any) laneUsage {
	if m == nil {
		return laneUsage{}
	}
	return laneUsage{
		input:         int(uint64From(m["input_tokens"])),
		cacheRead:     int(uint64From(m["cache_read_input_tokens"])),
		cacheCreation: int(uint64From(m["cache_creation_input_tokens"])),
		output:        int(uint64From(m["output_tokens"])),
	}
}

// usageFromResponsesMap 原生分支：responses usage（input_tokens 已含缓存子
// 集）反推四列（§2.3 公式逆向）。
func usageFromResponsesMap(m map[string]any) laneUsage {
	if m == nil {
		return laneUsage{}
	}
	cacheRead := int(uint64AtPath(m, "input_tokens_details", "cached_tokens"))
	cacheWrite := int(uint64AtPath(m, "input_tokens_details", "cache_write_tokens"))
	if cacheWrite == 0 {
		cacheWrite = int(uint64From(m["cache_creation_input_tokens"]))
	}
	total := int(uint64From(m["input_tokens"]))
	input := total - cacheRead - cacheWrite
	if input < 0 {
		input = 0
	}
	return laneUsage{
		input:         input,
		cacheRead:     cacheRead,
		cacheCreation: cacheWrite,
		output:        int(uint64From(m["output_tokens"])),
	}
}

// serveResponsesLane 车道入口（server.go ServeHTTP 按路径分流进来）。
func (s *Server) serveResponsesLane(w http.ResponseWriter, r *http.Request, view *upstreamView, canonicalPath string) {
	if view.up == nil {
		// 纯透传旧形态（票01 形状，无供应商条目）：照旧整体代理，零行为变化。
		s.proxy.ServeHTTP(w, r)
		return
	}
	meta := &codexLaneMeta{
		start:   time.Now(),
		session: firstNonEmpty(r.Header.Get("session_id"), r.Header.Get("session-id")),
	}
	defer s.recordCodexLaneRow(meta)

	body, err := io.ReadAll(r.Body)
	if err != nil {
		// 与既有读体口径同款：按已读部分继续（异常分支不为此加错误路径）。
		logger.Printf("codex 车道读体失败（按已读部分继续）: %v", err)
	}

	switch view.up.CodexAvailability() {
	case config.CodexUnsupported:
		// F11：即刻带原因显式错误——不挂起、不静默超时。
		meta.mode = modeCodexUnsupported
		meta.status = http.StatusServiceUnavailable
		writeResponsesError(w, http.StatusServiceUnavailable, responsesErrorShape(
			fmt.Sprintf("上游供应商 %q 被标记为 codex 不支持（codex=\"unsupported\"）；"+
				"请用 provider switch 切换到支持 codex 的供应商，或以 --cc-only 语义知悉 codex 断供",
				upstreamLabel(view)),
			"proxy_error", "codex_unsupported", nil))
		return
	case config.CodexNative:
		meta.mode = modeCodexNative
		s.serveCodexNative(w, r, view, canonicalPath, body, meta)
		return
	default:
		meta.mode = modeCodexTranslate
		s.serveCodexTranslate(w, r, view, canonicalPath, body, meta)
	}
}

// upstreamLabel 活跃条目标签（resolver 名或旧单值兜底，与 noteUpstream 同词）。
func upstreamLabel(view *upstreamView) string {
	if view.upName != "" {
		return view.upName
	}
	return "旧单值"
}

// serveCodexTranslate 翻译分支主流程（§0 请求向五步）。
func (s *Server) serveCodexTranslate(w http.ResponseWriter, r *http.Request,
	view *upstreamView, canonicalPath string, body []byte, meta *codexLaneMeta) {

	obj := decodeJSONObject(body)
	if obj == nil {
		meta.status = http.StatusBadRequest
		writeResponsesError(w, http.StatusBadRequest, responsesErrorShape(
			"请求体不是 JSON object（responses 方言要求）", "invalid_request_error", nil, nil))
		return
	}

	// 1. model 改写（responses 体上、翻译前；§5.3 目录保真语义）。
	modelIn := mapStr(obj, "model")
	modelOut, known := rewriteCodexModel(modelIn, view.up)
	if modelIn != "" && !known {
		s.codexEvent().fire("unknown-model:"+modelIn, fmt.Sprintf(
			"未知模型名透传: lane=codex provider=%s model=%s（不在已知模型集且未配 codex 主模型键）",
			upstreamLabel(view), modelIn))
	}
	obj["model"] = modelOut
	meta.modelIn, meta.modelOut = modelIn, modelOut

	// 2. 翻译（工具注册表先建——响应向还原同用）。
	tc := buildToolContext(obj)
	anth, dropped, err := responsesToAnthropic(obj, tc)
	if err != nil {
		meta.status = http.StatusBadRequest
		writeResponsesError(w, http.StatusBadRequest, responsesErrorShape(
			err.Error(), "invalid_request_error", nil, nil))
		return
	}
	for _, d := range dropped {
		s.codexEvent().fire("dropped-field:"+d, fmt.Sprintf(
			"responses 未知顶层字段被丢弃: lane=codex provider=%s field=%s（白名单构造，Anthropic 方言无承载位）",
			upstreamLabel(view), d))
	}

	// 3. [1M] 剥离（译文之上；§1.6/§5.5）。出站真值随之定版（D9：账本行记
	// 出站模型名）。
	oneM := false
	if m, ok := asStr(anth["model"]); ok && hasOneMSuffix(m) {
		anth["model"] = stripOneMSuffix(m)
		meta.modelOut = stripOneMSuffix(m)
		oneM = true
	}

	// 4. GLM thinking:disabled（§5.6 落点：翻译产物之上、序列化之前，幂等
	// 无条件覆盖；防御性一并剥 output_config——本车道本不产出）。
	if isGLMAnthropicUpstream(view.up) {
		anth["thinking"] = map[string]any{"type": "disabled"}
		delete(anth, "output_config")
	}

	// 5. cache 断点注入（恒开，§1.6）+ 出站定稿（`_` 前缀剥除+键序规范）。
	injectCacheBreakpoints(anth)
	outBody := finalizeOutboundBody(anth)

	streamRequested := responsesBodyIsStream(obj)

	// 出站请求：端点改写 /responses* → /v1/messages（query 原样保留，§4.3）。
	targetURL := buildCodexUpstreamURL(view.target, "/v1/messages", r.URL.RawQuery)
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, targetURL, bytes.NewReader(outBody))
	if err != nil {
		meta.status = http.StatusBadGateway
		writeResponsesError(w, http.StatusBadGateway, responsesErrorShape(
			"渡口组装出站请求失败: "+err.Error(), "proxy_error", nil, nil))
		return
	}
	req.Header = buildCodexOutboundHeaders(r.Header, view.up.APIKey, oneM)
	resp, err := s.laneClient.Do(req)
	if err != nil {
		meta.status = http.StatusBadGateway
		writeResponsesError(w, http.StatusBadGateway, responsesErrorShape(
			"渡口连不上上游（"+err.Error()+"）；请稍后重试", "proxy_error", nil, nil))
		return
	}
	defer resp.Body.Close()
	s.relayCodexTranslateResponse(w, resp, streamRequested, tc, meta)
}

// serveCodexNative 原生透传分支（不译；仅 model 改写+鉴权注入，未知字段逐
// 字节达上游——model 已知时体逐字节不动）。
func (s *Server) serveCodexNative(w http.ResponseWriter, r *http.Request,
	view *upstreamView, canonicalPath string, body []byte, meta *codexLaneMeta) {

	outBody := body
	obj := decodeJSONObject(body)
	if obj != nil {
		modelIn := mapStr(obj, "model")
		modelOut, known := rewriteCodexModel(modelIn, view.up)
		if modelIn != "" && !known {
			s.codexEvent().fire("unknown-model:"+modelIn, fmt.Sprintf(
				"未知模型名透传: lane=codex-native provider=%s model=%s（不在已知模型集且未配 codex 主模型键）",
				upstreamLabel(view), modelIn))
		}
		meta.modelIn, meta.modelOut = modelIn, modelOut
		if modelOut != modelIn {
			obj["model"] = modelOut
			outBody = encodeCompact(obj)
		}
	}

	targetURL := buildCodexUpstreamURL(view.target, canonicalPath, r.URL.RawQuery)
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, targetURL, bytes.NewReader(outBody))
	if err != nil {
		meta.status = http.StatusBadGateway
		writeResponsesError(w, http.StatusBadGateway, responsesErrorShape(
			"渡口组装出站请求失败: "+err.Error(), "proxy_error", nil, nil))
		return
	}
	req.Header = buildCodexNativeHeaders(r.Header, view.up.APIKey)
	resp, err := s.laneClient.Do(req)
	if err != nil {
		meta.status = http.StatusBadGateway
		writeResponsesError(w, http.StatusBadGateway, responsesErrorShape(
			"渡口连不上上游（"+err.Error()+"）；请稍后重试", "proxy_error", nil, nil))
		return
	}
	defer resp.Body.Close()
	s.relayCodexNativeResponse(w, resp, meta)
}

// relayCodexTranslateResponse 翻译分支响应向三分支（§0 响应向 + §2.5 异形
// 态兜底）。上游非 2xx：原状态码 + 规整错误体（§2.4 表行 1）。
func (s *Server) relayCodexTranslateResponse(w http.ResponseWriter, resp *http.Response,
	streamRequested bool, tc *toolContext, meta *codexLaneMeta) {

	ct := resp.Header.Get("Content-Type")
	isSSE := strings.HasPrefix(ct, "text/event-stream")
	isJSON := strings.Contains(ct, "json")

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		meta.status = resp.StatusCode
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(encodeCompact(normalizeUpstreamErrorBody(errBody)))
		return
	}

	switch {
	case isSSE || (!isJSON && streamRequested):
		// 流式转换（含 JSON-hold 兜底：网关无视 stream:true 回整体 JSON 时由
		// 状态机 finish 路径合成生命周期事件，§2.5 表行 1）。
		s.streamTranslateRelay(w, resp, tc, meta)
	case streamRequested:
		// 显式 JSON Content-Type 的 2xx + 流式请求：读整体 JSON 合成 SSE。
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
		meta.status = resp.StatusCode
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(resp.StatusCode)
		var out []byte
		if obj := decodeJSONObject(body); obj != nil {
			out, meta.usage = synthesizeSSEFromJSONMessage(obj, tc)
		} else {
			// 顶层非对象（数组/标量）→ response.failed(invalid_response)，不 panic。
			st := newStreamTranslator(tc)
			out = st.failedEvent("upstream returned a non-object Anthropic message body",
				"invalid_response")
		}
		if len(out) > 0 {
			_, _ = w.Write(out)
		}
	default:
		// 非流式：整体 JSON 转换（SSE 体无标记形态先聚合，§2.4 表行 6）。
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
		result, usage, status, errBody := convertNonStreamResponse(body, tc)
		meta.status = status
		meta.usage = usage
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if errBody != nil {
			_, _ = w.Write(encodeCompact(errBody))
			return
		}
		_, _ = w.Write(encodeCompact(result))
	}
}

// streamTranslateRelay SSE 中继：边读边译即写即冲（SSE 缓冲＝重试风暴红线）。
func (s *Server) streamTranslateRelay(w http.ResponseWriter, resp *http.Response,
	tc *toolContext, meta *codexLaneMeta) {
	st := newStreamTranslator(tc)
	meta.status = resp.StatusCode
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(resp.StatusCode)
	flusher, _ := w.(http.Flusher)
	flush := func() {
		if flusher != nil {
			flusher.Flush()
		}
	}
	buf := make([]byte, 32*1024)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			out, failed := st.feed(buf[:n])
			if len(out) > 0 {
				_, _ = w.Write(out)
				flush()
			}
			if failed {
				break // 终态已发（response.failed），残余吞掉
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			if ev := st.streamError(err); len(ev) > 0 {
				_, _ = w.Write(ev)
				flush()
			}
			break
		}
	}
	if out := st.finish(); len(out) > 0 {
		_, _ = w.Write(out)
		flush()
	}
	meta.usage = usageFromAnthropicMap(st.usage)
}

// synthesizeSSEFromJSONMessage 整体 JSON → 合成 SSE 生命周期（§2.5 表行 1）。
// 返回事件字节与记账四列（信封内 message.usage 直取）。
func synthesizeSSEFromJSONMessage(obj map[string]any, tc *toolContext) ([]byte, laneUsage) {
	st := newStreamTranslator(tc)
	out, _ := responsesSSEFromAnthropicMessageWithState(obj, tc, st)
	return out, usageFromAnthropicMap(st.usage)
}

// convertNonStreamResponse 非流式响应转换：JSON message → responses JSON；
// 错误信封 → 错误体（422，转换失败形态）；SSE 体（无标记）→ 先聚合再转。
func convertNonStreamResponse(body []byte, tc *toolContext) (
	result map[string]any, usage laneUsage, status int, errBody map[string]any) {

	obj := decodeJSONObject(body)
	if obj == nil {
		// JSON object 解析失败：SSE 形态聚合兜底（§2.4 表行 6）。
		if agg, err := aggregateAnthropicSSE(body); err == nil {
			obj = agg
		} else {
			return nil, laneUsage{}, http.StatusUnprocessableEntity, responsesErrorShape(
				"上游响应体既非 Anthropic message JSON 也非可聚合的 SSE 体: "+err.Error(),
				"proxy_error", nil, nil)
		}
	}
	result, err := anthropicToResponses(obj, tc)
	if err != nil {
		// 2xx 错误信封（§0 响应提交前校验）→ 按失败处理（422 转换失败形态）。
		msg, errType := anthropicErrorEnvelope(obj)
		return nil, laneUsage{}, http.StatusUnprocessableEntity, responsesErrorShape(
			msg, errType, nil, nil)
	}
	usage = usageFromAnthropicMap(nil)
	if u, ok := asMap(obj["usage"]); ok {
		usage = usageFromAnthropicMap(u)
	}
	return result, usage, http.StatusOK, nil
}

// relayCodexNativeResponse 原生分支响应中继：逐字节透传（体未改时），体经
// responses usage 扫描器记账（response.completed 携带的 usage）。
func (s *Server) relayCodexNativeResponse(w http.ResponseWriter, resp *http.Response, meta *codexLaneMeta) {
	meta.status = resp.StatusCode
	// 头中继：Content-Type 等实义头照抄；逐跳/实体头剥除（Go 客户端自管）。
	for _, k := range []string{"Connection", "Transfer-Encoding", "Keep-Alive",
		"Proxy-Authenticate", "Proxy-Authorization", "Te", "Trailer"} {
		resp.Header.Del(k)
	}
	resp.Header.Del("Content-Length")
	for k, vs := range resp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	flusher, _ := w.(http.Flusher)
	acc := newResponsesUsageScanner()
	buf := make([]byte, 32*1024)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			acc.feed(buf[:n])
			if _, werr := w.Write(buf[:n]); werr != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if err != nil {
			break
		}
	}
	meta.usage = acc.usage()
}

// writeResponsesError codex 错误响应（JSON 体 + 状态码；错误体已规整成
// {"error":{message,type,code,param}} 形）。
func writeResponsesError(w http.ResponseWriter, status int, body map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(encodeCompact(body))
}

// rewriteCodexModel model 改写（§5.3，目录保真语义）：
//  1. 剥 [1M] 后 ∈ 条目已知模型集（model_map 全部值域）→ 原样透传（含
//     [1M] 标记——由译文后剥离步收敛并置 beta 旗标；已知真名精确保留）；
//  2. 否则 → codex 主模型键目标名；
//  3. 主模型键未配置 → 透传原文（known=false，调用方记告警事件；不静默替换
//     ——codex 会话状态按模型名组织，换名＝换会话人格，D3）。
func rewriteCodexModel(raw string, up *config.DockUpstream) (string, bool) {
	bare := stripOneMSuffix(raw)
	if bare == "" {
		return raw, true
	}
	for _, v := range up.ModelMap {
		if v == bare {
			return raw, true
		}
	}
	if cm := up.CodexModel(); cm != "" {
		return cm, true
	}
	return raw, false
}

// isGLMAnthropicUpstream 活跃供应商是否智谱 GLM 的 Anthropic 方言端点
// （§5.6 触发条件）。条目无厂商名字段——按双信号判定：base_url 主机属智谱
// 域（bigmodel.cn/zhipuai.ai/z.ai）或条目模型位值域含 glm 前缀真名。原生
// responses 分支（智谱 /api/v1）不触发——非 Anthropic 方言无 thinking 字段。
func isGLMAnthropicUpstream(up *config.DockUpstream) bool {
	if up == nil || up.Dialect == config.DialectOpenAIResponses {
		return false
	}
	if u, err := url.Parse(up.BaseURL); err == nil {
		h := strings.ToLower(u.Hostname())
		for _, dom := range []string{"bigmodel.cn", "zhipuai.ai", "z.ai"} {
			if h == dom || strings.HasSuffix(h, "."+dom) {
				return true
			}
		}
	}
	for _, v := range up.ModelMap {
		if strings.HasPrefix(strings.ToLower(v), "glm") {
			return true
		}
	}
	return false
}

// finalizeOutboundBody 出站体定稿（§1.6 第五步）：`_` 前缀顶层私有字段剥除
// + 键序规范化（Go map 序即排序序）+ 紧凑编码。
func finalizeOutboundBody(body map[string]any) []byte {
	for k := range body {
		if strings.HasPrefix(k, "_") {
			delete(body, k)
		}
	}
	return encodeCompact(body)
}

// responsesBodyIsStream responses 体顶层 stream 布尔（响应向分支判定）。
func responsesBodyIsStream(obj map[string]any) bool {
	b, _ := asBool(obj["stream"])
	return b
}

// buildCodexUpstreamURL 上游 URL 拼装（§4.3）：base 尾 /v1 直拼、纯 origin
// 补 /v1、自定义前缀直拼、/v1/v1 去重；base 已是完整端点（尾缀等值）时防
// 双拼原样用；query 原样保留。
func buildCodexUpstreamURL(target *url.URL, endpointPath, rawQuery string) string {
	u := *target
	base := strings.TrimSuffix(u.Path, "/")
	switch {
	case base != "" && strings.EqualFold(base, strings.TrimSuffix(endpointPath, "/")) ||
		strings.HasSuffix(strings.ToLower(base), strings.ToLower(endpointPath)):
		// base 已是完整端点：原样用（防 …/v1/messages/v1/messages 双拼）。
		u.Path = base
	case base == "" || base == "/":
		u.Path = "/v1" + endpointPath // 纯 origin 补 /v1（/v1/v1 由去重收敛）
	default:
		u.Path = base + endpointPath
	}
	for strings.Contains(u.Path, "/v1/v1") {
		u.Path = strings.ReplaceAll(u.Path, "/v1/v1", "/v1")
	}
	u.RawQuery = rawQuery
	return u.String()
}

// buildCodexOutboundHeaders 翻译分支出站头（§3.3 清单照抄）：占位凭证剥除
// （真钥 Bearer 注入）、追踪/CDN/codex 指纹剥除、accept 强制 application/
// json（流式由 body 驱动）、accept-encoding 强制 identity（防上游压缩体）、
// anthropic-version 必带、anthropic-beta 重建（仅 [1M] 注 context-1m；普通
// 翻译路径不发任何 beta——身份模拟默认关）。
func buildCodexOutboundHeaders(in http.Header, apiKey string, oneM bool) http.Header {
	out := http.Header{}
	for k, vs := range in {
		lk := strings.ToLower(k)
		if codexStripExact[lk] || codexStripPrefixHas(lk) {
			continue
		}
		switch lk {
		case "authorization", "x-api-key", "x-goog-api-key", // 占位凭证剥除（§3.1）
			"content-length", "transfer-encoding", // 译文重算
			"anthropic-beta",            // 重建
			"accept", "accept-encoding": // 强制
			continue
		}
		for _, v := range vs {
			out.Add(k, v)
		}
	}
	out.Set("Accept", "application/json")
	out.Set("Accept-Encoding", "identity")
	if out.Get("Anthropic-Version") == "" {
		out.Set("Anthropic-Version", anthropicVersion)
	}
	if oneM {
		out.Set("Anthropic-Beta", context1MBeta)
	}
	if out.Get("Content-Type") == "" {
		out.Set("Content-Type", "application/json")
	}
	// 真钥只进出站头（T39）。
	out.Set("Authorization", "Bearer "+apiKey)
	return out
}

// buildCodexNativeHeaders 原生分支出站头：占位凭证剥除+真钥注入+追踪/CDN
// 剥除；其余头原样（codex 指纹对 responses 上游是本客户端正常头；accept
// 照抄——上游原生支持 text/event-stream）。
func buildCodexNativeHeaders(in http.Header, apiKey string) http.Header {
	out := http.Header{}
	for k, vs := range in {
		lk := strings.ToLower(k)
		switch lk {
		case "authorization", "x-api-key", "x-goog-api-key",
			"content-length", "transfer-encoding",
			"x-real-ip", "traceparent", "tracestate", "baggage", "sentry-trace",
			"forwarded", HeaderFerrymanReplay:
			continue
		}
		if strings.HasPrefix(lk, "x-forwarded-") || strings.HasPrefix(lk, "cdn-") {
			continue
		}
		for _, v := range vs {
			out.Add(k, v)
		}
	}
	if out.Get("Content-Type") == "" {
		out.Set("Content-Type", "application/json")
	}
	out.Set("Authorization", "Bearer "+apiKey)
	return out
}

func codexStripPrefixHas(lk string) bool {
	for _, p := range codexStripPrefixes {
		if strings.HasPrefix(lk, p) {
			return true
		}
	}
	return false
}

// recordCodexLaneRow codex 车道账本行（agent=codex lane 标注 + 四列；与既有
// /v1/messages 车道同族接入——同 dock 科目、纯元数据、失败只记日志）。
func (s *Server) recordCodexLaneRow(m *codexLaneMeta) {
	if s.acc == nil {
		return
	}
	status := m.status
	if status == 0 {
		status = http.StatusOK
	}
	f := accounts.Fields{
		"agent":                 "codex",
		"session_id":            m.session,
		"mode":                  m.mode,
		"model_in":              m.modelIn,
		"model_out":             m.modelOut,
		"input_tokens":          m.usage.input,
		"cache_read_tokens":     m.usage.cacheRead,
		"cache_creation_tokens": m.usage.cacheCreation,
		"output_tokens":         m.usage.output,
		"latency_s":             time.Since(m.start).Seconds(),
		"status":                status,
	}
	if _, err := s.acc.Record("dock", -1, f); err != nil {
		logger.Printf("codex 车道记账失败（不影响转发）: %v", err)
	}
}

// codexEvent 车道事件日志（懒建；Server 构造时初始化，nil 安全兜底）。
func (s *Server) codexEvent() *codexLaneEvent {
	if s.codexEvents == nil {
		s.codexEvents = newCodexLaneEvent()
	}
	return s.codexEvents
}

// responsesUsageScanner 原生分支 usage 扫描器：SSE response.completed /
// response.failed 的 response.usage，或整体 JSON 的 usage 键（非流式响应）。
// 四列由 §2.3 公式逆向（input_tokens 已含缓存子集）。
type responsesUsageScanner struct {
	lineBuf   []byte
	data      []string
	lastUsage map[string]any
}

func newResponsesUsageScanner() *responsesUsageScanner {
	return &responsesUsageScanner{}
}

func (a *responsesUsageScanner) feed(p []byte) {
	for len(p) > 0 {
		if i := bytes.IndexByte(p, '\n'); i >= 0 {
			a.lineBuf = append(a.lineBuf, p[:i]...)
			a.handleLine()
			p = p[i+1:]
		} else {
			a.lineBuf = append(a.lineBuf, p...)
			p = nil
		}
	}
}

func (a *responsesUsageScanner) handleLine() {
	line := strings.TrimSuffix(string(a.lineBuf), "\r")
	a.lineBuf = a.lineBuf[:0]
	switch {
	case line == "":
		a.flush()
	case strings.HasPrefix(line, "data:"):
		v := strings.TrimPrefix(line, "data:")
		v = strings.TrimPrefix(v, " ")
		a.data = append(a.data, v)
	}
}

func (a *responsesUsageScanner) flush() {
	if len(a.data) == 0 {
		return
	}
	payload := strings.Join(a.data, "\n")
	a.data = a.data[:0]
	obj := decodeJSONObject([]byte(payload))
	if obj == nil {
		return
	}
	t := mapStr(obj, "type")
	if t != "response.completed" && t != "response.failed" {
		return
	}
	if resp, ok := asMap(obj["response"]); ok {
		if u, ok := asMap(resp["usage"]); ok {
			a.lastUsage = u
		}
	}
}

// usage 定版（流尾残段兜底；无 usage＝零值）。
func (a *responsesUsageScanner) usage() laneUsage {
	if len(a.lineBuf) > 0 {
		a.handleLine()
	}
	a.flush()
	return usageFromResponsesMap(a.lastUsage)
}
