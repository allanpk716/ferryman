package daemon

// poller_baseline_test.go — dsh-host-guard 票02：poller 身份心跳基线的钉子
//（compact_test.go 先例;时间纪律同 gate_test.go：clock 冻结在 t0,advance 推进）。
//
//   - 生命周期矩阵：online（新鲜窗 90s 内）/stale（24h 内有心跳∧无下线∧静默
//     >90s）/offline（显式下线标记,静默多久都不算 stale）/retired（静默>24h）;
//     边界钉点：恰 90s 仍 online、恰 24h 仍 stale（判定用严格大于）。
//   - 首建前（基线文件不存在且无心跳）→读面空＝无任何可判对象（F8 首建盲区:
//     不判任何沉默）;读路径不得建盘。
//   - roundtrip：心跳→落盘;包级态复位（＝重启等价——单守护进程现实下包级态
//     即进程态,resetDshPollers 清内存回落加载标记）→同 DataDir 再问即自盘
//     恢复，「插件先死、daemon 后重启」可检出（恢复条目推进 91s 判 stale）。
//   - 损坏即弃：坏 JSON 读不动→弃置不炸;后续心跳按重建写回好文件。
//   - 跃迁日志：只在 online→stale 跃迁打一行（pollerLogf 缝捕获）,持续静默
//     不重复打;复活后再沉默＝新的一轮静默,可再打。

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newPollerEnv 票02 基线环境：gateEnv 全套（DataDir 钉沙箱＋clock 冻结）＋
// 包级基线态复位（resetDshPollers——freezeClock 还原同纪律,防跨用例渗漏）。
func newPollerEnv(t *testing.T) *gateEnv {
	t.Helper()
	resetDshPollers()
	t.Cleanup(resetDshPollers)
	return newGateEnv(t)
}

// pollerFile 基线文件路径（与生产同解析式：<DataDir>/dsh-pollers.json）。
func pollerFile(e *gateEnv) string {
	return filepath.Join(e.d.Cfg.DataDir(), "dsh-pollers.json")
}

// pollerStateOfName 读面快照里找某 poller 的状态（缺名回空串）。
func pollerStateOfName(e *gateEnv, name string) string {
	for _, p := range e.d.dshPollerStates() {
		if p.Name == name {
			return p.State
		}
	}
	return ""
}

func TestDshPollerLifecycleMatrix(t *testing.T) {
	e := newPollerEnv(t)

	// online：新鲜窗（90s）内的心跳。
	e.d.dshPollerBeat("desktop", false, e.t0)
	if got := pollerStateOfName(e, "desktop"); got != "online" {
		t.Fatalf("刚心跳应 online, got %s", got)
	}
	e.advance(30)
	e.d.dshPollerSweep(*e.now)
	if got := pollerStateOfName(e, "desktop"); got != "online" {
		t.Fatalf("静默 30s（<90s 窗）应 online, got %s", got)
	}

	// stale：24h 内有心跳 ∧ 无下线标记 ∧ 静默>90s。
	e.advance(61) // 累计静默 91s
	e.d.dshPollerSweep(*e.now)
	if got := pollerStateOfName(e, "desktop"); got != "stale" {
		t.Fatalf("静默 91s 应 stale, got %s", got)
	}

	// 边界钉点：恰 90s 仍 online（判定严格大于）;恰 24h 仍 stale。
	e.d.dshPollerBeat("edge", false, *e.now)
	e.advance(90)
	if got := pollerStateOfName(e, "edge"); got != "online" {
		t.Fatalf("静默恰 90s 应 online（>90s 才 stale）, got %s", got)
	}
	e.advance(0.5)
	if got := pollerStateOfName(e, "edge"); got != "stale" {
		t.Fatalf("静默 90.5s 应 stale, got %s", got)
	}
	e.advance(dshPollerRetireS - 90.5) // 静默恰 24h 整
	if got := pollerStateOfName(e, "edge"); got != "stale" {
		t.Fatalf("静默恰 24h 应 stale（>24h 才 retired）, got %s", got)
	}
	e.advance(1)
	if got := pollerStateOfName(e, "edge"); got != "retired" {
		t.Fatalf("静默 24h+1s 应 retired, got %s", got)
	}

	// offline：显式下线标记压过一切推断——静默再久也不算 stale/retired
	//（插件 dispose 上报形态）。
	e.d.dshPollerBeat("web", true, *e.now)
	e.advance(2 * 86400)
	if got := pollerStateOfName(e, "web"); got != "offline" {
		t.Fatalf("显式下线标记应 offline（非 stale/retired）, got %s", got)
	}

	// 复活：下线标记后一轮真实心跳即消除（spec E F9「下轮心跳即消除」）。
	e.d.dshPollerBeat("web", false, *e.now)
	if got := pollerStateOfName(e, "web"); got != "online" {
		t.Fatalf("下线标记后心跳应复活 online, got %s", got)
	}
}

func TestDshPollerFirstBuildBlindSpot(t *testing.T) {
	e := newPollerEnv(t) // 无心跳、无基线文件
	// 读面空＝无任何可判对象（spec E F8：首建前盲区,不判任何沉默）。
	if got := e.d.dshPollerStates(); len(got) != 0 {
		t.Fatalf("首建前读面应为空: %+v", got)
	}
	// 读路径不得建盘（基线只随心跳落盘）。
	if _, err := os.Stat(pollerFile(e)); !os.IsNotExist(err) {
		t.Fatalf("读路径不得创建基线文件: %v", err)
	}
}

