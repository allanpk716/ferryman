package daemon

// compact_trigger_test.go — dsh-hot-compaction 票03：触发扫描表驱动钉子
//（compact_test.go 票02 同风格；freezeClock 先例 gate_test.go）。
//
// 契约＝spec「架构与契约」daemon 节逐字：触发扫描五条件——闲置 ≥
// trigger_ratio×TTL ∧ peak_ctx ≥ min_peak_tokens ∧ 无在途请求（族系运行态）
// ∧ 无有效 compressed 标记（票02 DshCompressedActive 单源）∧ 无未过期在槽
// 指令 → EnqueueDshCompact 入槽（expires_at=now+command_ttl_ratio×TTL）；
// 同会话重复触发覆盖过期旧槽；并行交接走既有 L1 摆渡管线（w.Enqueue 缝，
// regen 先例）——与指令独立、失败只日志不回滚指令、不重复摆渡（HandedOffAt
// 章＋ValidHandoff 去重，maybeDshRegen 同款两道）。
//
// 时间纪律：clock 冻结在 t0（advance 推进），闲置由 last_write 与冻结钟差给
// 出；接线一例走真实钟＋文件 mtime 拨旧（闲置造在文件侧——冻结钟与真实
// mtime 相差一个纪元，regen/dsh_boot 测试同款取舍反向用）。

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"ferryman/internal/accounts"
	"ferryman/internal/config"
	"ferryman/internal/ledger"
	"ferryman/internal/store"
)

const trigSID = "session-33333333-3333-4333-8333-333333333333"

// trigEnv 票03 触发面环境：冻结钟＋真账本/store＋Daemon/Watcher 同一 cfg 互
// 接（触发面读 w.Cfg、入槽走 d 的票02 槽 API——生产同为一份 cfg）。enqOK
// 交接入队缝（可编程成败）：enqTry 记尝试、enqDone 记成功。
type trigEnv struct {
	d   *Daemon
	w   *Watcher
	led *ledger.Ledger
	st  *store.Store
	acc *accounts.Accounts
	tmp string
	t0  float64
	now *float64

	mu      sync.Mutex
	enqOK   bool
	enqTry  []string
	enqDone []string
}

func newTrigEnv(t *testing.T, ttls float64) *trigEnv {
	t.Helper()
	e := &trigEnv{tmp: t.TempDir(), t0: 1_800_000_000.0, enqOK: true}
	e.now = freezeClock(t, e.t0)
	acc, err := accounts.New(filepath.Join(e.tmp, "acc"))
	if err != nil {
		t.Fatal(err)
	}
	e.acc = acc
	st, err := store.New(filepath.Join(e.tmp, "data"))
	if err != nil {
		t.Fatal(err)
	}
	e.st = st
	e.led = ledger.New()
	cfg := config.Default()
	cfg.Server.DataDir = filepath.Join(e.tmp, "data") // 测试卫生：DataDir 钉沙箱
	cfg.Heartbeat.TTLS = ttls
	e.d = NewDaemon(cfg, e.led, st, func(*ledger.SessionState) bool { return true },
		acc, 0, nil)
	e.w = NewWatcher(cfg, e.led, st, func(s *ledger.SessionState) bool {
		e.mu.Lock()
		defer e.mu.Unlock()
		e.enqTry = append(e.enqTry, s.Agent+"/"+s.SessionID)
		if !e.enqOK {
			return false
		}
		e.enqDone = append(e.enqDone, s.Agent+"/"+s.SessionID)
		return true
	}, e.t0, acc, e.d, nil, nil)
	resetCompactMiss()   // 守护可见性票01 包级态卫生（本文件 sweep/计轮用例也要）
	resetCompactClaims() // 票01 在飞领取窗包级态同款卫生
	return e
}

func (e *trigEnv) advance(dt float64) { *e.now += dt }

func (e *trigEnv) trigPath() string { return filepath.Join(e.tmp, trigSID+".jsonl.zstd") }

