// chain.go — 票03：摆渡多级顺位链执行器（失败语义 + 逐级尝试事实回放）。
//
// 第三方摆渡按顺位链逐级执行：每级**单次尝试、零重试**（滑落即重试——级内
// 不重发，失败换下一级）；失败判定（任一即滑落）：连接失败/超时、HTTP≥400、
// 响应解析失败、输出为空、输出未通过交接结构校验（与同模型档产物结构校验
// 同款纪律：ParseSameModelOutput 两标记齐、顺序正、两层非空）。成功 = 非空
// 且结构校验过。云端级沿用既有 context 总时限语义（每次调用按 timeoutS 限时，
// worker 墙钟不变）；本地级（顺位 0 且内网/回环）拨号限时 5 秒——训练期死亡
// 罚则有界（只限连接建立，不限生成）。
//
// 记账与告警不在本文件：每次尝试经 onAttempt 即时回放给调用方（worker 侧
// 逐行落账 + 滑落告警；墙钟超时弃协程时已完成级的行不丢）。链尾骨架行为
// 不变（worker 既有 saveSkeleton 兜底）。
package ferry

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"ferryman/internal/extract"
)

// LocalDialTimeoutS 本地级（顺位 0 且 BaseURL 内网/回环）拨号超时秒数（票03
// 缺省 5；var 形 = 后续 config 接线缝，SameModelMaxTokens 同款先例）。训练期
// 本地模型下线时拨号挂死被此限时收口，链快速滑落云端，不吃满墙钟。
var LocalDialTimeoutS = 5.0

// 链尝试结果码（handoff 科目 outcome 同词表）。
const (
	ChainOutcomeFresh  = "fresh"
	ChainOutcomeFailed = "failed"
)

// ChainAttempt 单级尝试的记账/告警事实（票03：每次尝试逐行记账的载荷）。
// Usage 三键之和（失败无 usage 时三键 0）；Err 为失败原因摘要（日志/告警用，
// 不落账本——隐私与白名单纪律）。
type ChainAttempt struct {
	Pos      int // 顺位序号（0 起；账本 chain_pos 列）
	Provider string
	Model    string
	Protocol string
	Outcome  string // ChainOutcomeFresh | ChainOutcomeFailed
	WallS    float64
	Usage    map[string]any
	Err      string
}

// ChainSession 链式摆渡执行器：对 chain 按序逐级单次尝试，首级成功即返回
// (交接md, meta, nil)；全链败 → ("", nil, 聚合error)。onAttempt 在每级尝试
// 完成时同步回放（含成功级；nil 安全）——worker 侧据此即时记账/告警，墙钟
// 超时弃协程时已完成级的行不丢。空链 → error（防御形：装配处保证非空才开链）。
//
// 材料一次抽取、逐级复用（提取与 provider 无关）；L1/L2 分块随各级 Window
// 走（sessionReply 与 FerrySession 同源单源）。
func ChainSession(path string, chain []Provider, timeoutS float64, agent string,
	onAttempt func(ChainAttempt)) (string, map[string]any, error) {
	if len(chain) == 0 {
		return "", nil, fmt.Errorf("ferry: 空链（装配处应降级不开链）")
	}
	facts, items, skeleton, material, matTokens := sessionMaterial(path, agent)
	report := func(a ChainAttempt) {
		if onAttempt != nil {
			onAttempt(a)
		}
	}
	var errs []string
	for idx, pr := range chain {
		attemptT0 := time.Now()
		reply, mode, calls, cerr := sessionReply(pr, skeleton, material, items,
			matTokens, timeoutS, chainCall(idx, pr))
		usage := sumUsage(calls) // 失败时 calls=nil → 三键 0（usage 失败记 0）
		wall := round1(time.Since(attemptT0).Seconds())
		fail := func(reason string) {
			errs = append(errs, fmt.Sprintf("%d %s: %s", idx, pr.Name, reason))
			report(ChainAttempt{Pos: idx, Provider: pr.Name, Model: pr.Model,
				Protocol: pr.Protocol, Outcome: ChainOutcomeFailed,
				WallS: wall, Usage: usage, Err: reason})
		}
		if cerr != nil { // 连接失败/超时、HTTP≥400、响应解析失败（调用层错误三类）
			fail(cerr.Error())
			continue
		}
		if strings.TrimSpace(reply) == "" { // 输出为空
			fail("输出为空")
			continue
		}
		inject, full, perr := ParseSameModelOutput(reply) // 与同模型档同款结构纪律
		if perr != nil {
			fail(perr.Error())
			continue
		}
		meta := map[string]any{
			"source": path, "title": facts.Title, "mode": mode,
			"model": pr.Model, "provider": pr.Name,
			"covers_until_iso": facts.LastTS,
			"mat_tokens_est":   matTokens, "chunks": len(calls),
			"wall_s": wall,
			"usage":  usage, "call_walls": callWallsOf(calls),
			"inject_tokens_est": extract.TokenEstimate(inject),
			"full_tokens_est":   extract.TokenEstimate(full),
			"chain_pos":         idx,
		}
		report(ChainAttempt{Pos: idx, Provider: pr.Name, Model: pr.Model,
			Protocol: pr.Protocol, Outcome: ChainOutcomeFresh,
			WallS: wall, Usage: usage})
		return HandoffMarkdown(facts.Title, inject, full, meta), meta, nil
	}
	return "", nil, fmt.Errorf("链 %d 级皆败：%s", len(chain), strings.Join(errs, "；"))
}

