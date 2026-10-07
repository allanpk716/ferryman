package daemon

// compact_test.go — dsh-hot-compaction 票02：指令槽与 poll/compacted 两口的
// 表驱动钉子（gate_test.go 先例）。契约＝spec「架构与契约」逐字：
//
//   - poll：{agent:"dsh", sessions:[{sid,idle_s}]} → {commands:[{action,
//     session_id,cwd}], poll_hint_s}；应答即清槽（先到先得，多宿主同 sid
//     天然去重）；过期指令丢弃不派发（N1）；同槽新指令覆盖旧指令；
//     enabled 关＝空应答；只应答请求清单内会话。
//   - compacted：恒落 kind=compacted 账本行（字段齐全）；ok=true 且带
//     prefix_tokens → compressed 标记（带 expires）＋prefix 覆盖；ok=false
//     只落行不设标记；标记有效期（不再续期）与流量作废（红利只领一次）
//     语义可测。
//   - 守门序与 /dsh/gate 同族：loopback → POST → Bearer。

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"ferryman/internal/accounts"
	"ferryman/internal/config"
	"ferryman/internal/ledger"
	"ferryman/internal/store"
)

const (
	compactSID  = "session-44444444-4444-4444-8444-444444444444"
	compactSID2 = "session-44555555-5555-4555-8555-555555555555"
)

// compactEnv 票02 环境：真 Accounts（kind=compacted 行断言）＋可配 TTL
//（heartbeat.ttl_s 驱动指令/标记两有效期）。clock 冻结在 t0（newDshRcvEnv
// 同款测试卫生：DataDir 钉沙箱）。
type compactEnv struct {
	gateEnv
	acc *accounts.Accounts
}

func newCompactEnv(t *testing.T, ttls float64) *compactEnv {
	t.Helper()
	e := &compactEnv{gateEnv: gateEnv{tmp: t.TempDir(), t0: 1_800_000_000.0}}
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
	e.store = st
	e.led = ledger.New()
	cfg := config.Default()
	cfg.Server.DataDir = filepath.Join(e.tmp, "data")
	cfg.Heartbeat.TTLS = ttls
	e.d = NewDaemon(cfg, e.led, st, func(*ledger.SessionState) bool { return true },
		acc, 0, nil)
	return e
}

// compactRows kind=compacted 科目流水中该会话的行。
func (e *compactEnv) compactRows(sid string) []map[string]any {
	out := []map[string]any{}
	for _, r := range e.acc.Read(accounts.ReadOpts{Kind: "compacted"}) {
		if r["session_id"] == sid {
			out = append(out, r)
		}
	}
	return out
}

// regCompact 登记一个 dsh 会话（last_write=t0-idleS、peak=peakCtx）。
func (e *compactEnv) regCompact(sid string, idleS float64, peakCtx int) {
	e.led.TouchFull("dsh", sid, filepath.Join(e.tmp, sid+".jsonl.zstd"),
		e.t0-idleS, 10, "C:/proj", "", peakCtx, 0)
}

// compactMark 锁内抄压缩标记（nil=无标记）。
func (e *compactEnv) compactMark(sid string) *ledger.DshCompressMark {
	e.d.Ledger.Mu().Lock()
	defer e.d.Ledger.Mu().Unlock()
	st := e.d.Ledger.GetLocked("dsh", sid)
	if st == nil {
		return nil
	}
	return st.DshCompressed
}

// compactPeak 锁内抄 PeakCtx 现值。
func (e *compactEnv) compactPeak(sid string) int {
	e.d.Ledger.Mu().Lock()
	defer e.d.Ledger.Mu().Unlock()
	st := e.d.Ledger.GetLocked("dsh", sid)
	if st == nil {
		return -1
	}
	return st.PeakCtx
}

// pollBody spec 契约请求形：{agent:"dsh", sessions:[{sid, idle_s}]}。
func pollBody(sids ...string) map[string]any {
	sessions := make([]any, 0, len(sids))
	for i, sid := range sids {
		sessions = append(sessions, map[string]any{"sid": sid, "idle_s": float64(i * 100)})
	}
	return map[string]any{"agent": "dsh", "sessions": sessions}
}

// ---- 指令槽：入槽/覆盖/清槽/过期丢弃（N1） ----

