// dock_upstream.go — 票01：渡口上游表（ADR-0011 多上游直连）。
//
// 渡口上游（CONTEXT.md）＝渡口转发目的地的供应商条目，Anthropic Messages
// 协议，自带密钥与 model 映射；[dock].active 单选。全体条目只收真供应商端点，
// 本地中转地址只作回退通道（守卫强制透传）。
//
// 解析优先级（F9 裁定，本文件 ActiveUpstream 是唯一裁决点）：新表+active
// 优先；旧单值仅在无表时兜底包装（未迁移/迁移失败回退，保证旧配置行为不变）。
// rewrite 隐含开启（旧 rewrite_enabled 废弃，解析容忍不生效）；上游为本地
// 中转地址时既有守卫（dock.DoubleRewriteRisk → IsLocalRelayAddr 单源）强制
// 退透传。
package config

import (
	"net/url"
	"sort"
	"strings"
)

// 渡口上游方言（票02 供应商接管，spec Implementation Decisions 1）：条目
// 供应商说的线协议。缺省 anthropic＝既有行为零变化。
const (
	// DialectAnthropic CC 说的 /v1/messages 线协议（既有车道行为）。
	DialectAnthropic = "anthropic"
	// DialectOpenAIResponses codex 说的 OpenAI /responses 线协议（原生透传，
	// 翻译车道在后续票落地；本票只入表可解析可校验）。
	DialectOpenAIResponses = "openai_responses"
)

// codex 可用性三态（CodexAvailability 的裁决值）。
const (
	// CodexUnsupported 显式否决：该条目不可作 codex 上游（switch 默认拒绝在
	// CLI 层（票06），端点只做存在性校验）。
	CodexUnsupported = "unsupported"
	// CodexTranslation anthropic 方言→需经渡口翻译车道。
	CodexTranslation = "translation"
	// CodexNative openai_responses 方言→原生透传（仅鉴权注入＋model 改写）。
	CodexNative = "native"
)

// pi 可用性三态（PiAvailability 的裁决值；票09）。pi 说 anthropic-messages
// 线协议、复用渡口 CC 车道（spec 决定 2/D4：不建 chat completions 入站），
// 故按 dialect 推导的方向与 codex 相反：anthropic＝可用、openai_responses＝不可用。
const (
	// PiUnsupported 显式否决：该条目不可作 pi 上游（仅此一个显式值；switch/
	// apply 的拒绝/放行行为面在票12，本票只做解析校验与展示）。
	PiUnsupported = "unsupported"
	// PiAvailable anthropic 方言→pi 可用（与 CC 同线协议）。
	PiAvailable = "available"
	// PiUnavailable openai_responses 方言→pi 无入站车道（chat completions
	// 入站不建）。
	PiUnavailable = "unavailable"
)

// DockUpstream 渡口上游条目（[dock.upstreams.<名>]）。字段：
// base_url（必填）/ api_key（可为空＝未激活预置，空 key 不影响解析，只在
// CLI/doctor 层提示）/ model_map（非本地条目必含非空 default，可选 opus/
// sonnet/haiku 档位键与 codex/pi 主模型键；本地条目属守卫透传域，可无）/
// text_only（可选，命中映射后模型名则 image 块降级文本占位）/ balance_url
// （可选，不配不显示——D11）/ dialect（可选，缺省 anthropic）/ codex（可选，
// 仅 "unsupported" 否决位）/ pi（可选，仅 "unsupported" 否决位）。
// text_only 语义沿用票06 的名单制（映射后目标模型名列表）。
type DockUpstream struct {
	BaseURL    string
	APIKey     string            // 真钥：只进出站 Authorization，永不入日志/账本/错误（T39）
	ModelMap   map[string]string // 别名→上游原生模型名；default 键＝未知名兜底；codex/pi 键＝对应车道主模型
	TextOnly   []string          // text-only 模型名单（按映射后模型名匹配）
	BalanceURL string            // 余额端点；空＝该条目不显示余额行
	// Dialect 线协议方言（票02）：解析层归一（缺省/空 → anthropic＝旧行为）；
	// 消费方（渡口车道分流/翻译车道）直接判等，不再各自兜缺省。
	Dialect string
	// Codex 显式否决位（票02）：仅认 "unsupported"（其余值 Validate 拒）；
	// 空＝按 dialect 推导（见 CodexAvailability）。
	Codex string
	// Pi pi 显式否决位（票09）：对标 Codex 同款——仅认 "unsupported"（其余
	// 值 Validate 拒）；空＝按 dialect 推导（见 PiAvailability，推导方向与
	// codex 相反：anthropic＝可用）。
	Pi string
}

