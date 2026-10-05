// dsh_material.go — 票02：dsh（DeepSeek Harness）摆渡材料分支。
//
// 材料源钉死（D4）：worker 侧取 dock.SnapshotStore.Main(sid) 的请求体字节
// （最大请求体＝最新主轮＝完整对话前缀，anthropic-messages 请求形：system+
// messages；心跳重放同源）传入本包——不是事件流、不把 zstd 会话文件当 CC
// jsonl 读、禁用 Last()（仅诊断，可能是标题类小请求）。本包只吃字节，不依赖
// dock（快照获取/缺料降级在 worker.doDsh）。
//
// 覆盖截止（F8）：coversAt＝入队时台账 lastWrite（item["covers_at"]，入队
// 闭包台账锁内快照），不在 worker 执行时读台账。facts.LastTS＝其 ISO 形——
// 与 FerrySession 的 covers_until_iso 同键位流转（SaveHandoff→CoversUntilS
// →ValidHandoff 对同时刻 coversBar 判覆盖）。
//
// CC/codex 零改动：本文件只新增 dsh 专用路径（DshFerrySession/DshMaterial），
// sessionMaterial 与 FerrySession 不动。
package ferry

import (
	"encoding/json"
	"strings"
	"time"

	"ferryman/internal/extract"
	"ferryman/internal/mathx"
)

// dsh 冻结档截断与尾标：extract 包 freezeFinalize 私有常量/文案的等值复刻
// （跨包取不到私有——dsh 骨架与 CC 骨架同形同截断）。
const (
	dshFreezeUserCap = 500
	dshFreezeAsstCap = 1500
	dshTruncMark     = "（已截断，全文见会话文件）"
)

// DshFerrySession 票02：dsh 会话摆渡（FerrySession 的 dsh 同构：材料→L1/L2
// →两层交接 MD；差异仅在材料来源与覆盖截止口径）。sid＝统一键（item 的
// session_id 直取，进 meta.source）；coversAt＝入队时台账 lastWrite（覆盖
// 截止，F8）。快照缺失不进本函数（worker.doDsh 提前骨架降级）；体坏/零消息
// → 骨架-only 材料照常走模型（fail-open，D7）。
func DshFerrySession(sid string, body []byte, coversAt float64, pr Provider,
	timeoutS float64) (string, map[string]any, error) {
	t0 := time.Now()
	facts, items := DshMaterial(sid, body, coversAt)
	skeleton := facts.SkeletonText()
	material := extract.MaterialText(facts, items)
	matTokens := extract.TokenEstimate(material)
	reply, mode, calls, err := sessionReply(pr, skeleton, material, items,
		matTokens, timeoutS, Chat)
	if err != nil {
		return "", nil, err
	}
	inject, full := ParseOutput(reply)
	meta := map[string]any{
		"source": sid, "title": facts.Title, "mode": mode,
		"model": pr.Model, "provider": pr.Name,
		"covers_until_iso": facts.LastTS, // ＝coversAt 的 ISO 形（入队口径，非提取时点）
		"mat_tokens_est":   matTokens, "chunks": len(calls),
		"wall_s":            round1(time.Since(t0).Seconds()),
		"usage":             sumUsage(calls),
		"call_walls":        callWallsOf(calls),
		"inject_tokens_est": extract.TokenEstimate(inject),
		"full_tokens_est":   extract.TokenEstimate(full),
	}
	return HandoffMarkdown(facts.Title, inject, full, meta), meta, nil
}

