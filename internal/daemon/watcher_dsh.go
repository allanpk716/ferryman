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
// 与守望同键）——2026-10-03 已切接法乙（接管路由换 llm-deepseek，provider
// apply v2 补丁），走渡口的 dsh 流量每请求发该头，渡口捕获归因自此有键
//（此前 pi-ai 路不发，捕获归因恒空）。
//
// P2-1/P2-2 边界（后续票接线，本文件不预铺）：
//   - 不 maybeEnqueue：摆渡依赖渡口快照；接法乙已切（2026-10-03）后快照键
//     自此可得（llm-deepseek 路每请求带 x-deepseek-harness-session-id 头），
//     但 maybeEnqueue/enrich 的 dsh 接线留给后续票（P2-5 设计面）——本文件
//     仍不预铺；
//   - 不喂 NoteUsage/ReqClock：停车窗/判热钟是 cc 面机制（判活运行态走
//     ledger.Dsh* 方法组，票 A）；
//   - 不同模型：cc-only（ADR-0015 决定一）。问询守望（等答复窗）已于票 03
//     （dsh-heartbeat）接线——见本文件"dsh 等答复窗"节，CC 侧两道验零变化。
//
// 并发纪律（本包铁律见 windows.go 顶部）：本文件只在守望单线程跑；台账
// 锁内只有内存操作，读盘/记账一律锁外。

import (
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"ferryman/internal/accounts"
	"ferryman/internal/beat"
	"ferryman/internal/clock"
	"ferryman/internal/dshtrans"
	"ferryman/internal/ledger"
	"ferryman/internal/mathx"
	"ferryman/internal/pathsx"
)

// dshSessionRec 一个 dsh 会话目录的采集态（守望单线程读写；harvest 偏移断点
// 在账本 usage 行，此处仅进程内缓存）。
type dshSessionRec struct {
	gen    dshtrans.Generation
	header dshtrans.Header
	offset int64
	title  string
	seen   map[int64]bool // 代文件内 seq 去重（CC msgSeen 同位）
	peak   int            // 计费输入（三输入列之和）的运行峰值
	// ---- 票03（dsh-heartbeat）：检测面（与 harvest 偏移分离；fed 会话只走
	// 检测不走 harvest——检测偏移独立推进，规格「接入点」钉死） ----
	detOff int64                 // 检测专用尾读偏移
	spk    dshtrans.SpeakerState // "最后说话人"累积态（跨 chunk 携带）
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
	if win := w.dshWindows[winKey{"dsh", header.ID}]; win != nil {
		win.rec = rec // 换代重锚：在飞窗的取消跳验②改看新采集态（旧 rec 不再推进）
	}
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
// Touch 登记观察）→检测→窗机→心跳→Pin→用量采集。
//
// 票03 接入序（对齐 pollCC 的 maybeQwatch→maybeFireBeats→reconcilePins）：
// 检测在 Touch＋观察窗之后、fed 早退之前跑（fed 会话必须也检测——fed 是
// 常态）；窗机/心跳/Pin 同样在 fed 早退之前（fed 只让位 usage 文件面采集，
// 不让位窗机）；harvest 偏移与检测偏移（detOff）互不相干。
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
		// 同款分流——不 Touch、不入摆渡队、不参与闲置判定/等答复窗；族系
		// 运行态经事件面 DshChildRunSet 记子键，父窗的开窗条件
		// DshFamilyRunning 覆盖之）。
		if w.dshIsFed(rec.header.ID) {
			return // 已被事件面接管（票05 跨源去重,策略 a）：子目录文件面让位
		}
		w.harvestDshUsage(rec, info.Size(), nil)
		return
	}
	st := w.Ledger.TouchFull("dsh", rec.header.ID, gen.Path,
		statMTime(info), int(info.Size()), rec.header.Cwd, "", 0, w.StartedAt)
	w.observeRecent(st, statMTime(info)) // 重启观察窗：存量近活会话补观察
	w.dshDetect(rec, info.Size())        // 票03 检测（fed 也跑；判活文件面刷新）
	if !w.dshIsFed(rec.header.ID) {
		// 非 fed：文件面 usage 采集先行（peak 的文件面来源——开窗条件④要
		// peak，与 pollCC 的 harvestUsage→maybeQwatch 同序）。fed 会话的
		// peak 由事件面维护（dsh_receive.go DshEvent 回写），此处让位
		//（票05 跨源去重策略 a：usage 单源化；表在 Daemon.dshFed，裁定
		// 理由与残余竞窗见 dsh_dedup.go）。
		w.harvestDshUsage(rec, info.Size(), st)
	}
	w.maybeDshQwatch(st, rec) // 票03 关窗（用户侧/到期）＋开窗（四条件）
	w.maybeFireBeats(st)      // 票03 dsh 分支：到期跳（cc+dsh 同入口）
	w.reconcilePins(st)       // 票03 dsh 分支：Pin 对账
	// 不 maybeEnqueue（见文件头 P2-1 边界注）。
}

