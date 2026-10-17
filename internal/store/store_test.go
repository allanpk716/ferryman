package store

// 规格：tests/test_store.py 全部 6 例 1:1（T06：valid_handoff 判据、待续 prompt、
// 消费标记、原子写）。另加票面验收专测 4 例：
//   - TestIndexJSONSnakeCaseKeys：落盘 index.json 直接反序列化为 map 断言
//     snake_case 键存在 + blocked_at/consumed_by null 语义（Python None）；
//   - TestTimeFormatsLocal：20060102_150405 / 2006-01-02 15:04:05 本地时区
//     （含 MarkBlocked 落盘回读与 ReadHandoff 基线）；
//   - TestNewBadOrMissingIndexStartsEmpty：index.json 坏/缺 → 空索引；
//   - TestSaveHandoffOverwritesSameSessionAgent：同 (session,agent) 覆盖。
// 另加票01（2026-10-06 强续消耗）专测 4 例：消耗→供出链拿不到 + 闸门判无
// 覆盖；同 (agent,cwd) 其他会话不受影响；重启（重开 store）后标记仍在；
// 标记范围（只标 fresh/skeleton）/幂等返回数/落盘载体（consumed_at null 语义）。

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

// 2026-09-30 归还锚定（/clear 串台案）：按 (agent,cwd) 找最新未消费、未超窗的
// 待续；旧条目（无锚定字段）不参与；消费后回落更早一条；全部消费/超窗不给。
func TestLatestPendingForAnchor(t *testing.T) {
	st := mk(t)
	st.SavePendingPromptFor("cc", "C:/Proj", "s1", "第一条")
	st.SavePendingPromptFor("cc", "C:/other", "s2", "别的项目") // cwd 不同
	st.SavePendingPrompt("legacy", "旧条目无锚定字段")          // 旧形态（升级前存量）
	p, ok := st.LatestPendingFor("cc", "c:/proj")               // 大小写/分隔符归一
	if !ok || p.SessionID != "s1" || p.Prompt != "第一条" {
		t.Fatalf("锚 = %+v ok=%v, want s1/第一条", p, ok)
	}
	if _, ok := st.LatestPendingFor("cc", "C:/nope"); ok {
		t.Fatal("cwd 无匹配不应给锚")
	}
	if _, ok := st.LatestPendingFor("codex", "C:/Proj"); ok {
		t.Fatal("agent 不同不应给锚")
	}
	if _, ok := st.LatestPendingFor("", "C:/Proj"); ok {
		t.Fatal("空 agent 不应给锚（防御）")
	}
	// 同 cwd 两条未消费：倒扫取最新（追加序）
	st.SavePendingPromptFor("cc", "C:/Proj", "s3", "第二条")
	if p, _ := st.LatestPendingFor("cc", "C:/Proj"); p.SessionID != "s3" {
		t.Fatalf("最新锚 = %s, want s3", p.SessionID)
	}
	// 消费最新 → 回落更早那条（仍新鲜未消费）
	if got := st.PopPendingPrompt("s3", "consumer-1"); got != "第二条" {
		t.Fatalf("Pop = %q", got)
	}
	if p, ok := st.LatestPendingFor("cc", "C:/Proj"); !ok || p.SessionID != "s1" {
		t.Fatalf("消费后应回落 s1, got %+v ok=%v", p, ok)
	}
	st.PopPendingPrompt("s1", "consumer-2")
	if _, ok := st.LatestPendingFor("cc", "C:/Proj"); ok {
		t.Fatal("全部消费后不应给锚")
	}
	// 超窗残锚（3 小时前被拦、一直没 /clear）不得劫持今日开场
	st.mu.Lock()
	old := time.Now().Add(-3 * time.Hour).Format(dispTimeFmt)
	st.index.PendingPrompts = append(st.index.PendingPrompts,
		PendingPrompt{SessionID: "s9", Prompt: "残锚", BlockedAt: old,
			Agent: "cc", Cwd: filepath.Join("C:", "Proj")}) // 反斜杠形态也须归一命中路径
	st.mu.Unlock()
	if _, ok := st.LatestPendingFor("cc", "C:/Proj"); ok {
		t.Fatal("超窗残锚不应劫持归还")
	}
	// 坏 BlockedAt 同样不劫持（fail-safe）
	st.mu.Lock()
	st.index.PendingPrompts = append(st.index.PendingPrompts,
		PendingPrompt{SessionID: "s10", Prompt: "坏时间", BlockedAt: "not-a-time",
			Agent: "cc", Cwd: "C:/Proj"})
	st.mu.Unlock()
	if _, ok := st.LatestPendingFor("cc", "C:/Proj"); ok {
		t.Fatal("坏 BlockedAt 不应给锚")
	}
}

