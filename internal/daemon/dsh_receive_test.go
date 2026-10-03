package daemon

// dsh_receive_test.go — dsh phase2 票04：daemon 接收面三口的表驱动钉子
//（spec「daemon 接收面」，D5 同构复用）。
//
//   - POST /dsh/gate 闸门问询：判定走既有 Gate 入口——台账 miss 放行、非 cc
//     档开关（gate.codex_mode）、observe 只警告＋摆渡入队、enforce 拦截＋交接
//     ＋原话保存，全套既有语义原样生效（「不复制阈值逻辑」的验收＝Stats/
//     决策/副作用全部按 Gate 出，本文件不另写一分判定）；
//   - POST /dsh/event 事件接收：Touch("dsh") 登记＋usage 四列入账；行字段与
//     P2-1 pollDsh 现行行做 keyset 直接 diff（逐字段一致，pollDsh 改字段即红）；
//     子会话直报随父入账不 Touch；坏形静默收窄（空键/未知事件/usage 非对象/
//     无账本——200 不炸不 5xx）；
//   - POST /dsh/handoff 交接查询：Restore 整体复用（agent 钉 dsh——同
//     agent+cwd 语义，cc 交接不串线）；守门序与 /shutdown、/provider_switch
//     同族：loopback → POST → Bearer；坏 JSON 400；替身 DaemonLike 不拦截，
//     落既有 doPost 未知路径 404。

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"ferryman/internal/accounts"
	"ferryman/internal/config"
	"ferryman/internal/ledger"
	"ferryman/internal/store"
)

const dshRcvSID = "session-33333333-3333-4333-8333-333333333333"