// dshIsFed 会话是否已被插件事件面接管（票05 跨源去重开关）。nil Daemon 或
// nil 表＝未接管——旧测试直构形态 fail-safe 到文件面（生产装配 serve.go:282
// 恒传 d,此 nil 位只在测试替身出现）。
func (w *Watcher) dshIsFed(sid string) bool {
	return w.Daemon != nil && w.Daemon.dshFed.has(sid)
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

// ---- dsh 等答复窗全套（票03 dsh-heartbeat，规格「dsh 等答复窗」节） ----
//
// 与 CC 问询守望（watcher.go maybeQwatch/fireOneBeat 族）同构分道：
//   - 窗态独立（dshWindows 表）：Ledger.Touch 的"任何新写入清 QWatch 窗"是
//     CC 语义（用户写入关窗），dsh 的机器侧写入（session/title、request/
//     header、compaction、turn/end、subagent/catalog 等）不清窗不取消跳——
//     关窗只认用户侧事件翻转（close_reason=user-write）或 block_s 到期
//    （close_reason=deadline，兜永不回话会话＋Pin 时限）；
//   - 取消跳＝并列两验（任一不满足即取消）：①窗口仍开着（关窗即作废整
//     计划）②检测态未翻转用户侧（开窗前提仍成立）——不复用 CC 的 mtime/
//     size 复验（机器侧写入会误伤；CC 侧两道验一行不动）；
//   - 熔断按 agent 隔离（dshBreaker）：连 MISS 只降 dsh_mode、连 ERROR 停
//     本窗剩余跳——CC 的 breaker 与 mode 零影响（D5）；
//   - 记账沿既有科目（qwatch_open/beat/qwatch_close），agent=dsh 由
//     bookQwatch 自动；close_reason 取值域 user-write|deadline（不落 CC 的
//     write）。observe 档走 NoopSender、零费入账。

// dshWindow 一条 dsh 等答复窗（守望单线程读写——与 CC 的 QWatch 字段族
// 对位：openedTS/beatsFired/plan/开窗基线，载体独立见上）。
type dshWindow struct {
	openedTS   float64
	deadlineTS float64 // openedTS+BlockS：到期自动关窗（参数断言保证全部心跳跳完在此之前）
	beatsFired int
	plan       []float64 // beatPlan(now)：开窗即排（全局 BeatIntervalS×MaxBeats 同参数起步）
	rec        *dshSessionRec
	baseMTime  float64 // 开窗基线（mtime/size 口径）——仅供记账与诊断，取消跳不复用（两验不含新鲜度）
	baseSize   int
}

// GetDshQWatchMode 读 question_watch.dsh_mode 活值（/stats 回显与守望侧共用；
// gate.go GetQWatchMode 的 dsh 对应物——同一 cfgMu 护栏纪律，dsh 独立键）。
func (d *Daemon) GetDshQWatchMode() string {
	d.cfgMu.Lock()
	defer d.cfgMu.Unlock()
	return d.Cfg.QuestionWatch.DshMode
}

// SetDshQWatchMode dsh_mode 运行时写通道（dsh 独立熔断降级 enforce→observe
// 用；SetQWatchMode 同款护栏——与 CC 的 mode 写点互不相干）。
func (d *Daemon) SetDshQWatchMode(v string) {
	d.cfgMu.Lock()
	d.Cfg.QuestionWatch.DshMode = v
	d.cfgMu.Unlock()
}

// setDshQWatchMode 守望侧 dsh_mode 写（熔断降级）：接线了 Daemon 走护栏
// 方法；旧测试形态（Daemon=nil）直写配置（setQWatchMode 同款）。
func (w *Watcher) setDshQWatchMode(v string) {
	if w.Daemon != nil {
		w.Daemon.SetDshQWatchMode(v)
		return
	}
	w.Cfg.QuestionWatch.DshMode = v
}

// qwatchModeFor 心跳模式按 agent 单源取值（票03 排程入口）：cc →
// question_watch.mode（既有 qwatchMode 活值口）；dsh → question_watch.dsh_mode
//（Daemon 护栏活值；零值容错：非 observe/enforce 一律按 off——裸构造形态
// 安全）；其他 agent → off（无窗口轨）。
func (w *Watcher) qwatchModeFor(agent string) string {
	if agent == "dsh" {
		m := w.Cfg.QuestionWatch.DshMode
		if w.Daemon != nil {
			m = w.Daemon.GetDshQWatchMode()
		}
		if m != "observe" && m != "enforce" {
			return "off"
		}
		return m
	}
	if agent != "cc" {
		return "off"
	}
	return w.qwatchMode()
}

// dshEffectiveMode dsh 侧生效 mode：enforce＋渡口关（NewWatcher 判定一次并
// 告警）→ observe 演练；off/observe 原样（waitEffectiveMode 同款）。
func (w *Watcher) dshEffectiveMode() string {
	if w.dshEnforceDowngraded && w.qwatchModeFor("dsh") == "enforce" {
		return "observe"
	}
	return w.qwatchModeFor("dsh")
}

// dshDetect 文件面"最后说话人"检测（票03 规格「检测态」）：独立偏移
//（detOff）尾读 → SpeakerState 累积推进。只更新态，不清窗不取消跳——
// 关窗判定在 maybeDshQwatch。顺带做两件事：
//   - 未接管会话的运行态刷新（ledger.DshRunRefresh 头注"票03 补"）：白名单
//     活动事件（turn/start、assistant/message）推进运行态时间戳——fed 会话
//     走事件口（dsh_receive.go），本路径覆盖文件面；两口幂等，双跑无害；
//   - 标题携带（rec.title，harvest 同字段——同文本重复解析结果幂等）。
//
// 一切异常吞掉（harvestDshUsage 同纪律）；无新增字节时 TailText 零读零推进。
func (w *Watcher) dshDetect(rec *dshSessionRec, size int64) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Printf("[qwatch] dsh 检测异常（忽略继续）: %s: %v\n",
				filepath.Base(rec.gen.Path), r)
		}
	}()
	if size < rec.detOff {
		rec.detOff = 0 // 同代收缩（写方修复残尾）：检测偏移同款从头重扫
	}
	res := dshtrans.TailText(rec.gen.Path, rec.gen.Zstd, rec.detOff)
	det := dshtrans.ParseChunkDetect(res.Text, rec.title, rec.spk)
	rec.spk = det.Speaker
	rec.title = det.Title
	if det.HasActivity {
		ts := det.LastActivityTS
		if ts <= 0 {
			ts = clock.Now() // 活动事件无 time：写入刚发生，取当下
		}
		w.Ledger.DshRunRefresh(rec.header.ID, ts)
	}
	rec.detOff = res.NewOffset
	if res.Err != nil {
		fmt.Printf("[qwatch] dsh 检测尾读异常（已消费到坏点前，下轮重试）: %s: %v\n",
			filepath.Base(rec.gen.Path), res.Err)
	}
}

