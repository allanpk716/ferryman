package daemon

// gate_exempt.go — 机器自发 prompt 豁免（fix/gate-machine-exempt，两起生产误拦）：
//
//   a) 目录豁免 [gate].exempt_cwds（2026-10-15 17:30 Orca 自动化案）：Orca 的
//      定时自动化（reuseSession）把固定 prompt 送进固定目录里已凉的会话，不给
//      自动化会话设置任何可识别的环境变量——只能按目录豁免。命中即放行
//      （reason=exempt-cwd），不警告/不动 pending/不存待续/不入账（不调
//      d.Acct，不污染 bypass 对照组），gate.log 记 [exempt] 行。豁免只影响
//      闸门判定，摆渡照常。对 cc/codex/dsh 都生效，cwd 用经
//      pathsx.CCLaunchCwd 校正后的值（接线点在 Gate 步骤 1.5，校正之后）。
//
//   b) /loop 定时唤醒豁免（2026-10-17 08:09:29 de9af594 案，仅 cc 打字入口）：
//      CC 的 scheduled_task_fire（/loop 与 cron 同族）是机器自发 prompt，按
//      cctrans.IsScheduledWakeup 认定。凉会话放行（reason=loop-wakeup）；唯一
//      例外是同会话有窗内未消费待续（用户正处在"被拦→/clear 接力"流程中）——
//      照拦（护住接力流程），但绝不把唤醒文本存成待续、也不覆盖已有待续
//      （案发形态：作答闸门刚存下用户选择，0.6s 后唤醒文本把选择顶掉）。

import (
	"fmt"
	"path"
	"strings"

	"ferryman/internal/accounts"
	"ferryman/internal/cctrans"
	"ferryman/internal/clock"
	"ferryman/internal/pathsx"
)

// cwdExemptHit 目录豁免命中判定：cwd 归一后等于某根或位于其子树内。归一口径
// ＝反斜杠先归一成正斜杠、path.Clean（去尾随分隔符与 ./ 段，防 "C:\a\b\" 与
// "C:\a\b" 不等；path.Clean 的斜杠语义跨平台一致）＋ pathsx.NormPath 的小写
// 键形。前缀不伪匹配：`C:\a\b` 不命中 `C:\a\bc`（前缀后必须跟分隔符）。空根
// 或空 cwd 不命中。
func cwdExemptHit(cwd string, roots []string) bool {
	if cwd == "" || len(roots) == 0 {
		return false
	}
	n := normExempt(cwd)
	for _, r := range roots {
		if r == "" {
			continue
		}
		if rn := normExempt(r); n == rn || strings.HasPrefix(n, rn+"/") {
			return true
		}
	}
	return false
}

// normExempt 豁免匹配用路径归一（见 cwdExemptHit）。
func normExempt(p string) string {
	return pathsx.NormPath(path.Clean(strings.ReplaceAll(p, "\\", "/")))
}

// loopWakeupExempt 1.5b /loop 定时唤醒豁免。返回 (resp, true)＝已裁决；
// (nil, false)＝非本豁免管辖，走正常闸门（旧行为）。
//
// 性能护栏（唤醒判定要读转录尾窗、未命中还可能等落盘竞态重读）：只有台账
// 在册且已临近/进入拦窗（闲置 ≥ summarize_s，或挂着待交接标记）的会话才值得
// 判——活跃/未登记会话闸门本就放行，白读转录还给每条人工 prompt 平添延迟。
// 预检闲置钟用台账缓存值（不刷内容时钟）：幻影写入只会把闲置钟压小 → 预检
// 偏保守（多判一次转录），不产生漏判；summarize_s（< block_s）作资格线吸收
// 时钟刷新漂移，宁可多读一次尾窗。
func (d *Daemon) loopWakeupExempt(agent, sessionID, transcriptPath, prompt string,
	entry accounts.GateEntry) (map[string]any, bool) {
	if agent != "cc" || entry == accounts.GateEntryAnswer || transcriptPath == "" {
		return nil, false
	}
	if mode := d.Cfg.GateCC; mode == "" || mode == "off" {
		return nil, false // 关着的闸门无豁免可言（mode-off 放行在步骤 3）
	}
	st := d.Ledger.Get(agent, sessionID) // 与步骤 2 同款解析（含 GetByPath 回落）
	if st == nil {
		st = d.Ledger.GetByPath(transcriptPath)
	}
	if st == nil {
		return nil, false // 台账 miss：步骤 2 本就放行（no-ledger）
	}
	d.Ledger.Mu().Lock() // 台账锁内抄快照（共享引用纪律）
	sid, peak := st.SessionID, st.PeakCtx
	bar := st.LastWrite
	if st.ContentTS > 0 {
		bar = st.ContentTS // 内容时钟优先（coversBar 同款，不现算）
	}
	d.Ledger.Mu().Unlock()
	th := d.Cfg.ThresholdFor(agent)
	idle := clock.Now() - bar
	_, pok := d.Pending.Get([2]string{agent, sid})
	if idle < th.SummarizeS && !pok {
		return nil, false // 活跃会话：正常路径放行，无需豁免判定
	}
	if !cctrans.IsScheduledWakeup(transcriptPath, prompt, clock.Now()) {
		return nil, false // 非定时唤醒：走正常闸门（旧行为）
	}
	// 唤醒确认。护选择例外：同会话有窗内未消费待续（用户正处在
	// "被拦→/clear 接力"流程中）→ 照常拦截，但绝不把唤醒文本存成待续、
	// 也不覆盖已有待续（block 科目照记＝可数；不动 pending 标记）。
	if d.Store != nil {
		if prev, ok := d.Store.PendingInWindow(sid); ok {
			if idle < 0 {
				idle = 0
			}
			d.Stats.addBlocks()
			d.Acct("block", st, "", "", "", gateBlockFields(entry, peak, idle))
			d.gateWarn(agent, sid, "loop-wakeup-block", idle)
			return map[string]any{"decision": "block",
				"reason":                 loopWakeupBlockReason(idle, prev.Prompt),
				"suppressOriginalPrompt": true}, true
		}
	}
	d.gateExempt(agent, sid, "loop-wakeup")
	return map[string]any{"decision": "allow", "reason": "loop-wakeup"}, true
}

// loopWakeupBlockReason 唤醒遇窗内待续的拦截文案：说明是定时唤醒被拦、会话
// 已凉、用户之前的输入已保存、/clear 接力；唤醒文本不保存不覆盖（blockPreview
// 让"已保存"看得见，同分支 5/6 纪律）。
func loopWakeupBlockReason(idleS float64, savedPrompt string) string {
	return fmt.Sprintf("[Ferryman] 此会话已闲置 %.0f 分钟（缓存已失效），本次是定时唤醒"+
		"（/loop 或 cron）而非用户输入，照常拦下：唤醒文本不保存、也不覆盖用户之前的输入。"+
		"用户之前的输入已保存：%s\n"+
		"\n【推荐】/clear 换新会话（约 10 秒）：新会话开场自动带去进度交接与上述输入，"+
		"随便发一个字即可接力继续。",
		idleS/60, blockPreview(savedPrompt))
}
