package daemon

// compact.go — dsh 会话热缓存压缩的 daemon 指令槽与两个 HTTP 业务口
//（dsh-hot-compaction 票02，spec「架构与契约」节逐字契约）：
//
//   - 指令槽：daemon 内存态 per-session {action:"compact", session_id, cwd,
//     enqueued_at, expires_at}；同槽新指令覆盖旧指令；触发面（票03 watcher）
//     经 EnqueueDshCompact 入槽；poll 应答即清槽——先到先得，多宿主同 sid
//     天然去重；应答时 expires_at 已过即丢弃不派发（N1：过期不执行）。
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
//     刷新）。
//   - 标记有效判定单源 DshCompressedActive（gate 联动票04 消费：标记有效 ∧
//     当前 prefix < min_peak_tokens → 放行）：标记在 ∧ 未过死线 ∧ 标记后无
//     机器产出流量（LastWrite 越过标记时刻即作废——压缩红利只领一次）。
//     同刻比较（LastWrite == TS 视作无流量）：压缩自身的 compaction/* 落盘
//     写发生在上报之前，按时刻值比较对守望轮询的到达滞后天然免疫。
//
// 并发纪律：指令槽被守望线程（入槽，票03）与 HTTP 线程（poll 读清）双头
// 读写——compactMu 独立小锁串行化，临界区纯内存、不嵌套其他锁（无锁序约
// 束，cfgMu 同款）；compressed 标记挂 SessionState（DshCompressed 整体换指
// 针），台账锁内读写——与其他台账字段同一把锁同一纪律。
//
// TTL 不可得（heartbeat.ttl_s 未实测/未配置 ≤0）的保守面：指令拒入槽（无
// 有效期即无 N1 语义）、标记 Expires 钉死在置位时刻（读侧恒无效）——cacheHot
// 「绝不伪造」同纪律。

import (
	"sort"

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

// EnqueueDshCompact 指令入槽（触发面票03 消费）：同槽覆盖旧指令；enabled 关
// 或 TTL 不可得 → false。入槽失败＝本链路静默（压缩做不了时选择卡兜底由
// 触发面自担），绝不炸守望。
func (d *Daemon) EnqueueDshCompact(sid, cwd string) bool {
	if sid == "" || d.Cfg == nil || !d.Cfg.DshCompact.Enabled {
		return false
	}
	ttl := d.compactCommandTTLS()
	if ttl <= 0 {
		return false
	}
	now := clock.Now()
	d.compactMu.Lock()
	defer d.compactMu.Unlock()
	if d.dshCompactSlot == nil { // 直构 Daemon 的旧测试形态懒建（PendingTable 同款）
		d.dshCompactSlot = map[string]*dshCompactCmd{}
	}
	d.dshCompactSlot[sid] = &dshCompactCmd{SessionID: sid, Cwd: cwd,
		EnqueuedAt: now, ExpiresAt: now + ttl}
	return true
}

// DshPoll 轮询取指令业务口（/dsh/poll 端点与测试共用）：只应答请求清单内的
// 会话；应答即清槽（先到先得）；过期指令丢弃不派发（N1）。坏形（sessions
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
	d.compactMu.Lock()
	for sid := range wanted {
		cmd := d.dshCompactSlot[sid]
		if cmd == nil {
			continue
		}
		delete(d.dshCompactSlot, sid) // 应答即清槽（过期也不回槽——N1 丢弃）
		if now >= cmd.ExpiresAt {
			continue // 过期指令丢弃不派发（N1）
		}
		cmds = append(cmds, map[string]any{"action": "compact",
			"session_id": cmd.SessionID, "cwd": cmd.Cwd})
	}
	d.compactMu.Unlock()
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
	if okB && hasPrefix && prefix > 0 {
		now := clock.Now()
		expires := now // TTL 不可得：expires=now → 读侧恒无效（保守面）
		if d.Cfg != nil && d.Cfg.Heartbeat.TTLS > 0 {
			expires = now + d.Cfg.DshCompact.CompressedFlagTTLRatio*d.Cfg.Heartbeat.TTLS
		}
		d.Ledger.Mu().Lock()
		if st != nil {
			st.DshCompressed = &ledger.DshCompressMark{TS: now, Expires: expires}
			st.PeakCtx = prefix // prefix 覆盖（enrich 只增不减，新流量照常刷新）
		}
		d.Ledger.Mu().Unlock()
	}
	return map[string]any{"ok": true}
}

// DshCompressedActive 压缩标记有效判定（票02 语义单源；gate 联动票04 消费）：
// 标记在 ∧ 未过死线（now < expires，不再续期）∧ 标记后无机器产出流量
//（LastWrite ≤ 标记时刻——压缩红利只领一次）。prefix＝覆盖后的前缀现值
//（PeakCtx，闸门拿它与 min_peak_tokens 比）。无标记/无台账 → (0, false)。
func (d *Daemon) DshCompressedActive(sid string) (int, bool) {
	d.Ledger.Mu().Lock()
	defer d.Ledger.Mu().Unlock()
	st := d.Ledger.GetLocked("dsh", sid)
	if st == nil || st.DshCompressed == nil {
		return 0, false
	}
	m := st.DshCompressed
	now := clock.Now()
	if now >= m.Expires || st.LastWrite > m.TS {
		return 0, false
	}
	return st.PeakCtx, true
}