func TestDshPollerBaselineRoundtrip(t *testing.T) {
	e := newPollerEnv(t)

	// 心跳→落盘（惰性:评估轮收口时写;心环节律至多每轮一次）。
	e.d.dshPollerBeat("desktop", false, e.t0)
	e.advance(10)
	e.d.dshPollerBeat("desktop", false, *e.now) // last_seen 前进也是变化,随轮落盘
	if _, err := os.Stat(pollerFile(e)); err != nil {
		t.Fatalf("心跳后基线文件应落盘: %v", err)
	}

	// 重启等价：包级态复位（内存清零＋加载标记回落）——单守护进程现实下包级
	// 态即进程态,同 DataDir 再问即自盘恢复。
	resetDshPollers()
	states := e.d.dshPollerStates()
	if len(states) != 1 || states[0].Name != "desktop" {
		t.Fatalf("重启后读面应自盘恢复 desktop: %+v", states)
	}
	if states[0].FirstSeen != e.t0 || states[0].LastSeen != e.t0+10 || states[0].Offline {
		t.Fatalf("恢复条目字段不符（first/last_seen 保真）: %+v", states[0])
	}
	// 「插件先死、daemon 后重启」可检出：恢复条目时间推进越 90s 窗 → stale。
	e.advance(91)
	if got := pollerStateOfName(e, "desktop"); got != "stale" {
		t.Fatalf("重启后恢复条目推进 91s 应判 stale（插件先死可检出）, got %s", got)
	}

	// 重启后新心跳照常入账;再一轮重启 first_seen 仍保真（重启链不漂移）。
	e.d.dshPollerBeat("web", false, *e.now)
	states = e.d.dshPollerStates()
	if len(states) != 2 {
		t.Fatalf("重启后应恢复+新入账并存: %+v", states)
	}
	resetDshPollers()
	for _, p := range e.d.dshPollerStates() {
		if p.Name == "desktop" && p.FirstSeen != e.t0 {
			t.Fatalf("多轮重启 first_seen 应保真: %+v", p)
		}
		if p.Name == "web" && p.LastSeen != *e.now {
			t.Fatalf("新入账条目 last_seen 应保真: %+v", p)
		}
	}
}

func TestDshPollerCorruptBaselineRebuilt(t *testing.T) {
	e := newPollerEnv(t)
	// 预置坏文件（截断/磁盘损坏形态）。
	if err := os.WriteFile(pollerFile(e), []byte("{not-json"), 0o644); err != nil {
		t.Fatal(err)
	}
	// 读面：弃置不炸、无条目可判（空表起步）。
	if got := e.d.dshPollerStates(); len(got) != 0 {
		t.Fatalf("损坏基线应弃置（空读面）: %+v", got)
	}
	// 后续心跳按重建写回好文件;重启等价后可恢复。
	e.d.dshPollerBeat("desktop", false, e.t0)
	resetDshPollers()
	states := e.d.dshPollerStates()
	if len(states) != 1 || states[0].Name != "desktop" {
		t.Fatalf("损坏弃置后应按心跳重建: %+v", states)
	}
}

func TestDshPollerTransitionLogOnce(t *testing.T) {
	e := newPollerEnv(t)
	orig := pollerLogf
	var lines []string
	pollerLogf = func(format string, args ...any) {
		lines = append(lines, fmt.Sprintf(format, args...))
	}
	t.Cleanup(func() { pollerLogf = orig })

	e.d.dshPollerBeat("desktop", false, e.t0) // online
	e.advance(91)
	e.d.dshPollerBeat("web", false, *e.now) // 心环节律:本轮评估全表
	if len(lines) != 1 {
		t.Fatalf("online→stale 跃迁应打恰一行, got %d: %q", len(lines), lines)
	}
	if !strings.Contains(lines[0], "[poller] desktop") || !strings.Contains(lines[0], "静默>90s") {
		t.Fatalf("跃迁行形状不符（[poller] <名> 静默>90s）: %q", lines[0])
	}
	wantTS := time.Unix(int64(e.t0), 0).Format("2006-01-02 15:04:05")
	if !strings.Contains(lines[0], wantTS) {
		t.Fatalf("跃迁行应带最近心跳时刻 %q: %q", wantTS, lines[0])
	}

	// 持续静默不重复打（跃迁制）。
	e.advance(100)
	e.d.dshPollerBeat("web", false, *e.now)
	if len(lines) != 1 {
		t.Fatalf("持续静默不得重复打, got %d: %q", len(lines), lines)
	}

	// 复活→再沉默＝新的一轮静默,可再打一行。
	e.d.dshPollerBeat("desktop", false, *e.now) // 复活（评估态复位 online）
	e.advance(91)
	e.d.dshPollerBeat("web", false, *e.now)
	if len(lines) != 2 {
		t.Fatalf("复活后再沉默应再打一行（新静默轮）, got %d: %q", len(lines), lines)
	}

	// 自盘恢复条目（lastState 未评估）首轮判 stale＝状态确立非跃迁:不打行
	//（「只在 online→stale 跃迁时打」的严格口径;检出走读面）。
	resetDshPollers() // 内存清零,基线文件仍在（前两段已落盘）
	e.advance(dshPollerFreshS + 1)
	before := len(lines)
	_ = e.d.dshPollerStates() // 读面触发自盘加载
	e.d.dshPollerSweep(*e.now)
	if len(lines) != before {
		t.Fatalf("恢复条目首轮确立 stale 不得打跃迁行, got %q", lines[before:])
	}
}