// maybeDshQwatch dsh 等答复窗开窗/关窗判定（maybeQwatch 的 dsh 对位；pollDsh
// 每轮对每个主会话调用）。关窗检查先于 mode 判定（mode 运行时翻 off 也不留
// 悬窗——到期/用户侧关照常收口，Unpin 防泄漏）。开窗四条件（规格钉死）：
// ①dsh 后说（检测态）∧ ②运行态不在效（DshFamilyRunning，票 A API）∧
// ③未终结 ∧ ④peak ≥ MinCtxTokens；版本章缓存防重复判定（①④结论性盖章，
// ②③瞬态不盖——事件面可无写入翻转，idle 复位/resume 后同版本可开）。
func (w *Watcher) maybeDshQwatch(st *ledger.SessionState, rec *dshSessionRec) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Printf("[qwatch] dsh 开窗判定异常（忽略继续）: %v\n", r)
		}
	}()
	key := winKey{"dsh", st.SessionID}
	// 关窗检查（窗口开着才有）：只认用户侧翻转或 block_s 到期——机器侧写入
	//（title/request/header/compaction/turn/end/catalog…）不清窗。
	if win := w.dshWindows[key]; win != nil {
		if !rec.spk.DshSpokeLast() {
			w.closeDshWindow(st, key, "user-write")
		} else if clock.Now() >= win.deadlineTS {
			w.closeDshWindow(st, key, "deadline")
		}
		return // 已开窗：关窗只由用户侧/到期触发；重开只可能在关窗后的新版本
	}
	if w.qwatchModeFor("dsh") == "off" {
		return // off 零开销：不开新窗（关窗收口已在上方先行）
	}
	w.Ledger.Mu().Lock()
	lastWrite := st.LastWrite
	observed := st.ObservedActive
	disposed := st.DshDisposedTS != nil
	w.Ledger.Mu().Unlock()
	if w.stampGet(&w.qwatchSeen, key) == lastWrite {
		return // 该写入版本已判定过（版本章，qwatchSeen 键含 agent 不与 cc 撞）
	}
	if !observed {
		return // 与摆渡同纪律：启动后只见登记不动作（重启观察窗内的除外）
	}
	if !rec.spk.DshSpokeLast() {
		w.stampSet(&w.qwatchSeen, key, lastWrite) // 条件①结论性不满足：随版本缓存
		return
	}
	if disposed {
		return // 条件③瞬态（resume 事件可清，不盖版本章）
	}
	if w.Ledger.DshFamilyRunning(st.SessionID) {
		return // 条件②瞬态（idle/失效上界后同版本可开，不盖版本章）
	}
	th := w.Cfg.ThresholdFor("dsh")
	w.Ledger.Mu().Lock()
	peak := st.PeakCtx
	w.Ledger.Mu().Unlock()
	if peak < th.MinCtxTokens {
		w.stampSet(&w.qwatchSeen, key, lastWrite) // 条件④结论性：peak 只随新写入增长
		return
	}
	// 开窗（守望单线程原子；CC 侧的开窗临界区为双锁 check-then-act——那是
	// 因为 HTTP 线程可并发开停车窗，dsh 窗无跨线程写者，台账读已各自短锁）。
	now := clock.Now()
	win := &dshWindow{openedTS: now, deadlineTS: now + th.BlockS,
		plan: w.beatPlan(now), rec: rec}
	w.Ledger.Mu().Lock()
	win.baseMTime, win.baseSize = st.LastWrite, st.Size
	// 观测面镜像（DshQWatch* 字段族头注）：开窗清零重排——/beats、/session
	// 只读面自此能看见本窗。
	st.DshQWatchOpenedTS = &now
	st.DshQWatchBeatsFired = 0
	st.DshQWatchPlanned = len(win.plan)
	w.Ledger.Mu().Unlock()
	w.dshWindows[key] = win
	if w.DshQWatchStats != nil { // dsh 泳道计数器（/stats qw["dsh"]）
		w.DshQWatchStats.RecordWindowOpened()
	}
	w.dshSetPin(key, st.SessionID, true) // 窗开即 Pin（快照保活；关窗路径 Unpin）
	// unit_count＝qwatch_open 白名单必填键的 dsh 形态：dsh 无提问潮计数
	//（扳机是"最后说话人"检测态而非提问潮），恒 0 如实记（账本包票外不可
	// 改白名单；升档指标全部出自 qwatch_close/beat 行，不受此键影响）。
	w.bookQwatch("qwatch_open", st, accounts.Fields{"unit_count": 0, "prefix_tokens": peak})
}

