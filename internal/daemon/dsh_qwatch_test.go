package daemon

// dsh_qwatch_test.go — dsh-heartbeat 票03「dsh 等答复窗全套」：检测接线、
// 四条件开窗、关窗二分（用户侧关 vs 机器侧不清窗）、到期关窗、取消跳两验、
// 排程入口放宽、分档三态、熔断隔离、Pin 对账、记账（agent=dsh）。
//
// 规格：.scratch/dsh-heartbeat/spec.md「dsh 等答复窗」节。夹具复用
// dsh_wiring_test.go（writeDshSession/zstdBatches）与 test_qwatch_scheduler_
// test.go（recordingSender/beatMiss/beatErr）。
//
// 时间纪律：clock.Now 真实时钟；文件 mtime 经真实追加推进（与生产 pollDsh
// 同口径）。到期跳以直摆 dshWindows[key].plan 到过去时刻驱动（openWindow/
// setPlan 的 CC 同位手法）；到期关窗另有一例走真实小 BlockS 睡眠。

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"ferryman/internal/accounts"
	"ferryman/internal/beat"
	"ferryman/internal/clock"
	"ferryman/internal/config"
	"ferryman/internal/dshtrans"
	"ferryman/internal/ledger"
	"ferryman/internal/pathsx"
)

// ---- 夹具 ----

// nowMS 当前时刻毫秒（dsh 事件行 time 口径）。
func nowMS() int64 { return int64(clock.Now() * 1000) }

// dshHeaderFor 主会话头行（任意 id；cwd C:\proj）。
func dshHeaderFor(id string) string {
	return `{"type":"session","version":4,"id":"` + id + `","createdAt":1790905220031,"cwd":"C:\\proj","isSeeded":false,"delegationDepth":0}` + "\n"
}

// dshEv 事件行构造（ms=毫秒时间戳）。
func dshEv(typ string, seq int, ms int64, extra string) string {
	return `{"type":"` + typ + `","seq":` + strconv.Itoa(seq) + `,"time":` + strconv.FormatInt(ms, 10) + `,"data":{` + extra + `}}` + "\n"
}

// dshQwUserSide 用户侧批：turn/start + user/message（认领排队输入＝用户侧）。
func dshQwUserSide(seqBase int, ms int64) string {
	return dshEv("turn/start", seqBase, ms, `"turn":1`) +
		dshEv("user/message", seqBase+1, ms+100, `"message":{"role":"user"}`)
}

// dshQwAsstUsage dsh 侧批：assistant/message 带 usage（peak 来源；末条=assistant）。
func dshQwAsstUsage(seq int, ms int64) string {
	return `{"type":"assistant/message","seq":` + strconv.Itoa(seq) + `,"time":` + strconv.FormatInt(ms, 10) +
		`,"data":{"message":{"role":"assistant","source":{"kind":"model","model":"glm-5.3"}},` +
		`"usage":{"inputTokens":1000,"outputTokens":50,"cacheReadTokens":20000}}}` + "\n"
}

// dshQwMachineSide 机器侧批（规格钉死不清窗的事件族）：session/title、
// request/header、compaction/start、turn/end、subagent/catalog。
func dshQwMachineSide(seqBase int, ms int64) string {
	return dshEv("session/title", seqBase, ms, `"title":"机器侧"`) +
		dshEv("request/header", seqBase+1, ms+100, `"x":1`) +
		dshEv("compaction/start", seqBase+2, ms+200, `"x":1`) +
		dshEv("turn/end", seqBase+3, ms+300, `"x":1`) +
		dshEv("subagent/catalog", seqBase+4, ms+400, `"childId":"session-c","childCreatedAt":1,"mode":"one-shot"`)
}

