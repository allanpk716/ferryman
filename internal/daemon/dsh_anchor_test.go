package daemon

// dsh_anchor_test.go — 票09（dsh-post-accept-fixes）：dsh 文件面闲置锚根治。
//
// 根因（票02 评审钉死）：pollDshSession 对主会话无条件 TouchFull(statMTime)，
// 被拦回合的落盘写（turn/start、inbox splice、turn/end）也顶新台账
// last_write——dsh 无内容钟（gate coversBar 回落 lastWrite），锚被顶新＝闸门
// 判定的 idle 被冲成"刚活跃"（2026-10-06 晨间实测 8.8h 显示 16.5s），分支5
// 错过、block 账本假数。修法=文件面与事件面对齐（v0.8.10 DshEvent 同款语义）：
// 只有机器产出增量（assistant/message、compaction/*）才推进闲置锚。
//
// 时间纪律同 dsh_qwatch_test.go：clock.Now 真实时钟；历史锚用 bootChmtime
// 拨盘（watcher_boot_test.go 同款），增量批经真实追加推进 mtime。

import (
	"math"
	"path/filepath"
	"testing"
	"time"

	"ferryman/internal/accounts"
	"ferryman/internal/config"
	"ferryman/internal/ledger"
	"ferryman/internal/store"
)

// dshAnchorLastWrite 台账锚读数（锁内抄齐）。
func dshAnchorLastWrite(t *testing.T, led *ledger.Ledger, sid string) float64 {
	t.Helper()
	st := led.Get("dsh", sid)
	if st == nil {
		t.Fatal("dsh 主会话应已登记台账")
	}
	led.Mu().Lock()
	defer led.Mu().Unlock()
	return st.LastWrite
}

// dshAnchorFixture 会话已登记（锚=8.8h 前的机器产出，磁盘 mtime 同拨）——
// 事故形态的地基：后续任何文件写入与守望轮询都发生在"已登记＋长闲置"之上。
func dshAnchorFixture(t *testing.T, root string) (*Watcher, string, float64) {
	t.Helper()
	w, _, _, _ := newDshQwFixture(t, root, nil)
	prod := nowMS() - int64(8.8*3600*1000) // 末次机器产出=8.8h 前
	p := writeDshSession(t, root, dshMainID, dshHeaderFor(dshMainID),
		dshQwUserSide(2, prod)+dshQwAsstUsage(4, prod+1000))
	bootChmtime(t, p, float64(prod+1000)/1000) // 磁盘面：末次写入=8.8h 前
	w.pollDsh()                                // 首登记：锚=mtime
	return w, p, dshAnchorLastWrite(t, w.Ledger, dshMainID)
}

// TestDshBlockedTurnWriteDoesNotRefreshIdleAnchor ①被拦回合写入（turn/start
// ＋inbox splice＋turn/end，无 assistant/message）后 idle 不被顶新——闸门判定
// 的闲置保持真实（事故构造版：8.8h 不得被冲成"刚活跃"）。
func TestDshBlockedTurnWriteDoesNotRefreshIdleAnchor(t *testing.T) {
	root := t.TempDir()
	w, p, base := dshAnchorFixture(t, root)
	// 被拦回合的落盘批：用户回流 turn/start＋注入 splice（user/message——
	// 合成注入按用户侧口径）＋turn/end；文件 mtime 随真实追加推进到当下。
	appendDshBatch(t, p, dshEv("turn/start", 10, nowMS(), `"turn":2`)+
		dshEv("user/message", 11, nowMS()+100, `"message":{"role":"user"}`)+
		dshEv("turn/end", 12, nowMS()+200, `"x":1`))
	w.pollDsh()
	if lw := dshAnchorLastWrite(t, w.Ledger, dshMainID); lw != base {
		t.Fatalf("被拦回合写入顶新了闲置锚: %v → %v（应保持不动）", base, lw)
	}
}

// TestDshMachineReplyAdvancesIdleAnchor ②含 assistant/message 的正常回合后
// idle 正常推进——内容感知锚不得把会话钉死在"永不活跃"。
func TestDshMachineReplyAdvancesIdleAnchor(t *testing.T) {
	root := t.TempDir()
	w, p, base := dshAnchorFixture(t, root)
	appendDshBatch(t, p, dshQwAsstUsage(20, nowMS())) // 正常回复（带 usage）
	w.pollDsh()
	if lw := dshAnchorLastWrite(t, w.Ledger, dshMainID); lw <= base {
		t.Fatalf("机器产出后闲置锚应推进: %v → %v", base, lw)
	}
}

