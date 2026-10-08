package daemon

// compact_trigger.go — dsh-hot-compaction 票03：daemon 触发判定接线（守望扫
// 描入槽＋并行交接）。票01 2026-10-07 修订：在飞领取窗结算前置＋重触发节流。
//
// 触发面（spec「架构与契约」daemon 节逐字，票01 增补在飞窗一道）：watcher
// 扫描（pollDshSession 尾，maybeDshRegen 之后——重铸线优先，HandedOffAt 章
// 使两线同轮至多入队一次）按六条件判定：
//
//	闲置 ≥ trigger_ratio×TTL ∧ peak_ctx ≥ min_peak_tokens ∧ 无在途请求
//	（族系运行态 DshFamilyRunning）∧ 无有效 compressed 标记 ∧ 无在飞领取窗
//	（票01）∧ 无未过期在槽指令
//
// dsh-host-guard 票01（2026-10-08 事故）在六条件前加两道宿主防护闸（先于一切
// 条件判定、后于在飞窗结算）：no-agent 退避早退（spec D——连续领取后无成功
// 上报满阈值 → 30 分钟不入槽）；live 聚合压制（spec B——近窗内显式上报全部
// live=false 才压制；任一 true 或无显式上报照旧，前向兼容硬约束）。
//
// → EnqueueDshCompact 入槽（expires_at=now+command_ttl_ratio×TTL，票02 槽
// 语义经票01 收窄：槽内未过期指令不覆盖、在飞领取窗内不重入槽——poll 应答
// 清槽后由在飞窗接棒节流，"领取→不执行→30s 重发"空转环就此闭死）；同时按
// 既有 L1 摆渡管线异步生成交接文档（w.Enqueue 通道本体，regen 先例）——与指
// 令独立、互不阻塞、交接失败只日志不回滚指令。
//
// 在飞领取窗结算（票01）先于一切判定：领取后 dshCompactExecWindowS 内无
// /dsh/compacted ok 上报 → sweep 计一轮"未送达"（与"过期无人领取"同计数同
// gateWarn 告警路径）并清领取位放行重触发。结算与节流都在守望单线程里先后
// 发生，无锁序问题。
//
// 前置判定分工（票02 代码事实钉死，勿重复实现）：EnqueueDshCompact 只管
// enabled＋TTL 可得（票01 另加：未过期槽不覆盖＋在飞窗不重入槽，结构性兜
// 底）；「无有效标记」「无在飞窗」「无未过期在槽」三道由本触发面自判——
// 标记判走票02 DshCompressedActive 单源（有效期＋红利只领一次语义全在那），
// 槽判走本文件 dshCompactSlotFresh（compactMu 锁内只读），在飞窗判走票01
// dshCompactClaimActive（独立小锁）。
//
// 判定面全部来自既有状态源（零新增内存态）：闲置/peak/观察窗＝台账会话态
//（dsh 的 peak 由文件面 harvestDshUsage／事件面回写，闲置锚 TouchFull 票09
// 口径）；在途＝族系运行态（ledger.Dsh* 方法组）；标记与槽＝票02 面。指令槽
// 未过期窗口（0.2×TTL）与在飞领取窗（票01，180s+60s）即触发节流：窗口内不
// 重判不重入槽；两窗全过期后条件仍真则重复触发覆盖旧槽（spec「同槽新指令
// 覆盖旧指令」的票01 收窄形），交接侧由 HandedOffAt 章＋ValidHandoff 去重
// 保证不重摆（regen 同款两道）。
//
// 并发纪律：守望单线程调用（pollDshSession 各段同款）；台账锁内只有内存抄
// 字段；一切异常吞掉——压缩判定永不弄断守望。
//
// TTL 不可得（heartbeat.ttl_s ≤0）保守不触发：触发线与指令有效期皆不可算，
// EnqueueDshCompact 亦必拒（compact.go「绝不伪造」同纪律）。

import (
	"fmt"

	"ferryman/internal/clock"
	"ferryman/internal/ledger"
)

// dshCompactRunGraceS 在途判定宽限（v0.9.4）：运行信号晚于 lastWrite+本宽限才
// 挡触发——容忍转录落盘批延迟（事件批 200ms 落盘先例同量级思想，取宽值防误放）。
const dshCompactRunGraceS = 300.0

// dshCompactSlotFresh 槽内是否存在未过期指令（触发面自判，票02 compact.go
// 槽锁内只读）。过期指令留槽等覆盖（N1 派发侧丢弃语义在 DshPoll）——留槽
// 的过期指令不挡新触发（票01：覆盖收窄到过期槽，EnqueueDshCompact 双道同判）。
func (d *Daemon) dshCompactSlotFresh(sid string) bool {
	d.compactMu.Lock()
	defer d.compactMu.Unlock()
	cmd := d.dshCompactSlot[sid]
	return cmd != nil && clock.Now() < cmd.ExpiresAt
}