// CodexAvailability codex 可用性裁决（票02，spec 决定 1）：显式否决位优先；
// 否则按 dialect 推导——anthropic→需翻译、openai_responses→原生透传。
// 零值结构体（旧形态）＝translation（与既有"渡口上游皆 Anthropic 线协议"
// 语义一致）。
func (u *DockUpstream) CodexAvailability() string {
	if u.Codex == CodexUnsupported {
		return CodexUnsupported
	}
	if u.Dialect == DialectOpenAIResponses {
		return CodexNative
	}
	return CodexTranslation
}

// CodexModel codex 主模型位（model_map 的 "codex" 键）；缺键＝空（不凭空
// 造默认——codex 车道改写规则属后续票，本票只供读）。
func (u *DockUpstream) CodexModel() string { return u.ModelMap["codex"] }

// PiAvailability pi 可用性裁决（票09，spec 决定 5）：对标 CodexAvailability
// 的"显式否决位+按 dialect 推导"，推导方向相反——anthropic→可用（pi 说
// anthropic-messages，复用渡口 CC 车道）、openai_responses→不可用（chat
// completions 入站不建）。零值结构体（旧形态）＝可用；无新键的既有条目仅
// 增展示面，行为零变化（switch/apply 行为在票12）。
func (u *DockUpstream) PiAvailability() string {
	if u.Pi == PiUnsupported {
		return PiUnsupported
	}
	if u.Dialect == DialectOpenAIResponses {
		return PiUnavailable
	}
	return PiAvailable
}

// PiModel pi 主模型位（model_map 的 "pi" 键）；缺键＝空（不凭空造默认——
// 写入器 models 列表派生与空值拒写属票12 行为面，本票只供读与展示）。
func (u *DockUpstream) PiModel() string { return u.ModelMap["pi"] }

// ActiveUpstream 渡口上游解析单源（serve 装配与 /stats 余额查询都走这里）：
//   - upstreams 表非空：返回 active 指向的条目（拷贝，改返回值不污染配置）；
//     active 空/悬空 → ("", nil)——表在时 Validate 拒启，nil 仅防御路径可见；
//   - 表空（未迁移/迁移失败回退/手写旧形态）：旧单值包装为匿名回退条目
//     （名 ""），保证旧配置零行为变化地继续跑。
//
// 新表与旧单值并存（F9）：len(Upstreams)>0 即以新表为准，旧单值不参与。
func (d *DockCfg) ActiveUpstream() (string, *DockUpstream) {
	if len(d.Upstreams) > 0 {
		if d.Active == "" {
			return "", nil
		}
		up, ok := d.Upstreams[d.Active]
		if !ok {
			return "", nil
		}
		return d.Active, &up // map 取值即拷贝
	}
	return "", &DockUpstream{
		BaseURL:    d.UpstreamBaseURL,
		APIKey:     d.APIKey,
		ModelMap:   d.ModelMap,
		TextOnly:   d.TextOnly,
		BalanceURL: d.BalanceURL,
		Dialect:    DialectAnthropic, // 旧单值形态＝既有 Anthropic 线协议行为（票02 缺省归一）
	}
}

// ---- 本地中转地址判定（自 dock/guard.go 迁入的单一事实源） ----
//
// guard.DoubleRewriteRisk 与本包校验（守卫透传域豁免）共用 IsLocalRelayAddr，
// 绝不出现两套判据（guard.go 原单源纪律的延续）。

