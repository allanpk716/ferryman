package daemon

// watcher_boot_test.go — 重启观察窗 + 摆渡去重（2026-09-30 重启无交接保护案，
// v0.4.4）：daemon 重启后台账清零，存量闲置会话 observed=false 且 enrich 不跑
// → 摆渡永不入队、用户回来无交接（分支6跑步机）。观察窗=24h（FreshWindowS）：
// 窗内 mtime 的存量会话补观察（重启前的活动是真实活动）；窗外古老文件维持
// 不观察（全量摆渡风暴守卫，Python 期有意设计）。去重=ValidHandoff 同口径
// （与闸门分支5同一判定：命中 ⇔ 闸门本来就能用现存交接拦，跳过重摆无回归）。

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"ferryman/internal/config"
	"ferryman/internal/ledger"
	"ferryman/internal/store"
)

// bootFixture 带可控时间戳的 CC 转录（writeMergeTranscript 的时间戳烤死在
// 2026-09-18，与本组冻结钟不匹配——去重用的 ValidHandoff 有 24h 新鲜窗，
// covers 必须与冻结钟同窗，故本地自写）。ts=末条带时间戳记录的 epoch 秒。
func bootFixture(t *testing.T, projects, sid string, ts float64) string {
	t.Helper()
	dir := filepath.Join(projects, "C--proj")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	f := filepath.Join(dir, sid+".jsonl")
	tsISO := time.Unix(int64(ts), 0).UTC().Format("2006-01-02T15:04:05.000Z")
	lines := []string{
		`{"type": "user", "timestamp": "` + tsISO + `", "message": {"role": "user", "content": "干活"}}`,
		`{"type": "assistant", "timestamp": "` + tsISO + `", "message": {"role": "assistant",` +
			` "content": [{"type": "text", "text": "干完了"}]}}`,
	}
	if err := os.WriteFile(f, []byte(lines[0]+"\n"+lines[1]+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return f
}

// bootChmtime 把转录 mtime 拨到指定时刻（闲置模拟的磁盘面：mtime=末次写入）。
func bootChmtime(t *testing.T, f string, at float64) {
	t.Helper()
	tv := time.Unix(int64(at), 0)
	if err := os.Chtimes(f, tv, tv); err != nil {
		t.Fatal(err)
	}
}

// bootWatcher 装配"刚重启"形态的守望者：StartedAt=冻结 now（TouchFull 只认
// mtime ≥ StartedAt 的新写入——存量会话若无观察窗就永不被观察，即本组回归
// 所钉的缺口）。
func bootWatcher(t *testing.T, cfg *config.Config, led *ledger.Ledger, st *store.Store,
	now float64, enqueued *[]string) *Watcher {
	t.Helper()
	w := NewWatcher(cfg, led, st, func(s *ledger.SessionState) bool {
		*enqueued = append(*enqueued, s.SessionID)
		return true
	}, now, nil, nil, nil, nil) // StartedAt=now：daemon 刚起
	w.enrich = func(st *ledger.SessionState) { // 富化替身（同款测试先例）：peak+cwd
		led.Mu().Lock()
		st.PeakCtx = 50000
		st.Cwd = "C:/proj-x"
		led.Mu().Unlock()
	}
	return w
}

func bootCfg(projects string) *config.Config {
	cfg := config.Default()
	cfg.Watch.CCProjectsDir = projects
	cfg.Thresholds = config.ThresholdCfg{SummarizeS: 10, BlockS: 100,
		MinCtxTokens: 1000, CacheWarnS: 720}
	return cfg
}

func TestWatcherObserveRecentWindowFerriesIdleSessionAfterRestart(t *testing.T) {
	now := freezeClock(t, 1_800_000_000.0)
	tmp := t.TempDir()
	projects := filepath.Join(tmp, "projects")
	// 存量会话：重启前 2h 停笔（mtime 早于 daemon 启动时刻 → 旧逻辑永不观察）
	f := bootFixture(t, projects, "boot-1", *now-7200)
	bootChmtime(t, f, *now-7200)

	stt, err := store.New(filepath.Join(tmp, "data"))
	if err != nil {
		t.Fatal(err)
	}
	var enqueued []string
	led := ledger.New()
	w := bootWatcher(t, bootCfg(projects), led, stt, *now, &enqueued)
	w.cxDirs = []string{filepath.Join(tmp, "no-codex")}
	w.pollCC()

	st := led.Get("cc", "boot-1")
	led.Mu().Lock()
	obs, handed := st.ObservedActive, st.HandedOffAt
	led.Mu().Unlock()
	if !obs {
		t.Fatal("重启观察窗：24h 内停笔的存量会话应被补观察")
	}
	if len(enqueued) != 1 || enqueued[0] != "boot-1" {
		t.Fatalf("应入队摆渡 boot-1（重启后首个轮询周期内补交接）: %v", enqueued)
	}
	if handed == 0 {
		t.Fatal("入队应记 HandedOffAt")
	}
}

func TestWatcherObserveRecentWindowSkipsAncientFiles(t *testing.T) {
	// 风暴守卫不破：72h 前的古老文件不补观察、不入队（否则每次重启全量摆渡）。
	now := freezeClock(t, 1_800_000_000.0)
	tmp := t.TempDir()
	projects := filepath.Join(tmp, "projects")
	f := bootFixture(t, projects, "boot-old", *now-3*86400)
	bootChmtime(t, f, *now-3*86400)

	stt, err := store.New(filepath.Join(tmp, "data"))
	if err != nil {
		t.Fatal(err)
	}
	var enqueued []string
	led := ledger.New()
	w := bootWatcher(t, bootCfg(projects), led, stt, *now, &enqueued)
	w.cxDirs = []string{filepath.Join(tmp, "no-codex")}
	w.pollCC()

	st := led.Get("cc", "boot-old")
	led.Mu().Lock()
	obs, handed := st.ObservedActive, st.HandedOffAt
	led.Mu().Unlock()
	if obs {
		t.Fatal("72h 前的古老文件不得补观察（防全量摆渡风暴）")
	}
	if len(enqueued) != 0 {
		t.Fatalf("古老文件不得入队: %v", enqueued)
	}
	if handed != 0 {
		t.Fatal("未入队不得动 HandedOffAt")
	}
}

func TestMaybeEnqueueDedupesWhenValidHandoffCovers(t *testing.T) {
	// 重启后 HandedOffAt/HandledContentTS 归零，但库中已有覆盖当前内容的有效
	// 交接（与闸门分支5同口径）→ 跳过重摆并补记处置章。
	now := freezeClock(t, 1_800_000_000.0)
	tmp := t.TempDir()
	projects := filepath.Join(tmp, "projects")
	f := bootFixture(t, projects, "boot-1", *now-7200)
	bootChmtime(t, f, *now-7200)

	stt, err := store.New(filepath.Join(tmp, "data"))
	if err != nil {
		t.Fatal(err)
	}
	// 既有交接：covers=内容钟+30s（容差内覆盖）、24h 新鲜窗内、同 (cc, cwd)。
	stt.SaveHandoff("boot-1", "cc", "C:/proj-x", "既有交接",
		isoUTC(*now-7200+30), "fresh", "<<<INJECT>>>x<<</INJECT>>>")

	var enqueued []string
	led := ledger.New()
	w := bootWatcher(t, bootCfg(projects), led, stt, *now, &enqueued)
	w.cxDirs = []string{filepath.Join(tmp, "no-codex")}
	w.pollCC()

	st := led.Get("cc", "boot-1")
	led.Mu().Lock()
	handed, handledTS, lastWrite := st.HandedOffAt, st.HandledContentTS, st.LastWrite
	led.Mu().Unlock()
	if len(enqueued) != 0 {
		t.Fatalf("已有有效交接覆盖 → 不得重摆: %v", enqueued)
	}
	if handed != lastWrite {
		t.Fatalf("去重应补记 HandedOffAt=LastWrite: %v vs %v", handed, lastWrite)
	}
	if d := handledTS - (*now - 7200); d < -1 || d > 1 {
		t.Fatalf("HandledContentTS 应=内容钟: %v vs %v", handledTS, *now-7200)
	}
}

func TestWatcherObserveRecentCodexWindow(t *testing.T) {
	// codex 轨同形：24h 内停笔的存量 rollout 补观察并摆渡。
	// sid 不带连字符：codexSidFromStem 按 rollout 文件名末段取 sid，带杠会被截。
	now := freezeClock(t, 1_800_000_000.0)
	tmp := t.TempDir()
	cx := filepath.Join(tmp, "cx")
	f := writeRollout(t, cx, "codexboot1")
	bootChmtime(t, f, *now-7200)

	stt, err := store.New(filepath.Join(tmp, "data"))
	if err != nil {
		t.Fatal(err)
	}
	var enqueued []string
	led := ledger.New()
	w := bootWatcher(t, bootCfg(filepath.Join(tmp, "no-cc")), led, stt, *now, &enqueued)
	w.ccDir = filepath.Join(tmp, "no-cc")
	w.cxDirs = []string{cx}
	w.pollCodex()

	st := led.Get("codex", "codexboot1")
	if st == nil {
		t.Fatal("rollout 应登记")
	}
	led.Mu().Lock()
	obs := st.ObservedActive
	led.Mu().Unlock()
	if !obs {
		t.Fatal("codex 重启观察窗：存量近活会话应被补观察")
	}
	if len(enqueued) != 1 || enqueued[0] != "codexboot1" {
		t.Fatalf("应入队摆渡 codexboot1: %v", enqueued)
	}
}
