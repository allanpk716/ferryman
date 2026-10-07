package daemon

// compact.go — dsh 会话热缓存压缩的 daemon 指令槽与两个 HTTP 业务口
//（dsh-hot-compaction 票02，spec「架构与契约」节逐字契约；票01 2026-10-07
// 修订领取语义，见下「领取后执行窗」节）：
//
//   - 指令槽：daemon 内存态 per-session {action:"compact", session_id, cwd,
//     enqueued_at, expires_at}；槽内**未过期**指令不被同会话新指令覆盖（票01
//     重触发节流——30s 空转环治点的一半），过期槽照旧允许覆盖；触发面（票03
//     watcher）经 EnqueueDshCompact 入槽；poll 应答即清槽并起**在飞执行窗**
//     （票01：领取不再是终点，窗内无 ok 上报计未送达轮）——先到先得，多宿主
//     同 sid 天然去重；应答时 expires_at 已过即丢弃不派发（N1：过期不执行）。
//   - POST /dsh/poll：请求 {agent:"dsh", sessions:[{sid, idle_s}]}（插件报其
//     宿持有会话）；应答 {commands:[{action:"compact", session_id, cwd}],
//     poll_hint_s}——只回请求清单内且未过期的（插件只执行其宿持有的会话，
//     未 poll 到的指令留在槽内等其宿）；poll_hint_s＝配置建议轮询间隔，插件
//     取 max(提示, 10s)。
//   - POST /dsh/compacted：请求 {session_id, ok, reason?, prefix_tokens?,
//     source?}；恒落账本 kind=compacted（字段齐全＝请求带来的字段全量入行）；
//     ok=true 且 prefix_tokens>0 时更新 gate 会话状态：compressed 标记（带
//     expires＝now+compressed_flag_ttl_ratio×TTL，置位钉死不再续期）＋prefix
//     覆盖（PeakCtx ← prefix_tokens；其后新流量按既有 enrich 逻辑只增不减
//     刷新）。ok=true 另收口在飞执行窗（票01）；ok=false（含 no-agent/busy/
//     timeout/error）不收口也不计送达——执行窗照走满，重触发节流不解除。
//   - 标记有效判定单源 DshCompressedActive（gate 联动票04 消费：标记有效 ∧
//     当前 prefix < min_peak_tokens → 放行）：标记在 ∧ 未过死线 ∧ 标记后无
//     机器产出流量（LastWrite 越过标记时刻即作废——压缩红利只领一次）。
//     同刻比较（LastWrite == TS 视作无流量）：压缩自身的 compaction/* 落盘
//     写发生在上报之前，按时刻值比较对守望轮询的到达滞后天然免疫。
//
// 并发纪律：指令槽被守望线程（入槽，票03）与 HTTP 线程（poll 读清）双头
// 读写——compactMu 独立小锁串行化，临界区纯内存、不嵌套其他锁（无锁序约
// 束，cfgMu 同款）；compressed 标记挂 SessionState（DshCompressed 整体换指
// 针），台账锁内读写——与其他台账字段同一把锁同一纪律。守护可见性票01 的
// 未送达计数与在飞领取窗另走两把包级独立小锁（dshCompactMissMu /
// dshCompactClaimMu，锁内纯内存），与 compactMu 互不嵌套——含落盘 IO 的
// 日志/告警一律在 compactMu 锁外。
//
// TTL 不可得（heartbeat.ttl_s 未实测/未配置 ≤0）的保守面：指令拒入槽（无
// 有效期即无 N1 语义）、标记 Expires 钉死在置位时刻（读侧恒无效）——cacheHot
// 「绝不伪造」同纪律。

import (
	"fmt"
	"sort"
	"sync"

	"ferryman/internal/accounts"
	"ferryman/internal/clock"
	"ferryman/internal/ledger"
)

// dshCompactCmd 指令槽一条（N1：expires_at 过点即弃）。
type dshCompactCmd struct {
	SessionID  string
	Cwd        string
	EnqueuedAt float64
	ExpiresAt  float64
}

