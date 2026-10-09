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
	resetDshLiveReports() // dsh-host-guard 票01：live 聚合记忆同款卫生
	resetCompactBackoff() // dsh-host-guard 票01：no-agent 退避表同款卫生
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

// ---- dsh-host-guard 票01：live 聚合（spec B 逐字）＋双宿主混合＋no-agent 退避（spec D） ----
//
// 聚合是跨轮询的记忆态（近窗 90s）：近窗内任一 poller 报 live=true → 可执行照
// 常触发；仅当近窗内有显式上报且全部 live=false → 压制不下发；近窗内无任何显
// 上报（全缺键/无轮询）→ 未知 → 照旧触发（与今天行为一致）。「健康双宿主形
// 态」（桌面 true + web 播种 false 交错）恒为可执行——false 不压制 true。

// TestDshCompactTriggerLiveAggregation 聚合三分支表（B 节钉死语义）。
func TestDshCompactTriggerLiveAggregation(t *testing.T) {
	tt, ff := true, false
	cases := []struct {
		name  string
		ttls  float64
		setup func(e *trigEnv)
		check func(t *testing.T, e *trigEnv)
	}{
		{"近窗任一live=true→照常触发", 100,
			func(e *trigEnv) {
				e.regTrig(80, 50000)
				e.d.DshPoll(pollBodyLive(trigSID, &tt))
			},
			func(t *testing.T, e *trigEnv) {
				if e.trigSlot(trigSID) == nil {
					t.Fatal("live=true 在近窗：照常按六条件触发入槽")
				}
			}},
		{"近窗全部显式false→压制", 100,
			func(e *trigEnv) {
				e.regTrig(80, 50000)
				e.d.DshPoll(pollBodyLive(trigSID, &ff))
			},
			func(t *testing.T, e *trigEnv) { wantNoSlot(t, e) }},
		{"无显式上报(无轮询)→照旧触发", 100,
			func(e *trigEnv) { e.regTrig(80, 50000) },
			func(t *testing.T, e *trigEnv) {
				if e.trigSlot(trigSID) == nil {
					t.Fatal("无任何 live 上报＝未知：与现行行为一致照旧触发")
				}
			}},
		{"缺键旧体→照旧触发(前向兼容硬约束)", 100,
			func(e *trigEnv) {
				e.regTrig(80, 50000)
				e.d.DshPoll(pollBodyLive(trigSID, nil))
			},
			func(t *testing.T, e *trigEnv) {
				if e.trigSlot(trigSID) == nil {
					t.Fatal("缺键（旧协议体）＝未知：照旧触发，旧插件行为零变化")
				}
			}},
		{"显式false过90s近窗→未知→照旧触发", 100,
			func(e *trigEnv) {
				e.regTrig(80, 50000)
				e.d.DshPoll(pollBodyLive(trigSID, &ff))
				e.advance(91) // 唯一显式上报出窗
			},
			func(t *testing.T, e *trigEnv) {
				if e.trigSlot(trigSID) == nil {
					t.Fatal("出窗的 false 不再压制（近窗无显式上报＝未知）")
				}
			}},
		{"true出窗后窗内只剩false→压制", 100,
			func(e *trigEnv) {
				e.regTrig(80, 50000)
				e.d.DshPoll(pollBodyLive(trigSID, &tt))
				e.advance(91)      // true 出窗
				e.d.DshPoll(pollBodyLive(trigSID, &ff)) // 窗内唯一显式上报=false
			},
			func(t *testing.T, e *trigEnv) { wantNoSlot(t, e) }},
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

// TestDshCompactTriggerDualHostMixed 双宿主混合用例（B×C 交钉）：同一 sid 宿主
// B（播种宿主）报 false、宿主 A（活宿主）报 true，交错轮询 → 触发不被压制（false
// 不压制 true），且槽内指令只派给 A——B 即便先来也领不走。
func TestDshCompactTriggerDualHostMixed(t *testing.T) {
	e := newTrigEnv(t, 100)
	e.regTrig(80, 50000)
	tt, ff := true, false
	e.d.DshPoll(pollBodyLive(trigSID, &ff)) // 宿主 B 先轮询：报 false
	e.d.DshPoll(pollBodyLive(trigSID, &tt)) // 宿主 A 交错轮询：报 true
	e.run()                                 // 触发不被压制 → 入槽
	if e.trigSlot(trigSID) == nil {
		t.Fatal("双宿主混合（true+false 同窗）：false 不得压制 true,应照常入槽")
	}
	// 派发路由：B 报 false 即便列了该 sid 也不派——指令留槽
	if cmds := e.d.DshPoll(pollBodyLive(trigSID, &ff))["commands"].([]map[string]any); len(cmds) != 0 {
		t.Fatalf("播种宿主（live=false）不得领取, got %v", cmds)
	}
	if e.trigSlot(trigSID) == nil {
		t.Fatal("false 宿主轮询不得清槽——指令等其活宿主")
	}
	// 指令只派给 A
	if cmds := e.d.DshPoll(pollBodyLive(trigSID, &tt))["commands"].([]map[string]any); len(cmds) != 1 {
		t.Fatalf("活宿主（live=true）应领取, got %v", cmds)
	}
}

// TestDshCompactNoAgentBackoff no-agent 退避（spec D）：同一会话连续「领取后无
// 成功上报」轮（执行窗结算）满阈值后进入固定 30 分钟退避——退避期内触发扫描
// 早退不再入槽；解除条件任一：该 sid 再被报 live=true；台账 last_write 前进。
func TestDshCompactNoAgentBackoff(t *testing.T) {
	// spinClaimMissRounds 构造 n 轮「触发入槽→领取→执行窗走满」（每轮 ~245s）。
	// 第 i 轮的结算发生在第 i+1 轮 run() 的 sweep——末轮领取挂账未结，由调用方
	// 续跑结算（退避正是在那次结算的 run 里生效并早退）。
	spin := func(e *trigEnv, t *testing.T, n int) {
		t.Helper()
		for i := 0; i < n; i++ {
			e.run()
			if e.trigSlot(trigSID) == nil {
				t.Fatalf("第 %d/%d 轮应入槽（未到退避阈值）", i+1, n)
			}
			if got := len(e.d.DshPoll(pollBody(trigSID))["commands"].([]map[string]any)); got != 1 {
				t.Fatalf("第 %d/%d 轮应领取, got %d 条", i+1, n, got)
			}
			e.advance(dshCompactExecWindowS + 5)
		}
	}
	t.Run("连续no-agent轮后30分钟早退,期满放行", func(t *testing.T) {
		e := newTrigEnv(t, 100)
		log := captureCompactLog(t)
		e.regTrig(80, 50000)
		spin(e, t, dshCompactMissAlertRounds)
		e.run() // 结算末轮：连续领取后无成功上报满阈值 → 进退避 → 早退
		if e.trigSlot(trigSID) != nil {
			t.Fatal("退避期内触发扫描应早退,不得入槽")
		}
		if got := e.enqTryList(); len(got) != 1 {
			t.Fatalf("退避期内不重摆交接（首摆之外零尝试）, got %v", got)
		}
		if !strings.Contains(log.String(), "退避") {
			t.Fatalf("进退避应落显著日志: %q", log.String())
		}
		e.advance(60) // 退避期内（30min）多轮扫描照旧早退
		e.run()
		if e.trigSlot(trigSID) != nil {
			t.Fatal("退避期内多轮扫描照旧早退")
		}
		e.advance(30*60 - 60 + 1) // 越过 30 分钟死线
		e.run()
		if e.trigSlot(trigSID) == nil {
			t.Fatal("退避期满应放行重入槽（六条件仍真）")
		}
	})
	t.Run("再报live=true即解除", func(t *testing.T) {
		e := newTrigEnv(t, 100)
		e.regTrig(80, 50000)
		spin(e, t, dshCompactMissAlertRounds)
		e.run() // 进退避
		if e.trigSlot(trigSID) != nil {
			t.Fatal("前置：退避内不得入槽")
		}
		tt := true
		e.d.DshPoll(pollBodyLive(trigSID, &tt)) // 任一宿主再报 live=true → 解除
		e.run()
		if e.trigSlot(trigSID) == nil {
			t.Fatal("live=true 上报应即解除退避,照常入槽")
		}
	})
	t.Run("台账last_write前进即解除", func(t *testing.T) {
		e := newTrigEnv(t, 100)
		e.regTrig(80, 50000)
		spin(e, t, dshCompactMissAlertRounds)
		e.run() // 进退避
		if e.trigSlot(trigSID) != nil {
			t.Fatal("前置：退避内不得入槽")
		}
		// 用户回流：台账 last_write 前进到 now-80（闲置仍过触发线）
		e.led.TouchFull("dsh", trigSID, e.trigPath(), *e.now-80, 10, "C:/proj", "",
			50000, 0)
		e.run()
		if e.trigSlot(trigSID) == nil {
			t.Fatal("last_write 前进（用户回流）应解除退避,照常入槽")
		}
	})
}

// ---- 条件②红利门槛改增量（2026-10-09 d5927238 空转案）----
//
// 实锚（生产账本+宿主 session.v4.jsonl 双证）：会话压缩一次后前缀落到地板
// 25218（系统提示+工具+摘要本体），标记 2×TTL 过期后五条件全真 → 每小时空压
// 一轮，15 小时 27 发仅降 279 token，宿主 19 撞「summary is not smaller than
// the shadowed content」拒。修复：压过（标记在册，含过期）看增量
// peak−PostPrefix ≥ min_peak_tokens——纯闲置增量为零永不再压，新增长过线
// （用户回流聊出新上下文，enrich 只增不减推高 PeakCtx）才再压。PostPrefix=0
// 历史标记回落绝对门槛（不比无标记更差）。

func TestDshCompactTriggerIncrementalGate(t *testing.T) {
	cases := []struct {
		name  string
		ttls  float64
		setup func(e *trigEnv)
		check func(t *testing.T, e *trigEnv)
	}{
		{"压过后纯闲置:标记过期不再空压(本案)", 100,
			func(e *trigEnv) {
				e.regTrig(80, 50000) // 首次触发条件齐备
				e.d.DshCompacted(map[string]any{"session_id": trigSID,
					"ok": true, "prefix_tokens": 25218}) // 压缩成功:PeakCtx←25218+标记(PostPrefix=25218)
				e.advance(250) // 标记 200s 过期、闲置 330s 过线——空转案时序
			},
			func(t *testing.T, e *trigEnv) { wantNoSlot(t, e) }},
		{"压过后微量增长不足门槛:不入槽", 100,
			func(e *trigEnv) {
				e.regTrig(80, 50000)
				e.d.DshCompacted(map[string]any{"session_id": trigSID,
					"ok": true, "prefix_tokens": 25000})
				e.advance(250)
				// 回流+微量新流量:enrich 推高 PeakCtx 到 28000(增 3000 < 20000)
				e.led.TouchFull("dsh", trigSID, e.trigPath(), *e.now-80, 10,
					"C:/proj", "", 28000, 0)
			},
			func(t *testing.T, e *trigEnv) { wantNoSlot(t, e) }},
		{"压过后新增长过线:照常入槽", 100,
			func(e *trigEnv) {
				e.regTrig(80, 50000)
				e.d.DshCompacted(map[string]any{"session_id": trigSID,
					"ok": true, "prefix_tokens": 25000})
				e.advance(250)
				// 回流+大量新上下文:PeakCtx 46000,增 21000 ≥ 20000——真有东西可压
				e.led.TouchFull("dsh", trigSID, e.trigPath(), *e.now-80, 10,
					"C:/proj", "", 46000, 0)
			},
			func(t *testing.T, e *trigEnv) {
				if e.trigSlot(trigSID) == nil {
					t.Fatal("压缩后新增长过门槛应再压（真有新历史可压）")
				}
			}},
		{"PostPrefix=0历史标记:回落绝对门槛不误伤", 100,
			func(e *trigEnv) {
				e.regTrig(80, 50000)
				// 历史标记形（旧版本立位/缺前缀上报）：PostPrefix=0,增量腿失效
				e.led.Mu().Lock()
				if st := e.led.GetLocked("dsh", trigSID); st != nil {
					st.DshCompressed = &ledger.DshCompressMark{
						TS: e.t0 - 500, Expires: e.t0 - 100, PrePeak: 60000}
				}
				e.led.Mu().Unlock()
			},
			func(t *testing.T, e *trigEnv) {
				if e.trigSlot(trigSID) == nil {
					t.Fatal("PostPrefix=0 回落绝对门槛:peak 50000 应照常入槽")
				}
			}},
		{"PostPrefix=0历史标记+peak不足:绝对门槛照拦", 100,
			func(e *trigEnv) {
				e.regTrig(80, 19999)
				e.led.Mu().Lock()
				if st := e.led.GetLocked("dsh", trigSID); st != nil {
					st.DshCompressed = &ledger.DshCompressMark{
						TS: e.t0 - 500, Expires: e.t0 - 100, PrePeak: 60000}
				}
				e.led.Mu().Unlock()
			},
			func(t *testing.T, e *trigEnv) { wantNoSlot(t, e) }},
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

// TestDshCompactTriggerNoRetriggerAfterFullCycle 全链回归：触发→领取→压缩成功
// 收口→标记过期+执行窗全过→闲置期间零再触发（空转案的生产形态闭环）。
func TestDshCompactTriggerNoRetriggerAfterFullCycle(t *testing.T) {
	e := newTrigEnv(t, 100) // 触发线 80s、指令期 20s、标记期 200s、执行窗 240s
	e.regTrig(80, 50000)
	e.run() // 首次触发入槽
	if e.trigSlot(trigSID) == nil {
		t.Fatal("前置：首次触发应入槽")
	}
	if got := len(e.d.DshPoll(pollBody(trigSID))["commands"].([]map[string]any)); got != 1 {
		t.Fatalf("前置：指令应被领取, got %d", got)
	}
	e.d.DshCompacted(map[string]any{"session_id": trigSID,
		"ok": true, "prefix_tokens": 24985}) // 宿主压缩成功+前缀上报
	// 越过一切节流窗（标记 200s、执行窗 240s、指令期 20s）到稳态闲置
	e.advance(3600)
	for i := 0; i < 3; i++ { // 多轮扫描都不再入槽
		e.run()
		if e.trigSlot(trigSID) != nil {
			t.Fatalf("压缩后纯闲置 1h 不得再触发（第 %d 轮扫描）——空转案回归", i+1)
		}
	}
	if got := e.enqTryList(); len(got) != 1 {
		t.Fatalf("交接不随空转重摆, got %v", got)
	}
}