// ---------- 票01（2026-10-06 强续消耗）：store 层交接消耗标记 ----------
//
// 语义：bypass 一经使用，被强续会话自己的旧交接立即作废——供出链
//（RestoreCandidates）与闸门覆盖判定（ValidHandoff，去重/boot 补记共用）
// 均不认；同 (agent,cwd) 其他会话不受影响；标记持久化于 index.json
//（consumed_at，与 pending 同载体），重启后仍在。agent 限定（仅 dsh 消耗）
// 由票02 接线面执行，store 层只做能力与过滤。

func TestConsumeHandoffsInvalidatesForRestoreAndGate(t *testing.T) {
	st := mk(t)
	proj := filepath.Join(t.TempDir(), "proj")
	save(t, st, "s1", "dsh", proj, nil, "fresh")
	if len(st.RestoreCandidates("dsh", proj)) != 1 {
		t.Fatal("消耗前供出链应能拿到")
	}
	if st.ValidHandoff("dsh", proj, 0) == nil {
		t.Fatal("消耗前闸门应判覆盖命中")
	}
	if n := st.ConsumeHandoffs("dsh", "s1"); n != 1 {
		t.Fatalf("应消耗 1 条: %d", n)
	}
	if len(st.RestoreCandidates("dsh", proj)) != 0 {
		t.Fatal("消耗后供出链不应再拿到")
	}
	if st.ValidHandoff("dsh", proj, 0) != nil {
		t.Fatal("消耗后闸门应判无覆盖")
	}
}

func TestConsumeHandoffsLeavesSameDirOtherSessions(t *testing.T) {
	st := mk(t)
	proj := filepath.Join(t.TempDir(), "proj")
	now := clock.Now()
	save(t, st, "old", "dsh", proj, fptr(now-3600), "fresh")
	save(t, st, "new", "dsh", proj, fptr(now-60), "fresh")
	if h := st.ValidHandoff("dsh", proj, now-7200); h == nil || h.SessionID != "new" {
		t.Fatalf("消耗前闸门应取 covers 最大: %+v", h)
	}
	if n := st.ConsumeHandoffs("dsh", "new"); n != 1 {
		t.Fatalf("应消耗 1 条: %d", n)
	}
	// 被消耗会话出列；同目录其他会话照常供给/认定。lastWrite=now-7200 时
	// old 的 60s 容差判定本就通过——new 出列只能因消耗，非容差假象。
	if h := st.ValidHandoff("dsh", proj, now-7200); h == nil || h.SessionID != "old" {
		t.Fatalf("同目录其他会话应不受影响: %+v", h)
	}
	cands := st.RestoreCandidates("dsh", proj)
	if len(cands) != 1 || cands[0].SessionID != "old" {
		t.Fatalf("供出链应只剩 old: %+v", cands)
	}
	// agent/session 不匹配 → 0 条，不动别人
	if n := st.ConsumeHandoffs("cc", "old"); n != 0 {
		t.Fatalf("agent 不同不应消耗: %d", n)
	}
	if n := st.ConsumeHandoffs("dsh", "s9"); n != 0 {
		t.Fatalf("session 不存在不应消耗: %d", n)
	}
	if h := st.ValidHandoff("dsh", proj, now-7200); h == nil || h.SessionID != "old" {
		t.Fatalf("空消耗调用后 old 应仍有效: %+v", h)
	}
}

