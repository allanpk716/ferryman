package daemon

// query_sessions_test.go — 票01：GET /sessions 与 GET /session 验收钉子＋
// 三个 stub 端点注册钉子＋红线反向断言（无消息内容/无凭据回显）＋/stats
// HTTP 面字段契约快照（改动前后逐字一致）。
//
// 夹具风格：真 Daemon（台账/交接库/账本）＋临时端口 serveBg（绝不绑定
// 15722/15724 生产端口）＋冻结时钟（idle 判定确定性）。

import (
	"encoding/json"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ferryman/internal/accounts"
	"ferryman/internal/config"
	"ferryman/internal/ledger"
	"ferryman/internal/pathsx"
	"ferryman/internal/store"
)

// queryEnv 查询面测试夹具：真 Daemon（台账/交接库/账本）＋临时端口 HTTP。
type queryEnv struct {
	d     *Daemon
	led   *ledger.Ledger
	st    *store.Store
	acc   *accounts.Accounts
	port  int
	token string
	t0    float64
	now   *float64
}

func newQueryEnv(t *testing.T) *queryEnv {
	t.Helper()
	tmp := t.TempDir()
	e := &queryEnv{t0: 1_800_000_000.0, token: "qtest-token"}
	e.now = freezeClock(t, e.t0)
	st, err := store.New(filepath.Join(tmp, "data"))
	if err != nil {
		t.Fatal(err)
	}
	acc, err := accounts.New(filepath.Join(tmp, "acc"))
	if err != nil {
		t.Fatal(err)
	}
	e.st = st
	e.acc = acc
	e.led = ledger.New()
	cfg := config.Default()
	cfg.GateCC = "enforce"
	cfg.Thresholds = config.ThresholdCfg{SummarizeS: 10.0, BlockS: 30.0,
		MinCtxTokens: 100, CacheWarnS: 720}
	e.d = NewDaemon(cfg, e.led, st, func(*ledger.SessionState) bool { return true },
		acc, 0, nil)
	e.port = freePort(t)
	serveBg(t, e.d, e.port, e.token)
	return e
}

// qreg 登记一条会话（last_write = t0 - idleS）。
func (e *queryEnv) qreg(agent, sid, path, cwd string, idleS float64, peak int) {
	e.led.TouchFull(agent, sid, path, e.t0-idleS, 10, cwd, "", peak, 0)
}

// keys JSON 对象键集（顶层）。
func keys(m map[string]any) map[string]bool {
	out := map[string]bool{}
	for k := range m {
		out[k] = true
	}
	return out
}

func keySetEqual(got map[string]bool, want ...string) bool {
	if len(got) != len(want) {
		return false
	}
	for _, w := range want {
		if !got[w] {
			return false
		}
	}
	return true
}

// walkKeys 递归收集全部对象键（凭据字段反向断言用）。
func walkKeys(v any, out *[]string) {
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			*out = append(*out, k)
			walkKeys(val, out)
		}
	case []any:
		for _, item := range x {
			walkKeys(item, out)
		}
	}
}

// ---- GET /sessions ----

