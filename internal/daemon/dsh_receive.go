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
//     compaction/*；判活收编（票 A，dsh-heartbeat 规格「判活」节）：
//     agent/status（running|idle 闭集）与 agent/disposed——维护台账运行/终结
//     态、不入账；status 闭集外按 unknown-event 收窄）。闲置钟锚
//     （2026-10-05 漏拦案）：Touch 只认机器产出事件（assistant/message、
//     compaction）与新会话首登记；turn/start 与 status=running 是本输入自己
//     的信号，不顶新已登记会话的闲置钟（否则回流判定时刻 idle 恒≈0，凉会话
//     永进不了拦窗）；idle/disposed 恒不 Touch；assistant/message 带 usage 时 usage 科目四列
//     入账——字段与 P2-1 pollDsh 逐字段同构（白名单零新键，测试与守望现行行
//     做 keyset diff 防漂移）。子会话直报
//     （parent_session_id 非空）随父入账、不 Touch（守望同款分流）；其运行态
//     记子键（族系判定＝父 OR 任一已知子键，票 A）。无文件事件没有代文件偏移/
//     谱系：offset 恒 0、lineage_id
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
// 判定入口唯一（验收：不复制阈值逻辑，测试引用同一判定入口）；先惰性回种
// 族系子键（判活地基票 A，见 dshEnsureChildrenSeeded）。
func (d *Daemon) DshGate(body map[string]any) map[string]any {
	body["agent"] = "dsh"
	d.dshEnsureChildrenSeeded()
	return d.Gate(body)
}

// dshEnsureChildrenSeeded 族系子键账本回种（判活地基票 A，规格「判活」节子键
// 来源②）：dsh usage 行 subagent 列非空者＝子会话直报随父入账的行（本文件
// DshEvent 记账同列），session_id=父键、subagent=子键——daemon 重启后族系映射
// 的持久恢复（dshFed.seedFromAccounts 同款的一次全量读，成本已知可收）。装配
// 位置如实声明：NewDaemon 在 daemon.go（本票涉及路径外不可改），启动钩子不可
// 达——回种挂 DshGate 首问惰性触发，DshChildrenSeedClaim test-and-set 防重
//（并发首问只有一个获执行权）；live 直报的登记（DshChildSeen）不经此路、
// 事件到达即记。锁外读账本，锁内只内存写。
func (d *Daemon) dshEnsureChildrenSeeded() {
	if d.Ledger == nil || !d.Ledger.DshChildrenSeedClaim() {
		return
	}
	if d.Accounts == nil {
		return
	}
	for _, e := range d.Accounts.Read(accounts.ReadOpts{Kind: "usage"}) {
		if ag, _ := e["agent"].(string); ag != "dsh" {
			continue
		}
		sub, _ := e["subagent"].(string)
		sid, _ := e["session_id"].(string)
		if sid != "" && sub != "" {
			d.Ledger.DshChildSeen(sid, sub)
		}
	}
}

