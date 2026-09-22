// httpsender.go — 票03：HttpBeatSender＝快照重放发送器（beat.Sender 真身）。
//
// 语义（spec「心跳」节＋ADR-0006/0007；F1 唯一错误语义）：
//   - 前缀源＝渡口内存主快照（票01 SnapshotStore.Main＝最大体＝最新主轮），
//     原样重放、唯一改写顶层 max_tokens→1（输出封顶；messages/system/tools
//     等其余键经 json.RawMessage 原样直通——逐字段不变）。
//   - 头部＝快照白名单头集（content-type/anthropic-version/anthropic-beta/
//     user-agent/accept）＋auth 占位令牌字面量（真钥只在渡口出站替换，
//     sender 永不接触——T39）＋accept-encoding 固定 identity（beat 请求不
//     压缩，规避解压；响应仍带 content-encoding 即按错误处理——S4 防御）。
//   - 错误语义（F1）：任何传输层错误（连接失败/超时/429/5xx）不重试，直接
//     BeatResult{Sent:true, OK:false, Err:<类别>}；只有 2xx 且 SSE 完整读到
//     message_delta 的 usage 才算 OK。单跳总超时秒级——Send 的时限铁律见
//     beat.go Sender 注释（守望单线程串行调用，挂起会拖垮全部会话）。
//   - miss 不判：sender 只报 usage，HIT/MISS 归 beat.Classify（熔断语义单源）。
package beat

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"ferryman/internal/dock"
)

// DefaultDockURL 渡口入站默认地址（票01 默认监听 127.0.0.1:15722 的 URL 形）。
const DefaultDockURL = "http://127.0.0.1:15722"

// beatTimeout 单跳总超时：覆盖连接、发请求、读完整 SSE 一整程（秒级）。
const beatTimeout = 10 * time.Second

// placeholderToken auth 占位令牌字面量（Q14 工程经验：重放头按占位令牌重建，
// 不带转发器脱敏残留值；真钥由渡口出站/上游替换，sender 永不持有）。
const placeholderToken = "PROXY_MANAGED"

// 错误类别（BeatResult.Err 的稳定字面量；只记类别不记消息内容——隐私铁律）。
const (
	errSnapshotMissing = "snapshot_missing"  // 会话无主快照（如 daemon 重启后窗口仍调度）
	errBadBody         = "bad_snapshot_body" // 快照体非 JSON，无法改写 max_tokens
	errConn            = "conn_error"        // 连接失败/被拒/重置
	errTimeout         = "timeout"           // 总超时（连接/读流任一阶段）
	errContentEncoding = "content_encoding"  // 响应带压缩编码（请求侧已固定 identity）
	errSSE             = "sse_error"         // SSE 事件 data 非 JSON
	errSSEIncomplete   = "sse_incomplete"    // 2xx 但流终未见 message_delta 的 usage
)

// HttpBeatSender 快照重放发送器：零重试、无内部 goroutine、绝不 panic——
// 一切异常路径收敛为 BeatResult{Sent:true, OK:false, Err:<类别>}（守望单线程
// 串行调用即安全；sender-raise 兜底在 watcher.sendBeat 另有 recover）。
type HttpBeatSender struct {
	dockURL string              // 渡口入站地址（beat 与真流量同路径同改写）
	store   *dock.SnapshotStore // 快照只读句柄（Main 取主快照）
	timeout time.Duration       // 单跳总超时（同包测试可缩短，生产行为不变）
	// appendTimeout 票03：追加重放总超时（交接生成分钟级；执行方在独立
	// goroutine，不占守望线程）。零值（裸构造防御）由发送侧回落默认。
	appendTimeout time.Duration
	client        *http.Client
}

// 编译期接口形状钉死（beat.Sender）。
var _ Sender = (*HttpBeatSender)(nil)