func TestConsumeHandoffsPersistsAcrossReopen(t *testing.T) {
	base := t.TempDir()
	data := filepath.Join(base, "data")
	st, err := New(data)
	if err != nil {
		t.Fatal(err)
	}
	proj := filepath.Join(base, "proj")
	save(t, st, "s1", "dsh", proj, nil, "fresh")
	save(t, st, "s2", "dsh", proj, nil, "fresh")
	if n := st.ConsumeHandoffs("dsh", "s1"); n != 1 {
		t.Fatalf("应消耗 1 条: %d", n)
	}
	st2, err := New(data) // 重开 store（模拟重启重载 index.json）
	if err != nil {
		t.Fatal(err)
	}
	if h := st2.ValidHandoff("dsh", proj, 0); h == nil || h.SessionID != "s2" {
		t.Fatalf("重启后 s1 应仍被闸门拒认、s2 照常: %+v", h)
	}
	cands := st2.RestoreCandidates("dsh", proj)
	if len(cands) != 1 || cands[0].SessionID != "s2" {
		t.Fatalf("重启后供出链应只剩 s2: %+v", cands)
	}
}

func TestConsumeHandoffsScopeCountAndCarrier(t *testing.T) {
	st := mk(t)
	proj := filepath.Join(t.TempDir(), "proj")
	save(t, st, "sOld", "dsh", proj, nil, "stale") // 非 fresh/skeleton：不标
	save(t, st, "sSkel", "dsh", proj, nil, "skeleton")
	save(t, st, "sNew", "dsh", proj, nil, "fresh")
	if n := st.ConsumeHandoffs("dsh", "sOld"); n != 0 {
		t.Fatalf("stale 本就不供给，不应标记: %d", n)
	}
	if n := st.ConsumeHandoffs("dsh", "sSkel"); n != 1 {
		t.Fatalf("skeleton 应消耗: %d", n)
	}
	if n := st.ConsumeHandoffs("dsh", "sNew"); n != 1 {
		t.Fatalf("fresh 应消耗: %d", n)
	}
	if n := st.ConsumeHandoffs("dsh", "sNew"); n != 0 {
		t.Fatalf("重复消耗应幂等返回 0: %d", n)
	}
	// 落盘载体：已消耗条目 consumed_at 为时间串，未标记条目保持 null
	//（snake_case 键名，与 blocked_at/consumed_by 载体纪律同形）。
	raw, err := os.ReadFile(st.indexPath)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, hi := range m["handoffs"].([]any) {
		h := hi.(map[string]any)
		sid, _ := h["session_id"].(string)
		seen[sid] = true
		switch sid {
		case "sNew", "sSkel":
			if _, ok := h["consumed_at"].(string); !ok {
				t.Fatalf("已消耗条目 consumed_at 应为时间串: %v", h)
			}
		case "sOld":
			if v, ok := h["consumed_at"]; !ok || v != nil {
				t.Fatalf("未标记条目 consumed_at 应为 null: %v", h["consumed_at"])
			}
		}
	}
	for _, sid := range []string{"sOld", "sSkel", "sNew"} {
		if !seen[sid] {
			t.Fatalf("落盘 index 缺 %s", sid)
		}
	}
}

