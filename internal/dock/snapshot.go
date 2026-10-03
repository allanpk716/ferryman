// snapshot.go — 票01：渡口内存快照库。
//
// 语义（spec「渡口·内存快照」，F2/F3/F4 钉死）：
//   - 每会话两份：主快照＝最大请求体＋最小必要头集（心跳重放的前缀源，主循环
//     单调增长 ⇒ 最大=最新主轮，标题生成类小请求永不覆盖）；last＝最后一份
//     （仅形态漂移诊断）。
//   - 头集白名单：content-type / anthropic-version / anthropic-beta /
//     user-agent / accept / accept-encoding，键小写。auth 类头（authorization、
//     x-api-key 等）一律不入——渡口出站会替换认证，beats 按占位令牌重建。
//   - Pinned 不变式：Pin 住的会话不可淘汰；LRU 只淘汰非 pinned，上限 16 个
//     非 pinned 会话；Unpin（对应等待窗收尾＋最后一跳结算）后 recency 归位、
//     才可被清理。
//   - 全内存不落盘：daemon 重启＝内存全丢（等待窗仍调度则该跳记
//     snapshot_missing，票03 语义），这是设计决定而非缺陷——快照含请求体，
//     持久化违反隐私红线（台账/账本永不落消息内容）。
package dock

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
)

// MaxUnpinnedSessions 非 pinned 会话的 LRU 容量上限（spec：最近 16 个）。
const MaxUnpinnedSessions = 16

// snapHeaderKeys 快照保留的最小必要头集（小写键，与 http.Header 的规范大写
// 查找无关——Values() 内部做规范化）。白名单外一律丢弃（auth 类头绝不入）。
var snapHeaderKeys = [...]string{
	"content-type",
	"anthropic-version",
	"anthropic-beta",
	"user-agent",
	"accept",
	"accept-encoding",
}

// Snapshot 单份快照的只读形状（票03 HttpBeatSender 依赖此形状，勿改字段语义）。
// Headers 为最小头集、键小写；Body 为完整请求体字节。返回值为快照内部引用，
// 调用方只读、不得修改。
type Snapshot struct {
	Body    []byte
	Headers map[string]string
}

// sessionRec 会话粒度的快照记录（store 内部）。
type sessionRec struct {
	main    Snapshot // 主快照＝最大请求体（同尺寸首见保留，见 Capture）
	hasMain bool
	last    Snapshot // 最后一份（仅诊断）
	hasLast bool
	pinned  bool  // Pinned 不变式：true 时 LRU 永不淘汰
	lastUse int64 // LRU 时钟序号（store.tick 单调递增）
}

// SnapshotStore 港口请求内存快照库。零值不可用，一律 NewSnapshotStore 构造。
// 所有方法并发安全（单互斥锁；临界区纯内存操作，量级 ≤ 上限 16+pinned，无锁序
// 约束）。
type SnapshotStore struct {
	mu       sync.Mutex
	sessions map[string]*sessionRec
	clock    int64 // LRU 单调时钟（与真实时间无关，只比先后）
	skipped  int   // session_id 提取失败/缺失的跳过计数（只记数不告警）
	stats    *proxyStats // 票01 W1：代理面统计读侧（server 回写、此处只读转发；nil＝未接线报 0/0）
}

// NewSnapshotStore 构造空快照库。
func NewSnapshotStore() *SnapshotStore {
	return &SnapshotStore{sessions: map[string]*sessionRec{}}
}

// Capture 捕获一份请求。sessionID 为空（提取失败/缺失）＝不入库、跳过计数 +1
// ——透传不受影响（spec 渡口节）。body 字节由库自有（克隆），调用方事后改写
// 原切片不污染快照。
func (s *SnapshotStore) Capture(sessionID string, body []byte, h http.Header) {
	if sessionID == "" {
		s.mu.Lock()
		s.skipped++
		s.mu.Unlock()
		return
	}
	hdrs := snapshotHeaders(h)
	b := bytes.Clone(body)
	s.mu.Lock()
	defer s.mu.Unlock()
	rec := s.sessions[sessionID]
	if rec == nil {
		rec = &sessionRec{}
		s.sessions[sessionID] = rec
	}
	rec.last, rec.hasLast = Snapshot{Body: b, Headers: hdrs}, true
	// 主快照＝最大体：严格大于才替换（同尺寸首见保留——心跳重放体≈主快照体长，
	// 不打擂避免无谓覆盖；主循环增长 ⇒ 新主轮必然严格更大）。
	if !rec.hasMain || len(b) > len(rec.main.Body) {
		rec.main, rec.hasMain = Snapshot{Body: b, Headers: hdrs}, true
	}
	s.tick(rec)
	s.evictLocked()
}

// Main 会话主快照（最大请求体）。未捕获/已淘汰返回 false。
func (s *SnapshotStore) Main(sessionID string) (Snapshot, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.sessions[sessionID]
	if !ok || !rec.hasMain {
		return Snapshot{}, false
	}
	return rec.main, true
}

