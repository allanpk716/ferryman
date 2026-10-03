// Package dshtrans dsh（DeepSeek Harness）会话日志的防御式读取器
// （dsh phase 2 P2-1 第一块砖；与 cctrans/codextrans 同族）。
//
// 源码依据：deepseek-ai/deepseek-harness master@639ed015（0.2.0-rc.2，调研克隆
// 2026-10-02）。物理布局（packages/session/session-persistence-jsonl/src/format.ts
// + index.ts）：
//
//		~/.dsh/sessions/--<规范化cwd>--/<转义id>/session[.vN].jsonl[.zstd]
//
//	  - 文件＝「不可变格式代」：代号＝会话格式版本（v0 无名，v1 起带 .vN），
//	    读取取数值最高代（resolveGenerationInDirectory 同款规则，天然向前兼容：
//	    未来 v5 出现即被选中）；v3→v4 迁移会另发新代文件、旧代原地保留；
//	  - 当前代文件追加式：每批事件＝一个独立 zstd 帧（checksummed、帧拼接），
//	    或 compression:'none' 时明文 JSONL 行；物质化时首帧＝仅头行；
//	  - 行形状：头行 {type:"session",version,id,createdAt,cwd?,parentSession?,
//	    isSeeded,origin?,delegationDepth,agentPreset?}；事件行
//	    {type,seq,time(毫秒),data,surfaceOp?,ignorable?}。
//
// 台账四列（调研§3.1 定案，与 Ferryman 四列同构直接映射）：assistant/message
// 事件 data.usage＝TokenUsage{inputTokens(仅未缓存),outputTokens,
// cacheReadTokens?,cacheWriteTokens?}——**不相交口径，计费输入＝三者之和**
// （packages/llm/llm/src/types.ts:170-189）。
//
// 子代理族系（第四块砖）：子代理＝同项目目录下的独立会话目录，自头
// origin:"subagent"+parentSession+delegationDepth；父头里有 subagent/catalog
// 事件引用子女（childId/childCreatedAt/mode/label，
// packages/subagent/subagent/src/catalog.ts）。
//
// 防御纪律（cctrans/codextrans 同源）：坏行/缺字段/坏帧/读失败一律静默跳过或
// 收窄返回，绝不向调用方抛错；只解析元数据与计数，永不携带消息内容（隐私
// 不变量——本包输出的行只含 token 数、模型名、标题、seq）。
package dshtrans

import (
	"strings"

	"ferryman/internal/cctrans"
	"ferryman/internal/jsonl"
)

// UsageRow 一条 assistant/message 用量出行（四列不相交口径）。
type UsageRow struct {
	TS    float64 // 事件行 time（毫秒）→ epoch 秒；HasTS=false=行内无 time
	HasTS bool
	Seq   int64  // 包络 seq——会话内（单代文件内）单调唯一，CC message.id 的同位去重键
	Model string // data.message.source.replayState.response.responseModel，缺则 source.model

	InputTokens      int // inputTokens：仅未缓存输入（≠ OpenAI 含缓存口径）
	CacheReadTokens  int // cacheReadTokens?：缓存读（缺省 0）
	CacheWriteTokens int // cacheWriteTokens?：缓存写（缺省 0；账面 cache_creation_tokens）
	OutputTokens     int // outputTokens
}

// BilledInput 计费输入＝三输入列之和（不相交口径的记账语义）。
func (r UsageRow) BilledInput() int {
	return r.InputTokens + r.CacheReadTokens + r.CacheWriteTokens
}

// Child 父头 subagent/catalog 事件里的一条子女引用（族系数据源，P2-5 判活
// 的父侧索引；子侧权威在子会话自头 origin/parentSession——archive-admission
// 同款判据）。
type Child struct {
	ChildID        string
	ChildCreatedAt int64  // 毫秒
	Mode           string // one-shot | continuable | unknown（v1 起）
	Label          string // 可缺省
}

// Header 会话头行（首帧/首行的 type:"session" 记录）。v0 旧头缺 isSeeded/
// parentSession 等可选键——全部按缺省收窄，不拒读。
type Header struct {
	Version         int64
	ID              string
	CreatedAt       int64  // 毫秒
	Cwd             string // 可缺省（无 cwd 会话落 _no-cwd 项目目录）
	ParentSession   string // 子代理会话：父会话 id；主会话空
	Origin          string // "subagent" 或空
	DelegationDepth int64
	IsSeeded        bool
}