// newDshQwFixture 全套装配：真 Daemon（fed 表/Pin 面经真路径）＋真账本＋
// Pin 记录器（dockPin/dockUnpin 缝替换）。sender nil＝observe 演练形态。
func newDshQwFixture(t *testing.T, root string, sender beat.Sender) (*Watcher, *Daemon, *accounts.Accounts, *[]string) {
	t.Helper()
	acc, err := accounts.New(filepath.Join(t.TempDir(), "acc"))
	if err != nil {
		t.Fatal(err)
	}
	led := ledger.New()
	cfg := config.Default()
	cfg.Watch.DshSessionsDir = root
	cfg.Thresholds = config.ThresholdCfg{SummarizeS: 900, BlockS: 1200, MinCtxTokens: 1000}
	cfg.QuestionWatch.Mode = "observe" // CC 侧独立开关置 observe（熔断隔离断言的对照面）
	cfg.QuestionWatch.DshMode = "observe"
	d := NewDaemon(cfg, led, nil, func(*ledger.SessionState) bool { return true }, acc, 0, nil)
	w := NewWatcher(cfg, led, nil, func(*ledger.SessionState) bool { return true },
		0, acc, d, sender, nil)
	pins := &[]string{}
	w.dockPin = func(sid string) { *pins = append(*pins, "pin:"+sid) }
	w.dockUnpin = func(sid string) { *pins = append(*pins, "unpin:"+sid) }
	return w, d, acc, pins
}

// dshSessionDirOf writeDshSession 产出的会话目录（dshSessions 表键）。
func dshSessionDirOf(genPath string) string {
	return pathsx.NormPath(filepath.Dir(genPath))
}

// dshForceDue 直摆 dsh 窗计划到过去时刻（CC openWindow/setPlan 同位手法）。
func dshForceDue(w *Watcher, key winKey, n int) {
	if win := w.dshWindows[key]; win != nil {
		plan := make([]float64, n)
		for i := range plan {
			plan[i] = clock.Now() - 1
		}
		win.plan = plan
	}
}

func pinCount(pins *[]string, want string) int {
	n := 0
	for _, p := range *pins {
		if p == want {
			n++
		}
	}
	return n
}

// statOr os.Stat 的测试形（失败即 Fatal）。
func statOr(t *testing.T, p string) os.FileInfo {
	t.Helper()
	info, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	return info
}

// appendDshBatch 向 zstd 代文件追加一批（每批一帧）。
func appendDshBatch(t *testing.T, p, batch string) {
	t.Helper()
	f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(zstdBatches(batch)); err != nil {
		t.Fatal(err)
	}
	f.Close()
}

// ---- 检测接线：Touch＋观察窗之后、fed 早退之前；偏移独立 ----

func TestDshDetectRunsForFedWithIndependentOffset(t *testing.T) {
	// fed 会话（事件面接管）：检测照跑（detOff 推进）、harvest 让位（offset
	// 恒 0、文件面零 usage 行）；peak 由事件面维护（DshEvent 的 peak 回写），
	// 窗机照常接线（fed 是常态）。
	root := t.TempDir()
	w, d, acc, _ := newDshQwFixture(t, root, nil)
	p := writeDshSession(t, root, dshMainID, dshHeaderFor(dshMainID),
		dshQwUserSide(2, nowMS())+dshQwAsstUsage(4, nowMS()+1000))
	// 事件面接管：一条带 usage 的 assistant/message（peak 回写＋fed 标记）。
	d.DshEvent(map[string]any{
		"session_id": dshMainID, "event": "assistant/message",
		"time": float64(nowMS()),
		"usage": map[string]any{"input_tokens": 1000, "cache_read_tokens": 20000,
			"cache_creation_tokens": 0, "output_tokens": 50},
	})
	w.pollDsh()
	rec := w.dshSessions[dshSessionDirOf(p)]
	if rec == nil {
		t.Fatal("会话采集态缺失")
	}
	if rec.detOff == 0 || rec.offset != 0 {
		t.Fatalf("检测偏移 %d / harvest 偏移 %d——fed 应检测推进、harvest 恒 0", rec.detOff, rec.offset)
	}
	if !rec.spk.DshSpokeLast() {
		t.Fatalf("检测态应 dsh 后说: %+v", rec.spk)
	}
	if rows := dshUsageRows(t, acc); len(rows) != 1 {
		t.Fatalf("usage 行 = %d（1 条事件行；文件面让位不双计）", len(rows))
	}
	if w.dshWindows[winKey{"dsh", dshMainID}] == nil {
		t.Fatal("fed 会话也应开窗（窗机在 fed 早退之前接线）")
	}
}

// ---- 开窗：四条件＋字段族＋版本章 ----