// Last 会话最后一份快照（仅诊断）。未捕获/已淘汰返回 false。
func (s *SnapshotStore) Last(sessionID string) (Snapshot, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.sessions[sessionID]
	if !ok || !rec.hasLast {
		return Snapshot{}, false
	}
	return rec.last, true
}

// Pin 钉住会话（Pinned 不变式：不可被 LRU 淘汰）。未知会话也接受——建占位
// pinned 记录（票03 场景：等待窗先开、后续请求才到，占位保证到货即受保护）。
func (s *SnapshotStore) Pin(sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec := s.sessions[sessionID]
	if rec == nil {
		rec = &sessionRec{}
		s.sessions[sessionID] = rec
	}
	rec.pinned = true
	s.tick(rec)
}

// Unpin 解钉（对应等待窗收尾＋最后一跳结算后才调用）。解钉即记 recency——
// 窗口刚收尾的会话不该当场被挤，老化后才可被 LRU 淘汰。未知会话 no-op。
func (s *SnapshotStore) Unpin(sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.sessions[sessionID]
	if !ok {
		return
	}
	rec.pinned = false
	s.tick(rec)
	s.evictLocked()
}

// Skipped session_id 提取失败/缺失的累计跳过数（只记数，不告警）。
func (s *SnapshotStore) Skipped() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.skipped
}

// ProxyStats 代理面流量统计只读抄表（票01 W1 静默门数据面）：在途数与最后
// 完成时刻（Unix 秒）。统计只覆盖 15722 代理面上真实 CC 流量——心跳自产
// 重放与管理口 15700 流量一律不在内（回写侧见 server.go proxyStats）。
// stats 为 nil（直构快照库/统计未接线）＝0,0，与"渡口未启用如实报零"同语义，
// 调用方（daemon /stats）不报错。
func (s *SnapshotStore) ProxyStats() (inflight int, lastTS int64) {
	if s.stats == nil {
		return 0, 0
	}
	return s.stats.snapshot()
}

// tick 刷新 LRU 时钟（须持锁）。与真实时间脱钩：只要求全序一致。
func (s *SnapshotStore) tick(rec *sessionRec) {
	s.clock++
	rec.lastUse = s.clock
}

// evictLocked 非 pinned 会话数压回上限内（须持锁）：超出则淘汰最久未用的
// 非 pinned（pinned 永不在候选）。读接口（Main/Last）不刷新 recency——
// 心跳在读的会话必然处于窗口期＝pinned，读不算"用"。
func (s *SnapshotStore) evictLocked() {
	for {
		nonPinned := 0
		for _, r := range s.sessions {
			if !r.pinned {
				nonPinned++
			}
		}
		if nonPinned <= MaxUnpinnedSessions {
			return
		}
		var victimID string
		var victim *sessionRec
		for id, r := range s.sessions {
			if r.pinned {
				continue
			}
			if victim == nil || r.lastUse < victim.lastUse {
				victimID, victim = id, r
			}
		}
		if victim == nil { // 理论不可达（nonPinned>0 才进循环），防御收口
			return
		}
		delete(s.sessions, victimID)
	}
}

// snapshotHeaders 提取最小必要头集：白名单键、小写、多值逗号并（HTTP 语义
// 等价值）。白名单外（authorization/x-api-key/追踪头等）一律不入。
func snapshotHeaders(h http.Header) map[string]string {
	out := make(map[string]string, len(snapHeaderKeys))
	for _, k := range snapHeaderKeys {
		if vs := h.Values(k); len(vs) > 0 {
			out[k] = strings.Join(vs, ",")
		}
	}
	return out
}

// ShouldCapture 该请求是否入快照：仅 /v1/messages 的 POST。count_tokens 子
// 路径显式排除——纯计数探针，重放无保温价值且会污染主快照的体量比较；非
// messages 路径（余额查询等）一律透传不入账。
func ShouldCapture(method, path string) bool {
	if method != http.MethodPost {
		return false
	}
	if strings.HasSuffix(path, "/v1/messages/count_tokens") {
		return false
	}
	return strings.HasSuffix(path, "/v1/messages")
}

// HeaderClaudeCodeSessionID CC 每请求携带的会话 ID 头（claude-cli 2.1.273 地面
// 真值：每请求带裸 UUID；值与台账会话 ID 机制级等值＝转录文件名，已实证）。
// 渡口侧：体 metadata.session_id 缺失时回落读本头归因；beat 发送侧重放请求
// 带本头（值=目标会话 ID），渡口记账行据此按会话归集。
const HeaderClaudeCodeSessionID = "x-claude-code-session-id"

// conflictLog 会话归因头体冲突的留痕口（var 而非直接调 logger：测试钉"留痕
// 恰一行"不必截 stderr；生产路径即 logger.Printf 一行）。
var conflictLog = func(format string, args ...any) { logger.Printf(format, args...) }