// 2026-10-11 上一代交接案（c2ffdd87→23040098→db572a88）：同会话重摆渡若内容
// 没推进（covers 未越过旧条目+容差），继承旧条目的注入记录——否则"已被摆渡
// 接走"的事实随重生丢失，上一代交接冒充别的工作线；内容真推进（强续后续
// 干）则不继承，两条线都活着。
func TestSaveHandoffCarriesInjectedWhenCoversNotAdvanced(t *testing.T) {
	st := mk(t)
	base := clock.Now() - 3600
	e1 := save(t, st, "s1", "cc", "C:/p", fptr(base), "fresh")
	st.MarkInjected(e1.HandoffID, "succ")
	e2 := save(t, st, "s1", "cc", "C:/p", fptr(base+30), "fresh") // 容差内＝同内容重生
	if len(e2.Injected) != 1 || e2.Injected[0] != "succ" {
		t.Fatalf("同内容重生应继承注入记录: %+v", e2.Injected)
	}
	if got := st.RestoreCandidates("cc", "C:/p"); len(got) != 1 || len(got[0].Injected) != 1 {
		t.Fatalf("索引内条目应带继承的注入记录: %+v", got)
	}
	e3 := save(t, st, "s1", "cc", "C:/p", fptr(base+600), "fresh") // 内容真推进
	if len(e3.Injected) != 0 {
		t.Fatalf("内容推进后不继承: %+v", e3.Injected)
	}
}

// DropSuperseded：交接 H 注入过的某会话 T 已有自己的交接（T 接着干出了
// 内容）＝H 是本线上一代，供出清单剔除；只注入未产出（T 无交接）不算接替。
func TestDropSupersededAncestor(t *testing.T) {
	st := mk(t)
	base := clock.Now() - 3600
	anc := save(t, st, "anc", "cc", "C:/p", fptr(base), "fresh")
	st.MarkInjected(anc.HandoffID, "idle-clear") // 注入后没干活：无交接
	other := save(t, st, "other", "cc", "C:/p", fptr(base+10), "fresh")
	got := st.DropSuperseded(st.RestoreCandidates("cc", "C:/p"))
	if len(got) != 2 {
		t.Fatalf("注入会话无交接不算接替, 应保留 2 条: %+v", got)
	}
	st.MarkInjected(anc.HandoffID, "succ")
	succ := save(t, st, "succ", "cc", "C:/p", fptr(base+100), "fresh")
	got = st.DropSuperseded(st.RestoreCandidates("cc", "C:/p"))
	ids := []string{}
	for _, e := range got {
		ids = append(ids, e.HandoffID)
	}
	if len(got) != 2 || got[0].HandoffID != succ.HandoffID || got[1].HandoffID != other.HandoffID {
		t.Fatalf("上一代应被剔除、其余保序: %v", ids)
	}
	// 接替者是别的 agent 的同名会话不算（agent 隔离）。
	st2 := mk(t)
	a2 := save(t, st2, "anc", "cc", "C:/p", fptr(base), "fresh")
	st2.MarkInjected(a2.HandoffID, "succ")
	save(t, st2, "succ", "codex", "C:/p", fptr(base+100), "fresh")
	if got := st2.DropSuperseded(st2.RestoreCandidates("cc", "C:/p")); len(got) != 1 {
		t.Fatalf("跨 agent 不算接替: %+v", got)
	}
}

// ---------- fix/gate-machine-exempt：作答→打字护选择（de9af594 案） ----------
//
// savePending 的 PriorChoice 携带规则：打字入口覆盖同会话窗内未消费记录时
// 保留旧选择（作答取其 Prompt；打字续传其 PriorChoice——作答→打字→打字链
// 不丢）；作答入口不继承（新一轮选择）；旧记录须未消费且在 PendingAnchorS
// 窗内。

func TestSavePendingAnswerThenTypingKeepsPriorChoice(t *testing.T) {
	st := mk(t)
	st.SavePendingPromptForEntry("cc", "C:/p", "s1", "午饭吃面还是吃饭?→面", "answer")
	st.SavePendingPromptForEntry("cc", "C:/p", "s1", "唤醒后用户自己打字", "")
	p, ok := st.PopPendingPromptFull("s1", "")
	if !ok {
		t.Fatal("应有待续")
	}
	if p.Prompt != "唤醒后用户自己打字" || p.Entry != "" {
		t.Fatalf("新待续应为打字条目: %+v", p)
	}
	if p.PriorChoice != "午饭吃面还是吃饭?→面" {
		t.Fatalf("旧作答选择应保留到 PriorChoice: %q", p.PriorChoice)
	}
}