// NewHttpBeatSender 构造。dockURL 空＝DefaultDockURL；store nil 视同
// snapshot_missing（防御收口：serve 注入点保证非 nil，这里不炸构造）。
func NewHttpBeatSender(dockURL string, store *dock.SnapshotStore) *HttpBeatSender {
	if dockURL == "" {
		dockURL = DefaultDockURL
	}
	return &HttpBeatSender{
		dockURL:       dockURL,
		store:         store,
		timeout:       beatTimeout,
		appendTimeout: appendReplayTimeout, // 票03：追加重放形态
		client:        &http.Client{},      // 超时按跳由请求 ctx 携带（见 Send）
	}
}

// Send（beat.Sender）。秒级内必返回；快照缺失/体坏/传输错/SSE 异常一律
// error 语义（不阻塞调度、不入 observe 桶）。
func (s *HttpBeatSender) Send(plan BeatPlan) BeatResult {
	if s.store == nil {
		return BeatResult{Sent: true, OK: false, Err: errSnapshotMissing}
	}
	snap, ok := s.store.Main(plan.SessionID)
	if !ok {
		// daemon 重启＝内存快照全丢（票01 隐私设计：快照不落盘）；窗口仍调度
		// 则该跳记 error，不 panic、不阻塞（spec 渡口节已知边界）。
		return BeatResult{Sent: true, OK: false, Err: errSnapshotMissing}
	}
	body, model, err := beatBody(snap.Body)
	if err != nil {
		return BeatResult{Sent: true, OK: false, Err: errBadBody}
	}
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimSuffix(s.dockURL, "/")+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return BeatResult{Sent: true, OK: false, Err: errConn} // URL 坏＝本地装配错，按传输错收口
	}
	beatHeaders(req.Header, snap.Headers)
	resp, err := s.client.Do(req)
	if err != nil {
		return BeatResult{Sent: true, OK: false, Err: transportErrCategory(err)}
	}
	defer resp.Body.Close()
	// 状态码语义（F1 不重试）：429/5xx 单列类别，其余非 2xx 泛化 http_<code>。
	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
		return BeatResult{Sent: true, OK: false, Err: "http_429"}
	case resp.StatusCode >= 500:
		return BeatResult{Sent: true, OK: false, Err: "http_5xx"}
	case resp.StatusCode < 200 || resp.StatusCode >= 300:
		return BeatResult{Sent: true, OK: false, Err: fmt.Sprintf("http_%d", resp.StatusCode)}
	}
	// 请求侧已固定 identity；响应仍带 content-encoding＝解压不在职责内，
	// 按错误处理（spec：解析前处理解压——本实现选择不请求压缩，防御收口）。
	if resp.Header.Get("Content-Encoding") != "" {
		return BeatResult{Sent: true, OK: false, Err: errContentEncoding}
	}
	usage, errCat := parseSSEUsage(resp.Body)
	if errCat != "" {
		return BeatResult{Sent: true, OK: false, Err: errCat}
	}
	return BeatResult{
		Sent:            true,
		OK:              true,
		InputTokens:     usage.input,
		CacheReadTokens: usage.cacheRead,
		OutputTokens:    usage.output,
		Model:           model, // 快照体顶层 model——beat 科目行的记账元数据
	}
}

// beatBody 快照体改写：唯一改顶层 max_tokens→1，其余键的 JSON 经 RawMessage
// 原样直通（messages/system/tools 逐字段不变——重放保真的关键；键序由 Go
// map 渲染为字典序，JSON 语义等价）。顺带提取顶层 model（记账元数据）。
func beatBody(src []byte) ([]byte, string, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(src, &m); err != nil {
		return nil, "", err
	}
	model := ""
	if raw, ok := m["model"]; ok {
		_ = json.Unmarshal(raw, &model) // 非 string 型静默取空——记账降级不炸重放
	}
	m["max_tokens"] = json.RawMessage("1")
	out, err := json.Marshal(m)
	if err != nil {
		return nil, "", err
	}
	return out, model, nil
}

// beatHeaders 头部重建：快照白名单内容头（有则带，键值原样）＋auth 占位令牌
// 字面量＋accept-encoding 固定 identity。快照永不存 auth 头（票01 白名单），
// 占位令牌到渡口出站/上游被替换——本函数产出的头集恰为白名单头集＋两枚
// 占位令牌，零新增头（测试钉死）。
func beatHeaders(dst http.Header, snap map[string]string) {
	for _, k := range [...]string{
		"content-type", "anthropic-version", "anthropic-beta", "user-agent", "accept",
	} {
		if v, ok := snap[k]; ok {
			dst.Set(k, v)
		}
	}
	dst.Set("Authorization", "Bearer "+placeholderToken)
	dst.Set("x-api-key", placeholderToken)
	dst.Set("Accept-Encoding", "identity")
}

