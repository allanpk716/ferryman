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
	"sync"
	"sync/atomic"
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
	// Resolver 逐请求活跃上游解析 seam（票02 供应商接管，F4）：非 nil 时渡口
	// 对每个新请求向它要当前活跃条目——热切换即时生效；在途请求/SSE 流持有
	// 进入时的视图（上游连接/真钥/改写配置）自然跑完。nil＝构造期固化（票01
	// 既有行为零变化，Upstream/target 即全部）。实现须并发安全（daemon 生产
	// 装配为原子换绑持有者，internal/daemon/provider_switch.go）。
	Resolver UpstreamResolver
	// Accounts dock 科目账本；nil＝不记账（旧测试零改动）。
	Accounts *accounts.Accounts
	// Alert 形态漂移推送函数（daemon 侧给 AlertViaNotify(cfg) 的闭包）；
	// nil＝只记日志不推送。
	Alert func(title, message string)
}

// UpstreamResolver 逐请求活跃上游解析 seam（票02，F4："渡口对每个新请求读
// 内存态活跃供应商"）。config.DockCfg 自带同名方法即天然实现；daemon 生产
// 装配传入原子换绑持有者，测试可注桩。
type UpstreamResolver interface {
	ActiveUpstream() (string, *config.DockUpstream)
}

// upstreamView 单请求生效的上游视图（票02，F4 边界语义的载体）：目标/改写
// 配置/真钥三件一套，ServeHTTP 入口解析一次——在途请求与本 view 同生命周期
// （切上游不影响已进入的请求），新请求即刻拿新视图。票03 增设 up/upName：
// 条目原貌（dialect/codex 可用性/模型位/codex 主模型键）与活跃条目标签——
// responses 车道从同一 view 取目标与钥（车道分支与 model 改写据此裁决）。
type upstreamView struct {
	target    *url.URL
	rewriteOn bool
	rwCfg     RewriteConfig
	apiKey    string
	up        *config.DockUpstream // nil＝纯透传旧形态（票01 无条目）
	upName    string               // 活跃条目标签（""＝旧单值/未知）
}

// newUpstreamView 由上游条目派生视图：URL 解析＋守卫改写准入（与构造期
// resolveRewrite 同一判据单源；本地中转地址/缺 default＝透传）。条目原貌
// 与标签随行（票03 车道用）。
func newUpstreamView(name string, up *config.DockUpstream) (*upstreamView, string, error) {
	target, err := url.Parse(up.BaseURL)
	if err != nil || target.Scheme == "" || target.Host == "" {
		return nil, "", fmt.Errorf("dock: 上游地址无效: %q", up.BaseURL)
	}
	v := &upstreamView{target: target, up: up, upName: name}
	rw, ok, reason := resolveRewrite(true, up.BaseURL, up.ModelMap, up.TextOnly)
	if ok {
		v.rewriteOn, v.rwCfg, v.apiKey = true, rw, up.APIKey
	}
	return v, reason, nil
}

// reqMeta 单请求记账元数据。指针经 context 从 handler 带到出站 transport
// （状态码/usage 在响应链上回填）；usage 喂养与读尾都在 handler goroutine
// 的同步链上（proxy.ServeHTTP 内拷贝循环读毕后才 recordRow），无并发访问。
// 票02 增设：gate（首包闸门开，流式 messages POST）与 entry（排水注册句柄，
// expireDrain 从 Shutdown goroutine 侧访问——一律经 Server.mu，见
// markResponded/expireDrain）。
type reqMeta struct {
	start    time.Time
	session  string
	ua       string // 入站 User-Agent（agent 归因：cc 缺省 / deepseek-harness→dsh）
	mode     string
	modelIn  string
	modelOut string
	usage    sseUsageAcc // 上游响应 SSE usage 累计（响应体读穿透时喂养）
	status   int         // 上游状态码（transport 层捕获）
	gate     bool        // 首包闸门：流式 messages POST（热修 3）
	entry    *inflight   // 排水注册句柄（热修 1；nil＝非记账路径不参与排水）
}

// ctxKeyMeta context 私有键。
type ctxKeyMeta struct{}

