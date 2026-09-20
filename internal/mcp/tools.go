package mcp

// tools.go — 票04：工具注册表＋五件只读工具（sessions、session_detail、
// gate_check、cost_report、heartbeat_status——第六件 doctor 在票05，进程内
// 复用检查函数不经 HTTP，不注册转发表）。
//
// D12 命名纪律：名称/参数英文；描述中文并直接引用 CONTEXT.md 词条原文
// （台账、凉会话、拦截阈值、有效交接、成效账、等待窗口、等答复窗口（问询窗）、
// 心跳），不造第二套翻译。
//
// 红线（规格「红线」节）：工具面不存在任何写操作工具——五件全部转发 daemon
// 只读 GET 端点，参数透传（limit/过滤/scope/key），响应 JSON 原样透传；响应
// 不含消息内容/凭据/token（daemon 侧已守，本层只透传不改写）。

import (
	"fmt"
	"math"
	"net/url"
	"strconv"
)

// ParamSpec 工具参数规格：kind 限定 JSON 类型（string|integer）；required 缺失
// 即错；query 为 daemon 端点 query 参数名（缺省与工具参数同名——/session 的
// 端点参数名是 id，工具参数名保持 session_id，经此映射透传）。
type ParamSpec struct {
	Name     string
	Kind     string // "string" | "integer"
	Required bool
	Query    string // 空＝与 Name 同名
}

// Tool 工具表项。
type Tool struct {
	Name        string
	Description string
	InputSchema map[string]any
	// Endpoint 转发的 daemon 只读 GET 端点路径。
	Endpoint string
	// Params 参数透传规格（空＝无参工具）。
	Params []ParamSpec
}

// buildQuery 工具参数 → daemon query（透传归一化）：未知参数拒绝（typo 防护，
// schema additionalProperties=false 的服务端镜像）；类型/必填校验；limit 类
// 整数须为 ≥1 的整值。空串视同缺省（daemon qsOr 同语义）。
func (t Tool) buildQuery(args map[string]any) (url.Values, error) {
	q := url.Values{}
	known := map[string]bool{}
	for _, p := range t.Params {
		known[p.Name] = true
	}
	for k := range args {
		if !known[k] {
			return nil, fmt.Errorf("未知参数 %q（本工具参数：%v）", k, t.paramNames())
		}
	}
	for _, p := range t.Params {
		v, ok := args[p.Name]
		if !ok || v == nil {
			if p.Required {
				return nil, fmt.Errorf("缺少必需参数 %q", p.Name)
			}
			continue
		}
		qn := p.Query
		if qn == "" {
			qn = p.Name
		}
		switch p.Kind {
		case "string":
			s, ok := v.(string)
			if !ok {
				return nil, fmt.Errorf("参数 %q 必须为字符串", p.Name)
			}
			if s == "" {
				continue // 空串=缺省
			}
			q.Set(qn, s)
		case "integer":
			f, ok := v.(float64) // JSON number 解码形
			if !ok || f != math.Trunc(f) || f < 1 {
				return nil, fmt.Errorf("参数 %q 必须为正整数", p.Name)
			}
			q.Set(qn, strconv.FormatInt(int64(f), 10))
		default:
			return nil, fmt.Errorf("内部错误：参数 %q 未知类型 %q", p.Name, p.Kind)
		}
	}
	return q, nil
}

func (t Tool) paramNames() []string {
	out := make([]string, 0, len(t.Params))
	for _, p := range t.Params {
		out = append(out, p.Name)
	}
	return out
}

// objSchema JSON Schema 装配小件。
func objSchema(props map[string]any, required ...string) map[string]any {
	m := map[string]any{"type": "object", "properties": props,
		"additionalProperties": false}
	if len(required) > 0 {
		m["required"] = required
	}
	return m
}

func strProp(desc string) map[string]any {
	return map[string]any{"type": "string", "description": desc}
}

func intProp(desc string) map[string]any {
	return map[string]any{"type": "integer", "minimum": 1, "description": desc}
}