// closeDshWindow 关窗（幂等：无窗即回）：删窗（计划随窗作废——dsh 计划的
// 唯一载体就是窗记录）→ Unpin（与 reconcilePins 同一 lanePins 账）→
// qwatch_close 入账（close_reason ∈ {user-write, deadline}，规格钉死不落
// CC 的 write）→ 盖版本章（关窗版本不再重判：到期关窗时版本未变，不盖章
// 下一轮四条件仍全真会立刻重开窗——deadline 是"兜永不回话会话＋Pin 时限"，
// 重开即无限开窗循环；用户侧关窗的版本本就判负，盖章幂等）。新写入（新
// 版本）照常重判。窗口到期上界同时是 Pin 时限（防泄漏）。
func (w *Watcher) closeDshWindow(st *ledger.SessionState, key winKey, reason string) {
	win := w.dshWindows[key]
	if win == nil {
		return
	}
	delete(w.dshWindows, key)
	w.dshSetPin(key, st.SessionID, false)
	w.Ledger.Mu().Lock()
	st.DshQWatchOpenedTS = nil // 观测面镜像：窗关即隐（BeatsFired 留终值、Planned 清零）
	st.DshQWatchPlanned = 0
	lastWrite := st.LastWrite
	w.Ledger.Mu().Unlock()
	w.stampSet(&w.qwatchSeen, key, lastWrite)
	now := clock.Now()
	w.bookQwatch("qwatch_close", st, accounts.Fields{
		"opened_ts":    mathx.Round(win.openedTS, 3),
		"closed_ts":    mathx.Round(now, 3),
		"dur_s":        mathx.Round(math.Max(0.0, now-win.openedTS), 1),
		"beats_fired":  win.beatsFired,
		"close_reason": reason,
	})
}

