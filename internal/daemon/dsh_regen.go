package daemon

// dsh_regen.go — 票04（dsh-post-accept-fixes）：dsh 强续后重铸触发。
//
// 场景：强续（bypass）解除拦截后用户留在本会话继续干活；旧交接已被强续消耗
//（票02 ConsumeHandoffs），新会话此刻开要等下一次长闲置的常规摆渡线才有新交
// 接可拿。本票在「强续交换完成（assistant 回复落账）后该会话的下一次渡口请求
//（dock 记账）」时刻主动入队重铸一份新交接——材料=快照 Main(sid)（此时已含
// 完整强续交换），覆盖截止=入队时台账 last_write（与常规入队闭包同口径，F8）。
//
// 全持久面推导（重启免丢失，零新增内存态持久化——账本与 store 索引都是持久
// 的，daemon 重启后按同一推导重算、结论一致）：
//
//   - 待重铸谓词＝该会话存在 bypass 账痕 B（取最新）∧ 不存在未消耗交接
//    （ConsumedAt==nil 且 status∈{fresh,skeleton}，与供出同口径）其 covers ≥ B。
//     语义＝「bypass 交换尚未被任何可供出的交接捕获」。空集（交接全被强续消耗
//     ／从未有交接）时只要存在 bypass 账痕即待重铸——主流程（被拦→强续→票02
//     即消耗该会话全部 fresh 交接→此后无未消耗交接）因此可达；重铸产物
//     covers=入队时 last_write＞B 恒成立，落库即自然熄火；多次强续各自重新
//     起圈。
//     （F12 修正注明：票面原句「会话无任何未消耗交接时谓词不成立」方向写反，
//     按该句字面实现则重铸在主场景永不触发——2026-10-06 协调者裁定为规格笔误，
//     空集＝待重铸；入队仍走 maybeEnqueue 的 peak 门等既有闸，小会话不放大。）
//   - 触发时点（语义等价「强续完成后的下一次 dock」，不早于回复落账）须同满足：
//     ① bypass 后存在该会话主会话 usage 行（assistant 回复落账——事件面
//     DshEvent 与文件面 harvestDshUsage 同科目）；
//     ② bypass 后已有第 2 条计费输入非零的 dsh dock 行——第 1 条即强续请求
//     本身（它过 dock 时模型回复尚未产生，材料不全，F9）；count_tokens 子请
//     求虽也记 dock 行但无 SSE usage 可扫、token 列恒 0，不计数——否则强续消
//     息自带的 count_tokens 行会顶替「下一次请求」造成提前入队（材料缺回复
//     半边，F2 复发；ShouldCapture 对 count_tokens 本就不捕获快照）。
//     beat/同模型重放行 agent=cc（不带 harness 归因头），agent 过滤自然排除。
//
// 入队走既有 maybeEnqueue 通道的尾段语义：观察窗/防重章、peak 门、
// ValidHandoff 去重、入队即记 HandedOffAt 全复用，不另造摆渡路径；闲置门
// （SummarizeS）不适用——重铸本来就是活跃期动作。进程内另盖每 bypass 账痕
// 一次的防重闸（dshSessionRec.regenFiredTS，纯内存章——重启丢章＝按持久面
// 推导重算，非新增持久态）：工人墙钟 8min 而用户持续写入时 HandedOffAt 章
// 会被新 lastWrite 顶开，无闸会逐轮重复入队重复上模型。入队成功后 worker 侧
// SaveHandoff 同 (agent,sid) 覆盖落库，新条目未消耗且 covers＞bypass 时刻 →
// 谓词熄火不重复重铸。
//
// 作用域（F10/D8④）：仅 agent=="dsh"——钩子只挂在 pollDshSession（cc/codex
// 守望路径不经过），谓词函数再按 agent 显式收窄（cc/codex 恒 false：无限定
// 时该谓词对 cc 在任一 bypass 后恒真，测试钉 TestDshRegenPredicateScopedToDsh）。
//
// 并发纪律：守望单线程调用；台账锁内只内存抄字段，账本读（ReadMonths）在锁
// 外；一切异常吞掉——重铸判定永不弄断守望（pollDshSession 各段同纪律）。

import (
	"fmt"

	"ferryman/internal/accounts"
	"ferryman/internal/clock"
	"ferryman/internal/ledger"
	"ferryman/internal/store"
)

