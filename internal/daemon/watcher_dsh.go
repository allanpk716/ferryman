package daemon

// watcher_dsh.go — dsh 轨守望＋台账（dsh phase 2 P2-1：四块砖的 daemon 落点）。
//
// 与 cc/codex 通道同框架（pollCC/pollCodex 同族）：
//   - 守望：pollDsh 轮询 ~/.dsh/sessions（配置 [watch].dsh_sessions_dir 或
//     $DSH_HOME/sessions 覆盖），每会话目录取数值最高代文件，Touch("dsh")
//     登记 + 重启观察窗（observeRecent）——台账/闸门面从此看得见 dsh 会话；
//   - 台账：assistant/message 事件 TokenUsage 四列（不相交口径、计费输入＝
//     三者之和）经既有 usage 科目入账（白名单零改动，CC harvest 同款字段）；
//     子代理会话（自头 origin=subagent）随父入账（CC 票01/ADR-0008 同款
//     语义：session_id=父 sid、subagent=子 sid），不 Touch、不参与守望判定；
//   - 断点：偏移随 usage 流水入账、daemon 重启后从账本恢复（账本即唯一
//     状态，CC harvest 同纪律）；代文件切换（格式迁移发新代）偏移清零重采
//     ——迁移会重编事件 seq（v3→v4 有合成事件消费 seq），跨代去重不做，
//     迁移期 usage 可能重复记账一次（rc 期格式升级罕见事件，如实接受）；
//     同代文件收缩（写方 rollbackAppend/truncateTornTail 修复残尾）偏移
//     清零重采、seq 去重保留（同文件内 seq 唯一，防重账）。
//
// P2-2 会话键对齐结论（2026-10-03 调查钉死，详见 dock.HeaderDeepSeekHarnessSessionID
// 注记与 .scratch/dsh-phase2 清单）：三方统一键＝会话头行 id（session-<uuid>，
// 即目录名本体——守望 Touch/usage 现行键，零改动即已对齐）；dsh 请求体**没有**
// metadata.session_id（pi-ai 路无任何键上线；PR #4 的"捕获键零改动就位"系把
// 自家 CC 会话记账行误读为 dsh 行——当日 dsh 流量两行 session_id 恒空，渡口
// 账面实证）。渡口已加 X-Deepseek-Harness-Session-Id 头第三回落（键=头行 id，
// 与守望同键）——但当前接管路由 llm-pi-ai 不发该头，捕获归因仍空。
//
// P2-1/P2-2 边界（后续票接线，本文件不预铺）：
//   - 不 maybeEnqueue：摆渡依赖渡口快照，而 pi-ai 路快照捕获恒 skipped（无键）
//     ——接线必 snapshot_missing，待 P2-3 接法乙（llm-deepseek 路由带
//     x-deepseek-harness-session-id 头）或 P2-4 原生插件（session/event 直报，
//     不依赖渡口捕获）解锁；enrich 亦无 dsh 分支（标题/峰值改由采集顺带回写，
//     不读第二遍盘）；
//   - 不喂 NoteUsage/ReqClock：停车窗/判热钟是 cc 面机制，dsh 判活信号待
//     P2-5 设计；
//   - 不 qwatch/心跳/同模型：三处均 cc-only（各自入口 agent 检查）。
//
// 并发纪律（本包铁律见 windows.go 顶部）：本文件只在守望单线程跑；台账
// 锁内只有内存操作，读盘/记账一律锁外。

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"ferryman/internal/accounts"
	"ferryman/internal/dshtrans"
	"ferryman/internal/ledger"
	"ferryman/internal/pathsx"
)

// dshSessionRec 一个 dsh 会话目录的采集态（守望单线程读写；偏移断点在
// 账本 usage 行，此处仅进程内缓存）。
type dshSessionRec struct {
	gen    dshtrans.Generation
	header dshtrans.Header
	offset int64
	title  string
	seen   map[int64]bool // 代文件内 seq 去重（CC msgSeen 同位）
	peak   int            // 计费输入（三输入列之和）的运行峰值
}

// dshHarvest dsh 用量采集器（harvest.HarvestState 的 dsh 形；不共用其
// (agent, sid, stem) 复合键——dsh 代文件 stem=session.vN 跨会话同名，键
// 改按会话目录）。
type dshHarvest struct {
	accounts *accounts.Accounts
	// resume 停机前的断点表：lineage_id（代文件归一路径）→ (max offset,
	// last title)。构造时一次读账（CC NewHarvestState 同款）；新造文件无
	// 断点即从 0 全量回填。
	resume map[string]dshResumeEnt
}