func TestSavePendingOtherCombosOverwrite(t *testing.T) {
	// 打字→打字：最新覆盖，无 PriorChoice
	st := mk(t)
	st.SavePendingPromptForEntry("cc", "C:/p", "s1", "第一条打字", "")
	st.SavePendingPromptForEntry("cc", "C:/p", "s1", "第二条打字", "")
	p, _ := st.PopPendingPromptFull("s1", "")
	if p.Prompt != "第二条打字" || p.PriorChoice != "" {
		t.Fatalf("打字→打字应最新覆盖且不携带: %+v", p)
	}
	// 作答→作答：最新覆盖，无 PriorChoice
	st2 := mk(t)
	st2.SavePendingPromptForEntry("cc", "C:/p", "s1", "选择一", "answer")
	st2.SavePendingPromptForEntry("cc", "C:/p", "s1", "选择二", "answer")
	p2, _ := st2.PopPendingPromptFull("s1", "")
	if p2.Prompt != "选择二" || p2.Entry != "answer" || p2.PriorChoice != "" {
		t.Fatalf("作答→作答应最新覆盖且不携带: %+v", p2)
	}
}

func TestSavePendingPriorChoiceNeedsFreshUnconsumedAnswer(t *testing.T) {
	// 旧作答已超窗（3 小时前被拦、一直没 /clear）→ 打字不携带
	st := mk(t)
	st.SavePendingPromptForEntry("cc", "C:/p", "s1", "陈年选择", "answer")
	st.mu.Lock()
	old := time.Now().Add(-3 * time.Hour).Format(dispTimeFmt)
	for i := range st.index.PendingPrompts {
		if st.index.PendingPrompts[i].SessionID == "s1" {
			st.index.PendingPrompts[i].BlockedAt = old
		}
	}
	st.mu.Unlock()
	st.SavePendingPromptForEntry("cc", "C:/p", "s1", "今日打字", "")
	p, _ := st.PopPendingPromptFull("s1", "")
	if p.PriorChoice != "" {
		t.Fatalf("超窗旧作答不应携带: %+v", p)
	}
	// 旧作答已消费（已注入过）→ 打字不携带
	st2 := mk(t)
	st2.SavePendingPromptForEntry("cc", "C:/p", "s1", "已交付选择", "answer")
	if got := st2.PopPendingPrompt("s1", "consumer"); got != "已交付选择" {
		t.Fatalf("消费取回应得旧作答: %q", got)
	}
	st2.SavePendingPromptForEntry("cc", "C:/p", "s1", "之后的打字", "")
	p2, _ := st2.PopPendingPromptFull("s1", "")
	if p2.Prompt != "之后的打字" || p2.PriorChoice != "" {
		t.Fatalf("已消费旧作答不应携带: %+v", p2)
	}
}