// ferrymanTools 五件只读工具注册表（doctor 在票05）。
func ferrymanTools() []Tool {
	return []Tool{
		{
			Name:     "sessions",
			Endpoint: "/sessions",
			Description: `列出全部会话的台账清单（闲置状态与凉热判定，按最后写入倒序，默认 50 条）。

台账（ledger）：session_id 到最后活动时间、上下文大小、标题、项目路径、Agent 类型的登记表；闲置判定的唯一事实源，判定时不解析 jsonl。

凉会话（stale session）：闲置时长超过拦截阈值的会话；缓存大概率已失效，继续使用即全量重付。

只读。可选：agent（按 Agent 精确过滤，cc|codex）、cwd（项目路径前缀过滤）、limit（返回条数上限）。`,
			InputSchema: objSchema(map[string]any{
				"agent": strProp("按 Agent 精确过滤（cc|codex）"),
				"cwd":   strProp("项目路径前缀过滤"),
				"limit": intProp("返回条数上限（默认 50）"),
			}),
			Params: []ParamSpec{
				{Name: "agent", Kind: "string"},
				{Name: "cwd", Kind: "string"},
				{Name: "limit", Kind: "integer"},
			},
		},
		{
			Name:     "session_detail",
			Endpoint: "/session",
			Description: `查询单个会话：台账条目＋四列会话总账（主转录＋各子代理转录加总）＋最近一次有效交接覆盖（covers_until、fresh/skeleton）＋窗口状态（等待窗口/问询窗，含停车标志）。

有效交接：与被拦会话同 Agent 且同项目目录、状态为 fresh 或骨架、覆盖截止不早于台账最后写入的交接；闸门 block 的唯一依据。

只读。必需：session_id。`,
			InputSchema: objSchema(map[string]any{
				"session_id": strProp("会话 ID"),
			}, "session_id"),
			Params: []ParamSpec{
				// /session 端点 query 参数名是 id（工具参数名保持 session_id）。
				{Name: "session_id", Kind: "string", Required: true, Query: "id"},
			},
		},
		{
			Name:     "gate_check",
			Endpoint: "/gate_check",
			Description: `闸门判定的只读推演（不改任何状态）。带 session_id＝单会话判定：此刻 allow/warn/block、离拦截阈值剩余分钟、判定依据（有效交接路径或缺失原因）；不带 session_id＝全台账汇总：逐会话一行，按预计拦截时刻升序。

拦截阈值：判定会话变凉、闸门开始拒绝的闲置时长；默认 35 分钟（E0a 实测拐点 +5min 余量），按 Agent/服务商可配。

只读。可选：session_id（缺省＝汇总模式）、limit（汇总模式返回条数上限）。`,
			InputSchema: objSchema(map[string]any{
				"session_id": strProp("单会话判定；缺省为全台账汇总模式"),
				"limit":      intProp("汇总模式返回条数上限（默认 50）"),
			}),
			Params: []ParamSpec{
				{Name: "session_id", Kind: "string"},
				{Name: "limit", Kind: "integer"},
			},
		},
		{
			Name:     "cost_report",
			Endpoint: "/report",
			Description: `账本报表：按项目/会话/月汇总四列 token（input/output/缓存写/缓存读）、成效账（节省额；价格表缺缓存价时如实标注"节省额不可算"）、bypass 与无效保温单列。

成效账（savings statement）：report 层按版本化反事实公式算出的节省额；只有真实兑现的避免才计节省，bypass 绕过与无效保温单列。

只读。必需：scope（project|session|month）＋key（项目路径 / session_id / YYYY-MM）。`,
			InputSchema: objSchema(map[string]any{
				"scope": map[string]any{"type": "string",
					"enum":        []string{"project", "session", "month"},
					"description": "汇总维度"},
				"key": strProp("维度键：项目路径 / session_id / YYYY-MM"),
			}, "scope", "key"),
			Params: []ParamSpec{
				{Name: "scope", Kind: "string", Required: true},
				{Name: "key", Kind: "string", Required: true},
			},
		},
		{
			Name:     "heartbeat_status",
			Endpoint: "/beats",
			Description: `在飞等待窗口/问询窗清单（含停车状态、预估收尾原因）＋逐窗心跳遥测（跳数、hit/miss、TTL 观测、累计实收花费）。

心跳（heartbeat）：等待窗口内对主会话缓存前缀的定期体外重放；事件驱动、opt-in、默认关闭，会话恢复即停。

等待窗口（wait window）：主会话因等待在飞子代理而闲置的起止区间——起于"主会话闲置且存在在飞子代理"，止于**最后一个子代理完成**（主会话自动续跑，无需人在场）、主会话提前恢复写入，或异步等待的停车收尾（见下）；心跳触发、节律与策略对比的分析单元。

等答复窗口（answer window，问询窗）：问询守望命中后挂在会话上的窗口态：窗口内摆渡推迟但带硬死线（拦截阈值前 8 分钟强制入队，失败走既有骨架降级），闸门语义不动；随任何新写入立即关闭。与等待窗口（机器等机器）互为兄弟结构、两窗互斥、先开者赢。

只读。无参数。`,
			InputSchema: objSchema(map[string]any{}),
		},
	}
}