// regTrig 登记主会话（last_write=t0-idleS、peak=peakCtx；daemonStartedAt=0
// → TouchFull 自置 ObservedActive，与生产 24h 观察窗内会话同形态）。
func (e *trigEnv) regTrig(idleS float64, peakCtx int) {
	e.led.TouchFull("dsh", trigSID, e.trigPath(), e.t0-idleS, 10, "C:/proj", "",
		peakCtx, 0)
}

func (e *trigEnv) run() {
	if st := e.led.Get("dsh", trigSID); st != nil {
		e.w.maybeDshCompactTrigger(st)
	}
}

// trigSlot 锁内抄指令槽条目（nil=无；拷贝防逃逸共享引用）。
func (e *trigEnv) trigSlot(sid string) *dshCompactCmd {
	e.d.compactMu.Lock()
	defer e.d.compactMu.Unlock()
	cmd := e.d.dshCompactSlot[sid]
	if cmd == nil {
		return nil
	}
	cp := *cmd
	return &cp
}

func (e *trigEnv) enqTryList() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.enqTry...)
}

func (e *trigEnv) enqDoneList() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.enqDone...)
}

// handedOff 锁内抄 HandedOffAt 现值（未登记=0）。
func (e *trigEnv) handedOff() float64 {
	e.led.Mu().Lock()
	defer e.led.Mu().Unlock()
	if st := e.led.GetLocked("dsh", trigSID); st != nil {
		return st.HandedOffAt
	}
	return 0
}

func trigAbsDiff(a, b float64) float64 {
	if a > b {
		return a - b
	}
	return b - a
}

// wantNoSlot 未触发形态：无新指令、交接零尝试（不满足时什么都不做）。
func wantNoSlot(t *testing.T, e *trigEnv) {
	t.Helper()
	if cmd := e.trigSlot(trigSID); cmd != nil {
		t.Fatalf("条件不满足不应入槽, got %+v", cmd)
	}
	if got := e.enqTryList(); len(got) != 0 {
		t.Fatalf("条件不满足交接也应零尝试, got %v", got)
	}
}

// ---- 五条件表驱动（spec「架构与契约」逐字） ----