// compactCommandTTLS 指令有效期（N1）＝command_ttl_ratio×heartbeat ttl_s。
// TTL 不可得 → 0（调用方拒入槽）。
func (d *Daemon) compactCommandTTLS() float64 {
	if d.Cfg == nil || d.Cfg.Heartbeat.TTLS <= 0 {
		return 0
	}
	return d.Cfg.DshCompact.CommandTTLRatio * d.Cfg.Heartbeat.TTLS
}

// EnqueueDshCompact 指令入槽（触发面票03 消费）：槽内未过期指令不覆盖（票01
// 重触发节流——触发间隔 ≥ 指令有效期）、在飞领取窗内不重入槽（票01——30s
// 重发环治点），过期槽照旧覆盖；enabled 关或 TTL 不可得 → false。入槽失败＝
// 本链路静默（压缩做不了时选择卡兜底由触发面自担），绝不炸守望。
func (d *Daemon) EnqueueDshCompact(sid, cwd string) bool {
	if sid == "" || d.Cfg == nil || !d.Cfg.DshCompact.Enabled {
		return false
	}
	ttl := d.compactCommandTTLS()
	if ttl <= 0 {
		return false
	}
	now := clock.Now()
	if dshCompactClaimActive(sid, now) {
		return false // 票01：在飞领取窗内不重入槽（独立小锁，compactMu 外）
	}
	d.compactMu.Lock()
	defer d.compactMu.Unlock()
	if d.dshCompactSlot == nil { // 直构 Daemon 的旧测试形态懒建（PendingTable 同款）
		d.dshCompactSlot = map[string]*dshCompactCmd{}
	}
	if cmd := d.dshCompactSlot[sid]; cmd != nil && now < cmd.ExpiresAt {
		return false // 票01：槽内未过期指令不被覆盖（有效期内不重触发）
	}
	d.dshCompactSlot[sid] = &dshCompactCmd{SessionID: sid, Cwd: cwd,
		EnqueuedAt: now, ExpiresAt: now + ttl}
	return true
}

// DshPoll 轮询取指令业务口（/dsh/poll 端点与测试共用）：只应答请求清单内的
// 会话；应答即清槽（先到先得）；过期指令丢弃不派发（N1）——丢弃逐轮打日志、
// 连续 3 轮 gateWarn 告警一次（守护可见性票01）；指令被领取（派发应答）即起
// 在飞执行窗（票01：窗内无 ok 上报由守望 sweep 计未送达轮）。坏形（sessions
// 非数组/元素非对象）静默收窄——照常回空应答不 5xx（DshEvent 同纪律）。
func (d *Daemon) DshPoll(body map[string]any) map[string]any {
	hint := 30.0
	enabled := false
	if d.Cfg != nil {
		hint = d.Cfg.DshCompact.PollHintS
		enabled = d.Cfg.DshCompact.Enabled
	}
	cmds := []map[string]any{}
	resp := map[string]any{"commands": cmds, "poll_hint_s": hint}
	// 请求清单：只应答插件报其持有的会话（多宿主先到先得的匹配面）。
	list, _ := body["sessions"].([]any)
	wanted := map[string]bool{}
	for _, s := range list {
		m, ok := s.(map[string]any)
		if !ok {
			continue
		}
		if sid := pyStr(m["sid"]); sid != "" {
			wanted[sid] = true
		}
	}
	if !enabled || len(wanted) == 0 {
		return resp
	}
	now := clock.Now()
	var expired, claimed []string
	d.compactMu.Lock()
	for sid := range wanted {
		cmd := d.dshCompactSlot[sid]
		if cmd == nil {
			continue
		}
		delete(d.dshCompactSlot, sid) // 应答即清槽（过期也不回槽——N1 丢弃）
		if now >= cmd.ExpiresAt {
			expired = append(expired, sid) // 过期丢弃不派发（N1）；可见性记账锁外补（票01）
			continue
		}
		claimed = append(claimed, sid) // 领取＝在飞执行窗起点（票01，锁外补账）
		cmds = append(cmds, map[string]any{"action": "compact",
			"session_id": cmd.SessionID, "cwd": cmd.Cwd})
	}
	d.compactMu.Unlock()
	for _, sid := range claimed {
		dshCompactClaimStart(sid, now) // 票01：领取起执行窗（窗内无 ok 上报由守望 sweep 计轮）。
		// 旧"领取清零未送达计数"钩子随票01 撤销：领取不再是送达证据
		//（bb5d5e37 实锚：领了不执行把告警永久消音）——清零只认 ok=true 上报
	}
	d.noteDshCompactUndelivered(expired) // 票01：计数（独立小锁）＋日志/告警落盘全在 compactMu 外
	if len(cmds) > 1 { // 派发序确定化（map 迭代序随机；测试与排查可读）
		sort.Slice(cmds, func(i, j int) bool {
			return cmds[i]["session_id"].(string) < cmds[j]["session_id"].(string)
		})
	}
	resp["commands"] = cmds // append 可能换底：回填最终切片头
	return resp
}