func TestDshCompactPollClearsSlotOnce(t *testing.T) {
	e := newCompactEnv(t, 100) // TTL=100 → 指令有效期 0.2×100=20s
	if !e.d.EnqueueDshCompact(compactSID, "C:/proj") {
		t.Fatal("入槽应成功")
	}
	r := e.d.DshPoll(pollBody(compactSID))
	cmds, _ := r["commands"].([]map[string]any)
	if len(cmds) != 1 {
		t.Fatalf("commands = %v, want 恰 1 条", r["commands"])
	}
	c := cmds[0]
	if c["action"] != "compact" || c["session_id"] != compactSID || c["cwd"] != "C:/proj" {
		t.Fatalf("指令字段 = %v（契约 action/session_id/cwd）", c)
	}
	if r["poll_hint_s"] != 30.0 {
		t.Fatalf("poll_hint_s = %v, want 30", r["poll_hint_s"])
	}
	// 应答即清槽：同指令不二次派发（先到先得）
	if r2 := e.d.DshPoll(pollBody(compactSID)); len(r2["commands"].([]map[string]any)) != 0 {
		t.Fatalf("二次 poll 应空, got %v", r2["commands"])
	}
}

func TestDshCompactSlotOverwrite(t *testing.T) {
	e := newCompactEnv(t, 100)
	e.d.EnqueueDshCompact(compactSID, "C:/old")
	e.d.EnqueueDshCompact(compactSID, "C:/new") // 同槽覆盖旧指令
	cmds := e.d.DshPoll(pollBody(compactSID))["commands"].([]map[string]any)
	if len(cmds) != 1 || cmds[0]["cwd"] != "C:/new" {
		t.Fatalf("同槽应覆盖旧指令, got %v", cmds)
	}
}

func TestDshCompactPollDropsExpired(t *testing.T) {
	e := newCompactEnv(t, 100)
	e.d.EnqueueDshCompact(compactSID, "C:/proj")
	e.advance(21) // 越过 0.2×100=20s 死线
	// 过期指令丢弃不派发（N1），且已清槽——再 poll 也不复活
	for i := 0; i < 2; i++ {
		if got := e.d.DshPoll(pollBody(compactSID)); len(got["commands"].([]map[string]any)) != 0 {
			t.Fatalf("第 %d 次 poll 应丢弃过期指令, got %v", i+1, got["commands"])
		}
	}
}

func TestDshCompactPollOnlyListedSessions(t *testing.T) {
	e := newCompactEnv(t, 100)
	e.d.EnqueueDshCompact(compactSID, "C:/a")
	e.d.EnqueueDshCompact(compactSID2, "C:/b")
	// 只报 s1：只应答 s1（插件只执行其宿持有的会话）
	cmds := e.d.DshPoll(pollBody(compactSID))["commands"].([]map[string]any)
	if len(cmds) != 1 || cmds[0]["session_id"] != compactSID {
		t.Fatalf("只应答请求清单内会话, got %v", cmds)
	}
	// s2 指令未被别宿抢走：其宿来 poll 仍得
	cmds = e.d.DshPoll(pollBody(compactSID2))["commands"].([]map[string]any)
	if len(cmds) != 1 || cmds[0]["session_id"] != compactSID2 {
		t.Fatalf("未被 poll 的会话指令应保留, got %v", cmds)
	}
}

func TestDshCompactDisabledNoEnqueueNoDispatch(t *testing.T) {
	e := newCompactEnv(t, 100)
	e.d.Cfg.DshCompact.Enabled = false
	if e.d.EnqueueDshCompact(compactSID, "C:/proj") {
		t.Fatal("enabled=false 入槽应拒")
	}
	r := e.d.DshPoll(pollBody(compactSID))
	if len(r["commands"].([]map[string]any)) != 0 {
		t.Fatalf("关闭态应答应空, got %v", r["commands"])
	}
	if r["poll_hint_s"] != 30.0 {
		t.Fatalf("关闭态 poll_hint_s 照常回显, got %v", r["poll_hint_s"])
	}
}

func TestDshCompactEnqueueNeedsTTL(t *testing.T) {
	e := newCompactEnv(t, 0) // ttl_s 未配置 → 指令有效期不可算
	if e.d.EnqueueDshCompact(compactSID, "C:/proj") {
		t.Fatal("TTL 不可得应拒入槽（无 N1 语义保守不派发）")
	}
}

// ---- compacted：账本行＋compressed 标记＋prefix 覆盖 ----