// proxyStats 代理面流量统计（票01 W1 静默门数据面）：渡口上真实 CC 流量的
// 在途数与最后完成时刻。回写点＝记账路径 handler 的进出（与在途注册表同事件，
// 但独立记账——注册表还收心跳自产重放，统计口径排除它）；读侧经
// SnapshotStore.ProxyStats() 只读转发给 daemon /stats。"零请求视为静默成立"
// 的判定归消费方（升级门，票02），此处如实暴露零值。
type proxyStats struct {
	mu       sync.Mutex
	inflight int   // 非 replay 的在途记账请求数（回填窗 ⊇ 注册表窗，瞬时读数不欠账）
	lastTS   int64 // 最后一个代理面请求完成的 Unix 秒；守护启动以来无请求恒 0
}

// enter 在途 +1（请求进入记账路径时；replay 已在调用点排除）。
func (p *proxyStats) enter() {
	p.mu.Lock()
	p.inflight++
	p.mu.Unlock()
}

// done 完成一单：在途 -1 并记完成时刻。秒级截断是有意的——门判据是
// "距最后请求 ≥10 秒"，秒级精度足够，亚秒信息对消费方无意义。
func (p *proxyStats) done() {
	now := time.Now().Unix()
	p.mu.Lock()
	p.inflight--
	p.lastTS = now
	p.mu.Unlock()
}