// DshCompacted 压缩结果上报业务口（/dsh/compacted 端点与测试共用）：
//   - 恒落账本 kind=compacted（字段齐全；Acct 打印吞错——记账永不弄断接收
//     面，DshEvent 同纪律）；sid 空/ok 非布尔 → 坏形静默收窄（200＋skipped，
//     不落行不 5xx）。
//   - ok=true 且 prefix_tokens>0：compressed 标记（expires 置位钉死）＋prefix
//     覆盖（PeakCtx）。会话未登记（台账无）＝无状态可挂，只落行（重启窗内
//     上报的如实收口）。
func (d *Daemon) DshCompacted(body map[string]any) map[string]any {
	sid := pyStr(body["session_id"])
	if sid == "" {
		return map[string]any{"ok": true, "skipped": "empty-session-id"}
	}
	okB, isBool := body["ok"].(bool)
	if !isBool {
		return map[string]any{"ok": true, "skipped": "bad-ok"}
	}
	reason, source := pyStr(body["reason"]), pyStr(body["source"])
	prefix, hasPrefix := 0, false
	if _, present := body["prefix_tokens"]; present {
		prefix, hasPrefix = dshIntOr(body, "prefix_tokens"), true
	}
	st := d.Ledger.Get("dsh", sid) // 共享引用——写回在下方台账锁内
	f := accounts.Fields{"ok": okB}
	if reason != "" {
		f["reason"] = reason
	}
	if hasPrefix {
		f["prefix_tokens"] = prefix
	}
	if source != "" {
		f["source"] = source
	}
	d.Acct("compacted", st, "dsh", sid, "", f)
	if okB {
		dshCompactMissReset(sid)  // 守护可见性票01：成功压缩上报＝链路有产出，未送达计数清零
		dshCompactClaimClear(sid) // 票01：成功上报收口在飞执行窗（重入槽即刻放行）。
		// ok=false（no-agent/busy/timeout/error）不收口——不算送达，执行窗照走
		// 满，30s 重发环不复活（票01 治点）
	}
	if okB && hasPrefix && prefix > 0 {
		now := clock.Now()
		expires := now // TTL 不可得：expires=now → 读侧恒无效（保守面）
		if d.Cfg != nil && d.Cfg.Heartbeat.TTLS > 0 {
			expires = now + d.Cfg.DshCompact.CompressedFlagTTLRatio*d.Cfg.Heartbeat.TTLS
		}
		d.Ledger.Mu().Lock()
		if st != nil {
			prePeak := st.PeakCtx // v0.9.3 票1：压前峰值入标记（放行线比例腿基准）
			st.DshCompressed = &ledger.DshCompressMark{TS: now, Expires: expires, PrePeak: prePeak}
			st.PeakCtx = prefix // prefix 覆盖（enrich 只增不减，新流量照常刷新）
		}
		d.Ledger.Mu().Unlock()
	}
	return map[string]any{"ok": true}
}