// maybeDshRegen 票04 触发钩子（pollDshSession 尾调用；maybeEnqueue 之后＝同
// 轮常规线优先，HandedOffAt 章使二者每轮至多入队一次）。
func (w *Watcher) maybeDshRegen(st *ledger.SessionState, rec *dshSessionRec) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Printf("[watch] dsh 重铸判定异常（忽略继续）: %v\n", r)
		}
	}()
	if st.Agent != "dsh" || w.Store == nil || w.dsh == nil || w.dsh.accounts == nil {
		return // 作用域收窄（F10）＋未装配形态（旧测试/隐私关零变化）
	}
	w.Ledger.Mu().Lock()
	sid, cwd, lastWrite, observed, handedOff, peak :=
		st.SessionID, st.Cwd, st.LastWrite, st.ObservedActive, st.HandedOffAt, st.PeakCtx
	w.Ledger.Mu().Unlock()
	if !observed || handedOff >= lastWrite {
		return // 观察窗＋防重复入队（maybeEnqueue 同款两道）
	}
	acc := w.dsh.accounts
	bypassTS, pending := dshRegenPending(w.Store, acc, "dsh", sid, cwd)
	if !pending || bypassTS <= rec.regenFiredTS {
		return // 谓词不成立；或该条 bypass 账痕已入队过（进程内防重闸，重启丢章
		//      ＝按持久面推导重算——工人慢于用户写入时 HandedOffAt 章会被顶开，
		//      无此闸会逐轮重复入队重复上模型）
	}
	if !dshRegenDue(acc, sid, bypassTS) {
		return // 强续请求本身不是触发点（F9）；回复未落账同此
	}
	if peak < w.Cfg.ThresholdFor("dsh").MinCtxTokens {
		return // peak 门复用（不绕）
	}
	// 去重同款：已有有效未消耗交接覆盖当前内容则不重摆（dsh 内容钟恒
	// fail-open → coversBar=lastWrite，与 maybeEnqueue 的 dedupeBar 同口径）。
	if w.Store.ValidHandoff("dsh", cwd, lastWrite) != nil {
		return
	}
	// 入队走既有通道本体（w.Enqueue＝serve 装配闭包；材料/覆盖截止口径在闭包
	// 内与常规摆渡单源同款：covers_at=入队时台账 last_write，dsh 材料路在
	// worker.doDsh）——不另造摆渡路径。
	if w.Enqueue(st) {
		rec.regenFiredTS = bypassTS // 入队失败（队满）不盖＝下轮重试
		w.Ledger.Mu().Lock()
		st.HandedOffAt = clock.Now() // 入队即记（maybeEnqueue 同款防重复）
		w.Ledger.Mu().Unlock()
	}
}

// dshRegenPending 待重铸谓词：该会话存在 bypass 账痕 B（取最新）∧ 不存在未
// 消耗交接其 covers ≥ B。返回 (最新 bypass 账痕时刻, 谓词)；谓词假时 bypassTS
// 仍有意义（=B，触发时点用）。
//
// 「未消耗交接」与供出同口径（ConsumedAt==nil、status∈{fresh,skeleton}——
// RestoreCandidates 即此口径＋24h 新鲜窗＋cwd 键）：按 sid 过滤取该会话现行
// 条目（store 同 (agent,sid) 覆盖语义，至多一条），covers ≥ B 即已被可供出的
// 交接捕获 → 谓词假（重铸产物 covers＞B 恒成立，落库即自然熄火；24h 窗外的
// 古老条目不在供出口，捕获无从谈起）。bypass 痕按账本科目读最近两个月文件
//（月界即界，dshBootReplay 同款读法；强续重铸的目标窗口是分钟级，月外旧痕
// 不在射程），agent 过滤在代码侧（ReadOpts 无 agent 维）。
// 仅 agent=="dsh"（F10：cc/codex 恒 false）。
func dshRegenPending(st *store.Store, acc *accounts.Accounts, agent, sid, cwd string) (float64, bool) {
	if agent != "dsh" || st == nil || acc == nil || sid == "" || cwd == "" {
		return 0, false
	}
	cur, prev := dshMonthKeys(clock.Now())
	best := 0.0
	for _, e := range acc.ReadMonths(accounts.ReadOpts{Kind: "bypass", Session: sid}, cur, prev) {
		if ag, _ := e["agent"].(string); ag != "dsh" {
			continue
		}
		if ts := acctNum(e, "ts"); ts > best {
			best = ts
		}
	}
	if best == 0 {
		return 0, false // 无 bypass 账痕：没有「强续交换」待捕获
	}
	for _, e := range st.RestoreCandidates(agent, cwd) {
		if e.SessionID != sid {
			continue
		}
		if e.CoversUntilS >= best {
			return best, false // 已被可供出的交接捕获 → 熄火
		}
	}
	return best, true // 空集（全被消耗/从未有交接）也算待重铸（F12 修正方向）
}

// dshRegenDue 触发时点：bypass 后①主会话 usage 行在（assistant 回复落账）②
// 计费输入非零的 dsh dock 行已有第 2 条（第 1 条=强续请求本身，F9；计费零的
// count_tokens 行不计数；beat 重放行 agent=cc 不计）。两条件全持久面可重算
// ——重启同推导同结论。
func dshRegenDue(acc *accounts.Accounts, sid string, bypassTS float64) bool {
	if acc == nil || sid == "" || bypassTS <= 0 {
		return false
	}
	cur, prev := dshMonthKeys(clock.Now())
	docks, replied := 0, false
	for _, e := range acc.ReadMonths(accounts.ReadOpts{Session: sid, Since: bypassTS}, cur, prev) {
		if ag, _ := e["agent"].(string); ag != "dsh" {
			continue
		}
		switch e["kind"] {
		case "dock":
			if dshBilledInput(e) > 0 {
				docks++
			}
		case "usage":
			if sub, _ := e["subagent"].(string); sub == "" {
				replied = true
			}
		}
	}
	return docks >= 2 && replied
}