// DshEvent 事件接收业务口：Touch("dsh") 登记＋usage 四列入账（pollDsh 同构）；
// 判活收编（票 A）：agent/status・agent/disposed 维护台账运行/终结态（不入账）。
// 正常回 {"ok":true}；坏形静默收窄为 {"ok":true,"skipped":"<因>"}。
func (d *Daemon) DshEvent(body map[string]any) map[string]any {
	sid := pyStr(body["session_id"])
	if sid == "" {
		return map[string]any{"ok": true, "skipped": "empty-session-id"}
	}
	ev := pyStr(body["event"])
	isStatus, isDisposed := ev == "agent/status", ev == "agent/disposed"
	if ev != "turn/start" && ev != "assistant/message" && !strings.HasPrefix(ev, "compaction/") &&
		!isStatus && !isDisposed {
		return map[string]any{"ok": true, "skipped": "unknown-event"}
	}
	// agent/status：status 闭集收形（data.status 优先、顶层 status 兜底——插件
	// 转发层 events.ts 把 payload 平铺，两处都取）；闭集外（含缺/非字符串）按
	// unknown-event 收窄（200＋skipped，不 5xx）。
	status := ""
	if isStatus {
		status = dshStatusOf(body)
		if status != "running" && status != "idle" {
			return map[string]any{"ok": true, "skipped": "unknown-event"}
		}
	}
	// 跨源去重（票05,策略 a 事件接管单源化）：已知事件＝该会话有插件事件流量
	// ——标记接管,pollDsh 文件守望面让位（watcher_dsh.go dshIsFed 同表）。
	// turn/start 即标记：接管在带 usage 的事件出现前先落（毫秒级竞窗见
	// dsh_dedup.go 头注）。子会话直报标记子键（父键由父自身的事件标记,
	// 不从子事件推断）。agent/status|disposed 同为插件事件流量——接管口径
	// 不变（插件在位＝事件全量到）。
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

	// ---- 判活地基（票 A）：运行/终结态维护——不入账、不动 usage ----
	if parent != "" {
		// 子直报本身即族系证据（子键来源①；来源②＝账本 usage 行 subagent 列
		// 回种，见 DshGate→dshEnsureChildrenSeeded）。
		d.Ledger.DshChildSeen(parent, sid)
	}
	if isStatus {
		running := status == "running"
		if parent == "" {
			if running {
				// 主会话 running：Touch 活动登记（同其余活动事件路径——path
				// 缺省合成 dsh-event://<键>，头注同规）。2026-10-05 漏拦案：
				// 已登记会话只在**台账缺席**时 Touch——status=running 与
				// turn/start 同为本输入触发的信号（用户发消息的瞬间 runtime
				// 即发 running），照旧顶新闲置钟＝回流判定时刻 idle 恒≈0、
				// 凉会话永进不了拦窗；闲置锚只认机器产出（assistant/message/
				// compaction 的 Touch）。运行态本就由 DshMainRunSet 置位、
				// 豁免受 DshRunGraceS 宽限，不依赖这次 Touch。
				if d.Ledger.Get("dsh", sid) == nil {
					path := pyStr(body["path"])
					if path == "" {
						path = "dsh-event://" + sid
					}
					d.Ledger.TouchFull("dsh", sid, path, mt, 0, cwd, title, 0, d.StartedAt)
				}
			}
			// idle 不 Touch（活动性由同轮更早的活动事件登记；此处只复位——
			// 规格「判活」：idle → 清运行态）。
			d.Ledger.DshMainRunSet(sid, running, mt)
		} else {
			// 子会话：运行态记子键（不 Touch——父不因子事件登记，头注同规）。
			d.Ledger.DshChildRunSet(sid, running, mt)
		}
		return map[string]any{"ok": true}
	}
	if isDisposed {
		// 不 Touch（规格：disposed 置终结态）；终结即不在跑——运行态一并清
		//（否则 disposed 会话仍吃豁免直至 3600s 上界）。
		if parent == "" {
			d.Ledger.DshMainDisposed(sid, mt)
		} else {
			d.Ledger.DshChildDisposed(sid, mt)
		}
		return map[string]any{"ok": true}
	}
	// 运行态刷新（规格「判活」）：同键活动事件推进运行态时间戳——fed 会话走
	// 事件口（Ledger.DshRunRefresh）；未接管会话走文件面检测态推进——文件面
	// 检测态本票尚无，随票 03 补（如实声明）。resume 载荷清终结态（规格：同键
	// 后续 resume 或新 running 清除；新 running 已在 DshMain/ChildRunSet 清）。
	// turn/start 例外（2026-10-05 漏拦案）：它是用户输入信号不是机器活动——
	// 用户回流的第一事件即 turn/start，刷新运行态会让闸门把凉会话当「在跑」
	// 豁免放行（当晚 20:55/22:18 两枪真机实测漏拦）。刷新只认 assistant 侧。
	if pyStr(body["source"]) == "resume" {
		d.Ledger.DshDisposedClear(sid)
	}
	if ev != "turn/start" {
		d.Ledger.DshRunRefresh(sid, mt)
	}

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
	// 不参与闲置判定）。turn/start 例外（2026-10-05 漏拦案）：已登记会话的
	// turn/start 不顶新闲置钟——闲置锚=机器侧最后活动（assistant/message/
	// compaction/status=running），用户输入若顶新，回流的判定时刻闲置恒≈0，
	// 凉会话永进不了拦窗；台账缺席时仍 TouchFull（新会话首登记，path/cwd/
	// title 落账）。竞态（并发 Touch 同键）幂等无害。
	if parent == "" {
		if ev != "turn/start" || d.Ledger.Get("dsh", sid) == nil {
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
		// lineage_id 恒取 payload（缺省空串）——票05 去重的承重标记：事件行
		// lineage 恒空＝接管表回种（seedFromAccounts）与守望断点表的可辨识
		// 依据，不可填值；/session 的 dsh usage_total 因此按 session_id 聚合
		//（query_sessions.go，与 cost_report 的 session 口径一致）。
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

// dshStatusOf agent/status 的 status 收形：data.status 优先、顶层 status 兜底
//（插件转发层 events.ts 把 payload 平铺，两处都取）；缺/非字符串回空串＝闭集
// 外（调用方按 unknown-event 收窄）。
func dshStatusOf(body map[string]any) string {
	if data, ok := body["data"].(map[string]any); ok {
		if s, _ := data["status"].(string); s != "" {
			return s
		}
	}
	s, _ := body["status"].(string)
	return s
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