func TestDshCompactedOkSetsFlagAndPrefix(t *testing.T) {
	e := newCompactEnv(t, 100) // 标记有效期 2.0×100=200s
	e.regCompact(compactSID, 100, 50000)
	r := e.d.DshCompacted(map[string]any{"session_id": compactSID, "ok": true,
		"reason": "compacted", "prefix_tokens": 1234, "source": "plugin"})
	if r["ok"] != true {
		t.Fatalf("响应 = %v, want ok:true", r)
	}
	rows := e.compactRows(compactSID)
	if len(rows) != 1 {
		t.Fatalf("kind=compacted 行 = %d, want 1", len(rows))
	}
	row := rows[0]
	if row["agent"] != "dsh" || row["ok"] != true || row["prefix_tokens"] != 1234.0 ||
		row["reason"] != "compacted" || row["source"] != "plugin" {
		t.Fatalf("行字段不齐（spec：含全部字段）: %v", row)
	}
	mark := e.compactMark(compactSID)
	if mark == nil {
		t.Fatal("ok=true+prefix 应置 compressed 标记")
	}
	if d := mark.TS - e.t0; d < -1 || d > 1 {
		t.Fatalf("标记 TS = %v, want ≈t0", mark.TS)
	}
	if d := mark.Expires - (e.t0 + 200); d < -1 || d > 1 {
		t.Fatalf("标记 Expires = %v, want t0+2.0×TTL(200)", mark.Expires)
	}
	if got := e.compactPeak(compactSID); got != 1234 {
		t.Fatalf("prefix 覆盖后 PeakCtx = %d, want 1234", got)
	}
	if prefix, ok := e.d.DshCompressedActive(compactSID); !ok || prefix != 1234 {
		t.Fatalf("DshCompressedActive = (%d,%v), want (1234,true)", prefix, ok)
	}
}

func TestDshCompactedTableDriven(t *testing.T) {
	cases := []struct {
		name     string
		body     map[string]any
		wantRow  bool
		rowOK    bool
		wantMark bool
	}{
		{"ok=false只落行不设标记", map[string]any{"session_id": compactSID, "ok": false, "reason": "busy"}, true, false, false},
		{"ok=true无prefix只落行不设标记", map[string]any{"session_id": compactSID, "ok": true}, true, true, false},
		{"prefix=0不设标记", map[string]any{"session_id": compactSID, "ok": true, "prefix_tokens": 0}, true, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newCompactEnv(t, 100)
			e.regCompact(compactSID, 100, 50000)
			if r := e.d.DshCompacted(tc.body); r["ok"] != true {
				t.Fatalf("响应 = %v, want ok:true", r)
			}
			if tc.wantRow {
				rows := e.compactRows(compactSID)
				if len(rows) != 1 {
					t.Fatalf("账本行 = %d, want 1", len(rows))
				}
				if rows[0]["ok"] != tc.rowOK {
					t.Fatalf("行 ok = %v, want %v", rows[0]["ok"], tc.rowOK)
				}
			}
			if got := e.compactMark(compactSID) != nil; got != tc.wantMark {
				t.Fatalf("标记在=%v, want %v", got, tc.wantMark)
			}
			if _, ok := e.d.DshCompressedActive(compactSID); ok != tc.wantMark {
				t.Fatalf("Active=%v, want %v", ok, tc.wantMark)
			}
		})
	}
}

func TestDshCompactedBadShapesSkipped(t *testing.T) {
	cases := []struct {
		name string
		body map[string]any
		want string
	}{
		{"空sid", map[string]any{"session_id": "", "ok": true}, "empty-session-id"},
		{"缺ok", map[string]any{"session_id": compactSID}, "bad-ok"},
		{"ok非布尔", map[string]any{"session_id": compactSID, "ok": "yes"}, "bad-ok"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newCompactEnv(t, 100)
			r := e.d.DshCompacted(tc.body)
			if r["skipped"] != tc.want {
				t.Fatalf("skipped = %v, want %q", r["skipped"], tc.want)
			}
			if len(e.compactRows(compactSID)) != 0 {
				t.Fatal("坏形不应落账本行")
			}
		})
	}
}

// ---- compressed 标记语义：有效期（不再续期）与流量作废（红利只领一次） ----

func TestDshCompressedFlagExpiry(t *testing.T) {
	e := newCompactEnv(t, 100)
	e.regCompact(compactSID, 100, 50000)
	e.d.DshCompacted(map[string]any{"session_id": compactSID, "ok": true, "prefix_tokens": 1234})
	e.advance(199)
	if _, ok := e.d.DshCompressedActive(compactSID); !ok {
		t.Fatal("死线内应有效")
	}
	e.advance(2) // 越过 200s 死线
	if _, ok := e.d.DshCompressedActive(compactSID); ok {
		t.Fatal("过死线应无效（不再续期）")
	}
}