func TestDshQwatchWindowOpensFieldsAndBooks(t *testing.T) {
	root := t.TempDir()
	w, _, acc, pins := newDshQwFixture(t, root, nil)
	writeDshSession(t, root, dshMainID, dshHeaderFor(dshMainID),
		dshQwUserSide(2, nowMS())+dshQwAsstUsage(4, nowMS()+1000))
	w.pollDsh()
	key := winKey{"dsh", dshMainID}
	win := w.dshWindows[key]
	if win == nil {
		t.Fatal("四条件全真（dsh 后说∧不在跑∧未终结∧peak 达标）应开窗")
	}
	if win.beatsFired != 0 {
		t.Fatalf("beats_fired = %d, want 0", win.beatsFired)
	}
	if len(win.plan) != 2 || win.plan[0] != win.openedTS+420 || win.plan[1] != win.openedTS+840 {
		t.Fatalf("plan = %v（beatPlan 全局参数 420s×2，首跳 t0+interval）", win.plan)
	}
	if win.deadlineTS != win.openedTS+1200 {
		t.Fatalf("deadline = %v, want openedTS+BlockS(1200)", win.deadlineTS)
	}
	rows := acc.Read(accounts.ReadOpts{Kind: "qwatch_open"})
	if len(rows) != 1 || rows[0]["agent"] != "dsh" || rows[0]["session_id"] != dshMainID {
		t.Fatalf("qwatch_open 行 = %v", rows)
	}
	if rows[0]["prefix_tokens"] != float64(21000) {
		t.Fatalf("prefix_tokens = %v, want 21000（1000+20000）", rows[0]["prefix_tokens"])
	}
	if pinCount(pins, "pin:"+dshMainID) != 1 {
		t.Fatalf("开窗应 Pin（恰一次）: %v", *pins)
	}
}

func TestDshQwatchOpenConditionsNegatives(t *testing.T) {
	// 真值表负例：①用户后说（结论性盖章）②运行态在效（瞬态不盖章，idle 后
	// 同版本可开）③已终结（瞬态，清后同版本可开）④peak 不足（结论性盖章）。
	root := t.TempDir()
	w, _, _, _ := newDshQwFixture(t, root, nil)
	// ① 用户后说：末条 user/message，无 assistant。
	pUser := writeDshSession(t, root, "session-aaaa1", dshHeaderFor("session-aaaa1"),
		dshQwUserSide(2, nowMS()))
	// ② dsh 后说但运行态在效；③ dsh 后说但已终结；④ dsh 后说但 peak 不足。
	pRun := writeDshSession(t, root, "session-bbbb2", dshHeaderFor("session-bbbb2"),
		dshQwUserSide(2, nowMS())+dshQwAsstUsage(4, nowMS()+1000))
	pDisp := writeDshSession(t, root, "session-cccc3", dshHeaderFor("session-cccc3"),
		dshQwUserSide(2, nowMS())+dshQwAsstUsage(4, nowMS()+1000))
	writeDshSession(t, root, "session-dddd4", dshHeaderFor("session-dddd4"),
		dshQwUserSide(2, nowMS())+
			`{"type":"assistant/message","seq":4,"time":`+strconv.FormatInt(nowMS(), 10)+
			`,"data":{"usage":{"inputTokens":10,"outputTokens":1}}}`+"\n")
	// 预登记②③（DshMainRunSet/DshMainDisposed 需台账有条目）再置态。
	w.Ledger.TouchFull("dsh", "session-bbbb2", pRun, clock.Now(), int(statOr(t, pRun).Size()), "", "", 0, 0)
	w.Ledger.DshMainRunSet("session-bbbb2", true, clock.Now())
	w.Ledger.TouchFull("dsh", "session-cccc3", pDisp, clock.Now(), int(statOr(t, pDisp).Size()), "", "", 0, 0)
	w.Ledger.DshMainDisposed("session-cccc3", clock.Now())
	_ = pUser

	w.pollDsh()
	for _, sid := range []string{"session-aaaa1", "session-bbbb2", "session-cccc3", "session-dddd4"} {
		if w.dshWindows[winKey{"dsh", sid}] != nil {
			t.Fatalf("%s 不满足四条件不应开窗", sid)
		}
	}
	// 结论性负例盖版本章（用户后说/peak 不足）；瞬态负例不盖（运行/终结）。
	if got := w.stampGet(&w.qwatchSeen, winKey{"dsh", "session-aaaa1"}); got != w.Ledger.Get("dsh", "session-aaaa1").LastWrite {
		t.Fatalf("用户后说应盖版本章: %v", got)
	}
	if got := w.stampGet(&w.qwatchSeen, winKey{"dsh", "session-dddd4"}); got != w.Ledger.Get("dsh", "session-dddd4").LastWrite {
		t.Fatalf("peak 不足应盖版本章: %v", got)
	}
	if got := w.stampGet(&w.qwatchSeen, winKey{"dsh", "session-bbbb2"}); got != 0 {
		t.Fatalf("运行态在效应为瞬态（不盖章）: %v", got)
	}
	// 瞬态解除后同版本可开：idle 复位 / resume 清终结。
	w.Ledger.DshMainRunSet("session-bbbb2", false, clock.Now())
	w.Ledger.DshDisposedClear("session-cccc3")
	w.pollDsh()
	if w.dshWindows[winKey{"dsh", "session-bbbb2"}] == nil {
		t.Fatal("idle 复位后同版本应可开窗（瞬态不盖章）")
	}
	if w.dshWindows[winKey{"dsh", "session-cccc3"}] == nil {
		t.Fatal("终结态清除后同版本应可开窗（瞬态不盖章）")
	}
}