func TestSavePendingTypingChainKeepsPriorChoice(t *testing.T) {
	st := mk(t)
	st.SavePendingPromptForEntry("cc", "C:/p", "s1", "午饭吃面还是吃饭?→面", "answer")
	st.SavePendingPromptForEntry("cc", "C:/p", "s1", "第一次打字", "")
	st.SavePendingPromptForEntry("cc", "C:/p", "s1", "第二次打字", "")
	p, ok := st.PopPendingPromptFull("s1", "")
	if !ok {
		t.Fatal("应有待续")
	}
	if p.Prompt != "第二次打字" || p.Entry != "" {
		t.Fatalf("最新打字应覆盖: %+v", p)
	}
	// 回归（2026-10-17 审查）：第二次打字时旧记录已是打字（带 PriorChoice），
	// 若只认作答旧记录，选择会随覆盖丢失——须链式续传。
	if p.PriorChoice != "午饭吃面还是吃饭?→面" {
		t.Fatalf("作答→打字→打字后 PriorChoice 应仍是最初选择: %q", p.PriorChoice)
	}
	// 打字（带 PriorChoice）→作答：新一轮选择，不继承
	st.SavePendingPromptForEntry("cc", "C:/p", "s1", "新一轮选择→饭", "answer")
	p2, _ := st.PopPendingPromptFull("s1", "")
	if p2.Entry != "answer" || p2.PriorChoice != "" {
		t.Fatalf("作答入口不应继承旧 PriorChoice: %+v", p2)
	}
}

func TestSavePendingTypingChainNeedsInWindowUnconsumed(t *testing.T) {
	// 旧打字（带 PriorChoice）超窗 → 不续传
	st := mk(t)
	st.SavePendingPromptForEntry("cc", "C:/p", "s1", "最初选择", "answer")
	st.SavePendingPromptForEntry("cc", "C:/p", "s1", "陈年打字", "")
	st.mu.Lock()
	old := time.Now().Add(-3 * time.Hour).Format(dispTimeFmt)
	for i := range st.index.PendingPrompts {
		if st.index.PendingPrompts[i].SessionID == "s1" {
			st.index.PendingPrompts[i].BlockedAt = old
		}
	}
	st.mu.Unlock()
	st.SavePendingPromptForEntry("cc", "C:/p", "s1", "今日打字", "")
	p, _ := st.PopPendingPromptFull("s1", "")
	if p.Prompt != "今日打字" || p.PriorChoice != "" {
		t.Fatalf("超窗旧打字不应续传 PriorChoice: %+v", p)
	}
	// 旧打字（带 PriorChoice）已消费 → 不续传
	st2 := mk(t)
	st2.SavePendingPromptForEntry("cc", "C:/p", "s1", "最初选择", "answer")
	st2.SavePendingPromptForEntry("cc", "C:/p", "s1", "已交付打字", "")
	if got := st2.PopPendingPrompt("s1", "consumer"); got != "已交付打字" {
		t.Fatalf("消费取回应得打字待续: %q", got)
	}
	st2.SavePendingPromptForEntry("cc", "C:/p", "s1", "之后的打字", "")
	p2, _ := st2.PopPendingPromptFull("s1", "")
	if p2.Prompt != "之后的打字" || p2.PriorChoice != "" {
		t.Fatalf("已消费旧打字不应续传 PriorChoice: %+v", p2)
	}
}

func TestPendingInWindow(t *testing.T) {
	st := mk(t)
	st.SavePendingPromptForEntry("cc", "C:/p", "s1", "待续", "answer")
	if p, ok := st.PendingInWindow("s1"); !ok || p.Prompt != "待续" {
		t.Fatalf("窗内未消费应命中: %+v ok=%v", p, ok)
	}
	if _, ok := st.PendingInWindow("s2"); ok {
		t.Fatal("无记录不应命中")
	}
	st.PopPendingPrompt("s1", "consumer") // 消费
	if _, ok := st.PendingInWindow("s1"); ok {
		t.Fatal("已消费不应命中")
	}
	// 超窗/坏时间 fail-safe
	st.SavePendingPromptForEntry("cc", "C:/p", "s3", "超窗", "")
	st.mu.Lock()
	old := time.Now().Add(-3 * time.Hour).Format(dispTimeFmt)
	for i := range st.index.PendingPrompts {
		if st.index.PendingPrompts[i].SessionID == "s3" {
			st.index.PendingPrompts[i].BlockedAt = old
		}
	}
	st.mu.Unlock()
	if _, ok := st.PendingInWindow("s3"); ok {
		t.Fatal("超窗不应命中")
	}
}