type dshResumeEnt struct {
	offset int64
	title  string
}

// dshLedgerOffset 账本行 offset 字段的宽松取整（harvest.ledgerOffset 同义
// ——彼处未导出，本包按同规格复刻：None/零值 → 0；数值向零截断；整数字符
// 串可解析；其余 0）。
func dshLedgerOffset(v any) int64 {
	switch x := v.(type) {
	case float64:
		return int64(x)
	case int64:
		return x
	case int:
		return int64(x)
	case string:
		if n, err := strconv.ParseInt(strings.TrimSpace(x), 10, 64); err == nil {
			return n
		}
	}
	return 0
}

// newDshHarvest 构造 + 账本断点恢复（读失败 → 空表从零采，重复风险接受
// ——CC NewHarvestState 同语义）。
func newDshHarvest(a *accounts.Accounts) *dshHarvest {
	h := &dshHarvest{accounts: a, resume: map[string]dshResumeEnt{}}
	if a == nil {
		return h
	}
	for _, e := range a.Read(accounts.ReadOpts{Kind: "usage"}) {
		if ag, _ := e["agent"].(string); ag != "dsh" {
			continue
		}
		lineage, _ := e["lineage_id"].(string)
		if lineage == "" {
			continue
		}
		off := dshLedgerOffset(e["offset"])
		title, _ := e["title"].(string)
		ent := h.resume[lineage] // 同文件多行：offset 取 max、title 取末个非空
		if off > ent.offset {
			ent.offset = off
		}
		if title != "" {
			ent.title = title
		}
		h.resume[lineage] = ent
	}
	return h
}

// dshStateFor 会话目录的采集态：代文件未变 → 原值；换代/首见 → 读头行
// （磁盘 IO，锁外）+ 账本断点，重置采集态（标题保留——换代继承会话身份）。
// 头不可读返回 nil（不记忆，下轮重试——首行可能尚未落盘，CC subagentParent
// 同款）。w.dsh 可能 nil（HarvestUsage 关）——pollDshSession 只走登记面。
func (w *Watcher) dshStateFor(dir string, gen dshtrans.Generation) *dshSessionRec {
	key := pathsx.NormPath(dir)
	if rec := w.dshSessions[key]; rec != nil && rec.gen.Path == gen.Path {
		return rec
	}
	line, ok := dshtrans.ReadHeaderLine(gen.Path, gen.Zstd)
	if !ok {
		return nil
	}
	header, ok := dshtrans.ParseHeaderLine(line)
	if !ok || header.ID == "" {
		return nil
	}
	rec := &dshSessionRec{gen: gen, header: header, seen: map[int64]bool{}}
	if w.dsh != nil {
		if ent, ok := w.dsh.resume[pathsx.NormPath(gen.Path)]; ok {
			rec.offset = ent.offset
			rec.title = ent.title
		}
	}
	if old := w.dshSessions[key]; old != nil {
		rec.title = old.title // 换代继承标题（同会话身份）
	}
	w.dshSessions[key] = rec
	return rec
}

// pollDsh dsh 会话根轮询：根不存在静默跳过（未装 dsh 的机器零开销）。
// 目录层级 root/<项目目录>/<会话目录>/<代文件>——只对深度 2 的会话目录
// 动作；_no-cwd（无 cwd 会话的落点）是普通深度 1 目录，无特判。
func (w *Watcher) pollDsh() {
	defer func() {
		if r := recover(); r != nil {
			fmt.Printf("[watch] dsh 轮询异常（忽略继续）: %v\n", r)
		}
	}()
	if _, err := os.Stat(w.dshDir); err != nil {
		return
	}
	_ = filepath.WalkDir(w.dshDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return nil
		}
		rel, rerr := filepath.Rel(w.dshDir, p)
		if rerr != nil || rel == "." {
			return nil
		}
		if strings.Count(filepath.ToSlash(rel), "/") != 1 { // 项目/会话两层
			return nil
		}
		w.pollDshSession(p)
		return nil
	})
}