func TestDshQwatchModeOffNeverOpens(t *testing.T) {
	root := t.TempDir()
	w, d, _, _ := newDshQwFixture(t, root, nil)
	d.SetDshQWatchMode("off")
	writeDshSession(t, root, dshMainID, dshHeaderFor(dshMainID),
		dshQwUserSide(2, nowMS())+dshQwAsstUsage(4, nowMS()+1000))
	w.pollDsh()
	if w.dshWindows[winKey{"dsh", dshMainID}] != nil {
		t.Fatal("dsh_mode=off 不应开窗（默认零行为）")
	}
}

// ---- 关窗二分：用户侧关 / 机器侧不清窗 ----

func TestDshWindowClosedByUserSideWrite(t *testing.T) {
	root := t.TempDir()
	w, _, acc, pins := newDshQwFixture(t, root, nil)
	p := writeDshSession(t, root, dshMainID, dshHeaderFor(dshMainID),
		dshQwUserSide(2, nowMS())+dshQwAsstUsage(4, nowMS()+1000))
	w.pollDsh()
	key := winKey{"dsh", dshMainID}
	if w.dshWindows[key] == nil {
		t.Fatal("前提：应已开窗")
	}
	appendDshBatch(t, p, dshQwUserSide(10, nowMS())) // 用户侧新回合
	w.pollDsh()
	if w.dshWindows[key] != nil {
		t.Fatal("用户侧事件到达应关窗（close_reason=user-write）")
	}
	rows := acc.Read(accounts.ReadOpts{Kind: "qwatch_close"})
	if len(rows) != 1 || rows[0]["close_reason"] != "user-write" || rows[0]["agent"] != "dsh" {
		t.Fatalf("qwatch_close 行 = %v", rows)
	}
	for _, k := range []string{"opened_ts", "closed_ts", "dur_s", "beats_fired"} {
		if _, ok := rows[0][k]; !ok {
			t.Fatalf("close 行缺 %s: %v", k, rows[0])
		}
	}
	if pinCount(pins, "unpin:"+dshMainID) != 1 {
		t.Fatalf("关窗应 Unpin: %v", *pins)
	}
	// 对偶：新版本（dsh 再答一条带 usage 的消息）→ 重开新窗（关窗版本章只
	// 挡同版本，新写入照常重判）。
	appendDshBatch(t, p, dshQwAsstUsage(20, nowMS()))
	w.pollDsh()
	win2 := w.dshWindows[key]
	if win2 == nil {
		t.Fatal("新版本 dsh 后说应重开新窗")
	}
	if win2.openedTS <= rows[0]["opened_ts"].(float64) {
		t.Fatal("重开应重新计时（新 openedTS）")
	}
	if orows := acc.Read(accounts.ReadOpts{Kind: "qwatch_open"}); len(orows) != 2 {
		t.Fatalf("qwatch_open 行 = %d, want 2（关后重开）", len(orows))
	}
}

