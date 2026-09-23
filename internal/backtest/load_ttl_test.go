// load_ttl_test.go — 管子一测试：窗口×hit 跳配对、每窗最大深度、miss 不
// 反推、区间外跳丢弃、问询窗成对/未闭合、provider 归属、确定性。
package backtest

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func writeTTLFixture(t *testing.T, lines ...string) string {
	t.Helper()
	dir := t.TempDir()
	acc := filepath.Join(dir, "accounts")
	if err := os.MkdirAll(acc, 0o755); err != nil {
		t.Fatal(err)
	}
	body := ""
	for _, l := range lines {
		body += l + "\n"
	}
	if err := os.WriteFile(filepath.Join(acc, "202609.jsonl"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func beatRow(ts float64, outcome, provider, sid string) string {
	return `{"kind":"beat","ts":` + f(ts) + `,"agent":"cc","session_id":"` + sid +
		`","outcome":"` + outcome + `","provider":"` + provider + `","lane":"wait"}`
}

func winRow(sid string, opened, closed float64) string {
	return `{"kind":"window","ts":` + f(closed) + `,"agent":"cc","session_id":"` + sid +
		`","opened_ts":` + f(opened) + `,"closed_ts":` + f(closed) + `,"close_reason":"subagents_done"}`
}

func f(v float64) string { return fmt.Sprintf("%g", v) }

func TestHarvestTTLObsMaxDepthAndMissIgnored(t *testing.T) {
	dir := writeTTLFixture(t,
		winRow("s1", 100, 400),
		beatRow(160, "hit", "glm", "s1"),  // 深度 60s = 1 分钟
		beatRow(200, "miss", "glm", "s1"), // miss 不反推
		beatRow(280, "hit", "glm", "s1"),  // 深度 180s = 3 分钟（最强证据）
		winRow("s2", 500, 600),            // 无 hit 窗：计数照登不出观测
		beatRow(650, "hit", "glm", "s2"),  // 区间外：如实丢弃
	)
	set, err := HarvestTTLObs(dir)
	if err != nil {
		t.Fatal(err)
	}
	if set.Windows != 2 || set.HitWindows != 1 {
		t.Fatalf("Windows=%d HitWindows=%d, want 2/1", set.Windows, set.HitWindows)
	}
	if set.Beats != 4 || set.HitBeats != 3 {
		t.Fatalf("Beats=%d HitBeats=%d, want 4/3", set.Beats, set.HitBeats)
	}
	obs := set.ObsFor("glm")
	if len(obs) != 1 || obs[0] != 3 {
		t.Fatalf("obs = %v, want [3]（每窗最大深度，分钟）", obs)
	}
}

func TestHarvestTTLObsQWatchPairAndUnclosed(t *testing.T) {
	dir := writeTTLFixture(t,
		`{"kind":"qwatch_open","ts":700,"agent":"cc","session_id":"s1"}`,
		beatRow(760, "hit", "glm", "s1"), // 60s
		beatRow(820, "hit", "glm", "s1"), // 120s（更深）
		`{"kind":"qwatch_close","ts":900,"agent":"cc","session_id":"s1"}`,
		`{"kind":"qwatch_open","ts":1000,"agent":"cc","session_id":"s1"}`, // 未闭合
		beatRow(1030, "hit", "glm", "s1"),                                // 30s
	)
	set, err := HarvestTTLObs(dir)
	if err != nil {
		t.Fatal(err)
	}
	if set.Windows != 2 || set.HitWindows != 2 {
		t.Fatalf("Windows=%d HitWindows=%d, want 2/2", set.Windows, set.HitWindows)
	}
	obs := set.ObsFor("glm")
	if len(obs) != 2 || obs[0] != 0.5 || obs[1] != 2 {
		t.Fatalf("obs = %v, want [0.5 2]（升序）", obs)
	}
}

func TestHarvestTTLObsProviderBuckets(t *testing.T) {
	dir := writeTTLFixture(t,
		winRow("s1", 100, 400),
		beatRow(280, "hit", "glm", "s1"), // 3 分钟 → glm
		winRow("s2", 500, 800),
		beatRow(770, "hit", "kimi", "s2"), // 270s=4.5 分钟 → kimi
	)
	set, err := HarvestTTLObs(dir)
	if err != nil {
		t.Fatal(err)
	}
	if obs := set.ObsFor("glm"); len(obs) != 1 || obs[0] != 3 {
		t.Fatalf("glm obs = %v, want [3]", obs)
	}
	if obs := set.ObsFor("kimi"); len(obs) != 1 || obs[0] != 4.5 {
		t.Fatalf("kimi obs = %v, want [4.5]", obs)
	}
	// 确定性：两次收割逐元素一致
	set2, err := HarvestTTLObs(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(set2.ObsFor("glm")) != 1 || set2.ObsFor("glm")[0] != 3 {
		t.Fatalf("二次收割不一致: %v", set2.ObsFor("glm"))
	}
}