// pollDshSession 单个会话目录：选代→stat→采集态→（子代理？随父入账：
// Touch 登记观察）→用量采集。
func (w *Watcher) pollDshSession(dir string) {
	gen, ok := dshtrans.LatestGeneration(dir)
	if !ok {
		return
	}
	info, err := os.Stat(gen.Path)
	if err != nil {
		return // OSError → 跳过
	}
	rec := w.dshStateFor(dir, gen)
	if rec == nil {
		return
	}
	if rec.header.Origin == "subagent" && rec.header.ParentSession != "" {
		// 子代理会话＝独立目录但不独立守望：随父入账（CC subagents 路径
		// 同款分流——不 Touch、不入摆渡队、不参与闲置判定；族系判活信号
		// 待 P2-5）。
		w.harvestDshUsage(rec, info.Size(), nil)
		return
	}
	st := w.Ledger.TouchFull("dsh", rec.header.ID, gen.Path,
		statMTime(info), int(info.Size()), rec.header.Cwd, "", 0, w.StartedAt)
	w.observeRecent(st, statMTime(info)) // 重启观察窗：存量近活会话补观察
	w.harvestDshUsage(rec, info.Size(), st)
	// 不 maybeEnqueue（见文件头 P2-1 边界注）。
}

// harvestDshUsage 增量尾读 + 四列入账。st 非 nil＝主会话（台账 title/peak
// 顺带回写）；nil＝子代理（行落父 sid）。一切异常吞掉——记账永不弄断守望
// （harvestUsage 同纪律）；TailText 的结构性错误打印后照常推进（偏移只到
// 坏点前，下轮重试）。
func (w *Watcher) harvestDshUsage(rec *dshSessionRec, size int64, st *ledger.SessionState) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Printf("[harvest] dsh 用量采集失败（忽略继续）: %s: %v\n",
				filepath.Base(rec.gen.Path), r)
		}
	}()
	if w.dsh == nil {
		return // HarvestUsage 关（旧测试/隐私关形态）：只登记不采集
	}
	if size < rec.offset {
		rec.offset = 0 // 同代收缩（写方修复残尾）：从头重采（seen 保留防重账）
	}
	res := dshtrans.TailText(rec.gen.Path, rec.gen.Zstd, rec.offset)
	rows, title, _ := dshtrans.ParseChunk(res.Text, rec.title)
	sid, sub := rec.header.ID, ""
	if rec.header.Origin == "subagent" {
		sid, sub = rec.header.ParentSession, rec.header.ID // 随父入账
	}
	lineage := pathsx.NormPath(rec.gen.Path)
	for _, r := range rows {
		if rec.seen[r.Seq] {
			continue // 同文件重复事件（重放/重采）：只记首发
		}
		rec.seen[r.Seq] = true
		ts := -1.0 // 无 time → 账本盖章 now（harvestUsage 同款）
		if r.HasTS {
			ts = r.TS
		}
		if _, err := w.dsh.accounts.Record("usage", ts, accounts.Fields{
			"agent":                 "dsh",
			"session_id":            sid,
			"lineage_id":            lineage,
			"project":               rec.header.Cwd,
			"model":                 r.Model,
			"title":                 title,
			"input_tokens":          r.InputTokens,
			"cache_read_tokens":     r.CacheReadTokens,
			"cache_creation_tokens": r.CacheWriteTokens,
			"output_tokens":         r.OutputTokens,
			"offset":                int(res.NewOffset),
			"subagent":              sub, // 子会话：子 sid；主会话空串（白名单必填语义）
		}); err != nil {
			fmt.Printf("[harvest] dsh 用量采集失败（忽略继续）: %s: %v\n",
				filepath.Base(rec.gen.Path), err)
			return
		}
		if b := r.BilledInput(); b > rec.peak {
			rec.peak = b
		}
	}
	rec.title = title
	rec.offset = res.NewOffset
	if res.Err != nil {
		fmt.Printf("[harvest] dsh 尾读异常（已消费到坏点前，下轮重试）: %s: %v\n",
			filepath.Base(rec.gen.Path), res.Err)
	}
	// 主会话：标题/峰值顺带回写台账（enrich 的 dsh 替身——本采集已读盘，
	// 不再另读；锁内只有内存操作）。
	if st != nil {
		w.Ledger.Mu().Lock()
		if rec.title != "" {
			st.Title = rec.title
		}
		if rec.peak > st.PeakCtx {
			st.PeakCtx = rec.peak
		}
		w.Ledger.Mu().Unlock()
	}
}
