// server.go — 票01：渡口透传 handler（experiments/capture/forwarder.go 的
// 产品化：去捕获落盘，换内存快照）；票06：改写接线＋出站头卫生＋dock 科目
// ＋形态漂移观察。
//
// 保真口径（F7 实验已证）：ReverseProxy＋Rewrite API（SetURL＋Out.Host=
// target.Host＋FlushInterval:-1）不加 XFF、Host 可控、体逐字节同、逐跳头剥。
// 入站不鉴权（绑 127.0.0.1 已足；auth 类头照抄不校验——占位令牌也是照抄）。
//
// 票06 双模式语义（票01 起开关退役，D13）：Options.Upstream 注入即隐含请求
// 改写——纯透传（New 全零 Options / 守卫拒绝）时票01 路径逐字保留（头零处
// 理、体零触碰、不记账不观察）；改写开＝/v1/messages POST 过改写器（改写发生
// 在进代理前：读体→改写→回填→代理），count_tokens 仅同映射 model（其余键
// 不动）。守卫拒绝（上游指本地中转端口 / 缺 default 键）→ 退回纯透传＋日志，
// 绝不半改写。快照存改写前 CC 原始体——beat 重放原始请求经渡口再走同一改写
// （"beat 与真流量同路径"不变式）。
package dock

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"ferryman/internal/accounts"
	"ferryman/internal/config"
)

// TransportTimeoutS 透传侧超时对齐 CC 的 API_TIMEOUT_MS=50min：只约束"上游
// 首字节（响应头）"的等待，不限制流式体的总时长——不设整体 Timeout 是有意
// 的（会砍 SSE 长连接）。SSE 立即冲刷由 FlushInterval:-1 保证（红线）。
const TransportTimeoutS = 50 * time.Minute

// logger 渡口日志（stderr，独立前缀）。
var logger = log.New(log.Writer(), "[dock] ", log.LstdFlags|log.Lmsgprefix)

// dock 科目 mode 列的两个取值（透传/改写）。
const (
	modePassthrough = "passthrough"
	modeRewrite     = "rewrite"
)

// Options NewWithOptions 的注入面：daemon 接线把渡口上游条目（票01：active
// 条目，改写值与出站真钥的唯一来源）、账本句柄与漂移推送闭包交给渡口。
// New(listen, upstream) 等价于全零 Options＝纯透传、不记账、不观察（票01
// 形状与行为不变）。
type Options struct {
	// Upstream active 渡口上游条目（config.ActiveUpstream 的产物）。改写隐含
	// 开启（D13/D15：无开关）——非本地上游即改写＋真钥替换；上游为本地中转
	// 地址由守卫强制退透传（防双重改写）。nil＝纯透传不观察（票01 旧形状）。
	Upstream *config.DockUpstream
	// Accounts dock 科目账本；nil＝不记账（旧测试零改动）。
	Accounts *accounts.Accounts
	// Alert 形态漂移推送函数（daemon 侧给 AlertViaNotify(cfg) 的闭包）；
	// nil＝只记日志不推送。
	Alert func(title, message string)
}

// reqMeta 单请求记账元数据。指针经 context 从 handler 带到出站 transport
// （状态码/usage 在响应链上回填）；usage 喂养与读尾都在 handler goroutine
// 的同步链上（proxy.ServeHTTP 内拷贝循环读毕后才 recordRow），无并发访问。
type reqMeta struct {
	start    time.Time
	session  string
	mode     string
	modelIn  string
	modelOut string
	usage    sseUsageAcc // 上游响应 SSE usage 累计（响应体读穿透时喂养）
	status   int         // 上游状态码（transport 层捕获）
}

// ctxKeyMeta context 私有键。
type ctxKeyMeta struct{}

// Server 渡口服务：本机透传/改写中转（CC → 渡口 → 上游）＋内存快照捕获。
// Handler 形（ServeHTTP）可独立挂 httptest；Start/Close 是真实监听生命周期。
type Server struct {
	listen string
	target *url.URL
	store  *SnapshotStore
	proxy  *httputil.ReverseProxy

	// 票06 改写模式状态：守卫通过才置位；纯透传（New 或守卫拒绝）恒 false。
	rewriteOn bool
	rwCfg     RewriteConfig
	apiKey    string // 真钥：只进出站 Authorization 头，绝不入日志/账本/错误（T39）
	drift     *DriftTracker
	acc       *accounts.Accounts // nil＝不记账

	srv *http.Server
}