// TestDshCompactionCountsAsMachineProduction ③compaction/* 同机器产出（事件
// 面 DshEvent 的 Touch 口径）：纯 compaction 增量（无 assistant/message）也
// 推进闲置锚——既有 qwatch 测试"机器侧写入推进 last_write"前提的锚口径。
func TestDshCompactionCountsAsMachineProduction(t *testing.T) {
	root := t.TempDir()
	w, p, base := dshAnchorFixture(t, root)
	appendDshBatch(t, p, dshEv("compaction/start", 30, nowMS(), `"x":1`)+
		dshEv("compaction/end", 31, nowMS()+100, `"x":1`))
	w.pollDsh()
	if lw := dshAnchorLastWrite(t, w.Ledger, dshMainID); lw <= base {
		t.Fatalf("compaction 增量后闲置锚应推进: %v → %v", base, lw)
	}
}

// TestDshFedSessionFileFaceMatchesEventFace fed 会话（事件面已接管）与文件面
// 行为一致：被拦回合的文件写入不顶新锚（事件面 turn/start 本就不 Touch）、
// 机器产出增量照常推进、usage 单源化不双计。
func TestDshFedSessionFileFaceMatchesEventFace(t *testing.T) {
	root := t.TempDir()
	w, d, acc, _ := newDshQwFixture(t, root, nil)
	evMS := nowMS()
	p := writeDshSession(t, root, dshMainID, dshHeaderFor(dshMainID),
		dshQwUserSide(2, evMS)) // 文件此此刻只有用户侧批
	// 事件面接管：assistant/message（锚=事件时刻 T0；fed 标记同刻落）。
	d.DshEvent(map[string]any{"session_id": dshMainID, "event": "assistant/message",
		"time": float64(evMS),
		"usage": map[string]any{"input_tokens": 1000, "cache_read_tokens": 20000,
			"cache_creation_tokens": 0, "output_tokens": 50}})
	w.pollDsh() // 文件增量（用户侧批）不顶新——锚保持事件时刻
	base := dshAnchorLastWrite(t, w.Ledger, dshMainID)
	if math.Abs(base-float64(evMS)/1000) > 1 {
		t.Fatalf("fed 锚应=事件时刻: %v vs %v", base, float64(evMS)/1000)
	}
	// 被拦回合写入文件（无机器产出）→ 文件面同口径不顶新。
	appendDshBatch(t, p, dshEv("turn/start", 10, evMS+2000, `"turn":2`)+
		dshEv("turn/end", 12, evMS+2200, `"x":1`))
	w.pollDsh()
	if lw := dshAnchorLastWrite(t, w.Ledger, dshMainID); lw != base {
		t.Fatalf("fed 会话被拦回合文件写入顶新了锚: %v → %v", base, lw)
	}
	// 文件面机器产出 → 推进（与事件面同口径；锚取 max 无双计）。
	appendDshBatch(t, p, dshQwAsstUsage(20, evMS+3000))
	w.pollDsh()
	if lw := dshAnchorLastWrite(t, w.Ledger, dshMainID); lw <= base {
		t.Fatalf("fed 会话文件面机器产出后锚应推进: %v → %v", base, lw)
	}
	// usage 单源化：恰事件面 1 行，文件面让位不双计。
	if rows := dshUsageRows(t, acc); len(rows) != 1 {
		t.Fatalf("usage 行 = %d, want 1（fed 单源化不双计）", len(rows))
	}
}