// dshSetPin dsh 窗的 Pin/Unpin（lanePins 表＋渡口缝，reconcilePins 的 dsh 分
// 支与关窗路径共用同一账——状态一致，互为对账，幂等）。
func (w *Watcher) dshSetPin(key winKey, sid string, want bool) {
	w.stampMu.Lock()
	if w.lanePins[key] == want {
		w.stampMu.Unlock()
		return
	}
	if want {
		w.lanePins[key] = true
	} else {
		delete(w.lanePins, key)
	}
	w.stampMu.Unlock()
	if want {
		w.dockPin(sid)
	} else {
		w.dockUnpin(sid)
	}
}

// maybeFireDshBeats dsh 轨心跳到期扫描（maybeFireBeats 的 dsh 分支；计划在
// 独立窗表）。一轮至多一发（CC 同款全局串行节奏）。
func (w *Watcher) maybeFireDshBeats(st *ledger.SessionState) {
	key := winKey{"dsh", st.SessionID}
	win := w.dshWindows[key]
	if win == nil {
		return
	}
	now := clock.Now()
	found, minDue := false, 0.0
	for _, t := range win.plan {
		if t <= now && (!found || t < minDue) {
			found, minDue = true, t
		}
	}
	if found {
		w.fireOneDshBeat(st, win, minDue)
	}
}

// fireOneDshBeat 单跳（fireOneBeat 的 dsh 对位）：并列两验（窗口仍开 ∧ 检测
// 态未翻转用户侧；**不复用 mtime/size 复验**——机器侧写入会误伤，CC 的两道
// 验一行不动）＋全局在途占用 → 锁外发送 → 结账。窗表守望单线程，无跨线程
// 写者（CC 侧持台账锁串行的对位物即本形态）。
func (w *Watcher) fireOneDshBeat(st *ledger.SessionState, win *dshWindow, beatTS float64) {
	key := winKey{"dsh", st.SessionID}
	if w.dshWindows[key] == nil {
		return // 验①：窗已被关（关窗即作废整计划——计划随窗记录删除）
	}
	if win.rec == nil || !win.rec.spk.DshSpokeLast() {
		// 验②：检测态已翻转用户侧（开窗前提不成立）——按用户侧关窗收口
		//（maybeDshQwatch 的关窗检查同源判据；此处为规格要求的跳前复验）。
		w.closeDshWindow(st, key, "user-write")
		fmt.Printf("[qwatch] dsh 跳取消：检测态已翻转用户侧（%s），整计划作废\n",
			runeCap8(st.SessionID))
		return
	}
	if w.beatInFlight.Load() {
		return // 全局同时最多 1 跳在途（跨会话/跨泳道串行；下轮再试）
	}
	for i, t := range win.plan { // Python qwatch_plan.remove(beat_ts) 同位
		if t == beatTS {
			win.plan = append(win.plan[:i:i], win.plan[i+1:]...)
			break
		}
	}
	win.beatsFired++
	w.Ledger.Mu().Lock() // 观测面镜像：逐跳递增/递减（锁内只内存操作）
	st.DshQWatchBeatsFired = win.beatsFired
	if st.DshQWatchPlanned > 0 {
		st.DshQWatchPlanned--
	}
	w.Ledger.Mu().Unlock()
	w.beatInFlight.Store(true)
	defer w.beatInFlight.Store(false) // fireOneBeat 同款（跨临界区复位，atomic 防 -race）
	result := w.sendDshBeat(st, win, beatTS) // 网络绝不持台账锁
	w.settleDshBeat(st, win, result)
}