// maybeDshCompactTrigger 热缓存压缩触发扫描（pollDshSession 尾调用；仅 dsh
// ——本钩子只挂 dsh 专属路径，子代理会话在 pollDshSession 上方已早退）。
func (w *Watcher) maybeDshCompactTrigger(st *ledger.SessionState) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Printf("[compact] dsh 压缩触发判定异常（忽略继续）: %v\n", r)
		}
	}()
	if w.Daemon == nil || w.Cfg == nil || !w.Cfg.DshCompact.Enabled {
		return // 旧测试形态（无 daemon 无法入槽）／总开关关——零开销早退
	}
	ttl := w.Cfg.Heartbeat.TTLS
	if ttl <= 0 {
		return // TTL 不可得：触发线与指令有效期皆不可算（保守不触发）
	}
	w.Ledger.Mu().Lock()
	sid, cwd, lastWrite, observed, handedOff, peak :=
		st.SessionID, st.Cwd, st.LastWrite, st.ObservedActive, st.HandedOffAt, st.PeakCtx
	w.Ledger.Mu().Unlock()
	// 票01：在飞领取窗结算先于一切判定——过期未结（领取后窗内无 ok 上报）在
	// 此计一轮未送达并清领取位；即使后续条件早退，结算也已完成（与触发解耦）。
	w.Daemon.dshCompactClaimSweep(sid)
	// dsh-host-guard 票01 D：no-agent 退避早退——连续「领取后无成功上报」满
	// 阈值后 30 分钟内不入槽不重发不重摆（2026-10-08 事故空转环的端上闸）；
	// 解除＝该 sid 再被报 live=true（DshPoll 面主动清）或台账 last_write 前进
	//（用户回流——本判定惰性清）。
	if dshCompactBackoffActive(sid, clock.Now(), lastWrite) {
		return
	}
	// dsh-host-guard 票01 B：live 聚合压制——近窗（90s）内有显式上报且全部
	// live=false → 压制不下发；近窗任一 true 或无显式上报（未知）都不在此返
	// 回（false 不压制 true；旧体缺键＝照旧触发——前向兼容硬约束）。
	if dshLiveVerdict(sid, clock.Now()) == dshLiveSuppressed {
		return
	}
	if !observed {
		return // 与摆渡/重铸同纪律：启动后只见登记不动作（重启观察窗内的除外）
	}
	dc := w.Cfg.DshCompact
	idle := clock.Now() - lastWrite
	if idle < dc.TriggerRatio*ttl {
		return // 条件①闲置未到触发线（未到线每轮重判——闲置单调增长，不盖版本章）
	}
	if peak < dc.MinPeakTokens {
		return // 条件②peak 不足（压缩红利盖不过冷重付，不值得压）
	}
	if w.Ledger.DshFamilyRunningAfter(sid, clock.Now()-dshCompactRunGraceS) {
		return // 条件③在途（运行信号 300s 内有刷新——v0.9.4b 绝对新鲜度：宿主重载/未送
		// idle 都会留一次性 running 尖峰且无后续写入,与 lastWrite 比对会永久堵死;
		// 真在途的长生成事件流持续刷新信号(usage/agent 事件→DshRunRefresh),恒在
		// 窗内。误放兜底=插件侧 busyLive(其注册表 idle<300 才认忙)再拦一道）
	}
	if _, _, active := w.Daemon.DshCompressedActive(sid); active {
		return // 条件④有效 compressed 标记（刚压过、红利未消费完，不重复压）
	}
	if dshCompactClaimActive(sid, clock.Now()) {
		return // 票01 重触发节流：在飞领取窗内（指令已派发、执行未收口）——
		// 不重触发不重入槽，"领取→不执行→30s 重发"空转环闭死于此
	}
	if w.Daemon.dshCompactSlotFresh(sid) {
		return // 条件⑤未过期在槽指令（已触发过且指令还活着——不重复入槽）
	}
	// 五条件全真：指令入槽（票02 槽语义；expires_at=now+command_ttl_ratio×TTL）。
	if !w.Daemon.EnqueueDshCompact(sid, cwd) {
		return // enabled/TTL 复验败（运行时翻转）——本链路静默，绝不炸守望
	}
	fmt.Printf("[compact] dsh 触发热缓存压缩：%s 闲置 %.0fs ≥ 线 %.0fs（指令 %.0fs 内有效）\n",
		runeCap16(sid), idle, dc.TriggerRatio*ttl, w.Daemon.compactCommandTTLS())
	// 并行交接：既有 L1 摆渡管线异步生成交接文档——与指令独立、互不阻塞
	//（指令已在槽内，交接失败只日志不回滚）。防重同 regen 两道：常规线本版
	// 已接管（handedOff ≥ lastWrite）或库中已有有效交接覆盖当前内容 → 不重摆；
	// 入队成功即记 HandedOffAt（maybeEnqueue/regen 同款防重复），失败不盖章
	// ＝下轮重试（下轮若槽仍新鲜则整钩早退，重试归常规摆渡线兜底）。
	if handedOff >= lastWrite {
		return
	}
	if w.Store != nil && w.Store.ValidHandoff("dsh", cwd, lastWrite) != nil {
		return
	}
	if w.Enqueue(st) {
		w.Ledger.Mu().Lock()
		st.HandedOffAt = clock.Now() // 入队即记
		w.Ledger.Mu().Unlock()
		return
	}
	fmt.Printf("[compact] dsh 交接入队失败（队满？）——压缩指令不受影响（%s）\n",
		runeCap16(sid))
}
