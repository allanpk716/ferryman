package daemon

// dsh_boot_replay.go — 票06（dsh-post-accept-fixes）：守护重启贫血修复——
// 账本回放重建峰值。
//
// 根因：daemon 重启后 pollDsh 重建台账，peak 由 harvestDshUsage 增量尾读维护
// （断点恢复只回偏移不回峰值，watcher_dsh.go newDshHarvest/dshSessionRec）——
// 只对重启后有新流量的会话生效；纯闲置会话 peak 恒 0，摆渡 peak 门
//（maybeEnqueue 六道门之一）与守望开窗条件④（peak ≥ MinCtxTokens）双双挡死
// ——重启后被拦的会话铸不出新交接、守望不开窗。
//
// 修法：重建台账处（pollDshSession，每会话每采集态一次）对 peak==0 的 dsh 主
// 会话，从最近两个账本月文件回放 usage/dock 条目（键＝条目 session_id 或
// lineage_id 命中该会话）重建 peak＝各请求计费输入三列之和（input+cache_read
// +cache_creation）的最大值；title 顺带从最近交接文档头行补（台账现行标题空
// 时，取不到留空不报错）。只补 peak/title——last_write/运行态不伪造（既有
// Touch/检测面口径零变化）。
//
// 边界（验收钉死）：
//   - 子代理行（subagent 非空）不计——两现行面（文件面 rec.peak、事件面
//     billed 回写）的 peak 都只算主会话请求，回放口径对齐；
//   - 接线点在 harvest 之前：历史峰值先落位 st.PeakCtx，增量采集只增不减
//    （harvestDshUsage/DshEvent 回写均取 max）；fed 会话同享（接线点在
//     dshIsFed 分流之前）；事件面已回写峰值（peak≠0）→ 回放让位不覆盖；
//   - 回放每采集态一次（dshSessionRec.bootReplayed）：账本无该会话条目的
//     情形也不重复扫盘；月文件点名读取（accounts.ReadMonths）——当前月＋
//     上一月（跨月边界：条目落上月文件也要回放得到），更早月份不扫；
//   - cc/codex 的 boot/enrich 路径零变化——本文件只被 pollDshSession 的 dsh
//     主会话路径调用，enrichImpl 的 dsh 跳过规则一行不动；
//   - 一切异常吞掉——回放永不弄断守望（pollDshSession 各段同纪律）。

import (
	"fmt"
	"strings"
	"time"

	"ferryman/internal/accounts"
	"ferryman/internal/clock"
	"ferryman/internal/dshtrans"
	"ferryman/internal/jsonl"
	"ferryman/internal/ledger"
	"ferryman/internal/pathsx"
	"ferryman/internal/store"
)

// dshBootReplay 重启贫血修复主入口（pollDshSession 台账重建处调用；每采集态
// 恰一次——重复轮零开销）。
func (w *Watcher) dshBootReplay(st *ledger.SessionState, rec *dshSessionRec) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Printf("[watch] dsh 账本回放异常（忽略继续）: %v\n", r)
		}
	}()
	if rec.bootReplayed {
		return // 每采集态一次（账本无条目也不重复扫盘）
	}
	rec.bootReplayed = true
	w.Ledger.Mu().Lock()
	peak, sid, path, title, cwd := st.PeakCtx, st.SessionID, st.TranscriptPath,
		st.Title, st.Cwd
	w.Ledger.Mu().Unlock()
	// peak 回放：账本未装配（HarvestUsage 关/旧测试形态）无可回放；现行面已
	// 有峰值（事件面回写/同轮先到的流量）让位——只补 0 值，不覆盖。
	if w.dsh != nil && w.dsh.accounts != nil && peak == 0 {
		if p := dshReplayPeak(w.dsh.accounts, sid, pathsx.NormPath(path), clock.Now()); p > 0 {
			w.Ledger.Mu().Lock()
			if p > st.PeakCtx { // 双验（读盘期间事件面可能已回写）：只增不减
				st.PeakCtx = p
			}
			w.Ledger.Mu().Unlock()
		}
	}
	// title 补缺：最近交接文档头行（与账本无关的 store 面；空才补，取不到
	// 留空不报错）。回放后的真实标题由检测/采集面按会话事件自然覆盖。
	if title == "" && w.Store != nil {
		if t := dshBootTitle(w.Store, sid, cwd); t != "" {
			w.Ledger.Mu().Lock()
			if st.Title == "" {
				st.Title = t
			}
			w.Ledger.Mu().Unlock()
		}
	}
}

// dshReplayPeak 最近两个账本月文件的 usage/dock 条目回放：计费输入三列之和
// 的最大值。命中键＝条目 session_id 或 lineage_id（二者任一命中即该会话——
// 文件面行双键齐、事件面行常缺 lineage、dock 行缺 lineage，OR 口径全覆盖）。
func dshReplayPeak(a *accounts.Accounts, sid, lineage string, now float64) int {
	if a == nil || sid == "" {
		return 0
	}
	cur, prev := dshMonthKeys(now)
	best := 0
	for _, kind := range []string{"usage", "dock"} {
		for _, e := range a.ReadMonths(accounts.ReadOpts{Kind: kind}, cur, prev) {
			if ag, _ := e["agent"].(string); ag != "dsh" {
				continue
			}
			if sub, _ := e["subagent"].(string); sub != "" {
				continue // 子代理请求不计（两现行面 peak 口径对齐）
			}
			esid, _ := e["session_id"].(string)
			elineage, _ := e["lineage_id"].(string)
			if esid != sid && (lineage == "" || elineage != lineage) {
				continue
			}
			if b := dshBilledInput(e); b > best {
				best = b
			}
		}
	}
	return best
}