// chainCall 第 idx 级单发调用构造（票03 分派面）：anthropic 档 → 适配器，
// 其余（含空 Protocol 零值）→ 既有 Chat（票02 口径：非 anthropic 即 openai）；
// 本地级（拨号限时 >0）→ 同协议调用换拨号限时 client，云端级沿用
// http.DefaultClient（既有 context 总时限语义不变）。
func chainCall(idx int, pr Provider) chatCall {
	dialS := ChainDialTimeoutS(idx, pr)
	if pr.Protocol != ProtocolAnthropic {
		if dialS <= 0 {
			return Chat
		}
		client := dialCappedClient(dialS)
		return func(p Provider, system, user string, t float64, mt int) (string, map[string]any, error) {
			return chatOpenAI(client, p, system, user, t, mt)
		}
	}
	if dialS <= 0 {
		return AnthropicChat
	}
	client := dialCappedClient(dialS)
	return func(p Provider, system, user string, t float64, mt int) (string, map[string]any, error) {
		return chatAnthropic(client, p, system, user, t, mt)
	}
}

// ChainDialTimeoutS 第 idx 级的拨号超时秒数：本地级（顺位 0 且 BaseURL 指向
// 内网/回环）→ LocalDialTimeoutS；其余（云端、非链首）→ 0 = 不限拨号（沿用
// 既有语义）。票面语义「链序号 0 且 BaseURL 为内网时的默认拨号时限」的钉子。
func ChainDialTimeoutS(idx int, pr Provider) float64 {
	if idx == 0 && isIntranetBaseURL(pr.BaseURL) {
		return LocalDialTimeoutS
	}
	return 0
}

// isIntranetBaseURL 内网/回环判定：host 为 localhost、回环或 RFC1918 私网
// IP 字面量。域名形态（如 LAN 主机名）不判内网——拨号限时是保守罚则，宁可
// 少覆盖不误伤云端域名。
func isIntranetBaseURL(baseURL string) bool {
	u, err := url.Parse(baseURL)
	if err != nil {
		return false
	}
	host := u.Hostname()
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && (ip.IsLoopback() || ip.IsPrivate())
}

// dialCappedClient 拨号限时专 client：在 DefaultTransport 克隆上换 DialContext
// 包装（代理/HTTP2 等其余传输语义与默认一致），请求/读体仍由请求 ctx 的总
// 时限管——「拨号超时」只作用于连接建立。
func dialCappedClient(capS float64) *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	base := &net.Dialer{}
	tr.DialContext = cappedDialContext(capS, base.DialContext)
	return &http.Client{Transport: tr}
}

// cappedDialContext 拨号限时包装：capS>0 → 底层拨号在限时子 ctx 内进行（到
// 点取消）；capS<=0 → 直通底层。底层 dial 以参数注入（纯函数，测试以伪拨号
// 器钉行为，零网络）。
func cappedDialContext(capS float64, dial func(ctx context.Context, network, addr string) (net.Conn, error)) func(ctx context.Context, network, addr string) (net.Conn, error) {
	if capS <= 0 {
		return dial
	}
	capD := time.Duration(capS * float64(time.Second))
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		dctx, cancel := context.WithTimeout(ctx, capD)
		defer cancel()
		return dial(dctx, network, addr)
	}
}