// DshCompressedActive 压缩标记有效判定（票02 语义单源；gate 联动票04 消费）：
// 标记在 ∧ 未过死线（now < expires，不再续期）∧ 标记后无机器产出流量
//（LastWrite ≤ 标记时刻——压缩红利只领一次）。返回 (prefix, prePeak, true)：
// prefix＝覆盖后的前缀现值（PeakCtx），prePeak＝压前峰值（v0.9.3 票1 放行线
// 比例腿基准；0＝不可得）。放行比较归 gate 的 dshCompactPassLine（本处只报
// 值不判线）。无标记/无台账 → (0, 0, false)。
func (d *Daemon) DshCompressedActive(sid string) (int, int, bool) {
	d.Ledger.Mu().Lock()
	defer d.Ledger.Mu().Unlock()
	st := d.Ledger.GetLocked("dsh", sid)
	if st == nil || st.DshCompressed == nil {
		return 0, 0, false
	}
	m := st.DshCompressed
	now := clock.Now()
	if now >= m.Expires || st.LastWrite > m.TS {
		return 0, 0, false
	}
	return st.PeakCtx, m.PrePeak, true
}

// ---- 守护可见性票01：指令未送达不再静默（两路同权） ----
//
// 路径一"入槽后过期仍无人领取"：每次过期丢弃一行日志（compactLogf 缝）。
// 路径二"领取后执行窗超时"（票01 修订）：领取（DshPoll 派发应答）起
// dshCompactExecWindowS 执行窗，窗内无 /dsh/compacted ok=true 上报（ok=false
// 不算——bb5d5e37 实锚：领了报不可执行/不执行正是空转形态）→ 守望扫描
// sweep 结算计一轮。
//
// 两路同一会话共用同一连续计数：连续满 dshCompactMissAlertRounds 轮经
// gateWarn 告警一次（mode=compact-undelivered，每会话一次防刷屏）；唯一清零
// 钩子＝成功压缩上报（DshCompacted ok=true）——之后再满 3 轮可再告警（有界
// 重复）。入槽不清零：自然循环（入槽→过期→poll 丢弃→触发器下轮重灌）轮数
// 照常累计——这正是告警要抓的形态。领取也不清零（票01 修订：领取不是送达
// 证据，"领取即清零"曾把告警永久消音）。

// dshCompactMissAlertRounds 连续未送达告警阈值（票01）：第 3 轮恰告警一次；
// 4、5 轮只日志不叠加；计数清零后重新起算。
const dshCompactMissAlertRounds = 3

// compactLogf 压缩链日志缝（票01）：var 形＝测试捕获（appendLineBestEffort /
// logShutdownSource 惯例）。缺省 stdout 一行（compact_trigger.go 的 [compact]
// 前缀同款）。
var compactLogf = func(format string, args ...any) {
	fmt.Printf(format, args...)
}

// dshCompactMissN 会话"未送达"连续轮数（票01，两路共用）。包级态＋独立小锁
// ——daemon.go 不在票01 改动路径、Daemon 字段挂不进；单守护进程现实下等价
// Daemon 字段（测试经 resetCompactMiss 复位防跨用例渗漏）。锁内纯内存，绝不
// 与 compactMu 嵌套（头注并发纪律）。
var (
	dshCompactMissMu sync.Mutex
	dshCompactMissN  = map[string]int{}
)

// dshCompactExecWindowS 领取后执行窗秒数（票01）＝插件执行臂超时 180s
//（compact.ts DEFAULT_COMPACT_TIMEOUT_MS 镜像）＋余量 60s（上报 HTTP 往返与
// 一次静默重试的余量）。窗内无 ok 上报＝未送达一轮。
const dshCompactExecWindowS = 180.0 + 60.0