func TestDshWindowMachineSideWriteKeepsWindowAndBeat(t *testing.T) {
	root := t.TempDir()
	w, _, acc, _ := newDshQwFixture(t, root, nil)
	p := writeDshSession(t, root, dshMainID, dshHeaderFor(dshMainID),
		dshQwUserSide(2, nowMS())+dshQwAsstUsage(4, nowMS()+1000))
	w.pollDsh()
	key := winKey{"dsh", dshMainID}
	win := w.dshWindows[key]
	if win == nil {
		t.Fatal("前提：应已开窗")
	}
	baseLW := w.Ledger.Get("dsh", dshMainID).LastWrite
	// 机器侧写入批：title/request/header/compaction/turn/end/catalog。
	appendDshBatch(t, p, dshQwMachineSide(10, nowMS()))
	w.pollDsh()
	if w.dshWindows[key] == nil {
		t.Fatal("机器侧写入不清窗（关窗只认用户侧/到期——规格钉死）")
	}
	if lw := w.Ledger.Get("dsh", dshMainID).LastWrite; lw <= baseLW {
		t.Fatalf("前提失效：机器侧写入应推进 last_write（%v → %v）", baseLW, lw)
	}
	// 取消跳两验不含 mtime/size：last_write 已变，到期跳照发。
	dshForceDue(w, key, 1)
	w.pollDsh()
	if win.beatsFired != 1 || len(win.plan) != 0 {
		t.Fatalf("机器侧写入后到期跳仍应发: fired=%d plan=%v", win.beatsFired, win.plan)
	}
	brows := acc.Read(accounts.ReadOpts{Kind: "beat"})
	if len(brows) != 1 || brows[0]["agent"] != "dsh" || brows[0]["lane"] != "qwatch" ||
		brows[0]["outcome"] != "observe" || brows[0]["cost_actual"] != float64(0) {
		t.Fatalf("beat 行 = %v（observe 演练零费、agent=dsh、lane=qwatch）", brows)
	}
	if rows := acc.Read(accounts.ReadOpts{Kind: "qwatch_close"}); len(rows) != 0 {
		t.Fatalf("机器侧写入不应关账: %v", rows)
	}
}

// ---- block_s 到期自动关窗 ----

func TestDshWindowDeadlineAutoClose(t *testing.T) {
	root := t.TempDir()
	w2, _, acc2, pins2 := newDshQwFixture(t, root, nil)
	w2.Cfg.Thresholds.BlockS = 0.5 // 真实小 BlockS：真实到期路径
	writeDshSession(t, root, "session-eeee5", dshHeaderFor("session-eeee5"),
		dshQwUserSide(2, nowMS())+dshQwAsstUsage(4, nowMS()+1000))
	w2.pollDsh()
	k2 := winKey{"dsh", "session-eeee5"}
	if w2.dshWindows[k2] == nil {
		t.Fatal("前提：小 BlockS 会话应开窗")
	}
	time.Sleep(1200 * time.Millisecond)
	w2.pollDsh()
	if w2.dshWindows[k2] != nil {
		t.Fatal("block_s 到期应自动关窗")
	}
	rows := acc2.Read(accounts.ReadOpts{Kind: "qwatch_close"})
	if len(rows) != 1 || rows[0]["close_reason"] != "deadline" {
		t.Fatalf("close 行 = %v（deadline，绝不落 write）", rows)
	}
	if pinCount(pins2, "unpin:session-eeee5") != 1 {
		t.Fatalf("到期关窗应 Unpin: %v", *pins2)
	}
	// 到期关后不重开（deadline 是兜底收口——重开即无限开窗循环）：版本未变，
	// 下一轮判定被关窗版本章挡住。
	w2.pollDsh()
	if w2.dshWindows[k2] != nil {
		t.Fatal("到期关窗后同版本不应重开")
	}
	if orows := acc2.Read(accounts.ReadOpts{Kind: "qwatch_open"}); len(orows) != 1 {
		t.Fatalf("不应出现第二个 qwatch_open 行: %d", len(orows))
	}
}

// ---- 取消跳两验：窗口关→作废；检测态翻转→作废 ----