// New 构造纯透传渡口（票01 形状：签名与行为保持不变，daemon 既有调用点
// 零改动）。改写/记账/观察按 NewWithOptions 接线。
func New(listen, upstreamBaseURL string) (*Server, error) {
	return NewWithOptions(listen, upstreamBaseURL, Options{})
}

// NewWithOptions 构造渡口（票06 接线入口，票01 起上游条目化）：改写模式、
// dock 科目、形态漂移观察按 Options 装配；守卫拒绝＝退回纯透传＋日志（拒绝
// 是构造期一次判死——无热加载，见 guard.go 注释）。
func NewWithOptions(listen, upstreamBaseURL string, o Options) (*Server, error) {
	if listen == "" {
		return nil, fmt.Errorf("dock: listen 为空")
	}
	target, err := url.Parse(upstreamBaseURL)
	if err != nil || target.Scheme == "" || target.Host == "" {
		return nil, fmt.Errorf("dock: 上游地址无效: %q", upstreamBaseURL)
	}
	s := &Server{
		listen: listen,
		target: target,
		store:  NewSnapshotStore(),
	}
	if o.Upstream != nil {
		s.drift = NewDriftTracker(o.Alert)
		// 票01（D13/D15）：改写隐含开启——无开关，守卫单源裁决（本地中转
		// 地址退透传；非本地缺 default 退透传——配置层已拒，构造期兜底）。
		rw, ok, reason := resolveRewrite(true, upstreamBaseURL, o.Upstream.ModelMap, o.Upstream.TextOnly)
		if ok {
			s.rewriteOn, s.rwCfg, s.apiKey = true, rw, o.Upstream.APIKey
		} else {
			logger.Printf("[dock] %s", reason)
		}
	}
	if o.Accounts != nil {
		s.acc = o.Accounts
	}
	s.proxy = &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			// SetURL 保留入站路径与 query（join 语义）；Host 显式指上游——
			// 客户端 Host 不外泄、后端看到的就是它自己（F7 断言同款）
			pr.SetURL(target)
			pr.Out.Host = target.Host
			if s.rewriteOn {
				// 改写模式出站头卫生（透传模式零处理——票01 保真语义不动）
				sanitizeOutboundHeaders(pr.Out.Header, s.apiKey)
				// 删 Accept-Encoding 让 Transport 自加 gzip 并透明解压：
				// 客户端原值照抄会把 gzip 字节喂进 usage 扫描器；删掉后
				// Transport 自加并自行解压，扫描只见明文（透传模式不删——
				// 保真优先）
				pr.Out.Header.Del("Accept-Encoding")
			}
		},
		// SSE 必须立即冲刷：不设会缓冲流式响应，下游看成断流/超时 →
		// 客户端重试风暴（forwarder.go 实测教训，红线条款）
		FlushInterval: -1,
		// metaTransport 读穿透包装（meta 为 nil 时零行为）——纯 New 的
		// 票01 路径字节行为不变
		Transport: metaTransport{base: &http.Transport{
			Proxy:                 nil, // 显式不走环境代理：上游是直连目标
			DialContext:           (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
			MaxIdleConns:          100,
			MaxIdleConnsPerHost:   16, // 默认 2 太小：CC 并发请求会频繁重建连接
			IdleConnTimeout:       90 * time.Second,
			ResponseHeaderTimeout: TransportTimeoutS,
			// 不设整体 Timeout/正文超时：长 SSE 流按需无限流（见常量注释）
		}},
	}
	return s, nil
}

// Snapshots 快照库句柄（daemon 侧经 Daemon.DockSnapshot() 转交给票03 心跳）。
func (s *Server) Snapshots() *SnapshotStore { return s.store }