func TestQuerySessionsSevenFieldsOrderAndStale(t *testing.T) {
	e := newQueryEnv(t)
	e.qreg("cc", "s-old", `C:\tmp\s-old.jsonl`, `C:\proj`, 40, 9000)  // 凉（>30）
	e.qreg("cc", "s-mid", `C:\tmp\s-mid.jsonl`, `C:\proj`, 30, 8000)  // 恰达阈值 → 凉
	e.qreg("cc", "s-new", `C:\tmp\s-new.jsonl`, `C:\proj`, 5, 7000)   // 未凉
	e.qreg("codex", "s-cx", `C:\tmp\s-cx.jsonl`, `C:\proj2`, 1, 6000) // 未凉

	code, raw := getRaw(t, e.port, "/sessions", e.token)
	if code != 200 {
		t.Fatalf("GET /sessions = %d %q", code, raw)
	}
	var resp struct {
		Sessions []map[string]any `json:"sessions"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("响应非 JSON: %v (%q)", err, raw)
	}
	if len(resp.Sessions) != 4 {
		t.Fatalf("sessions 条数 = %d, want 4: %v", len(resp.Sessions), resp.Sessions)
	}
	// 按最后写入倒序（闲置最短在前）。
	wantOrder := []string{"s-cx", "s-new", "s-mid", "s-old"}
	for i, row := range resp.Sessions {
		if row["session_id"] != wantOrder[i] {
			t.Fatalf("第 %d 行 = %v, want %s（应按最后写入倒序）", i, row["session_id"], wantOrder[i])
		}
		if !keySetEqual(keys(row), "session_id", "agent", "cwd", "idle_s",
			"stale", "peak_ctx", "lineage_id") {
			t.Fatalf("行字段应为七要素, got %v: %v", keys(row), row)
		}
	}
	byID := map[string]map[string]any{}
	for _, row := range resp.Sessions {
		byID[row["session_id"].(string)] = row
	}
	// 闲置判定：恰达/超过拦截阈值（30s）→ 凉；跨 Agent 同阈值。
	for sid, wantStale := range map[string]bool{
		"s-old": true, "s-mid": true, "s-new": false, "s-cx": false} {
		if byID[sid]["stale"] != wantStale {
			t.Fatalf("%s stale = %v, want %v", sid, byID[sid]["stale"], wantStale)
		}
	}
	// 最后写入距今秒（冻结时钟下精确）。
	if idle, ok := byID["s-old"]["idle_s"].(float64); !ok || idle != 40.0 {
		t.Fatalf("s-old idle_s = %v, want 40", byID["s-old"]["idle_s"])
	}
	// 族系 = 归一化转录路径（lineage 唯一键形）。
	if byID["s-old"]["lineage_id"] != pathsx.NormPath(`C:\tmp\s-old.jsonl`) {
		t.Fatalf("lineage_id = %v", byID["s-old"]["lineage_id"])
	}
	if byID["s-old"]["agent"] != "cc" || byID["s-cx"]["agent"] != "codex" {
		t.Fatalf("agent 字段: %v / %v", byID["s-old"]["agent"], byID["s-cx"]["agent"])
	}
}

func TestQuerySessionsEmptyLedgerReturnsEmptyArray(t *testing.T) {
	e := newQueryEnv(t)
	code, raw := getRaw(t, e.port, "/sessions", e.token)
	if code != 200 {
		t.Fatalf("GET /sessions = %d %q", code, raw)
	}
	if !strings.Contains(string(raw), `"sessions":[]`) {
		t.Fatalf("空台账应返回空数组（非 null/非错误）: %q", raw)
	}
}

func TestQuerySessionsFiltersAndLimit(t *testing.T) {
	e := newQueryEnv(t)
	e.qreg("cc", "f-a", `C:\tmp\f-a.jsonl`, `C:\x\p1`, 5, 100)
	e.qreg("cc", "f-b", `C:\tmp\f-b.jsonl`, `C:\x\p2`, 10, 100)
	e.qreg("codex", "f-c", `C:\tmp\f-c.jsonl`, `C:\x\p3`, 1, 100)
	e.qreg("cc", "f-d", `C:\tmp\f-d.jsonl`, `C:\y\p4`, 2, 100)

	get := func(q string) []map[string]any {
		t.Helper()
		code, raw := getRaw(t, e.port, "/sessions?"+q, e.token)
		if code != 200 {
			t.Fatalf("GET /sessions?%s = %d %q", q, code, raw)
		}
		var resp struct {
			Sessions []map[string]any `json:"sessions"`
		}
		if err := json.Unmarshal(raw, &resp); err != nil {
			t.Fatalf("响应非 JSON: %v (%q)", err, raw)
		}
		return resp.Sessions
	}

	if rows := get("agent=cc"); len(rows) != 3 {
		t.Fatalf("agent=cc 应 3 条, got %d", len(rows))
	}
	if rows := get("agent=codex"); len(rows) != 1 || rows[0]["session_id"] != "f-c" {
		t.Fatalf("agent=codex 应只 f-c: %v", rows)
	}
	if rows := get("cwd=" + url.QueryEscape(`C:\x`)); len(rows) != 3 {
		t.Fatalf("cwd 前缀 C:\\x 应 3 条, got %d", len(rows))
	}
	if rows := get("agent=cc&cwd=" + url.QueryEscape(`C:\x\p1`)); len(rows) != 1 ||
		rows[0]["session_id"] != "f-a" {
		t.Fatalf("agent+cwd 组合应只 f-a: %v", rows)
	}
	if rows := get("limit=1"); len(rows) != 1 || rows[0]["session_id"] != "f-c" {
		t.Fatalf("limit=1 应只留最新（f-c）: %v", rows)
	}
	if code, raw := getRaw(t, e.port, "/sessions?limit=abc", e.token); code != 400 {
		t.Fatalf("limit 非法应 400, got %d %q", code, raw)
	}
}

func TestQuerySessionsDefaultLimit50(t *testing.T) {
	e := newQueryEnv(t)
	for i := 0; i < 55; i++ {
		sid := "l-" + string(rune('a'+i/26)) + string(rune('a'+i%26))
		e.qreg("cc", sid, `C:\tmp\`+sid+`.jsonl`, `C:\proj`, float64(i), 100)
	}
	code, raw := getRaw(t, e.port, "/sessions", e.token)
	if code != 200 {
		t.Fatalf("GET /sessions = %d %q", code, raw)
	}
	var resp struct {
		Sessions []map[string]any `json:"sessions"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Sessions) != 50 {
		t.Fatalf("默认 limit 应 50, got %d", len(resp.Sessions))
	}
	// 最旧的 5 条（i=50..54）被裁掉；最新的在最前。
	if resp.Sessions[0]["session_id"] != "l-aa" || resp.Sessions[49]["session_id"] != "l-bx" {
		t.Fatalf("默认裁最旧: 首 %v 末 %v", resp.Sessions[0]["session_id"], resp.Sessions[49]["session_id"])
	}
}