// transportErrCategory 传输错误归类：超时（ctx 截止/网络超时）vs 连接错。
// 类别只作熔断与报表口径，不驱动重试——F1 不重试是唯一语义。
func transportErrCategory(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return errTimeout
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return errTimeout
	}
	return errConn
}

// sseUsage 三列 token 的最终口径（delta 覆盖 start 后的累计值）。
type sseUsage struct {
	input     int
	cacheRead int
	output    int
}

// usageJSON 指针字段＝字段级覆盖合并：message_delta 只带部分列时，未带的列
// 保留 message_start 的值；delta 带的列一律覆盖 start（Q14：GLM 真 usage 在
// delta，start 恒 0——覆盖语义由此钉死）。
type usageJSON struct {
	InputTokens          *int `json:"input_tokens"`
	CacheReadInputTokens *int `json:"cache_read_input_tokens"`
	OutputTokens         *int `json:"output_tokens"`
}

// sseEventJSON 只解析用得到的三处；其余字段（delta/content_block 等）跳过。
type sseEventJSON struct {
	Type    string `json:"type"`
	Message *struct {
		Usage *usageJSON `json:"usage"`
	} `json:"message"`
	Usage *usageJSON `json:"usage"`
}

// parseSSEUsage 逐事件读 SSE 至 EOF，返回最终 usage。OK 判据＝流读完且见过
// message_delta 的 usage（缺 delta 的流视为不完整——绝不以 start 的值记 OK，
// 防止把"截断流"洗成 miss/hit）。第二返回值为错误类别（""＝成功）。
// event: 行不参与判定——事件类型以 data JSON 的 type 字段为准（上游惯例
// 两处一致；data 是权威）。
func parseSSEUsage(r io.Reader) (sseUsage, string) {
	var acc sseUsage
	sawDeltaUsage := false
	apply := func(u *usageJSON) {
		if u == nil {
			return
		}
		if u.InputTokens != nil {
			acc.input = *u.InputTokens
		}
		if u.CacheReadInputTokens != nil {
			acc.cacheRead = *u.CacheReadInputTokens
		}
		if u.OutputTokens != nil {
			acc.output = *u.OutputTokens
		}
	}
	sc := bufio.NewScanner(r)
	// 单行上限 4MiB：content_block 行可携带大块增量文本，64KiB 默认会截断误报。
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	var data []string
	flushEvent := func() string {
		if len(data) == 0 {
			return ""
		}
		payload := strings.Join(data, "\n") // SSE 多行 data 并接（规范行为）
		data = data[:0]
		var ev sseEventJSON
		if err := json.Unmarshal([]byte(payload), &ev); err != nil {
			return errSSE
		}
		switch ev.Type {
		case "message_start":
			if ev.Message != nil {
				apply(ev.Message.Usage)
			}
		case "message_delta":
			if ev.Usage != nil {
				apply(ev.Usage)
				sawDeltaUsage = true
			}
		}
		return ""
	}
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "": // 空行＝事件边界
			if cat := flushEvent(); cat != "" {
				return acc, cat
			}
		case strings.HasPrefix(line, "data:"):
			v := strings.TrimPrefix(line, "data:")
			v = strings.TrimPrefix(v, " ") // 单个前导空格是字段分隔符，不是内容
			data = append(data, v)
		}
		// 其余行（event:/注释/keep-alive）不参与解析
	}
	if cat := flushEvent(); cat != "" { // EOF 前无空行收尾的末事件兜底
		return acc, cat
	}
	if err := sc.Err(); err != nil { // 读失败（如总超时掐断）按传输错误归类
		return acc, transportErrCategory(err)
	}
	if !sawDeltaUsage {
		return acc, errSSEIncomplete
	}
	return acc, ""
}