// snapshot 只读抄表（一把锁一次抄齐，两字段同帧）。
func (p *proxyStats) snapshot() (inflight int, lastTS int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.inflight, p.lastTS
}

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

	// 票02（供应商接管，F4）：逐请求活跃上游 seam。resolver nil＝固定模式
	// （fixedView＝构造期视图，票01 行为零变化）；非 nil＝每请求解析（见
	// resolveView）。lastUpstream 是活跃条目标签的换绑日志锁存（同条目逐请求
	// 解析不重复记行；指针比较见 noteUpstream）。
	resolver     UpstreamResolver
	fixedView    *upstreamView
	lastUpstream atomic.Pointer[string]

	// 票02 优雅排水（热修 1）：在途记账请求注册表＋排水广播。drainCh 关闭
	// 即全体收尾（闸门转 504、drainBody 转注入）；drained 为幂等位。字段
	// 一律经 mu 访问（expireDrain 从 Shutdown 调用方 goroutine 侧进来）。
	mu        sync.Mutex
	drained   bool
	drainCh   chan struct{}
	idleCh    chan struct{}
	inflights map[*inflight]struct{}

	// 票01 W1：代理面统计回写侧（读侧在 store.stats，NewWithOptions 里两处
	// 指到同一份——daemon 经 DockSnap 取数与回写同源，无二次抄表漂移）。
	stats *proxyStats

	// 票03（responses 翻译车道）：独立出站客户端（与 proxy 的 transport 同
	// 参数——首包 50min 上限、不设整体超时保长流；车道自带响应处理不经
	// ReverseProxy）与车道事件日志（一次一值去重，测试断言面）。
	laneClient  *http.Client
	codexEvents *codexLaneEvent
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
	// 票02 排水通道初值：idleCh 初始关闭（零在途＝空载）；drainCh 待到期广播。
	idle := make(chan struct{})
	close(idle)
	s := &Server{
		listen:    listen,
		target:    target,
		store:     NewSnapshotStore(),
		drainCh:   make(chan struct{}),
		idleCh:    idle,
		inflights: make(map[*inflight]struct{}),
		stats:     &proxyStats{},
	}
	// 票01 W1：统计读侧挂到快照库（server 回写、store 只读转发）——daemon 手里
	// 只有 DockSnap，这是统计出渡口的唯一既有通道，不新增装配面。
	s.store.stats = s.stats
	if o.Upstream != nil || o.Resolver != nil {
		s.drift = NewDriftTracker(o.Alert)
	}
	if o.Upstream != nil {
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
	// 票02：构造期视图固化（固定模式的逐请求零开销快照；resolver 模式的
	// 悬空/解析失败回落位）。既有字段（rewriteOn/rwCfg/apiKey/target）保留
	// 原义——构造期日志与既有测试面不动。票03：条目原貌随行（responses 车
	// 道分流用；纯 New 无条目＝nil＝车道退整体代理）。
	s.fixedView = &upstreamView{
		target: s.target, rewriteOn: s.rewriteOn, rwCfg: s.rwCfg, apiKey: s.apiKey,
		up: o.Upstream,
	}
	s.resolver = o.Resolver
	// 票03：responses 车道装配——出站客户端（与 proxy 同参数口径）+ 事件
	// 日志。纯 New（全零 Options）也建（车道入口可达即用，构造廉价）。
	s.codexEvents = newCodexLaneEvent()
	s.laneClient = &http.Client{Transport: &http.Transport{
		Proxy:                 nil, // 显式不走环境代理：上游是直连目标
		DialContext:           (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   16,
		IdleConnTimeout:       90 * time.Second,
		ResponseHeaderTimeout: TransportTimeoutS,
		// 不设整体 Timeout/正文超时：长 SSE 流按需无限流（同 proxy 口径）
	}}
	s.proxy = &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			// 票02（F4）：出站目标取本请求入口解析的视图（resolver 模式经
			// context 带来；固定模式/防御路径回落构造期视图）——在途请求持有
			// 既有上游连接跑完，切上游只影响新请求。
			v := s.viewOf(pr.In.Context())
			// SetURL 保留入站路径与 query（join 语义）；Host 显式指上游——
			// 客户端 Host 不外泄、后端看到的就是它自己（F7 断言同款）
			pr.SetURL(v.target)
			pr.Out.Host = v.target.Host
			if v.rewriteOn {
				// 改写模式出站头卫生（透传模式零处理——票01 保真语义不动）
				sanitizeOutboundHeaders(pr.Out.Header, v.apiKey)
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
		// 票02（热修 2）：出站失败的标准形状（拨号失败 502＋错误体＋
		// Retry-After；闸门/排水 504；客户端先断静默）。两模式同接——自产
		// 错误不属于上游字节，透传保真不管辖（契约：渡口自产错误一律标准
		// 形状，替换 Go 默认空体 502）。
		ErrorHandler: s.proxyErrorHandler,
		// metaTransport 读穿透包装（meta 为 nil 时零行为）——纯 New 的
		// 票01 路径字节行为不变；票02 起附带首包闸门与排水注入（仅记账
		// 路径的流式 200 响应，见 gate.go）
		Transport: metaTransport{
			base: &http.Transport{
				Proxy:                 nil, // 显式不走环境代理：上游是直连目标
				DialContext:           (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
				MaxIdleConns:          100,
				MaxIdleConnsPerHost:   16, // 默认 2 太小：CC 并发请求会频繁重建连接
				IdleConnTimeout:       90 * time.Second,
				ResponseHeaderTimeout: TransportTimeoutS,
				// 不设整体 Timeout/正文超时：长 SSE 流按需无限流（见常量注释）
			},
			s: s,
		},
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

// ctxKeyUpstream 每请求上游视图的 context 私有键（ServeHTTP 入口解析→
// Rewrite 闭包取用；在途请求持有同一视图跑完＝F4 边界）。
type ctxKeyUpstream struct{}

// resolveView 每请求一次的活跃上游视图解析（票02，F4 seam 唯一入口）：
//   - 固定模式（resolver nil）：直返构造期视图，零额外开销，票01 行为不变；
//   - resolver 模式：逐请求向 resolver 要当前活跃条目并派生整套视图（目标/
//     改写配置/真钥一次换齐，绝不半换）。条目悬空（nil）或地址无效＝回落
//     构造期视图（与 ActiveUpstream 防御路径同纪律），守卫拒绝的条目如实
//     降透传并经 noteUpstream 记一次原因。
func (s *Server) resolveView() *upstreamView {
	if s.resolver == nil {
		return s.fixedView
	}
	name, up := s.resolver.ActiveUpstream()
	if up == nil {
		return s.fixedView
	}
	v, reason, err := newUpstreamView(name, up)
	if err != nil {
		logger.Printf("[dock] %v（活跃条目无效，回落构造期上游）", err)
		return s.fixedView
	}
	s.noteUpstream(name, up, v, reason)
	return v
}

// viewOf Rewrite 闭包取视图：context 带（resolver 模式每请求在
// ServeHTTP 入口塞入）用带的；否则构造期视图（固定模式/防御路径）。
func (s *Server) viewOf(ctx context.Context) *upstreamView {
	if v, ok := ctx.Value(ctxKeyUpstream{}).(*upstreamView); ok && v != nil {
		return v
	}
	return s.fixedView
}

// noteUpstream 活跃条目标签变化时记一行（热切换可见性）：同条目逐请求解析
// 不重复记；守卫拒绝（透传）时附原因。标签比较经原子锁存，并发竞争最多
// 多记一行，无害。
func (s *Server) noteUpstream(name string, up *config.DockUpstream, v *upstreamView, guardReason string) {
	label := name
	if label == "" {
		label = "旧单值" // 无表兜底包装（与 serve 装配横幅同词）
	}
	if old := s.lastUpstream.Swap(&label); old != nil && *old == label {
		return
	}
	mode := "改写"
	if !v.rewriteOn {
		mode = "透传"
		if guardReason != "" {
			mode += "：" + guardReason
		}
	}
	logger.Printf("活跃上游: %s → %s（%s）", label, up.BaseURL, mode)
}

// ServeHTTP 渡口入口（票06 双模式）：
//   - 纯透传（rewriteOn=false 且不记账）：与票01 逐字同路径——仅 /v1/messages
//     POST 读体捕获快照，其余流式直通；
//   - 记账开（acc 非 nil）且命中 messages/count_tokens：包 statusWriter 捕
//     状态码，代理返回后落一行 dock 科目（任何记账失败不影响转发）；
//   - 改写模式：体改写发生在进代理前；快照存改写前原始体。
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// 票02（供应商接管，F4）：每请求入口解析一次活跃上游视图——resolver 模式
	// 下新请求即刻新上游；本请求后续全程（改写判定/出站目标/真钥）用同一视
	// 图，在途请求/SSE 流据此持有既有上游连接自然跑完。固定模式与构造期固化
	// 同值，行为零变化。
	view := s.resolveView()
	if s.resolver != nil {
		r = r.WithContext(context.WithValue(r.Context(), ctxKeyUpstream{}, view))
	}
	// 票03（供应商接管，spec 决策 2）：responses 车道路径分流——/responses
	// 及对照表 §4.1 全部变体（POST）进新车道；既有 /v1/messages 分支以下
	// 零变化。车道内部自读体/自记账（不接快照/摆渡），出错自答（错误形状
	// 见 codexlane.go）。
	if r.Method == http.MethodPost {
		if canonical, ok := canonicalResponsesPath(r.URL.Path); ok {
			s.serveResponsesLane(w, r, view, canonical)
			return
		}
	}
	// 票03：自产重放（追加重放带 x-ferryman-replay 标记头）不入快照、不喂
	// 漂移——追加体会顶替主快照（见 replayguard.go）；dock 科目照记（record
	// 以 messagesPost 计，重放也有传输流水）。改写照走：重放与真流量同经
	// Rewrite，上游缓存的改写后前缀才咬合得上（2026-10-01 第二缺口：旧条件
	// 把 replay 排除在改写外，两笔重放缓存命中 128/0、input 全价重付 19.9 万/
	// 20.5 万，同模型档的缓存价差被整个架空）。
	messagesPost := ShouldCapture(r.Method, r.URL.Path)
	replay := isReplayRequest(r.Header)
	countTok := isCountTokens(r.Method, r.URL.Path)
	capture := messagesPost && !replay
	record := s.acc != nil && (messagesPost || countTok)

	needBody := capture || record || (view.rewriteOn && (messagesPost || countTok))
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

	// 票01 头回落：会话归因一次提取、两个消费点（快照捕获/记账归因）共用——
	// 体 metadata.session_id 第一优先，缺失回落 X-Claude-Code-Session-Id 头
	//（UUID 格式校验），再缺失回落 X-DeepSeek-Harness-Session-Id 头（dsh 会话
	// 形校验；头体冲突以体为准留痕，语义见 ExtractSessionID）。
	var sessionID string
	if capture || record {
		sessionID = ExtractSessionID(body, r.Header)
	}

	if capture {
		// 快照＝CC 原始请求（改写前）：beat 重放原始请求经渡口再走同一改写。
		// 空 session_id（缺失且头回落也无/坏）＝Capture 内部跳过计数，不入库。
		s.store.Capture(sessionID, body, r.Header)
		// 形态漂移观察同点喂原始头体（nil 安全：纯透传不观察）
		s.drift.Observe(r.Header.Get("Anthropic-Beta"), body)
	}

	// 改写独立于记账：不接账本时改写照常生效（view.rewriteOn 才有此分支）。
	// messages POST 全量改写，真流量与自产重放同经 Rewrite——同源体出站字节
	// 相同，上游缓存前缀/模型命名空间才对得上（快照存原始体的不变式依赖此
	// 处兑现）。count_tokens 仅同映射 model（不做图片降级，其余键不动）。
	// rewritten/rewriteDone 为请求局部值（并发请求不共享任何状态）。
	var rewritten Rewritten
	rewriteDone := false
	if view.rewriteOn && (messagesPost || countTok) {
		if messagesPost {
			rw, err := Rewrite(body, view.rwCfg)
			if err != nil {
				// 非法体：原体透传交上游校验应答（不替上游造 400）
				logger.Printf("改写失败（原体透传）: %v", err)
			} else {
				applyRewritten(r, body, rw.Body)
				rewritten, rewriteDone = rw, true
			}
		} else if nb, mi, mo, ok := mapCountTokensModel(body, view.rwCfg); ok {
			applyRewritten(r, body, nb)
			rewritten, rewriteDone = Rewritten{Body: nb, ModelIn: mi, ModelOut: mo}, true
		}
	}

	var meta *reqMeta
	if record {
		meta = &reqMeta{start: time.Now()}
		meta.session = sessionID
		meta.ua = r.UserAgent()
		if view.rewriteOn {
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
		// 票02（热修 3）：首包闸门只对流式 messages POST（判定自已读请求体；
		// count_tokens 与非流式不闸，纯透传不读体不闸——daemon 生产接线恒记账）。
		meta.gate = messagesPost && bodyIsStream(body)
		// 票02（热修 1）：注册排水句柄——出站挂上可取消 ctx，到期时未获响应
		// 的请求由 expireDrain 取消（ErrorHandler 回 504），响应已确立的流
		// 走 drainBody 注入。defers 在 recordRow 之后才跑（LIFO），行落账时
		// 仍在册，宽限等待据此涵盖注入交付。
		// 票01 W1：统计回填＝记账路径的非 replay 流量（心跳自产重放经同一
		// 注册表用于排水，但不计入统计——升级静默门只看真实 CC 流量，本程序
		// 自产心跳不该把门按住）。enter 先于注册、done 后于出册（defer LIFO：
		// untrackInflight 先跑），每个计入请求的统计窗 ⊇ 其注册表窗(重放刻意只入注册表不统计——dock_inflight=0 不蕴含注册表空)，瞬时读数不欠账。
		rctx, cancel := context.WithCancel(r.Context())
		defer cancel()
		if !replay {
			s.stats.enter()
			defer s.stats.done()
		}
		s.trackInflight(meta, cancel)
		defer s.untrackInflight(meta)
		r = r.WithContext(context.WithValue(rctx, ctxKeyMeta{}, meta))
		sw := &statusWriter{ResponseWriter: w}
		s.proxy.ServeHTTP(sw, r)
		s.recordRow(meta, sw)
		return
	}
	s.proxy.ServeHTTP(w, r)
}

// bodyIsStream 顶层 stream 布尔提取（首包闸门判定）；非 JSON/缺失＝false。
func bodyIsStream(body []byte) bool {
	var probe struct {
		Stream bool `json:"stream"`
	}
	if json.Unmarshal(body, &probe) != nil {
		return false
	}
	return probe.Stream
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
	stripDSHWireKeys(obj) // 接法乙方言卫生：count_tokens 与 messages 同款剥顶层 dsh_*
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
	u := m.usage.finish() // 流尾残段兜底＋定版（锁语义见 sseUsageAcc 注释）
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
	// agent 归因（2026-10-02 dsh 接管；2026-10-03 切接法乙）：dsh 的适配器
	// （pi-ai 与 llm-deepseek 两路同源 attribution.ts）每请求必带
	// `User-Agent: deepseek-harness/<版本>`（官方强制归因头）——以此分岔 dsh
	// 流量；其余（含 CC）照旧记 cc。beat 重放不带该头，不受影响。
	agent := "cc"
	if strings.HasPrefix(m.ua, "deepseek-harness/") {
		agent = "dsh"
	}
	f := accounts.Fields{
		"agent":                 agent,
		"session_id":            m.session,
		"mode":                  m.mode,
		"model_in":              m.modelIn,
		"model_out":             m.modelOut,
		"input_tokens":          u.input,
		"cache_read_tokens":     u.cacheRead,
		"cache_creation_tokens": u.cacheCreation,
		"output_tokens":         u.output,
		"latency_s":             time.Since(m.start).Seconds(),
		"status":                status,
	}
	// 票02（热修 4）：干净 EOF 观测——见过至少一个 SSE 事件但流尾无
	// message_stop 即截断（上游自断与排水注入收尾都算）。仅截断行携带
	// truncated（缺省不写，旧流水与读侧不受影响）；非 SSE 响应无事件可见，
	// 不标记；不改转发字节。
	if u.truncated {
		f["truncated"] = true
		logger.Printf("截断流: session=%s mode=%s status=%d（EOF 前未见 message_stop）",
			m.session, m.mode, status)
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
// meta 为 nil（纯透传不记账）时与裸 transport 无异。票02 起对流式 200 记账
// 响应加首包闸门（gate.go—— withhold 响应头至首字节，静默 60s 判失败）。
type metaTransport struct {
	base http.RoundTripper
	s    *Server // 排水广播/注册表访问（闸门与注入需要）
}

func (m metaTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := m.base.RoundTrip(req)
	if err != nil {
		return resp, err // 出站失败：ErrorHandler 统一形状（errorshape.go）
	}
	meta, _ := req.Context().Value(ctxKeyMeta{}).(*reqMeta)
	if meta == nil || resp == nil {
		return resp, err
	}
	meta.status = resp.StatusCode
	if resp.Body == nil {
		m.s.markResponded(meta)
		return resp, nil
	}
	ub := &usageBody{ReadCloser: resp.Body, acc: &meta.usage}
	if meta.gate && resp.StatusCode == http.StatusOK {
		return m.s.gateFirstByte(meta, resp, ub)
	}
	m.s.markResponded(meta)
	resp.Body = ub
	return resp, nil
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
//
// 票02 并发边界：闸门/排水的弃读 goroutine（gate.go）可能在 handler 收账
// 之后才把末次读取喂进来——mu 把喂食与 finish 定版互斥，弃读迟到即无害。
type sseUsageAcc struct {
	mu            sync.Mutex
	lineBuf       []byte
	data          []string
	input         int
	cacheRead     int
	cacheCreation int
	output        int
	sawEvent      bool // 见过至少一个 SSE 事件（截断判定分子，热修 4）
	sawStop       bool // 见过 message_stop（完整流的标志）
}

// usageSnapshot finish() 的定版快照（锁外消费）。
type usageSnapshot struct {
	input, cacheRead, cacheCreation, output int
	truncated                               bool
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

// feed 喂原始字节：按 \n 切行（增量，行尾残段留 buf）。锁内（并发边界见
// 结构体注释）。
func (a *sseUsageAcc) feed(p []byte) {
	a.mu.Lock()
	defer a.mu.Unlock()
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
	a.sawEvent = true                     // 事件边界到达即"见过事件"（非 JSON data 也算，热修 4）
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
	case "message_stop":
		a.sawStop = true // 完整流标志（截断判定分母，热修 4）
	}
}

// finish 流尾收账：锁内做残段兜底（EOF 无换行的末事件）并定版快照——此后
// 迟到的弃读喂食不会再与本快照竞争。截断判定（热修 4）在此定版。
func (a *sseUsageAcc) finish() usageSnapshot {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.lineBuf) > 0 {
		a.handleLine()
	}
	a.flushEvent()
	return usageSnapshot{
		input:         a.input,
		cacheRead:     a.cacheRead,
		cacheCreation: a.cacheCreation,
		output:        a.output,
		truncated:     a.sawEvent && !a.sawStop,
	}
}