func TestDshCompressedFlagVoidedByTraffic(t *testing.T) {
	e := newCompactEnv(t, 100)
	path := filepath.Join(e.tmp, compactSID+".jsonl.zstd")
	e.led.TouchFull("dsh", compactSID, path, e.t0-100, 10, "C:/proj", "", 50000, 0)
	e.d.DshCompacted(map[string]any{"session_id": compactSID, "ok": true, "prefix_tokens": 1234})
	// 标记后无流量：同刻 LastWrite（== 标记 TS，压缩自身落盘写不越过）仍有效
	if _, ok := e.d.DshCompressedActive(compactSID); !ok {
		t.Fatal("无流量应有效")
	}
	// 新流量（机器产出 Touch 推进 LastWrite 越过标记时刻）→ 立即作废（红利只领一次）
	e.led.TouchFull("dsh", compactSID, path, e.t0+10, 10, "C:/proj", "", 50001, 0)
	if _, ok := e.d.DshCompressedActive(compactSID); ok {
		t.Fatal("标记后有流量应作废")
	}
}

func TestDshCompressedActiveNoState(t *testing.T) {
	e := newCompactEnv(t, 100)
	if _, ok := e.d.DshCompressedActive("nobody"); ok {
		t.Fatal("无台账/无标记应无效")
	}
}

func TestDshCompressedFlagZeroTTLInvalid(t *testing.T) {
	e := newCompactEnv(t, 0) // ttl_s 未配置：expires 钉死在置位时刻 → 恒无效
	e.regCompact(compactSID, 10, 50000)
	e.d.DshCompacted(map[string]any{"session_id": compactSID, "ok": true, "prefix_tokens": 1234})
	if _, ok := e.d.DshCompressedActive(compactSID); ok {
		t.Fatal("TTL 不可得标记应恒无效（保守面）")
	}
	if got := e.compactPeak(compactSID); got != 1234 {
		t.Fatalf("PeakCtx = %d, want 1234（prefix 覆盖照常落）", got)
	}
}

// ---- HTTP 层：守门序与 /dsh/gate 同族（同 token 同 loopback 同方法面） ----

func TestDshCompactEndpointGuards(t *testing.T) {
	e := newCompactEnv(t, 100)
	h := makeHandler(e.d, "tok-c", nil, nil)
	nonLoopback := func(uri string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, uri, strings.NewReader(`{}`))
		req.Header.Set("Authorization", "Bearer tok-c")
		req.RemoteAddr = "203.0.113.7:443"
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	getReq := func(uri string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, uri, nil)
		req.Header.Set("Authorization", "Bearer tok-c")
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
		{"poll非loopback拒", nonLoopback("/dsh/poll"), http.StatusForbidden},
		{"compacted非loopback拒", nonLoopback("/dsh/compacted"), http.StatusForbidden},
		{"poll GET拒", getReq("/dsh/poll"), http.StatusMethodNotAllowed},
		{"compacted GET拒", getReq("/dsh/compacted"), http.StatusMethodNotAllowed},
		{"poll错token拒", dshPost(h, "WRONG", "/dsh/poll", `{}`), http.StatusUnauthorized},
		{"compacted错token拒", dshPost(h, "WRONG", "/dsh/compacted", `{}`), http.StatusUnauthorized},
		{"poll坏JSON四百", dshPost(h, "tok-c", "/dsh/poll", `not-json`), http.StatusBadRequest},
		{"compacted坏JSON四百", dshPost(h, "tok-c", "/dsh/compacted", `not-json`), http.StatusBadRequest},
		{"poll带query视为未知路径", dshPost(h, "tok-c", "/dsh/poll?x=1", `{}`), http.StatusNotFound},
		{"compacted带query视为未知路径", dshPost(h, "tok-c", "/dsh/compacted?x=1", `{}`), http.StatusNotFound},
	}
	for _, tc := range cases {
		if tc.rec.Code != tc.want {
			t.Errorf("%s: code = %d %q, want %d", tc.name, tc.rec.Code, tc.rec.Body.String(), tc.want)
		}
	}
	// happy path 过 HTTP：poll 空应答带 poll_hint_s；compacted 落行。
	rec := dshPost(h, "tok-c", "/dsh/poll", `{"agent":"dsh","sessions":[]}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"commands":[]`) ||
		!strings.Contains(rec.Body.String(), `"poll_hint_s":30`) {
		t.Fatalf("/dsh/poll = %d %q", rec.Code, rec.Body.String())
	}
	e.regCompact(compactSID, 10, 50000)
	rec = dshPost(h, "tok-c", "/dsh/compacted",
		`{"session_id":"`+compactSID+`","ok":true,"prefix_tokens":1234,"source":"plugin"}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"ok":true`) {
		t.Fatalf("/dsh/compacted = %d %q", rec.Code, rec.Body.String())
	}
	if len(e.compactRows(compactSID)) != 1 {
		t.Fatal("HTTP compacted 应落账本行")
	}
}