func TestDshCompactTriggerFiveConditions(t *testing.T) {
	cases := []struct {
		name  string
		ttls  float64
		setup func(e *trigEnv)
		check func(t *testing.T, e *trigEnv)
	}{
		{"条件①闲置未到线不入槽", 100,
			func(e *trigEnv) { e.regTrig(79.9, 50000) }, // 线=0.8×100=80s
			func(t *testing.T, e *trigEnv) { wantNoSlot(t, e) }},
		{"条件②peak不足不入槽", 100,
			func(e *trigEnv) { e.regTrig(80, 19999) }, // min_peak_tokens 默认 20000
			func(t *testing.T, e *trigEnv) { wantNoSlot(t, e) }},
		{"条件③族系在途不入槽", 100,
			func(e *trigEnv) {
				e.regTrig(1000, 50000)
				// v0.9.4b 绝对新鲜度：运行信号距今 100s(<300s 窗)=长生成/子代理在跑,不入槽
				e.led.DshMainRunSet(trigSID, true, e.t0-100)
			},
			func(t *testing.T, e *trigEnv) { wantNoSlot(t, e) }},
		{"条件③陈旧运行态不挡（v0.9.4 b 变体 bb5d5e37 实锚）", 100,
			func(e *trigEnv) {
				e.regTrig(1000, 50000)
				// 运行信号距今 950s(>300s 窗)=宿主重载尖峰/未送 idle 的挂死残影
				//——孤立尖峰 5min 自然过期,闲置已 1000s 过触发线,不挡
				e.led.DshMainRunSet(trigSID, true, e.t0-950)
			},
			func(t *testing.T, e *trigEnv) {
				if cmd := e.trigSlot(trigSID); cmd == nil {
					t.Fatal("陈旧运行态应放行入槽")
				}
			}},
		{"条件④有效压缩标记不入槽", 100,
			func(e *trigEnv) {
				e.regTrig(80, 50000)
				e.d.DshCompacted(map[string]any{"session_id": trigSID,
					"ok": true, "prefix_tokens": 1234})
				// DshCompacted 把 PeakCtx 覆盖成 prefix——同 mtime 抬回 peak，
				// 隔离条件②（本例只钉标记条件）
				e.led.TouchFull("dsh", trigSID, e.trigPath(), e.t0-80, 10,
					"C:/proj", "", 50000, 0)
			},
			func(t *testing.T, e *trigEnv) { wantNoSlot(t, e) }},
		{"条件⑤未过期在槽指令不重复入槽", 100,
			func(e *trigEnv) {
				e.regTrig(80, 50000)
				if !e.d.EnqueueDshCompact(trigSID, "C:/old") { // t0 入槽，活到 t0+20
					t.Fatal("前置入槽失败")
				}
			},
			func(t *testing.T, e *trigEnv) {
				cmd := e.trigSlot(trigSID)
				if cmd == nil || cmd.Cwd != "C:/old" || cmd.EnqueuedAt != e.t0 {
					t.Fatalf("旧指令应原样在槽（触发被挡）: %+v", cmd)
				}
				if got := e.enqTryList(); len(got) != 0 {
					t.Fatalf("未过期在槽期间交接也应零尝试: %v", got)
				}
			}},
		{"票01在飞领取窗:领取后30s重触发被拒", 100,
			func(e *trigEnv) {
				e.regTrig(80, 50000)
				e.run()                        // 首次触发：入槽＋交接
				e.d.DshPoll(pollBody(trigSID)) // 领取：在飞执行窗起点 t0
				e.advance(30)                  // 领取后 30s（窗 180s+60s=240s 内）
			},
			func(t *testing.T, e *trigEnv) {
				if cmd := e.trigSlot(trigSID); cmd != nil {
					t.Fatalf("在飞领取窗内重触发不得入槽（30s 重发环治点）, got %+v", cmd)
				}
				if got := e.enqTryList(); len(got) != 1 {
					t.Fatalf("在飞窗内交接也零尝试（首次那摆之外不重摆）: %v", got)
				}
			}},
		{"五条件全真入槽带expires_at", 100,
			func(e *trigEnv) { e.regTrig(80, 50000) },
			func(t *testing.T, e *trigEnv) {
				cmd := e.trigSlot(trigSID)
				if cmd == nil {
					t.Fatal("五条件全真应入槽")
				}
				if cmd.Cwd != "C:/proj" {
					t.Fatalf("cwd = %q, want 会话 cwd", cmd.Cwd)
				}
				wantExp := e.t0 + e.d.Cfg.DshCompact.CommandTTLRatio*e.d.Cfg.Heartbeat.TTLS
				if cmd.EnqueuedAt != e.t0 || trigAbsDiff(cmd.ExpiresAt, wantExp) > 1e-9 {
					t.Fatalf("enqueued/expires = (%v, %v), want (t0, t0+command_ttl_ratio×TTL=%v)",
						cmd.EnqueuedAt, cmd.ExpiresAt, wantExp)
				}
			}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newTrigEnv(t, tc.ttls)
			tc.setup(e)
			e.run()
			tc.check(t, e)
		})
	}
}

// ---- 重复触发：覆盖过期旧槽；未过期窗口内交接不重摆 ----

func TestDshCompactTriggerOverwritesExpiredSlot(t *testing.T) {
	e := newTrigEnv(t, 1000) // 线=800s、指令有效期=200s
	e.regTrig(800, 50000)
	e.run()
	if cmd := e.trigSlot(trigSID); cmd == nil || cmd.EnqueuedAt != e.t0 {
		t.Fatal("首次触发应入槽")
	}
	if got := e.enqDoneList(); len(got) != 1 {
		t.Fatalf("首次触发交接应入队恰一次, got %v", got)
	}
	e.advance(201) // 指令过期（200s）仍留槽（poll 未清）；闲置 1001s 仍满足条件①
	e.run()
	cmd := e.trigSlot(trigSID)
	if cmd == nil || trigAbsDiff(cmd.EnqueuedAt, e.t0+201) > 1e-9 ||
		trigAbsDiff(cmd.ExpiresAt, e.t0+201+200) > 1e-9 {
		t.Fatalf("过期旧槽应被重复触发覆盖（新 expires_at）: %+v", cmd)
	}
	if got := e.enqDoneList(); len(got) != 1 {
		t.Fatalf("交接不重摆（HandedOffAt 章仍覆盖同内容）: %v", got)
	}
}