func TestQueryEndpointsRequireBearer(t *testing.T) {
	e := newQueryEnv(t)
	for _, path := range []string{"/sessions", "/session?id=x", "/gate_check", "/report", "/beats"} {
		if code, body := getRaw(t, e.port, path, ""); code != 401 ||
			string(body) != `{"error":"unauthorized"}` {
			t.Fatalf("GET %s 无 Bearer = %d %q, want 401（与 /stats 同语义）", path, code, body)
		}
		if code, _ := getRaw(t, e.port, path, "wrong-token"); code != 401 {
			t.Fatalf("GET %s 错 token = %d, want 401", path, code)
		}
	}
}

// ---- GET /session ----

func TestQuerySessionDetailAggregation(t *testing.T) {
	e := newQueryEnv(t)
	e.qreg("cc", "sd-1", `C:\tmp\sd-1.jsonl`, `C:\proj`, 5, 5000)
	e.qreg("cc", "sd-2", `C:\tmp\sd-2.jsonl`, `C:\proj2`, 5, 4000)

	lin := pathsx.NormPath(`C:\tmp\sd-1.jsonl`)
	mustRec := func(sid string, ts, in, cr, cc, out float64) {
		t.Helper()
		if _, err := e.acc.Record("usage", ts, accounts.Fields{
			"agent": "cc", "session_id": sid, "lineage_id": lin, "project": "C:/proj",
			"model": "glm-5.3", "title": "t",
			"input_tokens": in, "cache_read_tokens": cr, "cache_creation_tokens": cc,
			"output_tokens": out, "offset": 0,
		}); err != nil {
			t.Fatal(err)
		}
	}
	// 主转录两行＋子代理流水一行（子代理行记主谱系——四列加总口径）。
	mustRec("sd-1", e.t0-40, 100, 10, 5, 30)
	mustRec("sd-1", e.t0-20, 200, 20, 10, 40)
	mustRec("sd-1-sub1", e.t0-10, 1000, 100, 50, 500)
	// 另一会话的流水不得串账。
	if _, err := e.acc.Record("usage", e.t0-5, accounts.Fields{
		"agent": "cc", "session_id": "sd-2", "lineage_id": pathsx.NormPath(`C:\tmp\sd-2.jsonl`),
		"project": "C:/proj", "model": "glm-5.3", "title": "t",
		"input_tokens": 99999, "cache_read_tokens": 0, "cache_creation_tokens": 0,
		"output_tokens": 0, "offset": 0}); err != nil {
		t.Fatal(err)
	}

	// 有效交接按项目目录匹配（CONTEXT「有效交接」）：sd-1/sd-2 各归各项目，
	// sd-1 为 fresh、sd-2 为 skeleton（covers 在未来）。
	coversISO := time.Unix(int64(e.t0+600), 0).UTC().Format(time.RFC3339)
	e.st.SaveHandoff("sd-1", "cc", `C:\proj`, "T1", coversISO, "fresh", "MD-SENTINEL-正文")
	e.st.SaveHandoff("sd-2", "cc", `C:\proj2`, "T2", coversISO, "skeleton", "md2")

	// 窗口状态：等待窗开（subagent start）＋停车标志；问询窗开。
	if _, err := e.d.Subagent(map[string]any{"event": "start", "agent": "cc",
		"session_id": "sd-1"}); err != nil {
		t.Fatal(err)
	}
	e.d.windowsMu.Lock()
	parked := e.t0 - 30
	e.d.windows[winKey{"cc", "sd-1"}].StopTS = &parked
	e.d.windowsMu.Unlock()
	e.led.Mu().Lock()
	st := e.led.GetLocked("cc", "sd-1")
	opened := e.t0 - 60
	st.QWatchOpenedTS = &opened
	st.QWatchBeatsFired = 2
	e.led.Mu().Unlock()

	code, raw := getRaw(t, e.port, "/session?id=sd-1", e.token)
	if code != 200 {
		t.Fatalf("GET /session?id=sd-1 = %d %q", code, raw)
	}
	var resp map[string]any
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("响应非 JSON: %v (%q)", err, raw)
	}
	lg := resp["ledger"].(map[string]any)
	if !keySetEqual(keys(lg), "session_id", "agent", "transcript_path", "cwd",
		"title", "last_write", "idle_s", "size", "peak_ctx", "observed_active",
		"handed_off_at", "lineage_id") {
		t.Fatalf("ledger 键集: %v", keys(lg))
	}
	if lg["session_id"] != "sd-1" || lg["agent"] != "cc" || lg["cwd"] != `C:\proj` ||
		lg["peak_ctx"] != 5000.0 || lg["lineage_id"] != lin {
		t.Fatalf("ledger 字段: %v", lg)
	}
	if idle, ok := lg["idle_s"].(float64); !ok || idle != 5.0 {
		t.Fatalf("idle_s = %v, want 5", lg["idle_s"])
	}
	// 四列总账＝主转录＋各子代理加总。
	ut := resp["usage_total"].(map[string]any)
	if !keySetEqual(keys(ut), "input_tokens", "cache_read_tokens",
		"cache_creation_tokens", "output_tokens", "requests") {
		t.Fatalf("usage_total 键集: %v", keys(ut))
	}
	if ut["input_tokens"] != 1300.0 || ut["cache_read_tokens"] != 130.0 ||
		ut["cache_creation_tokens"] != 65.0 || ut["output_tokens"] != 570.0 ||
		ut["requests"] != 3.0 {
		t.Fatalf("usage_total 四列加总: %v", ut)
	}
	// 有效交接覆盖：fresh＋covers_until。
	h := resp["handoff"].(map[string]any)
	if h["status"] != "fresh" || h["covers_until"] != coversISO {
		t.Fatalf("handoff = %v", h)
	}
	// 窗口状态：等待窗开且停车；问询窗开。
	wins := resp["windows"].(map[string]any)
	wait := wins["wait"].(map[string]any)
	if wait["open"] != true || wait["parked"] != true {
		t.Fatalf("wait window = %v（应开且停车）", wait)
	}
	qw := wins["qwatch"].(map[string]any)
	if qw["open"] != true || qw["beats_fired"] != 2.0 {
		t.Fatalf("qwatch window = %v", qw)
	}

	// sd-2：skeleton 交接、无窗口。
	code, raw = getRaw(t, e.port, "/session?id=sd-2", e.token)
	if code != 200 {
		t.Fatalf("GET /session?id=sd-2 = %d %q", code, raw)
	}
	var resp2 map[string]any
	if err := json.Unmarshal(raw, &resp2); err != nil {
		t.Fatal(err)
	}
	if h2 := resp2["handoff"].(map[string]any); h2["status"] != "skeleton" {
		t.Fatalf("sd-2 handoff = %v, want skeleton", h2)
	}
	w2 := resp2["windows"].(map[string]any)
	if w2["wait"].(map[string]any)["open"] != false ||
		w2["qwatch"].(map[string]any)["open"] != false {
		t.Fatalf("sd-2 无窗口: %v", w2)
	}
	if ut2 := resp2["usage_total"].(map[string]any); ut2["input_tokens"] != 99999.0 {
		t.Fatalf("sd-2 usage 不得串账: %v", ut2)
	}
}