// newDshRcvEnv 票04 接收面环境：gateEnv 全套＋账本 Accounts（事件口记账断言）
// ＋gate.codex_mode 档位（dsh 判定走非 cc 档＝既有语义）。clock 冻结在 t0。
func newDshRcvEnv(t *testing.T, gateMode string) *gateEnv {
	t.Helper()
	e := &gateEnv{tmp: t.TempDir(), t0: 1_800_000_000.0}
	e.now = freezeClock(t, e.t0)
	st, err := store.New(filepath.Join(e.tmp, "data"))
	if err != nil {
		t.Fatal(err)
	}
	e.store = st
	e.led = ledger.New()
	acc, err := accounts.New(filepath.Join(e.tmp, "acc"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	// 测试卫生（2026-09-30 生产污染案）：DataDir 钉沙箱——gateWarn 不落生产。
	cfg.Server.DataDir = filepath.Join(e.tmp, "data")
	cfg.GateCodex = gateMode
	cfg.Thresholds = config.ThresholdCfg{
		SummarizeS: testSummarizeS, BlockS: testBlockS, MinCtxTokens: testMinCtx,
		CacheWarnS: 720,
	}
	e.d = NewDaemon(cfg, e.led, st, func(s *ledger.SessionState) bool {
		e.mu.Lock()
		e.enqueued = append(e.enqueued, s.Agent+"/"+s.SessionID)
		e.mu.Unlock()
		return true
	}, acc, 0, nil)
	return e
}

// ---- ①闸门问询口：判定复用既有 Gate 语义（同一判定入口） ----

func TestDshGateReusesExistingJudgement(t *testing.T) {
	const sid = dshRcvSID
	reg := func(e *gateEnv, idleS float64) {
		e.led.TouchFull("dsh", sid, filepath.Join(e.tmp, "session.v4.jsonl.zstd"),
			e.t0-idleS, 10, "C:/proj", "", testMinCtx+50, 0)
	}
	saveHandoff := func(e *gateEnv) {
		e.store.SaveHandoff(sid, "dsh", "C:/proj", "dsh 交接",
			isoUTC(e.t0-30), "fresh", "交接正文 md")
	}
	cases := []struct {
		name       string
		mode       string
		setup      func(e *gateEnv)
		wantDec    string
		wantSubstr string // reason（block/放行因）或 additional_context（observe）子串
		wantCtx    bool   // true=wantSubstr 查 additional_context
	}{
		{"台账miss放行", "enforce", func(e *gateEnv) {}, "allow", "no-ledger", false},
		{"mode-off放行（非cc档开关）", "off", func(e *gateEnv) { reg(e, 120) }, "allow", "mode-off", false},
		{"enforce闲置拦截", "enforce", func(e *gateEnv) { reg(e, 120); saveHandoff(e) }, "block", "闲置", false},
		{"enforce新鲜放行", "enforce", func(e *gateEnv) { reg(e, 5) }, "allow", "", false},
		{"observe闲置只警告", "observe", func(e *gateEnv) { reg(e, 120) }, "allow", "observe", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newDshRcvEnv(t, tc.mode)
			tc.setup(e)
			r := e.d.DshGate(map[string]any{"session_id": sid, "cwd": "C:/proj", "prompt": "继续"})
			if got := r["decision"]; got != tc.wantDec {
				t.Fatalf("decision = %v, want %s（全响应 %v）", got, tc.wantDec, r)
			}
			if tc.wantSubstr != "" {
				key := "reason"
				if tc.wantCtx {
					key = "additional_context"
				}
				if s, _ := r[key].(string); !strings.Contains(s, tc.wantSubstr) {
					t.Fatalf("%s = %q, want 含 %q", key, s, tc.wantSubstr)
				}
			}
			if e.d.Stats.ByAgent["dsh"] != 1 {
				t.Fatalf("Stats.ByAgent[dsh] = %d, want 1（判定走既有 Gate 入口计数）",
					e.d.Stats.ByAgent["dsh"])
			}
		})
	}

	// block 案交付物细看：既有分支 5 全套副作用原样（handoff_path、原话保存、
	// suppressOriginalPrompt），reject 理由用户可见。
	e := newDshRcvEnv(t, "enforce")
	reg(e, 120)
	saveHandoff(e)
	r := e.d.DshGate(map[string]any{"session_id": sid, "cwd": "C:/proj", "prompt": "接着干"})
	if hp, _ := r["handoff_path"].(string); hp == "" {
		t.Fatalf("block 缺 handoff_path: %v", r)
	}
	if r["suppressOriginalPrompt"] != true {
		t.Fatalf("block 缺 suppressOriginalPrompt: %v", r)
	}
	if p, _ := e.store.LatestPendingFor("dsh", "C:/proj"); p.Prompt != "接着干" {
		t.Fatalf("原话未按既有语义保存: %q", p.Prompt)
	}

	// observe 案摆渡入队照旧触发（既有分支语义随判定入口一起复用）。
	e2 := newDshRcvEnv(t, "observe")
	reg(e2, 120)
	e2.d.DshGate(map[string]any{"session_id": sid, "cwd": "C:/proj", "prompt": "继续"})
	if got := e2.enqueuedList(); len(got) != 1 || got[0] != "dsh/"+sid {
		t.Fatalf("observe 入队 = %v, want [dsh/%s]", got, sid)
	}
}

// ---- ②事件接收口：Touch 登记＋usage 四列入账（pollDsh 同构） ----

func TestDshEventTouchesAndRecordsUsage(t *testing.T) {
	e := newDshRcvEnv(t, "enforce")
	sid := dshRcvSID

	// turn/start：登记（cwd/title 入台账、事件时间＝活动钟、活活动即观察），
	// 零 usage 行。
	if r := e.d.DshEvent(map[string]any{"session_id": sid, "event": "turn/start",
		"time": e.t0 * 1000, "cwd": "C:/proj", "title": "接线会话"}); r["ok"] != true {
		t.Fatalf("turn/start 应 ok: %v", r)
	}
	st := e.led.Get("dsh", sid)
	if st == nil {
		t.Fatal("turn/start 未 Touch 登记")
	}
	if st.Cwd != "C:/proj" || st.Title != "接线会话" {
		t.Fatalf("cwd/title = %q/%q, want C:/proj/接线会话", st.Cwd, st.Title)
	}
	if st.LastWrite != e.t0 || !st.ObservedActive {
		t.Fatalf("LastWrite=%v Observed=%v, want %v/true（事件时间秒＋活动即观察）",
			st.LastWrite, st.ObservedActive, e.t0)
	}
	if rows := dshUsageRows(t, e.d.Accounts); len(rows) != 0 {
		t.Fatalf("turn/start 不应出 usage 行: %d", len(rows))
	}

	// assistant/message 带 usage：恰一行四列＋peak 回写（计费输入=三输入列之和）。
	e.d.DshEvent(map[string]any{"session_id": sid, "event": "assistant/message",
		"time": e.t0 * 1000, "cwd": "C:/proj", "model": "glm-5.3",
		"usage": map[string]any{"input_tokens": 120, "cache_read_tokens": 30000,
			"cache_creation_tokens": 5000, "output_tokens": 80}})
	rows := dshUsageRows(t, e.d.Accounts)
	if len(rows) != 1 {
		t.Fatalf("want 1 usage row, got %d: %+v", len(rows), rows)
	}
	row := rows[0]
	if row["input_tokens"] != float64(120) || row["cache_read_tokens"] != float64(30000) ||
		row["cache_creation_tokens"] != float64(5000) || row["output_tokens"] != float64(80) {
		t.Fatalf("四列 = %v/%v/%v/%v", row["input_tokens"], row["cache_read_tokens"],
			row["cache_creation_tokens"], row["output_tokens"])
	}
	if row["agent"] != "dsh" || row["session_id"] != sid || row["project"] != "C:/proj" ||
		row["model"] != "glm-5.3" || row["title"] != "接线会话" ||
		row["subagent"] != "" || row["offset"] != float64(0) {
		t.Fatalf("公共科面不符: %+v", row)
	}
	if row["ts"] != e.t0 {
		t.Fatalf("ts = %v, want %v（事件毫秒→秒）", row["ts"], e.t0)
	}
	if st.PeakCtx != 35120 {
		t.Fatalf("peak = %d, want 35120（120+30000+5000，pollDsh 同款回写）", st.PeakCtx)
	}

	// compaction/*：登记推进（闲置钟走事件时间），无新行。
	e.advance(5)
	e.d.DshEvent(map[string]any{"session_id": sid, "event": "compaction/start",
		"time": *e.now * 1000})
	if st.LastWrite != *e.now {
		t.Fatalf("compaction 后 LastWrite = %v, want %v", st.LastWrite, *e.now)
	}
	if rows := dshUsageRows(t, e.d.Accounts); len(rows) != 1 {
		t.Fatalf("compaction 不应出 usage 行: %d", len(rows))
	}
}

// TestDshEventUsageRowKeysMatchPollDsh 逐字段一致验收：事件行与 pollDsh 现行
// 采集行的 keyset 直接 diff——pollDsh 字段形状一变本测试即红（防漂移锚）。
func TestDshEventUsageRowKeysMatchPollDsh(t *testing.T) {
	// pollDsh 现行行：守望采集 fixture 会话（dsh_wiring 同款夹具）。
	root, accDir := t.TempDir(), t.TempDir()
	writeDshSession(t, root, dshMainID, dshMainHeader,
		`{"type":"assistant/message","seq":3,"time":1790905227524,"data":{"message":{"source":{"kind":"model","provider":"x","model":"glm-5.3"}},"usage":{"inputTokens":10,"outputTokens":5}}}`+"\n")
	w, acc, _, _ := newDshWatcher(t, root, accDir)
	w.pollDsh()
	pollRows := dshUsageRows(t, acc)
	if len(pollRows) != 1 {
		t.Fatalf("poll fixture want 1 行, got %d", len(pollRows))
	}

	// 事件口行。
	e := newDshRcvEnv(t, "enforce")
	e.d.DshEvent(map[string]any{"session_id": dshMainID, "event": "assistant/message",
		"usage": map[string]any{"input_tokens": 1, "output_tokens": 2}})
	eventRows := dshUsageRows(t, e.d.Accounts)
	if len(eventRows) != 1 {
		t.Fatalf("事件口 want 1 行, got %d", len(eventRows))
	}

	pollKeys, eventKeys := rowKeySet(pollRows[0]), rowKeySet(eventRows[0])
	var onlyEvent, onlyPoll []string
	for k := range eventKeys {
		if !pollKeys[k] {
			onlyEvent = append(onlyEvent, k)
		}
	}
	for k := range pollKeys {
		if !eventKeys[k] {
			onlyPoll = append(onlyPoll, k)
		}
	}
	sort.Strings(onlyEvent)
	sort.Strings(onlyPoll)
	if len(onlyEvent) > 0 || len(onlyPoll) > 0 {
		t.Fatalf("事件行与 pollDsh 现行行字段不一致：仅事件行有 %v、仅 pollDsh 行有 %v",
			onlyEvent, onlyPoll)
	}
	// 形状锚：四列＋科面 12 字段一个不能少（防两边同时丢字段让 diff 空转）。
	for _, k := range [...]string{"agent", "session_id", "lineage_id", "project",
		"model", "title", "input_tokens", "cache_read_tokens",
		"cache_creation_tokens", "output_tokens", "offset", "subagent"} {
		if !eventKeys[k] {
			t.Errorf("事件行缺字段 %s", k)
		}
	}
}

func TestDshEventSubagentUnderParent(t *testing.T) {
	// 子会话直报（parent_session_id 非空）：随父入账（session_id=父键、
	// subagent=子键）、不 Touch——守望 pollDsh 同款分流。
	e := newDshRcvEnv(t, "enforce")
	e.d.DshEvent(map[string]any{"session_id": dshChildID, "parent_session_id": dshMainID,
		"event": "assistant/message", "cwd": "C:/proj",
		"usage": map[string]any{"input_tokens": 500, "cache_read_tokens": 8000,
			"output_tokens": 50}})
	if e.led.Get("dsh", dshChildID) != nil || e.led.Get("dsh", dshMainID) != nil {
		t.Error("子会话直报不应 Touch（父子都不登记）")
	}
	rows := dshUsageRows(t, e.d.Accounts)
	if len(rows) != 1 {
		t.Fatalf("want 1 行, got %d", len(rows))
	}
	row := rows[0]
	if row["session_id"] != dshMainID || row["subagent"] != dshChildID {
		t.Fatalf("随父入账不符: session_id=%v subagent=%v", row["session_id"], row["subagent"])
	}
	if row["input_tokens"] != float64(500) || row["cache_read_tokens"] != float64(8000) ||
		row["cache_creation_tokens"] != float64(0) || row["output_tokens"] != float64(50) {
		t.Fatalf("四列 = %v/%v/%v/%v", row["input_tokens"], row["cache_read_tokens"],
			row["cache_creation_tokens"], row["output_tokens"])
	}
}

func TestDshEventDefensive(t *testing.T) {
	const sid = dshRcvSID
	cases := []struct {
		name    string
		body    map[string]any
		wantReg bool // 应有台账登记
		wantRow bool // 应有 usage 行
	}{
		{"空键静默跳过", map[string]any{"event": "turn/start"}, false, false},
		{"未知事件类型", map[string]any{"session_id": sid, "event": "agent/status"}, false, false},
		{"assistant/message无usage只登记", map[string]any{"session_id": sid, "event": "assistant/message"}, true, false},
		{"usage非对象收窄", map[string]any{"session_id": sid, "event": "assistant/message", "usage": "oops"}, true, false},
		{"空body全缺", map[string]any{}, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newDshRcvEnv(t, "enforce")
			r := e.d.DshEvent(tc.body)
			if r["ok"] != true { // 静默收窄：ok 不炸不 5xx
				t.Fatalf("应 ok: %v", r)
			}
			if got := len(e.led.AllSessions()); got != boolToInt(tc.wantReg) {
				t.Fatalf("登记数 = %d, want %d", got, boolToInt(tc.wantReg))
			}
			if got := len(dshUsageRows(t, e.d.Accounts)); got != boolToInt(tc.wantRow) {
				t.Fatalf("usage 行 = %d, want %d", got, boolToInt(tc.wantRow))
			}
		})
	}

	// 无账本（旧测试/隐私关形态）：登记照常、记账跳过、不炸。
	e := newDshRcvEnv(t, "enforce")
	e.d.Accounts = nil
	if r := e.d.DshEvent(map[string]any{"session_id": sid, "event": "assistant/message",
		"usage": map[string]any{"input_tokens": 1}}); r["ok"] != true {
		t.Fatalf("无账本应 ok: %v", r)
	}
	if e.led.Get("dsh", sid) == nil {
		t.Error("无账本也应登记")
	}
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func rowKeySet(row map[string]any) map[string]bool {
	out := map[string]bool{}
	for k := range row {
		out[k] = true
	}
	return out
}

// ---- ③交接查询口：Restore 整体复用（agent 钉 dsh） ----

func TestDshHandoffRoutesThroughRestore(t *testing.T) {
	cases := []struct {
		name       string
		setup      func(e *gateEnv)
		wantSubstr string
		notSubstr  string
		wantNil    bool
	}{
		{"无候选", func(e *gateEnv) {}, "", "", true},
		{"单候选注入且agent钉dsh", func(e *gateEnv) {
			e.store.SaveHandoff("s-dsh", "dsh", "C:/proj", "dsh 线", isoUTC(e.t0-100), "fresh", "dsh 交接正文 ABC")
			e.store.SaveHandoff("s-cc", "cc", "C:/proj", "cc 线", isoUTC(e.t0-50), "fresh", "cc 交接正文 XYZ")
		}, "dsh 交接正文 ABC", "cc 交接正文 XYZ", false},
		{"多候选列清单", func(e *gateEnv) {
			e.store.SaveHandoff("s1", "dsh", "C:/proj", "h1", isoUTC(e.t0-100), "fresh", "md1")
			e.store.SaveHandoff("s2", "dsh", "C:/proj", "h2", isoUTC(e.t0-200), "fresh", "md2")
		}, "本项目有 2 份可用交接", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newDshRcvEnv(t, "enforce")
			tc.setup(e)
			r := e.d.DshHandoff("C:/proj", "newsid")
			ctx, _ := r["context"]
			if tc.wantNil {
				if ctx != nil {
					t.Fatalf("context = %v, want nil", ctx)
				}
				return
			}
			s, _ := ctx.(string)
			if !strings.Contains(s, tc.wantSubstr) {
				t.Fatalf("context 缺 %q: %q", tc.wantSubstr, s)
			}
			if tc.notSubstr != "" && strings.Contains(s, tc.notSubstr) {
				t.Fatalf("context 串线（cc 交接混入）: %q", s)
			}
		})
	}

	// 空 cwd 收窄：RestoreCandidates 对空 cwd 返回空 → context nil（不炸）。
	e := newDshRcvEnv(t, "enforce")
	if r := e.d.DshHandoff("", "newsid"); r["context"] != nil {
		t.Fatalf("空 cwd context = %v, want nil", r["context"])
	}
}