// ---- 交接异步生成分支：成功/失败/去重三态 ----

func TestDshCompactTriggerHandoffBranch(t *testing.T) {
	t.Run("成功入队并盖章", func(t *testing.T) {
		e := newTrigEnv(t, 100)
		e.regTrig(80, 50000)
		e.run()
		if cmd := e.trigSlot(trigSID); cmd == nil {
			t.Fatal("指令应入槽")
		}
		if got := e.enqTryList(); len(got) != 1 || got[0] != "dsh/"+trigSID {
			t.Fatalf("交接应经既有管线（w.Enqueue 缝）入队: %v", got)
		}
		if got := e.handedOff(); trigAbsDiff(got, e.t0) > 1e-9 {
			t.Fatalf("入队即记 HandedOffAt = %v, want t0", got)
		}
	})
	t.Run("入队失败不回滚指令", func(t *testing.T) {
		e := newTrigEnv(t, 100)
		e.regTrig(80, 50000)
		e.enqOK = false // 队满形
		e.run()
		if cmd := e.trigSlot(trigSID); cmd == nil {
			t.Fatal("交接失败不得回滚指令（指令应在槽）")
		}
		if got := e.enqTryList(); len(got) != 1 {
			t.Fatalf("交接应已尝试: %v", got)
		}
		if got := e.handedOff(); got != 0 {
			t.Fatalf("入队失败不盖章（下轮重试）, got %v", got)
		}
	})
	t.Run("已有覆盖交接不重摆", func(t *testing.T) {
		e := newTrigEnv(t, 100)
		e.regTrig(80, 50000)
		// covers=t0 ≥ last_write=t0-80（新鲜窗内、未消耗）→ ValidHandoff 命中
		e.st.SaveHandoff(trigSID, "dsh", "C:/proj", "t", isoUTC(e.t0), "fresh", "md")
		e.run()
		if cmd := e.trigSlot(trigSID); cmd == nil {
			t.Fatal("指令照入（与交接去重无关）")
		}
		if got := e.enqTryList(); len(got) != 0 {
			t.Fatalf("交接已被有效覆盖，不重摆: %v", got)
		}
	})
}

// ---- 开关与保守面 ----

func TestDshCompactTriggerDisabledAndNoTTL(t *testing.T) {
	e := newTrigEnv(t, 100)
	e.regTrig(80, 50000)
	e.d.Cfg.DshCompact.Enabled = false // w.Cfg 同一指针——触发面同见
	e.run()
	if cmd := e.trigSlot(trigSID); cmd != nil {
		t.Fatal("总开关关不触发")
	}
	e2 := newTrigEnv(t, 0) // TTL 不可得：触发线与指令有效期皆不可算
	e2.regTrig(80, 50000)
	e2.run()
	if cmd := e2.trigSlot(trigSID); cmd != nil {
		t.Fatal("TTL 不可得不触发（保守面）")
	}
}

func TestDshCompactTriggerNeedsObserved(t *testing.T) {
	e := newTrigEnv(t, 100)
	// daemonStartedAt 设为未来 → TouchFull 不置 ObservedActive（重启观察窗外
	// 的古老会话不动作——摆渡/重铸同纪律）
	e.led.TouchFull("dsh", trigSID, e.trigPath(), e.t0-80, 10, "C:/proj", "",
		50000, e.t0+100)
	e.run()
	if cmd := e.trigSlot(trigSID); cmd != nil {
		t.Fatal("未观察会话不触发")
	}
}

// ---- 票01 触发面全链：在飞领取窗节流→窗走满 sweep 计轮→放行重入槽 ----