func TestQuerySessionNotFoundAndMissingID(t *testing.T) {
	e := newQueryEnv(t)
	e.qreg("cc", "sd-1", `C:\tmp\sd-1.jsonl`, `C:\proj`, 5, 5000)
	code, raw := getRaw(t, e.port, "/session?id=nope", e.token)
	if code != 404 {
		t.Fatalf("id 不存在应 404, got %d %q", code, raw)
	}
	var e404 map[string]any
	if err := json.Unmarshal(raw, &e404); err != nil || e404["error"] == nil {
		t.Fatalf("404 应为 JSON error: %q", raw)
	}
	if code, raw = getRaw(t, e.port, "/session", e.token); code != 400 {
		t.Fatalf("缺 id 应 400, got %d %q", code, raw)
	}
}

// ---- stub 端点注册在位 ----

func TestQueryStubsReturnNotImplemented(t *testing.T) {
	e := newQueryEnv(t)
	for _, path := range []string{"/gate_check", "/report", "/beats"} {
		code, raw := getRaw(t, e.port, path, e.token)
		if code != 501 {
			t.Fatalf("GET %s = %d %q, want 501（stub）", path, code, raw)
		}
		var body map[string]any
		if err := json.Unmarshal(raw, &body); err != nil || body["error"] != "not implemented" {
			t.Fatalf("%s stub 应明确「未实现」: %q", path, raw)
		}
	}
	// 查询面之外的未知路径仍 404（既有语义不动）。
	if code, raw := getRaw(t, e.port, "/definitely-not", e.token); code != 404 ||
		string(raw) != `{"error":"not found"}` {
		t.Fatalf("未知 GET = %d %q, want 404", code, raw)
	}
}