func TestDshBeatCancelledWhenWindowClosed(t *testing.T) {
	// 验①：窗口已关（记录删）→ 到期跳不发（计划随窗作废）。
	root := t.TempDir()
	w, _, acc, _ := newDshQwFixture(t, root, nil)
	writeDshSession(t, root, dshMainID, dshHeaderFor(dshMainID),
		dshQwUserSide(2, nowMS())+dshQwAsstUsage(4, nowMS()+1000))
	w.pollDsh()
	key := winKey{"dsh", dshMainID}
	w.dshWindows[key].deadlineTS = clock.Now() - 1
	w.pollDsh() // 到期关窗
	if w.dshWindows[key] != nil {
		t.Fatal("前提：应已关窗")
	}
	w.pollDsh() // 计划已随窗作废——无跳可发
	if rows := acc.Read(accounts.ReadOpts{Kind: "beat"}); len(rows) != 0 {
		t.Fatalf("关窗后不应发跳: %v", rows)
	}
	// 关窗版本不再重判（到期收口，防无限开窗循环）。
	w.pollDsh()
	if w.dshWindows[key] != nil {
		t.Fatal("关窗后同版本不应重开")
	}
}

func TestDshBeatCancelledWhenSpeakerFlipped(t *testing.T) {
	// 验②：检测态翻转用户侧 → 关窗＋整计划作废（开窗前提不成立）。
	root := t.TempDir()
	w, _, acc, _ := newDshQwFixture(t, root, nil)
	writeDshSession(t, root, dshMainID, dshHeaderFor(dshMainID),
		dshQwUserSide(2, nowMS())+dshQwAsstUsage(4, nowMS()+1000))
	w.pollDsh()
	key := winKey{"dsh", dshMainID}
	if w.dshWindows[key] == nil {
		t.Fatal("前提：应已开窗")
	}
	// 直改检测态（用户侧翻转）——本轮 poll 的关窗检查即关。
	rec := w.dshSessions[dshSessionDirOf(w.Ledger.Get("dsh", dshMainID).TranscriptPath)]
	rec.spk = dshtrans.SpeakerState{LastUserSeq: 99, LastAssistantSeq: 4}
	w.pollDsh()
	if w.dshWindows[key] != nil {
		t.Fatal("检测态翻转用户侧应关窗")
	}
	if rows := acc.Read(accounts.ReadOpts{Kind: "beat"}); len(rows) != 0 {
		t.Fatalf("前提翻转后不应发跳: %v", rows)
	}
}

// ---- 排程入口放宽＋分档＋熔断隔离 ----

func TestDshBeatsFireThroughSchedulingEntry(t *testing.T) {
	// dsh 会话经同一心跳调度入口（maybeFireBeats）到期发跳；一轮至多一发。
	root := t.TempDir()
	w, _, acc, _ := newDshQwFixture(t, root, nil)
	writeDshSession(t, root, dshMainID, dshHeaderFor(dshMainID),
		dshQwUserSide(2, nowMS())+dshQwAsstUsage(4, nowMS()+1000))
	w.pollDsh()
	key := winKey{"dsh", dshMainID}
	win := w.dshWindows[key]
	dshForceDue(w, key, 2)
	w.pollDsh()
	if win.beatsFired != 1 || len(win.plan) != 1 {
		t.Fatalf("fired=%d plan=%v（一轮至多一发）", win.beatsFired, win.plan)
	}
	w.pollDsh()
	if win.beatsFired != 2 || len(win.plan) != 0 {
		t.Fatalf("fired=%d plan=%v（两跳发完）", win.beatsFired, win.plan)
	}
	if rows := acc.Read(accounts.ReadOpts{Kind: "beat"}); len(rows) != 2 {
		t.Fatalf("beat 行 = %d, want 2", len(rows))
	}
}