// ---- HTTP 层：守门序与分派（/shutdown、/provider_switch 同族） ----

// dshPost 构造 loopback Bearer POST（守门序测试与三口 happy path 共用）。
func dshPost(h http.Handler, token, uri, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, uri, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.RemoteAddr = "127.0.0.1:5555"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestDshReceiveEndpointGuards(t *testing.T) {
	e := newDshRcvEnv(t, "enforce")
	h := makeHandler(e.d, "tok-dsh", nil, nil)

	nonLoopback := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/dsh/event",
			strings.NewReader(`{"session_id":"s","event":"turn/start"}`))
		req.Header.Set("Authorization", "Bearer tok-dsh")
		req.RemoteAddr = "203.0.113.7:443"
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	getReq := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/dsh/gate", nil)
		req.Header.Set("Authorization", "Bearer tok-dsh")
		req.RemoteAddr = "127.0.0.1:5555"
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	cases := []struct {
		name string
		rec  *httptest.ResponseRecorder
		want int
	}{
		{"非loopback拒", nonLoopback(), http.StatusForbidden},
		{"GET方法拒", getReq(), http.StatusMethodNotAllowed},
		{"错token拒", dshPost(h, "WRONG", "/dsh/gate", `{}`), http.StatusUnauthorized},
		{"坏JSON四百不5xx", dshPost(h, "tok-dsh", "/dsh/event", `not-json`), http.StatusBadRequest},
		{"未知路径落既有404", dshPost(h, "tok-dsh", "/dsh/nope", `{}`), http.StatusNotFound},
		{"带query视为未知路径", dshPost(h, "tok-dsh", "/dsh/gate?x=1", `{}`), http.StatusNotFound},
	}
	for _, tc := range cases {
		if tc.rec.Code != tc.want {
			t.Errorf("%s: code = %d %q, want %d", tc.name, tc.rec.Code, tc.rec.Body.String(), tc.want)
		}
	}

	// 三口 happy path 过 HTTP：200＋业务回显＋台账/账面副作用。
	rec := dshPost(h, "tok-dsh", "/dsh/event",
		`{"session_id":"`+dshRcvSID+`","event":"turn/start","cwd":"C:/proj"}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"ok":true`) {
		t.Fatalf("/dsh/event = %d %q", rec.Code, rec.Body.String())
	}
	if e.led.Get("dsh", dshRcvSID) == nil {
		t.Error("/dsh/event 未落台账")
	}
	rec = dshPost(h, "tok-dsh", "/dsh/gate", `{"session_id":"nobody"}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"decision":"allow"`) {
		t.Fatalf("/dsh/gate = %d %q", rec.Code, rec.Body.String())
	}
	rec = dshPost(h, "tok-dsh", "/dsh/handoff", `{"cwd":"C:/proj","session_id":"newsid"}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"context":null`) {
		t.Fatalf("/dsh/handoff = %d %q", rec.Code, rec.Body.String())
	}

	// DaemonLike 替身不拦截：三口落既有 doPost 未知路径 404（auth 前判路径）。
	hd := makeHandler(&stopDaemon{}, "tok-dsh", nil, nil)
	if rec := dshPost(hd, "tok-dsh", "/dsh/event", `{}`); rec.Code != http.StatusNotFound {
		t.Fatalf("替身 = %d %q, want 404", rec.Code, rec.Body.String())
	}
}
