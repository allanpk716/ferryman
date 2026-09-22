package daemon

// adr0013_content_clock_test.go — ADR-0013 内容推进守卫的回归测试。
//
// 2026-09-21 archify 案回放：CC 对仍开着的会话周期性落无时间戳状态块
// （last-prompt/ai-title/mode/permission-mode/atis-latch），文件时钟（mtime）
// 前进而内容时钟（最后带时间戳记录）不动——旧 handedOff/lastWrite 双 mtime
// 口径每幻影写入一轮即重摆渡一次（6 天 356 次，covers 冻结致交接结构性
// 无效）。三件套：守望者不入队、闸门按内容时钟认交接、工人回写处置边界。

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ferryman/internal/cctrans"
	"ferryman/internal/clock"
	"ferryman/internal/config"
	"ferryman/internal/ledger"
	"ferryman/internal/store"
)

// appendFileLines 追加若干 jsonl 行（不改其余内容）。
func appendFileLines(t *testing.T, path string, lines ...string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, ln := range lines {
		if _, err := f.WriteString(ln + "\n"); err != nil {
			t.Fatal(err)
		}
	}
}

// phantomStateBlock CC 周期性本地状态块（实测形状：无 timestamp 字段）。
func phantomStateBlock(t *testing.T, sid string) []string {
	t.Helper()
	j := func(v any) string {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	return []string{
		j(map[string]any{"type": "last-prompt", "leafUuid": "d1", "sessionId": sid}),
		j(map[string]any{"type": "mode", "mode": "normal", "sessionId": sid}),
		j(map[string]any{"type": "permission-mode", "permissionMode": "default", "sessionId": sid}),
		j(map[string]any{"type": "atis-latch", "atis": "", "sessionId": sid}),
		j(map[string]any{"type": "ai-title", "aiTitle": "测试标题", "sessionId": sid}),
	}
}

// TestADR0013PhantomWriteNoReferry 核心回归：幻影写入（mtime 前进、内容不动）
// 不再重复入队；真新内容恢复入队。
func TestADR0013PhantomWriteNoReferry(t *testing.T) {
	tmp := t.TempDir()
	projects := filepath.Join(tmp, "projects")
	cfg := config.Default()
	cfg.Thresholds = config.ThresholdCfg{SummarizeS: 0.1, BlockS: 1.0, MinCtxTokens: 10}
	cfg.Watch.CCProjectsDir = projects
	cfg.Watch.CodexSessionsDir = filepath.Join(tmp, "no-codex")
	led := ledger.New()
	proj := filepath.Join(tmp, "proj")
	f := writeIntegSession(t, projects, "phantom-1", proj, 2000)

	var enqueued int
	w := NewWatcher(cfg, led, nil, func(*ledger.SessionState) bool {
		enqueued++
		return true
	}, 0, nil, nil, nil, nil)

	base := clock.Now()
	st := led.TouchFull("cc", "phantom-1", f, base-100, 10, proj, "", 0, 0)
	w.maybeEnqueue(st) // 首次：正常入队
	if enqueued != 1 {
		t.Fatalf("首次入队 = %d, want 1", enqueued)
	}

	// 模拟摆渡完成：worker 回写处置边界（内容时钟的最后时间戳）。
	contentTS, ok := cctrans.LastTimestamp(f)
	if !ok {
		t.Fatal("LastTimestamp 未取到")
	}
	led.Mu().Lock()
	st.HandledContentTS = contentTS
	st.HandedOffAt = base - 99 // 摆渡发生在幻影写入之前
	led.Mu().Unlock()

	// 幻影写入：追加状态块（无 timestamp），mtime 前进，闲置达标。
	appendFileLines(t, f, phantomStateBlock(t, "phantom-1")...)
	led.TouchFull("cc", "phantom-1", f, base-50, 20, proj, "", 0, 0)
	w.maybeEnqueue(st)
	if enqueued != 1 { // 旧口径此处会第 2 次入队（bug 现场）
		t.Fatalf("幻影写入后入队 = %d, want 1（内容未推进）", enqueued)
	}

	// 再来一轮幻影写入（小时级重复的等价压缩）：仍不入队。
	appendFileLines(t, f, phantomStateBlock(t, "phantom-1")...)
	led.TouchFull("cc", "phantom-1", f, base-30, 30, proj, "", 0, 0)
	w.maybeEnqueue(st)
	if enqueued != 1 {
		t.Fatalf("第二轮幻影后入队 = %d, want 1", enqueued)
	}

	// 真新内容（带 timestamp，晚于处置边界+容差）：恢复入队。
	newer := time.Now().Add(90 * time.Second).UTC().Format("2006-01-02T15:04:05.000Z")
	appendFileLines(t, f, fmt.Sprintf(`{"type":"user","timestamp":%q,"cwd":%q,"sessionId":"phantom-1","message":{"role":"user","content":"新活"}}`, newer, proj))
	led.TouchFull("cc", "phantom-1", f, base-10, 40, proj, "", 0, 0)
	w.maybeEnqueue(st)
	if enqueued != 2 {
		t.Fatalf("真新内容后入队 = %d, want 2", enqueued)
	}
}

// TestADR0013GateValidByContentClock 闸门侧：内容时钟基准下，covers 覆盖内容的
// 交接构成有效交接（分支5 block 带交接）；旧 mtime 基准此场景会退化为无交接
// 的分支7 警告。
func TestADR0013GateValidByContentClock(t *testing.T) {
	e := newGateEnv(t)
	proj := filepath.Join(e.tmp, "proj")
	// 会话闲置超 block；内容时钟停在 1 小时前（真内容），其后只有状态块幻影写入。
	e.reg("s1", "C:/p1.jsonl", proj, testBlockS+5, 99999)
	e.led.Mu().Lock()
	if st := e.led.GetLocked("cc", "s1"); st != nil {
		st.ContentTS = e.t0 - 3600
		st.ContentStamp = st.LastWrite // 版本章一致（已算态）
	} else {
		t.Fatal("台账无 s1")
	}
	e.led.Mu().Unlock()
	// 交接 covers 与内容时钟同刻（摆渡恰当时产出的形状）。
	e.store.SaveHandoff("s1", "cc", proj, "t", isoUTC(e.t0-3600), "fresh", "md")
	r := e.d.Gate(gateBody4("s1", "C:/p1.jsonl", proj, "被拦的原话"))
	if r["decision"] != "block" {
		t.Fatalf("decision = %v, want block（内容时钟基准下交接有效）", r["decision"])
	}
	if _, ok := r["handoff_path"]; !ok {
		t.Fatalf("block 应带交接路径: %v", r)
	}
	if got := e.enqueuedList(); len(got) != 0 {
		t.Fatalf("有效交接在场不应再入队, enqueued = %v", got)
	}
}

// TestADR0013WorkerMarksHandledContent 工人回写处置边界：fresh/skeleton 产出
// 后 HandledContentTS=covers epoch；未接线/坏 ISO/无会话静默跳过。
func TestADR0013WorkerMarksHandledContent(t *testing.T) {
	led := ledger.New()
	led.Touch("cc", "w1", "C:/x.jsonl", 100, 10, 0)
	w := &Worker{Ledger: led}
	w.markHandledContent("cc", "w1", "2026-09-21T07:56:35Z")
	led.Mu().Lock()
	got := led.GetLocked("cc", "w1").HandledContentTS
	led.Mu().Unlock()
	want, _ := cctrans.TSToEpoch("2026-09-21T07:56:35Z")
	if got != want {
		t.Fatalf("HandledContentTS = %v, want %v", got, want)
	}
	// 坏 ISO：不覆盖已有值。
	w.markHandledContent("cc", "w1", "not-a-time")
	led.Mu().Lock()
	got = led.GetLocked("cc", "w1").HandledContentTS
	led.Mu().Unlock()
	if got != want {
		t.Fatalf("坏 ISO 后 HandledContentTS = %v, want 保持 %v", got, want)
	}
	// 未接线/无会话：不 panic。
	w.Ledger = nil
	w.markHandledContent("cc", "w1", "2026-09-21T07:56:35Z")
}

// TestADR0013LineageInheritsHandledContent 族系继承：同转录换 sid 后处置边界
// 随之继承（防 resume 换代后重摆渡一轮）。
func TestADR0013LineageInheritsHandledContent(t *testing.T) {
	led := ledger.New()
	st1 := led.Touch("cc", "lin-1", `C:\proj\a.jsonl`, 100, 10, 0)
	led.Mu().Lock()
	st1.HandledContentTS = 12345.0
	led.Mu().Unlock()
	st2 := led.Touch("cc", "lin-2", `C:\proj\a.jsonl`, 100, 10, 0)
	led.Mu().Lock()
	got := st2.HandledContentTS
	led.Mu().Unlock()
	if got != 12345.0 {
		t.Fatalf("族系继承 HandledContentTS = %v, want 12345", got)
	}
}

// TestADR0013PruneOlderThan 交接库 30 天清理：超龄文件（条目+孤儿）删、新近
// 保留、非交接命名（用户误放）保留、超龄 index 行清。
func TestADR0013PruneOlderThan(t *testing.T) {
	tmp := t.TempDir()
	if _, err := store.New(tmp); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(tmp, "handoffs")
	old := time.Now().Add(-31 * 24 * time.Hour)
	oldName := old.Format("20060102_150405") + "_ab12cd.md"
	freshName := time.Now().Format("20060102_150405") + "_ff00ff.md"
	for _, n := range []string{oldName, freshName, "MORNING-REPORT-x.md"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// 超龄 index 行（手工造：写 index.json 后重载）。
	oldIdx := `{"handoffs":[{"handoff_id":"` + strings.TrimSuffix(oldName, ".md") +
		`","session_id":"s","agent":"cc","cwd":"","title":"","created_at":"` +
		old.Format("2006-01-02 15:04:05") +
		`","covers_until":"","covers_until_s":0,"status":"fresh","path":"","blocked_at":null,"injected":[]}],"pending_prompts":[]}`
	if err := os.WriteFile(filepath.Join(tmp, "index.json"), []byte(oldIdx), 0o644); err != nil {
		t.Fatal(err)
	}
	st2, err := store.New(tmp)
	if err != nil {
		t.Fatal(err)
	}
	n := st2.PruneOlderThan(store.PruneAge)
	if n != 1 {
		t.Fatalf("删除数 = %d, want 1（仅超龄 md；误放文件不计）", n)
	}
	for _, name := range []string{freshName, "MORNING-REPORT-x.md"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("%s 应保留: %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, oldName)); !os.IsNotExist(err) {
		t.Fatalf("超龄文件应已删: %v", err)
	}
	// index 行也应清空。
	data, _ := os.ReadFile(filepath.Join(tmp, "index.json"))
	if strings.Contains(string(data), strings.TrimSuffix(oldName, ".md")) {
		t.Fatalf("超龄 index 行未清: %s", data)
	}
}