func TestQwatchModeForAgentsSingleSource(t *testing.T) {
	root := t.TempDir()
	w, d, _, _ := newDshQwFixture(t, root, nil)
	if got := w.qwatchModeFor("cc"); got != "observe" {
		t.Fatalf("cc → %q（question_watch.mode）", got)
	}
	if got := w.qwatchModeFor("dsh"); got != "observe" {
		t.Fatalf("dsh → %q（dsh_mode）", got)
	}
	if got := w.qwatchModeFor("codex"); got != "off" {
		t.Fatalf("codex → %q, want off", got)
	}
	// 零值容错：裸构造形态 DshMode=""（空）按 off。
	d.Cfg.QuestionWatch.DshMode = ""
	if got := w.qwatchModeFor("dsh"); got != "off" {
		t.Fatalf("零值 dsh_mode → %q, want off", got)
	}
	// Daemon 护栏写通道：dsh 独立，CC mode 不动。
	d.SetDshQWatchMode("enforce")
	if got := w.qwatchModeFor("dsh"); got != "enforce" {
		t.Fatalf("SetDshQWatchMode 后 → %q", got)
	}
	if d.GetQWatchMode() != "observe" {
		t.Fatalf("CC mode 被误动: %q", d.GetQWatchMode())
	}
}

func TestDshBreakerIsolation(t *testing.T) {
	// dsh 独立断路器：连续 2 MISS 只降 dsh_mode（CC mode 与 CC breaker 零影响）；
	// 连续 3 ERROR 停本窗剩余跳（窗口不关、不降级）。
	root := t.TempDir()
	rec := &recordingSender{results: []beat.BeatResult{beatMiss, beatMiss}}
	w, d, _, _ := newDshQwFixture(t, root, rec)
	d.SetDshQWatchMode("enforce")
	writeDshSession(t, root, dshMainID, dshHeaderFor(dshMainID),
		dshQwUserSide(2, nowMS())+dshQwAsstUsage(4, nowMS()+1000))
	w.pollDsh()
	key := winKey{"dsh", dshMainID}
	win := w.dshWindows[key]
	if win == nil {
		t.Fatal("前提：应已开窗")
	}
	// 两连 MISS → demote（只降 dsh_mode）。
	dshForceDue(w, key, 1)
	w.pollDsh()
	dshForceDue(w, key, 1)
	w.pollDsh()
	if got := w.qwatchModeFor("dsh"); got != "observe" {
		t.Fatalf("两连 MISS 后 dsh_mode = %q, want observe（降级）", got)
	}
	if d.GetQWatchMode() != "observe" {
		t.Fatal("CC mode 应仍为 observe（dsh 熔断零影响）")
	}
	if w.breaker.MissStreak != 0 {
		t.Fatalf("CC breaker 连击 = %d（dsh MISS 不得计入）", w.breaker.MissStreak)
	}
	// 降级后跳走 noop（真发面不再扩）。
	sent := len(rec.plansList())
	dshForceDue(w, key, 1)
	w.pollDsh()
	if got := len(rec.plansList()); got != sent {
		t.Fatalf("降级 observe 后不应再真发: %d → %d", sent, got)
	}

	// 连续 3 ERROR → 停本窗剩余跳（窗口不关、mode 不降）。
	root2 := t.TempDir()
	rec2 := &recordingSender{results: []beat.BeatResult{beatErr, beatErr, beatErr, beatErr}}
	w2, d2, _, _ := newDshQwFixture(t, root2, rec2)
	d2.SetDshQWatchMode("enforce")
	writeDshSession(t, root2, "session-ffff6", dshHeaderFor("session-ffff6"),
		dshQwUserSide(2, nowMS())+dshQwAsstUsage(4, nowMS()+1000))
	w2.pollDsh()
	k2 := winKey{"dsh", "session-ffff6"}
	win2 := w2.dshWindows[k2]
	dshForceDue(w2, k2, 4)
	for i := 0; i < 4; i++ {
		w2.pollDsh()
	}
	if got := len(rec2.plansList()); got != 3 {
		t.Fatalf("连 3 ERROR 后停本窗剩余跳: 发送 %d, want 3", got)
	}
	if w2.dshWindows[k2] == nil {
		t.Fatal("ERROR 熔断停跳不关窗（窗口本身保留）")
	}
	if win2.plan != nil {
		t.Fatalf("停跳后计划应清空: %v", win2.plan)
	}
	if w2.qwatchModeFor("dsh") != "enforce" {
		t.Fatalf("ERROR 熔断不降级: %q", w2.qwatchModeFor("dsh"))
	}
}