// localRelayPorts 已知本地中转端口：15721=cc-switch（默认上游）、15722=渡口
// 自身、15723=验证转发器。上游指回这三者之一＝改写链路成环（cc-switch 已
// 改一遍、渡口再改一遍＝双重改写）。
var localRelayPorts = map[string]bool{"15721": true, "15722": true, "15723": true}

// isLoopbackHost 回环主机归一判定：localhost / ::1 / 127.0.0.0/8（含 IPv4
// 映射形 ::ffff:127.x）一律视为本机。
func isLoopbackHost(hostname string) bool {
	h := strings.ToLower(strings.TrimSpace(hostname))
	if h == "localhost" || h == "::1" || strings.HasPrefix(h, "127.") {
		return true
	}
	return strings.HasPrefix(h, "::ffff:127.")
}

// IsLocalRelayAddr 上游地址在回环主机＋已知本地中转端口上＝守卫透传域
// （防双重改写的强制透传域；model_map 的 default 要求对此域豁免）。
// 地址解析失败＝不判（构造期 New 另有合法性校验兜底，两处职责不同）。
func IsLocalRelayAddr(baseURL string) bool {
	u, err := url.Parse(baseURL)
	if err != nil || u.Host == "" {
		return false
	}
	return isLoopbackHost(u.Hostname()) && localRelayPorts[u.Port()]
}

// validateDockUpstreams 上游表校验（票01；Validate 与首启迁移共用——迁移
// 前先跑一遍，未来表不合法即拒迁，绝不写出会让下次启动拒启的配置）：
//   - active 必填且须指向存在的条目（表在时）；
//   - 条目 base_url 必填；
//   - 非本地 base_url 的条目 model_map 必含非空 default（缺失＝配置错误早
//     暴露）；本地地址条目（守卫透传域）豁免。
func validateDockUpstreams(d *DockCfg) []string {
	if len(d.Upstreams) == 0 {
		return nil
	}
	var probs []string
	if d.Active == "" {
		probs = append(probs, "[dock] 已配上游表但缺 active（单选键，须指向一条渡口上游）")
	} else if _, ok := d.Upstreams[d.Active]; !ok {
		probs = append(probs, "[dock].active 指向不存在的条目 "+d.Active+"（可用: "+
			strings.Join(sortedUpstreamNames(d), ", ")+"）")
	}
	for _, name := range sortedUpstreamNames(d) {
		up := d.Upstreams[name]
		if strings.TrimSpace(up.BaseURL) == "" {
			probs = append(probs, "[dock.upstreams."+name+"] 缺 base_url（必填）")
			continue
		}
		if !IsLocalRelayAddr(up.BaseURL) && up.ModelMap["default"] == "" {
			probs = append(probs, "[dock.upstreams."+name+"] 非本地 base_url 的条目 "+
				"model_map 必含非空 default（本地中转地址＝守卫透传域，可豁免）")
		}
		// 票02：dialect/codex 枚举校验（空＝缺省合法——零值结构体与旧配置同态）。
		if up.Dialect != "" && up.Dialect != DialectAnthropic && up.Dialect != DialectOpenAIResponses {
			probs = append(probs, "[dock.upstreams."+name+"] dialect 非法: "+
				up.Dialect+"（可选 anthropic（缺省）/openai_responses）")
		}
		if up.Codex != "" && up.Codex != CodexUnsupported {
			probs = append(probs, "[dock.upstreams."+name+"] codex 非法: "+
				up.Codex+"（仅可选 \"unsupported\" 否决位；可用性按 dialect 推导）")
		}
		// 票09：pi 否决位枚举校验（同 codex 纪律——仅认显式否决，推导值不落盘）。
		if up.Pi != "" && up.Pi != PiUnsupported {
			probs = append(probs, "[dock.upstreams."+name+"] pi 非法: "+
				up.Pi+"（仅可选 \"unsupported\" 否决位；可用性按 dialect 推导）")
		}
	}
	return probs
}

// sortedUpstreamNames 条目名排序（校验问题列表的确定性）。
func sortedUpstreamNames(d *DockCfg) []string {
	names := make([]string, 0, len(d.Upstreams))
	for k := range d.Upstreams {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}