// isCountTokens count_tokens 路径（POST）：改写模式仅同映射 model，其余键
// 不动；dock 科目同样记一行。
func isCountTokens(method, path string) bool {
	return method == http.MethodPost && strings.HasSuffix(path, "/v1/messages/count_tokens")
}

// ServeHTTP 渡口入口（票06 双模式）：
//   - 纯透传（rewriteOn=false 且不记账）：与票01 逐字同路径——仅 /v1/messages
//     POST 读体捕获快照，其余流式直通；
//   - 记账开（acc 非 nil）且命中 messages/count_tokens：包 statusWriter 捕
//     状态码，代理返回后落一行 dock 科目（任何记账失败不影响转发）；
//   - 改写模式：体改写发生在进代理前；快照存改写前原始体。
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// 票03：自产重放（追加重放带 x-ferryman-replay 标记头）不入快照、不喂
	// 漂移——追加体会顶替主快照（见 replayguard.go）；dock 科目照记（record
	// 以 messagesPost 计，重放也有传输流水）。
	messagesPost := ShouldCapture(r.Method, r.URL.Path)
	replay := isReplayRequest(r.Header)
	countTok := isCountTokens(r.Method, r.URL.Path)
	capture := messagesPost && !replay
	record := s.acc != nil && (messagesPost || countTok)

	needBody := capture || record || (s.rewriteOn && countTok)
	var body []byte
	if needBody {
		b, err := io.ReadAll(r.Body)
		if err != nil {
			// 与 forwarder.go 同款：按已读部分继续（该分支只在上游断连等
			// 异常时触达，不为此给正常路径加错误分支）
			logger.Printf("读体失败（按已读部分继续转发）: %v", err)
		}
		body = b
		r.Body = io.NopCloser(bytes.NewReader(body))
	}

	if capture {
		// 快照＝CC 原始请求（改写前）：beat 重放原始请求经渡口再走同一改写。
		// 空 session_id（缺失/非 JSON）＝Capture 内部跳过计数，不入库。
		s.store.Capture(ExtractSessionID(body), body, r.Header)
		// 形态漂移观察同点喂原始头体（nil 安全：纯透传不观察）
		s.drift.Observe(r.Header.Get("Anthropic-Beta"), body)
	}

	// 改写独立于记账：不接账本时改写照常生效（rewriteOn 才有此分支）。
	// count_tokens 仅同映射 model（不做图片降级，其余键不动）。
	// rewritten/rewriteDone 为请求局部值（并发请求不共享任何状态）。
	var rewritten Rewritten
	rewriteDone := false
	if s.rewriteOn && (capture || countTok) {
		if capture {
			rw, err := Rewrite(body, s.rwCfg)
			if err != nil {
				// 非法体：原体透传交上游校验应答（不替上游造 400）
				logger.Printf("改写失败（原体透传）: %v", err)
			} else {
				applyRewritten(r, body, rw.Body)
				rewritten, rewriteDone = rw, true
			}
		} else if nb, mi, mo, ok := mapCountTokensModel(body, s.rwCfg); ok {
			applyRewritten(r, body, nb)
			rewritten, rewriteDone = Rewritten{Body: nb, ModelIn: mi, ModelOut: mo}, true
		}
	}

	var meta *reqMeta
	if record {
		meta = &reqMeta{start: time.Now()}
		meta.session = ExtractSessionID(body)
		if s.rewriteOn {
			meta.mode = modeRewrite
			if rewriteDone {
				meta.modelIn, meta.modelOut = rewritten.ModelIn, rewritten.ModelOut
			} else {
				// 改写失败（非法体）：模型名尽力提取原值
				meta.modelIn = extractModelName(body)
				meta.modelOut = meta.modelIn
			}
		} else {
			meta.mode = modePassthrough
			meta.modelIn = extractModelName(body)
			meta.modelOut = meta.modelIn // 透传：前后同值
		}
		r = r.WithContext(context.WithValue(r.Context(), ctxKeyMeta{}, meta))
		sw := &statusWriter{ResponseWriter: w}
		s.proxy.ServeHTTP(sw, r)
		s.recordRow(meta, sw)
		return
	}
	s.proxy.ServeHTTP(w, r)
}