// sendDshBeat 选发送器（sendBeat 的 dsh 对位）：observe/启动降级 → NoopSender
//（零网络零费）；enforce → 注入的真实 sender（头分流在票02：dock 按
// SessionID 形状发 x-deepseek-harness-session-id，本票不碰 beat 包）；未注入
// 则告警一次并按 observe 演练。
func (w *Watcher) sendDshBeat(st *ledger.SessionState, win *dshWindow, beatTS float64) beat.BeatResult {
	w.Ledger.Mu().Lock()
	plan := beat.BeatPlan{
		Agent: st.Agent, SessionID: st.SessionID,
		TranscriptPath: st.TranscriptPath,
		OpenedTS:       win.openedTS,
		LastWrite:      st.LastWrite,
		Size:           st.Size,
		BeatIndex:      win.beatsFired,
		BeatTS:         beatTS,
	}
	w.Ledger.Mu().Unlock()
	mode := w.dshEffectiveMode()
	if mode == "enforce" && w.BeatSender != nil {
		var r beat.BeatResult
		func() { // 发送器炸掉按一跳 ERROR 记（sendBeat 同口径）
			defer func() {
				if p := recover(); p != nil {
					r = beat.BeatResult{Sent: true, OK: false,
						Err: fmt.Sprintf("sender-raise:%T", p)}
				}
			}()
			r = w.BeatSender.Send(plan)
		}()
		return r
	}
	if mode == "enforce" && !w.dshDrillWarned.Swap(true) { // 只告警一次
		fmt.Printf("[qwatch] ⚠ dsh_mode=enforce 但未注入真实 BeatSender" +
			"——dsh 心跳按 observe 演练记账\n")
	}
	return w.noopSender.Send(plan)
}

// settleDshBeat 结账（settleBeat 的 dsh 对位）：逐跳入账（lane=qwatch、
// agent=dsh 自动；observe 演练零费）→ **dsh 独立断路器**（w.dshBreaker——
// CC 的 breaker 与 mode 零影响）→ 动作：demote 只降 dsh_mode（护栏通道），
// pause 停本窗剩余跳（窗口不关）。dsh 泳道计数器（DshQWatchStats——与 CC 的
// QWatchStats 分账，CC /stats 面零变化的原约束不动；dsh 指标在 qw["dsh"]
// 子块回显，账本行仍是跨面事实源）。
func (w *Watcher) settleDshBeat(st *ledger.SessionState, win *dshWindow, result beat.BeatResult) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Printf("[qwatch] dsh 心跳结账异常（忽略）: %v\n", r)
		}
	}()
	w.noteUpstreamRequest(st.SessionID, result) // 票02 F3：真发重放计入判热时钟
	outcome := beat.Classify(result)
	w.bookBeat(st, outcome, result, "qwatch")
	if w.DshQWatchStats != nil {
		w.DshQWatchStats.RecordBeat(outcome, result.CostActual)
	}
	action := w.dshBreaker.Record(outcome)
	if action == "demote" && w.qwatchModeFor("dsh") == "enforce" {
		w.setDshQWatchMode("observe") // 安全降级只降 dsh_mode（CC mode 不动）；人工复核后拨回
		w.qwatchAlert(st, "dsh 问询守望熔断降级",
			fmt.Sprintf("连续 %d 跳 MISS，dsh_mode 已自动 enforce→observe（人工复核 observe 数据后拨回）",
				beat.MissLimit))
	} else if action == "pause" {
		win.plan = nil // 暂停本窗剩余跳（窗口本身不关）
		w.Ledger.Mu().Lock()
		st.DshQWatchPlanned = 0 // 观测面镜像随计划作废清零
		w.Ledger.Mu().Unlock()
		w.qwatchAlert(st, "dsh 问询守望错误熔断",
			fmt.Sprintf("连续 %d 跳 ERROR，已暂停当前窗口剩余 dsh 心跳", beat.ErrorLimit))
	}
}
