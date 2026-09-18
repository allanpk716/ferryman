package store

// 规格：tests/test_store.py 全部 6 例 1:1（T06：valid_handoff 判据、待续 prompt、
// 消费标记、原子写）。另加票面验收专测 4 例：
//   - TestIndexJSONSnakeCaseKeys：落盘 index.json 直接反序列化为 map 断言
//     snake_case 键存在 + blocked_at/consumed_by null 语义（Python None）；
//   - TestTimeFormatsLocal：20060102_150405 / 2006-01-02 15:04:05 本地时区
//     （含 MarkBlocked 落盘回读与 ReadHandoff 基线）；
//   - TestNewBadOrMissingIndexStartsEmpty：index.json 坏/缺 → 空索引；
//   - TestSaveHandoffOverwritesSameSessionAgent：同 (session,agent) 覆盖。

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ferryman/internal/clock"
	"ferryman/internal/mathx"
)

// isoEpoch 对应 Python 助手 _iso：
// datetime.fromtimestamp(epoch, tz=utc).isoformat().replace("+00:00", "Z")。
func isoEpoch(epoch float64) string {
	sec := math.Floor(epoch)
	return time.Unix(int64(sec), int64((epoch-sec)*1e9)).
		UTC().Format("2006-01-02T15:04:05.999999Z07:00")
}

func fptr(f float64) *float64 { return &f }

