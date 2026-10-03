package daemon

// dsh_receive.go — dsh phase2 票04：daemon 接收面三口（spec「daemon 接收面」
// 节，D5 同构复用）：POST /dsh/gate（闸门问询）、POST /dsh/event（事件接收）、
// POST /dsh/handoff（交接查询）。与 CC 面同构、不另起炉灶：
//
//   - /dsh/gate：body 钉 agent="dsh" 后整体交 Gate——闸门判定入口唯一（台账/
//     阈值/观察窗/observe-enforce 状态机零复制）；非 cc agent 的模式开关走
//     gate.codex_mode（既有语义，不另设 dsh 开关）；决策回显与 CC /gate 同形
//     （decision allow|block＋reason＋additional_context…），插件把 block
//     映射为 deny（桥同款），reason 即用户可见理由。
//   - /dsh/event：session/event 形状（turn/start、assistant/message 带 usage、
//     compaction/*）。已知事件 Touch("dsh") 活动登记；assistant/message 带
//     usage 时 usage 科目四列入账——字段与 P2-1 pollDsh 逐字段同构（白名单
//     零新键，测试与守望现行行做 keyset diff 防漂移）。子会话直报
//     （parent_session_id 非空）随父入账、不 Touch（守望同款分流，族系判活
//     信号待 P2-5）。无文件事件没有代文件偏移/谱系：offset 恒 0、lineage_id
//     取 payload（缺省空串——守望断点恢复只认非空 lineage，事件行不入断点
//     表，与守望采集互不干扰）。Touch 路径取 payload path，缺省合成
//     dsh-event://<键>：按会话唯一、与真转录路径不撞（台账 byPath 键不得为
//     空——空键会让不同会话经 byPath[""] 互相继承闲置史）；守望随后用真
//     代文件路径 Touch 同一会话时按 (agent,sid) 收敛到同一条状态。
//   - /dsh/handoff：agent 钉 "dsh" 后交 Restore 整体复用——锚定归还/多候选
//     清单/INJECT 提取/记账/MarkInjected 全在既有归还语义内（归还播种与
//     CC /restore 同一套；agent 钉死＝同 Agent＋cwd 语义，cc 交接不串线）。
//
// 坏形防御（dshtrans 同族纪律：坏输入静默收窄不炸）：空 session_id/未知事件
// 类型/usage 非对象一律 200＋skipped 标记，不 5xx；坏 JSON 走 badRequest 400
//（与 CC 面 /gate 同形）。usage 记账失败打印吞掉（harvestDshUsage 同纪律：
// 记账永不弄断接收面）。
//
// 注册惯例：与 /shutdown、/provider_switch 同族管理端点——makeHandler 方法
// 分派前单点拦截，守门序 loopback → 方法 POST → 先读光 body（RST 纪律）→
// Bearer → 业务。DaemonLike 非 *Daemon（测试替身）不拦截，落既有 doPost
// 未知路径 404。

import (
	"fmt"
	"io"
	"net/http"
	"strings"

	"ferryman/internal/accounts"
	"ferryman/internal/clock"
)

// isDshReceivePath 三口路径判定（makeHandler 拦截与 doDshReceive 分派共用）。
// RequestURI 精确匹配——带 query 视为未知路径（doPost 同规）。
func isDshReceivePath(uri string) bool {
	switch uri {
	case "/dsh/gate", "/dsh/event", "/dsh/handoff":
		return true
	}
	return false
}

// doDshReceive dsh 接收面拦截器：命中三口则回话并返回 true；未命中返回
// false 由调用方落既有分派。
func doDshReceive(d *Daemon, token string, w http.ResponseWriter, r *http.Request) bool {
	if !isDshReceivePath(r.RequestURI) {
		return false
	}
	if !isLoopback(r) {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "forbidden: loopback only"})
		return true
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method not allowed"})
		return true
	}
	bodyRaw, _ := io.ReadAll(r.Body) // 先读光 body 再回话（RST 纪律，同 doPost）
	if !isAuthed(r, token) {
		unauthorized(w)
		return true
	}
	body, err := decodeJSONObject(bodyRaw)
	if err != nil {
		badRequest(w, err)
		return true
	}
	switch r.RequestURI {
	case "/dsh/gate":
		writeJSON(w, http.StatusOK, d.DshGate(body))
	case "/dsh/event":
		writeJSON(w, http.StatusOK, d.DshEvent(body))
	case "/dsh/handoff":
		writeJSON(w, http.StatusOK, d.DshHandoff(pyStr(body["cwd"]), pyStr(body["session_id"])))
	}
	return true
}

// DshGate 闸门问询业务口（端点与测试共用）：agent 钉 "dsh" 后交 Gate——
// 判定入口唯一（验收：不复制阈值逻辑，测试引用同一判定入口）。
func (d *Daemon) DshGate(body map[string]any) map[string]any {
	body["agent"] = "dsh"
	return d.Gate(body)
}