// 在飞领取窗（票01）：sid → 领取时刻。条目在＝领取未收口（窗未走满且无
// ok=true 上报）；兼任重触发节流窗（EnqueueDshCompact 与触发面双道查）。
// 包级态＋独立小锁（dshCompactMissMu 同纪律；测试经 resetCompactClaims 复位）。
var (
	dshCompactClaimMu sync.Mutex
	dshCompactClaimAt = map[string]float64{}
)

func dshCompactClaimStart(sid string, now float64) {
	dshCompactClaimMu.Lock()
	dshCompactClaimAt[sid] = now
	dshCompactClaimMu.Unlock()
}

func dshCompactClaimClear(sid string) {
	dshCompactClaimMu.Lock()
	delete(dshCompactClaimAt, sid)
	dshCompactClaimMu.Unlock()
}

// dshCompactClaimActive 在飞执行窗内（领取未收口 ∧ 窗未走满）＝重触发节流
// 窗。窗已走满但守望 sweep 未跑到时不挡新指令——结算与放行在守望单线程里
// 先后发生（dshCompactClaimSweep 先于触发判定）。
func dshCompactClaimActive(sid string, now float64) bool {
	dshCompactClaimMu.Lock()
	defer dshCompactClaimMu.Unlock()
	at, ok := dshCompactClaimAt[sid]
	return ok && now < at+dshCompactExecWindowS
}

// dshCompactClaimSweep 领取后执行窗结算（票01；守望单线程调用，先于触发判
// 定）：窗走满且领取未收口（成功上报在 DshCompacted 已清领取位）→ 计一轮
// 未送达＋清领取位放行重触发。与"过期无人领取"同计数同告警路径。
func (d *Daemon) dshCompactClaimSweep(sid string) {
	now := clock.Now()
	dshCompactClaimMu.Lock()
	at, ok := dshCompactClaimAt[sid]
	if !ok || now < at+dshCompactExecWindowS {
		dshCompactClaimMu.Unlock()
		return
	}
	delete(dshCompactClaimAt, sid)
	dshCompactClaimMu.Unlock()
	d.noteDshCompactMiss(sid, true)
}

// noteDshCompactMiss 单会话计一轮未送达（票01 两路共用）：逐轮一行日志（文
// 案带因与轮号，sid 经 runeCap16 截 16 位）；连续满 dshCompactMissAlertRounds
// 轮经 gateWarn 告警一次（落 <DataDir>/gate.log，失败静默——appendGateWarn
// 尽力而为同纪律）。
func (d *Daemon) noteDshCompactMiss(sid string, claimedTimeout bool) {
	dshCompactMissMu.Lock()
	dshCompactMissN[sid]++
	n := dshCompactMissN[sid]
	dshCompactMissMu.Unlock()
	if claimedTimeout {
		compactLogf("[compact] dsh 指令领取后执行窗内无成功上报(第 %d 轮):%s\n", n, runeCap16(sid))
	} else {
		compactLogf("[compact] dsh 指令过期无人领取(第 %d 轮):%s\n", n, runeCap16(sid))
	}
	if n == dshCompactMissAlertRounds {
		d.gateWarn("dsh", sid, "compact-undelivered", 0)
	}
}

// noteDshCompactUndelivered 过期指令丢弃可见性（票01；DshPoll 出锁后调用）。
func (d *Daemon) noteDshCompactUndelivered(sids []string) {
	for _, sid := range sids {
		d.noteDshCompactMiss(sid, false)
	}
}

// dshCompactMissReset 未送达连续计数清零（票01，唯一清零钩子）：成功压缩上报
//（DshCompacted ok=true）＝指令真送达且有产出，既往未送达一笔勾销，下轮从 1
// 重新起算。票01 修订：领取不再清零（bb5d5e37 实锚：领了不执行把告警永久
// 消音——领取不是送达证据）。
func dshCompactMissReset(sid string) {
	dshCompactMissMu.Lock()
	delete(dshCompactMissN, sid)
	dshCompactMissMu.Unlock()
}