// TestCCFileFaceStillAnchorsToMtime ④cc/codex 文件面零变化钉：cc 锚=mtime
// 口径原样（幻影写免疫在闸门内容钟 ADR-0013，不在守望面）——mtime 前进而
// 内容不变的写入照样顶新。dsh 的内容感知锚（本票）不得外溢到 cc。
func TestCCFileFaceStillAnchorsToMtime(t *testing.T) {
	now := float64(time.Now().Unix())
	tmp := t.TempDir()
	projects := filepath.Join(tmp, "projects")
	f := bootFixture(t, projects, "cc-anchor", now-3600)
	bootChmtime(t, f, now-3600)
	stt, err := store.New(filepath.Join(tmp, "data"))
	if err != nil {
		t.Fatal(err)
	}
	var enqueued []string
	led := ledger.New()
	w := bootWatcher(t, bootCfg(projects), led, stt, now, &enqueued)
	w.cxDirs = []string{filepath.Join(tmp, "no-codex")}
	w.pollCC()
	st := led.Get("cc", "cc-anchor")
	if st == nil {
		t.Fatal("cc 会话应已登记台账")
	}
	led.Mu().Lock()
	base := st.LastWrite
	led.Mu().Unlock()
	bootChmtime(t, f, now) // 幻影写形态：mtime 顶新、内容不变
	w.pollCC()
	led.Mu().Lock()
	lw := st.LastWrite
	led.Mu().Unlock()
	if lw <= base {
		t.Fatalf("cc 文件面契约零变化：mtime 顶新仍应推进 last_write（%v → %v）", base, lw)
	}
}

// TestDshGateIdleRealAfterBlockedTurnWrites 票02/A5① 回归钉的文件面场景
// （2026-10-06 晨间事故构造版）：被拦回合写入落盘＋守望轮询之后，闸门照拦
// （分支5）且 block 记账的 idle_s 按真实机器产出锚（≈8.8h）——不再被文件
// mtime 稀释成"刚活跃"（分支5 错过、账本假数的根因闭环）。
func TestDshGateIdleRealAfterBlockedTurnWrites(t *testing.T) {
	tmp := t.TempDir()
	stt, err := store.New(filepath.Join(tmp, "data"))
	if err != nil {
		t.Fatal(err)
	}
	acc, err := accounts.New(filepath.Join(tmp, "acc"))
	if err != nil {
		t.Fatal(err)
	}
	led := ledger.New()
	cfg := config.Default()
	cfg.Watch.DshSessionsDir = filepath.Join(tmp, "dsh-sessions")
	cfg.Server.DataDir = filepath.Join(tmp, "data") // 测试卫生：警告行不落生产 gate.log
	cfg.Thresholds = config.ThresholdCfg{SummarizeS: 900, BlockS: 1200, MinCtxTokens: 1000}
	cfg.GateDsh = "enforce"
	cfg.QuestionWatch.DshMode = "off" // 本票只管锚——窗机零干扰
	d := NewDaemon(cfg, led, stt, func(*ledger.SessionState) bool { return true }, acc, 0, nil)
	w := NewWatcher(cfg, led, nil, func(*ledger.SessionState) bool { return true },
		0, acc, d, nil, nil)
	root := cfg.Watch.DshSessionsDir
	prod := nowMS() - int64(8.8*3600*1000)
	p := writeDshSession(t, root, dshMainID, dshHeaderFor(dshMainID),
		dshQwUserSide(2, prod)+dshQwAsstUsage(4, prod+1000))
	bootChmtime(t, p, float64(prod+1000)/1000)
	w.pollDsh()
	anchor := float64(prod+1000) / 1000
	// 有效交接覆盖当前锚（分支5 前提）。
	stt.SaveHandoff(dshMainID, "dsh", "C:\\proj", "t", isoUTC(anchor+30), "fresh", "md")
	// 被拦回合写入（无机器产出）＋守望轮询（事故形态：落盘与闸门判定之间
	// 恰好夹一轮 pollDsh）。
	appendDshBatch(t, p, dshEv("turn/start", 10, nowMS(), `"turn":2`)+
		dshEv("user/message", 11, nowMS()+100, `"message":{"role":"user"}`)+
		dshEv("turn/end", 12, nowMS()+200, `"x":1`))
	w.pollDsh()

	r := d.Gate(gateBodyAgent("dsh", dshMainID, p, "C:\\proj", "继续"))
	if r["decision"] != "block" {
		t.Fatalf("decision = %v, want block（真实闲置 8.8h ≥ block_s、交接有效——锚被稀释时此判放行）", r["decision"])
	}
	rows := acc.Read(accounts.ReadOpts{Kind: "block"})
	if len(rows) != 1 {
		t.Fatalf("block 行数 = %d, want 1", len(rows))
	}
	idle, _ := rows[0]["idle_s"].(float64)
	if idle < 31000 || idle > 32400 {
		t.Fatalf("block idle_s = %v, want ≈31680（真实锚口径；被顶新时落 ≈0 假数）", idle)
	}
}