func TestDshCompactTriggerClaimWindowLifecycle(t *testing.T) {
	e := newTrigEnv(t, 100) // 指令有效期 20s、执行窗 240s
	log := captureCompactLog(t)
	e.regTrig(80, 50000)
	e.run() // 首次触发：入槽＋交接
	if cmd := e.trigSlot(trigSID); cmd == nil {
		t.Fatal("首次触发应入槽")
	}
	if got := len(e.d.DshPoll(pollBody(trigSID))["commands"].([]map[string]any)); got != 1 {
		t.Fatalf("指令应被领取, got %d", got)
	}
	e.advance(30) // 领取后 30s：在飞窗（240s）内
	e.run()
	if cmd := e.trigSlot(trigSID); cmd != nil {
		t.Fatalf("在飞领取窗内重触发不得入槽, got %+v", cmd)
	}
	if got := e.enqTryList(); len(got) != 1 {
		t.Fatalf("在飞窗内交接零尝试（首次那摆之外不重摆）: %v", got)
	}
	if n, s := e.undeliveredAlertsTrig(); n != 0 {
		t.Fatalf("窗未走满不应计轮/告警, got %d\n%s", n, s)
	}
	e.advance(215) // t0+245：越过执行窗
	e.run()        // sweep 结算：计一轮未送达＋清领取位放行重触发
	cmd := e.trigSlot(trigSID)
	if cmd == nil || trigAbsDiff(cmd.EnqueuedAt, e.t0+245) > 1e-9 {
		t.Fatalf("窗走满应计轮并放行重入槽（新 expires_at）: %+v", cmd)
	}
	want := fmt.Sprintf("[compact] dsh 指令领取后执行窗内无成功上报(第 1 轮):%s", runeCap16(trigSID))
	if !strings.Contains(log.String(), want) {
		t.Fatalf("sweep 应落未送达日志: %q", log.String())
	}
}

// undeliveredAlertsTrig 触发面环境版告警读取（DataDir 沙箱同 compactEnv 版）。
func (e *trigEnv) undeliveredAlertsTrig() (int, string) {
	b, err := os.ReadFile(filepath.Join(e.tmp, "data", "gate.log"))
	s := string(b)
	if err != nil {
		return 0, s
	}
	return strings.Count(s, "mode=compact-undelivered"), s
}

// ---- 接线钉子：pollDshSession 全链 ----

func TestDshCompactTriggerWiredIntoPollDsh(t *testing.T) {
	root := t.TempDir()
	tmp := t.TempDir()
	sid := "session-55555555-5555-4555-8555-555555555555"
	p := writeDshSession(t, root, sid, dshHeaderFor(sid), dshQwAsstUsage(3, nowMS()))
	past := time.Now().Add(-100 * time.Second) // TTL=100 → 线=80s：闲置 100s 过线
	if err := os.Chtimes(p, past, past); err != nil {
		t.Fatal(err)
	}
	acc, err := accounts.New(filepath.Join(tmp, "acc"))
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.New(filepath.Join(tmp, "data"))
	if err != nil {
		t.Fatal(err)
	}
	led := ledger.New()
	cfg := config.Default()
	cfg.Watch.DshSessionsDir = root
	cfg.Heartbeat.TTLS = 100
	cfg.Server.DataDir = filepath.Join(tmp, "data") // 测试卫生：钉沙箱
	d := NewDaemon(cfg, led, st, func(*ledger.SessionState) bool { return true }, acc, 0, nil)
	var mu sync.Mutex
	enq := []string{}
	w := NewWatcher(cfg, led, st, func(s *ledger.SessionState) bool {
		mu.Lock()
		enq = append(enq, s.Agent+"/"+s.SessionID)
		mu.Unlock()
		return true
	}, 0, acc, d, nil, nil)
	w.pollDsh()
	var cmd *dshCompactCmd
	d.compactMu.Lock()
	if c := d.dshCompactSlot[sid]; c != nil {
		cp := *c
		cmd = &cp
	}
	d.compactMu.Unlock()
	if cmd == nil {
		t.Fatal("闲置过线：pollDsh 一轮应入槽")
	}
	if cmd.Cwd != `C:\proj` {
		t.Fatalf("cwd = %q, want 头行 C:\\proj", cmd.Cwd)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(enq) != 1 || enq[0] != "dsh/"+sid {
		t.Fatalf("交接应恰入队一次（常规线被 SummarizeS 挡死，入队只可能来自触发线）: %v", enq)
	}
}