// applyRewritten 改写体回填：Content-Length 须随体长重写，否则传输层按旧
// 长度写线，上游会截断/报协议错。体未变（如 count_tokens 无 model）不回填。
func applyRewritten(r *http.Request, oldBody, newBody []byte) {
	if bytes.Equal(oldBody, newBody) {
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(newBody))
	r.ContentLength = int64(len(newBody))
	r.Header.Del("Content-Length") // 旧长度不入出站克隆（传输层按 ContentLength 字段写线）
}

// extractModelName 顶层 model 字段尽力提取（改写失败/透传记账用）；非 JSON
// 或缺失返回 ""。
func extractModelName(body []byte) string {
	var probe struct {
		Model string `json:"model"`
	}
	if json.Unmarshal(body, &probe) != nil {
		return ""
	}
	return probe.Model
}

// mapCountTokensModel count_tokens 体仅同映射 model：解析后替换 model 键再
// 重编码（UseNumber＋SetEscapeHTML(false)，与 Rewrite 同保真口径）。body 非
// JSON／非 object／缺 model ＝ 原样透传（ok=false）。
func mapCountTokensModel(body []byte, cfg RewriteConfig) (out []byte, modelIn, modelOut string, ok bool) {
	var root any
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber() // 数字保真：按字面量保留
	if err := dec.Decode(&root); err != nil {
		return body, "", "", false
	}
	obj, isObj := root.(map[string]any)
	if !isObj {
		return body, "", "", false
	}
	raw, isStr := obj["model"].(string)
	if !isStr {
		return body, "", "", false
	}
	mo := mapModel(raw, cfg)
	obj["model"] = mo
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(obj); err != nil {
		return body, raw, raw, false
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), raw, mo, true
}

// recordRow dock 科目落一行（纯元数据，F13）。任何失败只记日志，绝不影响
// 转发（已在转发完成之后）。
func (s *Server) recordRow(m *reqMeta, sw *statusWriter) {
	m.usage.flushTail() // 流尾残段兜底（EOF 无换行的末事件）
	status := m.status
	if status == 0 && sw.code != 0 {
		status = sw.code // 代理自答（如上游拨号失败 502）：以客户端实收为准
	}
	if status == 0 {
		status = http.StatusOK // 未写头即 200（HTTP 语义）
	}
	// token 四列来源：改写模式＝上游响应 SSE usage（message_delta 真值覆盖
	// message_start 的 0，Q14 语义）；透传模式不解析响应（保真优先，压缩体
	// 亦扫不出），尽力而为记 0——票面许可条款，注释即说明。
	f := accounts.Fields{
		"agent":                 "cc",
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
		logger.Printf("dock 科目记账失败（不影响转发）: %v", err)
	}
}

// statusWriter 捕获代理写给客户端的状态码（记账用）。Flush/Unwrap 透传——
// SSE 立即冲刷（红线）与 ResponseController 的解包链不受包装影响。
type statusWriter struct {
	http.ResponseWriter
	code int
}

func (s *statusWriter) WriteHeader(code int) {
	if s.code == 0 {
		s.code = code
	}
	s.ResponseWriter.WriteHeader(code)
}

// Write 隐式 200：代理不写头直接 Write 的路径也记到状态。
func (s *statusWriter) Write(p []byte) (int, error) {
	if s.code == 0 {
		s.code = http.StatusOK
	}
	return s.ResponseWriter.Write(p)
}

func (s *statusWriter) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap http.ResponseController 的解包链（冲刷/全双工探测照达真身）。
func (s *statusWriter) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// metaTransport 出站 transport 包装：把上游状态码与响应体扫描器挂回该请求的
// reqMeta（指针经 context 从 handler 带来）。对请求/响应零改写——只读穿透；
// meta 为 nil（纯透传不记账）时与裸 transport 无异。
type metaTransport struct{ base http.RoundTripper }

func (m metaTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := m.base.RoundTrip(req)
	meta, _ := req.Context().Value(ctxKeyMeta{}).(*reqMeta)
	if meta == nil || resp == nil {
		return resp, err
	}
	meta.status = resp.StatusCode
	if resp.Body != nil {
		resp.Body = &usageBody{ReadCloser: resp.Body, acc: &meta.usage}
	}
	return resp, err
}