func TestDshEnforceWithoutDockDowngradedToObserve(t *testing.T) {
	// dsh enforce＋渡口关（无 [dock]）：启动告警一次＋按 observe 演练对待
	//（即使注入了 sender 也不真发——渡口关＝快照源不存在）。
	root := t.TempDir()
	rec := &recordingSender{}
	cfg := config.Default()
	cfg.Watch.DshSessionsDir = root
	cfg.Thresholds = config.ThresholdCfg{SummarizeS: 900, BlockS: 1200, MinCtxTokens: 1000}
	cfg.QuestionWatch.DshMode = "enforce"
	if cfg.Dock != nil {
		t.Fatal("前提：Default 无 [dock]")
	}
	readStdout := captureStdout(t)
	led := ledger.New()
	acc, err := accounts.New(filepath.Join(t.TempDir(), "acc"))
	if err != nil {
		t.Fatal(err)
	}
	w := NewWatcher(cfg, led, nil, func(*ledger.SessionState) bool { return true },
		0, acc, nil, rec, nil)
	out := readStdout()
	if !strings.Contains(out, "dsh_mode=enforce") || !strings.Contains(out, "observe") {
		t.Fatalf("启动告警缺失: %q", out)
	}
	writeDshSession(t, root, dshMainID, dshHeaderFor(dshMainID),
		dshQwUserSide(2, nowMS())+dshQwAsstUsage(4, nowMS()+1000))
	w.pollDsh()
	key := winKey{"dsh", dshMainID}
	if w.dshWindows[key] == nil {
		t.Fatal("前提：应已开窗（observe 对待仍有窗）")
	}
	dshForceDue(w, key, 1)
	w.pollDsh()
	if got := len(rec.plansList()); got != 0 {
		t.Fatalf("渡口关降级后不应真发: %d", got)
	}
}

// ---- Pin 对账（reconcilePins 扩 dsh） ----

func TestDshReconcilePinsLifecycle(t *testing.T) {
	// 窗开 → Pin；关窗（用户侧）→ Unpin；幂等（重复轮不重复 Pin/Unpin）。
	root := t.TempDir()
	w, _, _, pins := newDshQwFixture(t, root, nil)
	p := writeDshSession(t, root, dshMainID, dshHeaderFor(dshMainID),
		dshQwUserSide(2, nowMS())+dshQwAsstUsage(4, nowMS()+1000))
	w.pollDsh()
	w.pollDsh() // 幂等轮
	if pinCount(pins, "pin:"+dshMainID) != 1 {
		t.Fatalf("开窗应恰 Pin 一次: %v", *pins)
	}
	appendDshBatch(t, p, dshQwUserSide(10, nowMS()))
	w.pollDsh()
	if pinCount(pins, "unpin:"+dshMainID) != 1 {
		t.Fatalf("用户侧关窗应恰 Unpin 一次: %v", *pins)
	}
	w.pollDsh() // 关后幂等轮
	if pinCount(pins, "unpin:"+dshMainID) != 1 || pinCount(pins, "pin:"+dshMainID) != 1 {
		t.Fatalf("关窗后不应再有 Pin/Unpin: %v", *pins)
	}
}

// ---- 判活文件面刷新（ledger.DshRunRefresh 头注"票03 补"） ----

func TestDshRunRefreshFromFileFace(t *testing.T) {
	// 未接管会话：文件面白名单活动事件（turn/start、assistant/message）推进
	// 运行态时间戳（仅运行态在位时推进——置位仍只认 agent/status=running）。
	root := t.TempDir()
	w, _, _, _ := newDshQwFixture(t, root, nil)
	evMS := nowMS()
	p := writeDshSession(t, root, dshMainID, dshHeaderFor(dshMainID),
		dshQwUserSide(2, evMS)+dshQwAsstUsage(4, evMS+1000))
	w.Ledger.TouchFull("dsh", dshMainID, p, clock.Now(), int(statOr(t, p).Size()), "", "", 0, 0)
	w.Ledger.DshMainRunSet(dshMainID, true, float64(evMS)/1000-500) // 运行态在位（旧时刻）
	w.pollDsh()
	st := w.Ledger.Get("dsh", dshMainID)
	w.Ledger.Mu().Lock()
	run := st.DshRunningTS
	w.Ledger.Mu().Unlock()
	if run == nil || *run != float64(evMS+1000)/1000.0 {
		t.Fatalf("运行态应推进到末条白名单活动事件时刻: %v", run)
	}
}