// ParseHeaderLine 头行 JSON → Header；非 dict/type 不符/缺必填键 → false。
// 版本号不设上限（forward compat：更高版本的头按形状尽力读，事件行解析
// 本就不依赖版本号）。
func ParseHeaderLine(line string) (Header, bool) {
	rec, ok := jsonl.DecodeDict(line)
	if !ok {
		return Header{}, false
	}
	if typ, _ := rec["type"].(string); typ != "session" {
		return Header{}, false
	}
	id, ok := rec["id"].(string)
	if !ok || id == "" {
		return Header{}, false
	}
	h := Header{ID: id}
	if v, ok := cctrans.ToInt(rec["version"]); ok {
		h.Version = int64(v)
	}
	if v, ok := cctrans.ToInt(rec["createdAt"]); ok { // 安全整数毫秒（isHeaderLine 同域）
		h.CreatedAt = int64(v)
	}
	if v, ok := cctrans.ToInt(rec["delegationDepth"]); ok {
		h.DelegationDepth = int64(v)
	}
	h.Cwd, _ = rec["cwd"].(string)
	h.ParentSession, _ = rec["parentSession"].(string)
	h.Origin, _ = rec["origin"].(string)
	h.IsSeeded, _ = rec["isSeeded"].(bool)
	return h, true
}

// ParseChunk 解析一段完整 JSONL 事件行（TailText 产出的新增明文，行含行尾
// \n）→（用量出行, 携带后的标题, 本段新见的子女引用）。
//
// 只认三类记录：session/title（更新标题）、assistant/message 且 data.usage
// 非空（出行）、subagent/catalog（子女引用）。其余事件类型（含 ignorable
// 未知类型）一律跳过；坏行跳过不抛（codextrans 同纪律）。出行门槛与 CC
// harvest 对齐：usage 四键缺必填（input/output 取不动）整行跳过，可选缓存
// 两键缺省 0；seq 取不动（无法去重）同样跳过。
func ParseChunk(text, title string) (rows []UsageRow, newTitle string, children []Child) {
	newTitle = title
	for _, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		rec, ok := jsonl.DecodeDict(line)
		if !ok {
			continue
		}
		typ, _ := rec["type"].(string)
		switch typ {
		case "session/title":
			if data, ok := rec["data"].(map[string]any); ok {
				if t, ok := data["title"].(string); ok && t != "" {
					newTitle = t
				}
			}
		case "assistant/message":
			if row, ok := usageRow(rec); ok {
				rows = append(rows, row)
			}
		case "subagent/catalog":
			if c, ok := catalogChild(rec); ok {
				children = append(children, c)
			}
		}
	}
	return rows, newTitle, children
}

// usageRow 一条 assistant/message 事件行 → UsageRow。
func usageRow(rec map[string]any) (UsageRow, bool) {
	data, ok := rec["data"].(map[string]any)
	if !ok {
		return UsageRow{}, false
	}
	usage, ok := data["usage"].(map[string]any)
	if !ok || len(usage) == 0 {
		return UsageRow{}, false
	}
	inputTokens, ok1 := cctrans.ToInt(usage["inputTokens"])
	outputTokens, ok2 := cctrans.ToInt(usage["outputTokens"])
	if !ok1 || !ok2 { // 必填两列取不动：整行跳过，不落数字错误的账
		return UsageRow{}, false
	}
	cacheRead, cacheWrite := 0, 0
	if v, ok := cctrans.ToInt(usage["cacheReadTokens"]); ok { // 可选键：缺省 0
		cacheRead = v
	}
	if v, ok := cctrans.ToInt(usage["cacheWriteTokens"]); ok {
		cacheWrite = v
	}
	seq, ok := cctrans.ToInt(rec["seq"]) // 包络 seq：去重键，取不动不收行
	if !ok {
		return UsageRow{}, false
	}
	row := UsageRow{
		Seq:              int64(seq),
		InputTokens:      inputTokens,
		CacheReadTokens:  cacheRead,
		CacheWriteTokens: cacheWrite,
		OutputTokens:     outputTokens,
	}
	if ms, ok := cctrans.ToInt(rec["time"]); ok { // 毫秒 → 秒
		row.TS = float64(ms) / 1000.0
		row.HasTS = true
	}
	row.Model = messageModel(data)
	return row, true
}

// messageModel 模型名取值：source.replayState.response.responseModel（真实
// 上游模型——渡口改写后的实际执行者，与 dock 科目 model_out 同口径）优先，
// 缺则 source.model（请求路由名）。两处都无 → 空串。
func messageModel(data map[string]any) string {
	msg, _ := data["message"].(map[string]any)
	if msg == nil {
		return ""
	}
	src, _ := msg["source"].(map[string]any)
	if src == nil {
		return ""
	}
	if rs, ok := src["replayState"].(map[string]any); ok {
		if resp, ok := rs["response"].(map[string]any); ok {
			if m, ok := resp["responseModel"].(string); ok && m != "" {
				return m
			}
		}
	}
	m, _ := src["model"].(string)
	return m
}

// catalogChild 一条 subagent/catalog 事件行 → Child。
func catalogChild(rec map[string]any) (Child, bool) {
	data, ok := rec["data"].(map[string]any)
	if !ok {
		return Child{}, false
	}
	id, ok := data["childId"].(string)
	if !ok || id == "" {
		return Child{}, false
	}
	c := Child{ChildID: id}
	if v, ok := cctrans.ToInt(data["childCreatedAt"]); ok {
		c.ChildCreatedAt = int64(v)
	}
	c.Mode, _ = data["mode"].(string)
	c.Label, _ = data["label"].(string)
	return c, true
}