// usageBody 读穿透的响应体：字节原样上行给代理拷贝循环，顺手喂 usage 扫描器。
type usageBody struct {
	io.ReadCloser
	acc *sseUsageAcc
}

func (b *usageBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if n > 0 {
		b.acc.feed(p[:n])
	}
	return n, err
}

// sseUsageAcc 增量 SSE usage 扫描器（Q14 语义：message_delta 的 usage 列级
// 覆盖 message_start）。与 beat 的 parseSSEUsage 的差别：那边读至 EOF（发送
// 侧短响应），这边必须边流边扫——渡口响应要实时透传给 CC，绝不能为解析攒
// 整条流（SSE 缓冲＝重试风暴）。非 SSE 响应（count_tokens 的 JSON 应答、
// 压缩体等）无 data: 行＝扫不出，token 记 0（尽力而为条款）。
type sseUsageAcc struct {
	lineBuf       []byte
	data          []string
	input         int
	cacheRead     int
	cacheCreation int
	output        int
}

// dockUsageJSON 指针字段＝列级覆盖合并（与 beat usageJSON 同语义，多一列
// cache_creation——Anthropic 命名，GLM 不带则保持原值）。
type dockUsageJSON struct {
	InputTokens              *int `json:"input_tokens"`
	CacheReadInputTokens     *int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens *int `json:"cache_creation_input_tokens"`
	OutputTokens             *int `json:"output_tokens"`
}

// dockSSEEvent 只解析用到的三处；其余字段（delta/content_block 等）跳过。
type dockSSEEvent struct {
	Type    string `json:"type"`
	Message *struct {
		Usage *dockUsageJSON `json:"usage"`
	} `json:"message"`
	Usage *dockUsageJSON `json:"usage"`
}

func (a *sseUsageAcc) apply(u *dockUsageJSON) {
	if u == nil {
		return
	}
	if u.InputTokens != nil {
		a.input = *u.InputTokens
	}
	if u.CacheReadInputTokens != nil {
		a.cacheRead = *u.CacheReadInputTokens
	}
	if u.CacheCreationInputTokens != nil {
		a.cacheCreation = *u.CacheCreationInputTokens
	}
	if u.OutputTokens != nil {
		a.output = *u.OutputTokens
	}
}

// feed 喂原始字节：按 \n 切行（增量，行尾残段留 buf）。
func (a *sseUsageAcc) feed(p []byte) {
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

// handleLine 收一行（lineBuf 含整行，含可能的 \r）。
func (a *sseUsageAcc) handleLine() {
	line := strings.TrimSuffix(string(a.lineBuf), "\r")
	a.lineBuf = a.lineBuf[:0]
	switch {
	case line == "": // 空行＝事件边界
		a.flushEvent()
	case strings.HasPrefix(line, "data:"):
		v := strings.TrimPrefix(line, "data:")
		v = strings.TrimPrefix(v, " ") // 单个前导空格是字段分隔符，不是内容
		a.data = append(a.data, v)
	}
	// 其余行（event:/注释/keep-alive）不参与解析
}

func (a *sseUsageAcc) flushEvent() {
	if len(a.data) == 0 {
		return
	}
	payload := strings.Join(a.data, "\n") // SSE 多行 data 并接（规范行为）
	a.data = a.data[:0]
	var ev dockSSEEvent
	if json.Unmarshal([]byte(payload), &ev) != nil {
		return // 非 JSON 的 data（注释帧等）：跳过
	}
	switch ev.Type {
	case "message_start":
		if ev.Message != nil {
			a.apply(ev.Message.Usage)
		}
	case "message_delta":
		if ev.Usage != nil {
			a.apply(ev.Usage) // delta 列级覆盖 start（Q14 真值语义）
		}
	}
}

// flushTail 流尾兜底：EOF 无换行时残段仍算一行（与 beat parseSSEUsage 同款）。
func (a *sseUsageAcc) flushTail() {
	if len(a.lineBuf) > 0 {
		a.handleLine()
	}
	a.flushEvent()
}