// mk 对应 _mk：Store(tmp_path / "data")。
func mk(t *testing.T) *Store {
	t.Helper()
	st, err := New(filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// save 对应 _save：sid="s1"/agent="cc"/status="fresh" 为默认；cwd="" ≡ Python
// 默认 Path.cwd()；coversS nil ≡ 默认 time.time()。
func save(t *testing.T, st *Store, sid, agent, cwd string, coversS *float64, status string) Entry {
	t.Helper()
	if cwd == "" {
		wd, err := os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
		cwd = wd
	}
	covers := clock.Now()
	if coversS != nil {
		covers = *coversS
	}
	return st.SaveHandoff(sid, agent, cwd, "t", isoEpoch(covers), status, "x")
}

// ---------- test_valid_handoff_agent_cwd_keys ----------

func TestValidHandoffAgentCwdKeys(t *testing.T) {
	st := mk(t)
	base := t.TempDir()
	proj := filepath.Join(base, "proj") // 与 Python 同：目录不创建，resolve 照常归一
	other := filepath.Join(base, "other")
	save(t, st, "s1", "cc", proj, nil, "fresh")
	if st.ValidHandoff("cc", proj, 0) == nil {
		t.Fatal("同 agent+cwd 应命中")
	}
	if st.ValidHandoff("codex", proj, 0) != nil {
		t.Fatal("agent 双键不应命中")
	}
	if st.ValidHandoff("cc", other, 0) != nil {
		t.Fatal("cwd 双键不应命中")
	}
}

// ---------- test_valid_handoff_covers_tolerance_and_status ----------

func TestValidHandoffCoversToleranceAndStatus(t *testing.T) {
	st := mk(t)
	proj := filepath.Join(t.TempDir(), "proj")
	covers := clock.Now() - 3600
	save(t, st, "s1", "cc", proj, &covers, "fresh")
	if st.ValidHandoff("cc", proj, covers+30) == nil {
		t.Fatal("60s 容差内应命中")
	}
	if st.ValidHandoff("cc", proj, covers+120) != nil {
		t.Fatal("超容差不应命中")
	}
	save(t, st, "s2", "cc", proj, &covers, "stale")
	if e := st.ValidHandoff("cc", proj, 0); e == nil || e.Status == "stale" {
		t.Fatalf("stale 不算: %+v", e)
	}
}

// ---------- test_valid_handoff_freshness_window ----------

func TestValidHandoffFreshnessWindow(t *testing.T) {
	st := mk(t)
	proj := filepath.Join(t.TempDir(), "proj")
	save(t, st, "s1", "cc", proj, fptr(clock.Now()-25*3600), "fresh") // 25h 前
	if st.ValidHandoff("cc", proj, 0) != nil {
		t.Fatal("24h 窗口外不应命中")
	}
}

// ---------- test_valid_handoff_picks_max_covers ----------

func TestValidHandoffPicksMaxCovers(t *testing.T) {
	st := mk(t)
	proj := filepath.Join(t.TempDir(), "proj")
	now := clock.Now()
	save(t, st, "old", "cc", proj, fptr(now-7200), "fresh")
	save(t, st, "new", "cc", proj, fptr(now-60), "fresh")
	best := st.ValidHandoff("cc", proj, now-3600)
	if best == nil || best.SessionID != "new" {
		t.Fatalf("应取 covers 最大: %+v", best)
	}
}

// ---------- test_pending_prompt_truncate_and_consume ----------

func TestPendingPromptTruncateAndConsume(t *testing.T) {
	st := mk(t)
	st.SavePendingPrompt("s1", strings.Repeat("续", 600)) // 600 token > 500 上限
	p := st.PopPendingPrompt("s1", "")
	if p == "" || mathx.RuneLen(p) > 500+10 { // 500 截断 + 截断标记后缀
		t.Fatalf("截断后长度应 ≤510: %d", mathx.RuneLen(p))
	}
	if st.PopPendingPrompt("s1", "") == "" { // 未传 consume → 仍可取
		t.Fatal("未消费应仍可取")
	}
	if st.PopPendingPrompt("s1", "newsess") == "" {
		t.Fatal("消费取应成功")
	}
	if st.PopPendingPrompt("s1", "") != "" { // 已消费 → 不再给
		t.Fatal("已消费不应再给")
	}
	// 票面补充：覆盖同 session 旧条目（新条目未消费，可再取到新 prompt）
	st.SavePendingPrompt("s1", "新的续问")
	if got := st.PopPendingPrompt("s1", ""); got != "新的续问" {
		t.Fatalf("同 session 应覆盖旧条目: %q", got)
	}
}

// ---------- test_mark_injected_dedup_and_atomic_write ----------

func TestMarkInjectedDedupAndAtomicWrite(t *testing.T) {
	st := mk(t)
	e := save(t, st, "s1", "cc", "", nil, "fresh")
	st.MarkInjected(e.HandoffID, "sess-A")
	st.MarkInjected(e.HandoffID, "sess-A")
	raw, err := os.ReadFile(st.indexPath)
	if err != nil {
		t.Fatal(err)
	}
	var idx indexFile
	if err := json.Unmarshal(raw, &idx); err != nil {
		t.Fatal(err)
	}
	var entry *Entry
	for i := range idx.Handoffs {
		if idx.Handoffs[i].HandoffID == e.HandoffID {
			entry = &idx.Handoffs[i]
		}
	}
	if entry == nil {
		t.Fatal("落盘 index 应含该 handoff")
	}
	if len(entry.Injected) != 1 || entry.Injected[0] != "sess-A" {
		t.Fatalf("重复注入应去重: %v", entry.Injected)
	}
	tmps, err := filepath.Glob(filepath.Join(filepath.Dir(st.indexPath), "*.tmp"))
	if err != nil {
		t.Fatal(err)
	}
	if len(tmps) != 0 { // 原子写无残留
		t.Fatalf("不应残留 .tmp: %v", tmps)
	}
}

// ---------- 票面验收：键名断言（snake_case 直出 + null 语义） ----------

func TestIndexJSONSnakeCaseKeys(t *testing.T) {
	st := mk(t)
	e := save(t, st, "s1", "cc", "", nil, "fresh")
	st.SavePendingPrompt("p1", "继续")
	raw, err := os.ReadFile(st.indexPath)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	hs, ok := m["handoffs"].([]any)
	if !ok || len(hs) != 1 {
		t.Fatalf("handoffs 应为 1 条: %v", m["handoffs"])
	}
	h0 := hs[0].(map[string]any)
	for _, k := range []string{"handoff_id", "session_id", "agent", "cwd", "title",
		"created_at", "covers_until", "covers_until_s", "status", "path",
		"blocked_at", "injected"} {
		if _, ok := h0[k]; !ok {
			t.Fatalf("缺 snake_case 键 %q，实际键集: %v", k, h0)
		}
	}
	if v, ok := h0["blocked_at"]; !ok || v != nil {
		t.Fatalf("blocked_at 应为 null（Python None）: %v", h0["blocked_at"])
	}
	if in, ok := h0["injected"].([]any); !ok || len(in) != 0 {
		t.Fatalf("injected 应为空数组: %v", h0["injected"])
	}
	if _, leak := h0["handoffId"]; leak {
		t.Fatal("不得输出 CamelCase 键")
	}
	if h0["handoff_id"] != e.HandoffID {
		t.Fatalf("handoff_id 应回读一致: %v vs %q", h0["handoff_id"], e.HandoffID)
	}
	if _, ok := h0["covers_until_s"].(float64); !ok {
		t.Fatalf("covers_until_s 应为数值: %v", h0["covers_until_s"])
	}
	pending, ok := m["pending_prompts"].([]any)
	if !ok || len(pending) != 1 {
		t.Fatalf("pending_prompts 应为 1 条: %v", m["pending_prompts"])
	}
	p0 := pending[0].(map[string]any)
	for _, k := range []string{"session_id", "prompt", "blocked_at", "consumed_by"} {
		if _, ok := p0[k]; !ok {
			t.Fatalf("pending 缺 snake_case 键 %q，实际键集: %v", k, p0)
		}
	}
	if v, ok := p0["consumed_by"]; !ok || v != nil {
		t.Fatalf("consumed_by 应为 null（Python None）: %v", p0["consumed_by"])
	}
	if p0["prompt"] != "继续" {
		t.Fatalf("非 ASCII 应直出: %v", p0["prompt"])
	}
}

// ---------- 票面验收：时间格式与本地时区 ----------

func TestTimeFormatsLocal(t *testing.T) {
	st := mk(t)
	before := time.Now().Add(-time.Second)
	e := save(t, st, "s1", "cc", "", nil, "fresh")
	after := time.Now().Add(time.Second)

	// id = YYYYmmdd_HHMMSS_hex6
	if len(e.HandoffID) != 15+1+6 {
		t.Fatalf("id 形长应 22: %q", e.HandoffID)
	}
	idT, err := time.ParseInLocation("20060102_150405", e.HandoffID[:15], time.Local)
	if err != nil {
		t.Fatalf("id 前缀应 20060102_150405: %v (%q)", err, e.HandoffID)
	}
	if idT.Before(before) || idT.After(after) {
		t.Fatalf("id 秒级时间应取本地 now: %v ∉ [%v, %v]", idT, before, after)
	}
	for _, c := range e.HandoffID[16:] {
		if !strings.ContainsRune("0123456789abcdef", c) {
			t.Fatalf("id 尾 6 位应为 hex: %q", e.HandoffID)
		}
	}
	// created_at = 2006-01-02 15:04:05（本地）
	ca, err := time.ParseInLocation("2006-01-02 15:04:05", e.CreatedAt, time.Local)
	if err != nil {
		t.Fatalf("created_at 应 2006-01-02 15:04:05: %v (%q)", err, e.CreatedAt)
	}
	if ca.Before(before) || ca.After(after) {
		t.Fatalf("created_at 应取本地 now: %v ∉ [%v, %v]", ca, before, after)
	}
	// ReadHandoff 基线：正文原样读回
	if got := st.ReadHandoff(e); got != "x" {
		t.Fatalf("ReadHandoff 应回正文: %q", got)
	}
	// MarkBlocked：blocked_at null → 本地时间串（落盘回读钉死）
	st.MarkBlocked(e.HandoffID)
	raw, err := os.ReadFile(st.indexPath)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	h0 := m["handoffs"].([]any)[0].(map[string]any)
	ba, ok := h0["blocked_at"].(string)
	if !ok {
		t.Fatalf("MarkBlocked 后 blocked_at 应为时间串: %v", h0["blocked_at"])
	}
	if _, err := time.ParseInLocation("2006-01-02 15:04:05", ba, time.Local); err != nil {
		t.Fatalf("blocked_at 应 2006-01-02 15:04:05: %v (%q)", err, ba)
	}
	// ReadHandoff 读失败 → ""
	if got := st.ReadHandoff(Entry{Path: filepath.Join(st.dir, "missing.md")}); got != "" {
		t.Fatalf("读失败应空串: %q", got)
	}
}

// ---------- 票面补充：坏 index.json → 空索引 ----------

func TestNewBadOrMissingIndexStartsEmpty(t *testing.T) {
	base := t.TempDir()
	data := filepath.Join(base, "data")
	if err := os.MkdirAll(data, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(data, "index.json"), []byte("{bad json"), 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := New(data)
	if err != nil {
		t.Fatal(err)
	}
	if st.ValidHandoff("cc", base, 0) != nil {
		t.Fatal("坏 index 应视为空索引")
	}
	if st.PopPendingPrompt("s1", "") != "" {
		t.Fatal("坏 index 应视为空索引")
	}
}

// ---------- 票面补充：同 (session,agent) 覆盖 ----------

func TestSaveHandoffOverwritesSameSessionAgent(t *testing.T) {
	st := mk(t)
	e1 := save(t, st, "s1", "cc", "", nil, "fresh")
	e2 := save(t, st, "s1", "cc", "", nil, "fresh")
	if e1.HandoffID == e2.HandoffID {
		t.Fatal("两次保存应有不同 id")
	}
	raw, err := os.ReadFile(st.indexPath)
	if err != nil {
		t.Fatal(err)
	}
	var idx indexFile
	if err := json.Unmarshal(raw, &idx); err != nil {
		t.Fatal(err)
	}
	if len(idx.Handoffs) != 1 {
		t.Fatalf("同 (session,agent) 应覆盖为 1 条: %d", len(idx.Handoffs))
	}
	if idx.Handoffs[0].HandoffID != e2.HandoffID {
		t.Fatalf("应保留新条目: %q vs %q", idx.Handoffs[0].HandoffID, e2.HandoffID)
	}
}