// dshGatePeakBackfill 票03（dsh-first-live-followups）闸门路径同步兜底
//（dshAutoContinue 专用，"先补后记"的补）：强续时刻 PeakCtx=0（重启贫血且
// 本文件回放无料——接法乙 2026-10-03 前的历史流量无归因键，生产实锚
// session-5167d69a 横幅"约 0 tokens"实付 17,693）→ 优先账本回放
//（dshReplayPeak 复用，不新建回放通道），无行按转录粗估 token 量级
//（dshTranscriptRoughTokens）。返回 0 = 无任何补值来源（横幅降级"重付额度
// 未知"）。只读（账本月文件＋转录文件），不写台账、不动 bootReplayed 标记
//——补值只喂横幅与 bypass 行，粗估是量级不是真值，不入共享态。
//
// st 缺位（理论不可达：闸门分支2 台账 miss 已放行，防御同 dshAutoContinue）
// 时键全取入参：sid=sessionID、lineage=归一化 transcriptPath；转录读路径
// 优先宿主刚报的 transcriptPath（最鲜活），空则回落台账路径。
func dshGatePeakBackfill(a *accounts.Accounts, st *ledger.SessionState,
	sessionID, transcriptPath string, now float64) int {
	sid, path := sessionID, transcriptPath
	lineage := pathsx.NormPath(transcriptPath)
	if st != nil {
		sid = st.SessionID
		lineage = pathsx.NormPath(st.TranscriptPath)
		if path == "" {
			path = st.TranscriptPath
		}
	}
	if sid == "" {
		return 0
	}
	if p := dshReplayPeak(a, sid, lineage, now); p > 0 {
		return p
	}
	return dshTranscriptRoughTokens(path)
}

// dshTranscriptRoughTokens 转录粗估 token 量级（票03）：可解析事件行
//（jsonl dict 解码成功）的字节和 / 4——量级估计，不做精确 tokenizer（票面
// 背景材料口径）。读法＝dshtrans.TailText 全量（zstd 逐帧解/明文残行扣留，
// 压缩形态按 .zstd 物理后缀判——代文件名解析不参与，转录路径可能是任意
// 代）；读失败/坏帧 → 已解出的部分照估（前缀量级仍有效），全无 → 0。
// 无可解析行 → 0（调用方按"无来源"降级）。防御收口同包纪律：不向调用方
// 抛错。
func dshTranscriptRoughTokens(path string) int {
	if path == "" {
		return 0
	}
	res := dshtrans.TailText(path, strings.HasSuffix(path, ".zstd"), 0)
	total := 0
	for _, line := range strings.Split(res.Text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if _, ok := jsonl.DecodeDict(line); ok {
			total += len(line)
		}
	}
	return total / 4
}

// dshBilledInput 条目的计费输入三列之和（宽松取值：缺列/坏形按 0——账本行
// 自家产线恒齐四列，宽只防手写/旧版行）。
func dshBilledInput(e map[string]any) int {
	return dshLedgerInt(e["input_tokens"]) + dshLedgerInt(e["cache_read_tokens"]) +
		dshLedgerInt(e["cache_creation_tokens"])
}

// dshLedgerInt 账本 token 列的宽松取整（dshLedgerOffset 同族：JSON 解码是
// float64，进程内直调可能是 int/int64，余者 0）。
func dshLedgerInt(v any) int {
	switch x := v.(type) {
	case float64:
		return int(x)
	case int:
		return x
	case int64:
		return int(x)
	}
	return 0
}

// dshMonthKeys 回放的两个月键（当前月、上一月；本地时区与 Record 落盘月键
// 同源。月首运算——月首减一月无月末归一化坑，AddDate 直减在 3/31 会滑回
// 本月）。
func dshMonthKeys(now float64) (string, string) {
	t := time.Unix(int64(now), 0)
	y, m, _ := t.Date()
	cur := fmt.Sprintf("%04d%02d", y, int(m))
	prev := time.Date(y, m, 1, 0, 0, 0, 0, time.Local).AddDate(0, -1, 0)
	return cur, fmt.Sprintf("%04d%02d", prev.Year(), int(prev.Month()))
}

// dshBootTitle 最近交接文档头行取标题（票06）：RestoreCandidates 首条（covers
// 降序＝最近在前）的文档，头行 [Ferryman 交接 · 会话 X] / [Ferryman 交接(骨架)
// · 会话 X] 形取 X；占位符（无标题）/骨架 sid 缩写按无标题处理。取不到一律 ""
//（不报错——store 空库/文档读败/形不合都归此）。
func dshBootTitle(st *store.Store, sid, cwd string) string {
	if st == nil || cwd == "" {
		return ""
	}
	cands := st.RestoreCandidates("dsh", cwd)
	if len(cands) == 0 {
		return ""
	}
	return dshTitleFromDocHead(st.ReadHandoff(cands[0]), sid)
}

// dshTitleFromDocHead 交接文档头行解析：首行 [Ferryman 交接… · 会话 X] 取 X；
// 形不合 → ""。X 为占位符（HandoffMarkdown 的 (无标题) 回落）或骨架的 sid
// 缩写回落（saveDshSkeleton 同款）时按无标题处理（留空）。
func dshTitleFromDocHead(md, sid string) string {
	line := md
	if i := strings.IndexByte(md, '\n'); i >= 0 {
		line = md[:i]
	}
	line = strings.TrimSpace(line)
	const sep = " · 会话 "
	if !strings.HasPrefix(line, "[Ferryman 交接") || !strings.HasSuffix(line, "]") {
		return ""
	}
	i := strings.Index(line, sep)
	if i < 0 {
		return ""
	}
	title := strings.TrimSuffix(line[i+len(sep):], "]")
	if title == "" || title == "(无标题)" || title == runeCap8(sid) {
		return ""
	}
	return title
}