// ---- 红线反向断言：无消息内容、无凭据字段 ----

func TestQueryRedlinesNoContentNoCredentials(t *testing.T) {
	e := newQueryEnv(t)
	e.qreg("cc", "sd-1", `C:\tmp\sd-1.jsonl`, `C:\proj`, 5, 5000)
	// 被拦原话（对话原文）与交接正文：绝不经查询面外吐。
	e.st.SavePendingPrompt("sd-1", "PROMPT-SENTINEL-用户被拦原话")
	coversISO := time.Unix(int64(e.t0+600), 0).UTC().Format(time.RFC3339)
	e.st.SaveHandoff("sd-1", "cc", `C:\proj`, "T1", coversISO, "fresh",
		"HANDOFF-MSG-SENTINEL-交接正文摘录")

	paths := []string{"/sessions", "/session?id=sd-1", "/gate_check", "/report", "/beats"}
	for _, p := range paths {
		_, raw := getRaw(t, e.port, p, e.token)
		body := string(raw)
		for _, sentinel := range []string{"PROMPT-SENTINEL", "HANDOFF-MSG-SENTINEL", e.token} {
			if strings.Contains(body, sentinel) {
				t.Fatalf("GET %s 泄漏红线内容 %q: %q", p, sentinel, body)
			}
		}
		var v any
		if err := json.Unmarshal(raw, &v); err != nil {
			t.Fatalf("GET %s 非 JSON: %q", p, raw)
		}
		var allKeys []string
		walkKeys(v, &allKeys)
		for _, k := range allKeys {
			switch strings.ToLower(k) {
			case "api_key", "apikey", "token", "secret", "authorization", "password":
				t.Fatalf("GET %s 含凭据类字段 %q: %q", p, k, body)
			}
		}
	}
}

// ---- /stats 契约快照（改动前后逐字一致） ----

func TestQueryStatsHTTPContractUnchanged(t *testing.T) {
	e := newQueryEnv(t)
	code, raw := getRaw(t, e.port, "/stats", e.token)
	if code != 200 {
		t.Fatalf("GET /stats = %d %q", code, raw)
	}
	var resp map[string]any
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatal(err)
	}
	if !keySetEqual(keys(resp), "gate_calls_total", "gate_calls_by_agent",
		"last_gate_call_s_ago", "last_transcript_write_s_ago", "subagents_active",
		"subagent_events_total", "health_alert", "health_msg", "qwatch",
		"glm_balance") {
		t.Fatalf("/stats 字段集漂移: %v", keys(resp))
	}
}