// DshMaterial dsh 材料构造：请求体（system+messages）→ 与 CC 提取器等价的
// (facts, items)。messages 逐条成 items（user/assistant 分角色；user 的
// tool_result 块整条丢弃、thinking/tool_use 不入正文——CC 提取器同语义，
// 防注入面同位）；system 不入材料（指令非对话，CC 转录同无 system）。
// facts.Title 恒空（台账现行标题在上游，HandoffMarkdown 回落 (无标题)）；
// facts.LastTS＝coversAt 的 ISO 形。体坏/空/缺 messages → 零 items
// （骨架-only），不报错（D7 fail-open）。
func DshMaterial(sid string, body []byte, coversAt float64) (extract.Facts, []extract.Item) {
	facts := extract.Facts{Source: sid, LastTS: dshCoversISO(coversAt),
		Files: []extract.FileCount{}, Commands: []string{}}
	items := []extract.Item{}
	var req struct {
		Messages []dshMessage `json:"messages"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return facts, items
	}
	lastAsstText := ""
	lastAsstTools := []string{}
	asstCount := 0
	for _, m := range req.Messages {
		switch m.Role {
		case "user":
			text, hasResult := dshContentText(m.Content)
			if hasResult { // 工具输出整条丢弃（CC 提取器同位）
				continue
			}
			if s := strings.TrimSpace(text); s != "" {
				items = append(items, extract.Item{Role: "user",
					Text: mathx.RuneTrunc(s, extract.ItemCharCap)})
			}
		case "assistant":
			asstCount++
			text, _ := dshContentText(m.Content)
			if s := strings.TrimSpace(text); s != "" {
				items = append(items, extract.Item{Role: "assistant",
					Text: mathx.RuneTrunc(s, extract.ItemCharCap)})
			}
			// 末段定格跟踪（每条 assistant 覆盖——CC 提取器同位）
			lastAsstText, lastAsstTools = text, dshToolNames(m.Content)
		}
	}
	// 末段定格定稿（extract.freezeFinalize 同语义：末条 user/assistant 文本 +
	// 末条助手工具名，超长截断加尾标）。
	facts.NTurns = asstCount
	for i := len(items) - 1; i >= 0; i-- {
		if items[i].Role == "user" {
			facts.FreezeUser = items[i].Text
			if mathx.RuneLen(facts.FreezeUser) > dshFreezeUserCap {
				facts.FreezeUser = mathx.RuneTrunc(facts.FreezeUser, dshFreezeUserCap) + dshTruncMark
			}
			break
		}
	}
	at := strings.TrimSpace(lastAsstText)
	if mathx.RuneLen(at) > dshFreezeAsstCap {
		facts.FreezeAsstText = mathx.RuneTrunc(at, dshFreezeAsstCap) + dshTruncMark
	} else {
		facts.FreezeAsstText = at
	}
	facts.FreezeTools = lastAsstTools
	return facts, items
}

// dshMessage anthropic-messages 单条消息（content 双形：string 或 blocks 数组，
// 解析两者都容——RawMessage 延迟分流）。
type dshMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

// dshContentText content 的文本抽取（extract.contentText 同语义）：string 直
// 取；blocks 取 type=text 的 text 块按序拼接（thinking/tool_use 不入）。出现
// tool_result 块 → hasToolResult=true（user 语义下调用方整条丢弃——工具输出
// 防注入面）。坏形 → 空串（防御纪律同 cctrans：坏行静默跳过）。
func dshContentText(raw json.RawMessage) (text string, hasToolResult bool) {
	if len(raw) == 0 {
		return "", false
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s, false
	}
	var blocks []map[string]any
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return "", false
	}
	var parts []string
	for _, b := range blocks {
		switch b["type"] {
		case "text":
			if t, ok := b["text"].(string); ok {
				parts = append(parts, t)
			}
		case "tool_result":
			hasToolResult = true
		}
	}
	return strings.Join(parts, "\n"), hasToolResult
}

// dshToolNames assistant content 的工具名序列（末段定格 FreezeTools 用；CC
// 提取器 lastAsstTools 同位——空文字纯工具轮的骨架定格靠它）。
func dshToolNames(raw json.RawMessage) []string {
	var blocks []map[string]any
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return []string{}
	}
	names := []string{}
	for _, b := range blocks {
		if b["type"] == "tool_use" {
			if n, ok := b["name"].(string); ok && n != "" {
				names = append(names, n)
			}
		}
	}
	return names
}

// dshCoversISO coversAt（入队时台账 lastWrite，unix 秒）→ ISO 串（UTC+Z 尾，
// store.parseISOUTC / cctrans.TSToEpoch 均可回读同值）。coversAt<=0 → ""
// （缺值如实缺，不伪造 1970 覆盖——ValidHandoff 新鲜窗自然判弃）。
func dshCoversISO(coversAt float64) string {
	if coversAt <= 0 {
		return ""
	}
	sec := int64(coversAt)
	nsec := int64((coversAt - float64(sec)) * 1e9)
	return time.Unix(sec, nsec).UTC().Format("2006-01-02T15:04:05.000000Z")
}