// DshEvent 事件接收业务口：Touch("dsh") 登记＋usage 四列入账（pollDsh 同构）。
// 正常回 {"ok":true}；坏形静默收窄为 {"ok":true,"skipped":"<因>"}。
func (d *Daemon) DshEvent(body map[string]any) map[string]any {
	sid := pyStr(body["session_id"])
	if sid == "" {
		return map[string]any{"ok": true, "skipped": "empty-session-id"}
	}
	ev := pyStr(body["event"])
	if ev != "turn/start" && ev != "assistant/message" && !strings.HasPrefix(ev, "compaction/") {
		return map[string]any{"ok": true, "skipped": "unknown-event"}
	}
	// 跨源去重（票05,策略 a 事件接管单源化）：已知事件＝该会话有插件事件流量
	// ——标记接管,pollDsh 文件守望面让位（watcher_dsh.go dshIsFed 同表）。
	// turn/start 即标记：接管在带 usage 的事件出现前先落（毫秒级竞窗见
	// dsh_dedup.go 头注）。子会话直报标记子键（父键由父自身的事件标记,
	// 不从子事件推断）。
	d.dshFed.mark(sid)
	// 事件时间：native 毫秒 epoch；缺/坏 → 记账盖章 now（ts=-1 同义）、
	// 活动钟取 now。
	ms := dshMsOr(body["time"])
	ts := -1.0
	mt := clock.Now()
	if ms > 0 {
		ts = ms / 1000
		mt = ts
	}
	cwd, title := pyStr(body["cwd"]), pyStr(body["title"])
	parent := pyStr(body["parent_session_id"])
	u, hasUsage := body["usage"].(map[string]any)
	if ev != "assistant/message" { // usage 只认 assistant/message（pollDsh 同源）
		u, hasUsage = nil, false
	}
	var inTok, cacheRead, cacheWrite, outTok, billed int
	if hasUsage {
		inTok = dshIntOr(u, "input_tokens")
		cacheRead = dshIntOr(u, "cache_read_tokens")
		cacheWrite = dshIntOr(u, "cache_creation_tokens")
		outTok = dshIntOr(u, "output_tokens")
		billed = inTok + cacheRead + cacheWrite // 计费输入＝三输入列之和
	}
	// Touch 活动登记：主会话事件；子会话直报不 Touch（守望同款分流——子会话
	// 不参与闲置判定）。
	if parent == "" {
		path := pyStr(body["path"])
		if path == "" {
			path = "dsh-event://" + sid
		}
		st := d.Ledger.TouchFull("dsh", sid, path, mt, 0, cwd, title, 0, d.StartedAt)
		if hasUsage { // peak 回写（pollDsh harvestDshUsage 同款，锁内只内存操作）
			d.Ledger.Mu().Lock()
			if billed > st.PeakCtx {
				st.PeakCtx = billed
			}
			d.Ledger.Mu().Unlock()
		}
	}
	// usage 四列入账：字段与 pollDsh 逐字段同构（白名单零新键）；子会话直报
	// 随父入账（session_id=父键、subagent=子键）。payload 缺 title 回落台账
	// 现行标题（pollDsh 行带会话现行标题的同构——turn/start 已登记）。
	if hasUsage && d.Accounts != nil {
		acctSid, sub := sid, ""
		if parent != "" {
			acctSid, sub = parent, sid
		}
		if title == "" {
			if stT := d.Ledger.Get("dsh", acctSid); stT != nil {
				d.Ledger.Mu().Lock()
				title = stT.Title
				d.Ledger.Mu().Unlock()
			}
		}
		if _, err := d.Accounts.Record("usage", ts, accounts.Fields{
			"agent":                 "dsh",
			"session_id":            acctSid,
			"lineage_id":            pyStr(body["lineage_id"]),
			"project":               cwd,
			"model":                 pyStr(body["model"]),
			"title":                 title,
			"input_tokens":          inTok,
			"cache_read_tokens":     cacheRead,
			"cache_creation_tokens": cacheWrite,
			"output_tokens":         outTok,
			"offset":                0,
			"subagent":              sub,
		}); err != nil {
			fmt.Printf("[dsh-event] usage 记账失败（忽略继续）: %v\n", err)
		}
	}
	return map[string]any{"ok": true}
}

// DshHandoff 交接查询业务口：agent 钉 "dsh" 交 Restore 整体复用（归还播种＝
// CC /restore 同一套：锚定/清单/INJECT 提取/记账/MarkInjected）。
func (d *Daemon) DshHandoff(cwd, sessionID string) map[string]any {
	return d.Restore("dsh", cwd, sessionID)
}

// dshMsOr 事件毫秒时间戳的宽松收形（缺/坏/非正 → 0＝无时间）。数值口径
// 与 dshLedgerOffset 同宽：JSON 解码是 float64，进程内直调可能是 int。
func dshMsOr(v any) float64 {
	switch x := v.(type) {
	case float64:
		if x > 0 {
			return x
		}
	case int:
		if x > 0 {
			return float64(x)
		}
	case int64:
		if x > 0 {
			return float64(x)
		}
	}
	return 0
}

// dshIntOr usage 数值列的宽松收形（HTTP 面一律 float64；进程内直调可能是
// int/int64；缺/坏 → 0）。
func dshIntOr(u map[string]any, k string) int {
	switch x := u[k].(type) {
	case float64:
		return int(x)
	case int:
		return x
	case int64:
		return int(x)
	}
	return 0
}