// HeaderDeepSeekHarnessSessionID dsh（DeepSeek Harness）LLM 适配器的官方会话
// 归因头（llm-deepseek 每请求必带，值＝dsh 持久会话 ID——与守望/台账键同源：
// 会话头行 id，即目录名 session-<uuid> 的本体；铸造处 session-controller/
// subagent/headless 缺省皆 `session-${randomUUID()}`，另有 32hex/计数器变体）。
//
// P2-2 键对齐调查结论（2026-10-03，四源钉死：dsh 克隆 master@639ed015、pi-ai
// 0.87.1 与 main、装机 asar；生产账面交叉实证）：
//   - dsh 请求体**没有** metadata.session_id——pi-ai 的 anthropic-messages
//     只把 options.metadata.user_id 映进体（dsh 不传），sessionId 仅映
//     x-session-affinity/x-session-id 头且开关（sendSessionAffinityHeaders）
//     被 dsh 侧 catalog 标 'withhold' 不可开；llm-deepseek 的 serialize 亦不写。
//     2026-10-02 PR #4 验收说的"捕获键零改动就位"是把自家 CC 会话的记账行
//     误读成了 dsh 行——当日 dsh 流量两行（09:40:22/27）session_id 恒空。
//   - 唯一带会话键上线的 dsh 线通道＝本头（走 llm-deepseek 适配器的流量）。
//     2026-10-03 用户拍板切接法乙：接管路由已由 llm-pi-ai 换 llm-deepseek
//     （provider apply v2 补丁，见 internal/provider/dsh.go），走渡口的 dsh
//     流量每请求恒带本头——渡口捕获归因/记账归因自此有键（此前 pi-ai 路
//     无任何键上线，捕获恒 skipped）。
const HeaderDeepSeekHarnessSessionID = "x-deepseek-harness-session-id"

// ExtractSessionID 会话归因提取（快照捕获与渡口记账归因两个消费点共用，一处
// 实现）：请求体 JSON 的 metadata.session_id 第一优先；缺失时回落
// X-Claude-Code-Session-Id 头（UUID 形校验）；再缺失回落
// X-DeepSeek-Harness-Session-Id 头（dsh 会话形校验，见 isDshSessionIDShape）。
// 头值格式校验不合格＝缺失；头体并存以体为准（值不同留一行日志）；都缺返回
// ""（调用方据此走跳过计数——绝不伪造 ID）。
func ExtractSessionID(body []byte, h http.Header) string {
	sid := bodySessionID(body)
	hv := headerSessionID(h)
	if sid != "" {
		if hv != "" && hv != sid {
			conflictLog("会话归因头体冲突（以体为准）: body=%s header=%s", sid, hv)
		}
		return sid
	}
	if hv != "" {
		return hv
	}
	return dshHeaderSessionID(h)
}

// bodySessionID 体侧原样提取 metadata.session_id（不做格式校验——体是既有台账
// 口径，历史上如何记就如何记；格式防御只针对头回落）。失败/缺失返回 ""。
// 只做一层浅解析，不用全量 map[string]any：CC 请求体可到 MB 级，省一次全量
// 反序列化。
func bodySessionID(body []byte) string {
	var req struct {
		Metadata struct {
			SessionID string `json:"session_id"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return ""
	}
	return req.Metadata.SessionID
}

// headerSessionID 头回落值：无头或值不合格式＝""（视同缺失）。
func headerSessionID(h http.Header) string {
	if h == nil {
		return ""
	}
	v := h.Get(HeaderClaudeCodeSessionID)
	if v == "" || !isUUID36(v) {
		return ""
	}
	return v
}

// dshHeaderSessionID dsh 头回落值：无头或值不合 dsh 会话形＝""（视同缺失）。
func dshHeaderSessionID(h http.Header) string {
	if h == nil {
		return ""
	}
	v := h.Get(HeaderDeepSeekHarnessSessionID)
	if v == "" || !isDshSessionIDShape(v) {
		return ""
	}
	return v
}

// isDshSessionIDShape dsh 会话 ID 形校验：`session-` 前缀（全部铸造处的缺省
// 形态；自定 --session-id 不带前缀者放弃归因，同缺失语义）、总长 9..128、
// 字符集 [A-Za-z0-9._-]（覆盖 uuid36 连字符形/32hex/计数器种子；同
// EncodeSegment 安全集的哲学——形状对即可，归因识别面非安全面）。
func isDshSessionIDShape(v string) bool {
	if len(v) < 9 || len(v) > 128 || !strings.HasPrefix(v, "session-") {
		return false
	}
	for i := 0; i < len(v); i++ {
		c := v[i]
		switch {
		case '0' <= c && c <= '9', 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z':
		case c == '.' || c == '_' || c == '-':
		default:
			return false
		}
	}
	return true
}

// isUUID36 裸 UUID 形校验：36 字节、8-4-4-4-12 连字符分段、十六进制（大小写
// 均可）。不校验版本位——归因识别面而非安全面，形状对即可。
func isUUID36(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i := 0; i < 36; i++ {
		c := s[i]
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			if !('0' <= c && c <= '9' || 'a' <= c && c <= 'f' || 'A' <= c && c <= 'F') {
				return false
			}
		}
	}
	return true
}
