package daemon

// compact_test.go — dsh-hot-compaction 票02：指令槽与 poll/compacted 两口的
// 表驱动钉子（gate_test.go 先例）。契约＝spec「架构与契约」逐字：
//
//   - poll：{agent:"dsh", sessions:[{sid,idle_s}]} → {commands:[{action,
//     session_id,cwd}], poll_hint_s}；应答即清槽（先到先得，多宿主同 sid
//     天然去重）；过期指令丢弃不派发（N1）；槽内未过期指令不被覆盖（票01
//     重触发节流）；enabled 关＝空应答；只应答请求清单内会话。
//   - compacted：恒落 kind=compacted 账本行（字段齐全）；ok=true 且带
//     prefix_tokens → compressed 标记（带 expires）＋prefix 覆盖；ok=false
//     只落行不设标记；标记有效期（不再续期）与流量作废（红利只领一次）
//     语义可测。
//   - 守门序与 /dsh/gate 同族：loopback → POST → Bearer。
//   - 票01 领取后执行窗：领取起 dshCompactExecWindowS 内无 ok 上报 → 守望
//     sweep 计一轮未送达（与"过期无人领取"同计数同告警路径）；在飞窗内
//     不重入槽；ok=true 收口、ok=false 不收口。

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
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
	resetCompactMiss()  // 守护可见性票01 包级态测试卫生：防跨用例渗漏（freezeClock 还原同纪律）
	resetCompactClaims() // 票01 在飞领取窗包级态同款卫生
	resetDshLiveReports() // dsh-host-guard 票01：live 聚合记忆同款卫生
	resetCompactBackoff() // dsh-host-guard 票01：no-agent 退避表同款卫生
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

// pollBodyLive 带 live 值的 poll 体（dsh-host-guard 票01 spec A 三值面）：
// live=nil＝缺键（旧协议体＝未知）；&true/&false＝显式上报。
func pollBodyLive(sid string, live *bool) map[string]any {
	m := map[string]any{"sid": sid, "idle_s": 100.0}
	if live != nil {
		m["live"] = *live
	}
	return map[string]any{"agent": "dsh", "poller": "test-host", "sessions": []any{m}}
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

// slotPeek 锁内抄指令槽条目（nil=无；拷贝防逃逸共享引用）——不领取的检查面
//（poll 会起在飞执行窗，混入覆盖语义用例会串场）。
func (e *compactEnv) slotPeek(sid string) *dshCompactCmd {
	e.d.compactMu.Lock()
	defer e.d.compactMu.Unlock()
	cmd := e.d.dshCompactSlot[sid]
	if cmd == nil {
		return nil
	}
	cp := *cmd
	return &cp
}

// TestDshCompactSlotFreshNotOverwritten 票01 收窄覆盖语义：槽内未过期指令不被
// 同会话新指令覆盖（30s 重发环治点的一半——触发间隔 ≥ 指令有效期）；过期槽
// 照旧允许新指令覆盖（既有覆盖语义收窄到过期槽）。
func TestDshCompactSlotFreshNotOverwritten(t *testing.T) {
	e := newCompactEnv(t, 100)
	if !e.d.EnqueueDshCompact(compactSID, "C:/old") {
		t.Fatal("入槽应成功")
	}
	if e.d.EnqueueDshCompact(compactSID, "C:/new") {
		t.Fatal("有效期内同槽新指令应拒（票01 重触发节流）")
	}
	if cmd := e.slotPeek(compactSID); cmd == nil || cmd.Cwd != "C:/old" {
		t.Fatalf("槽内未过期指令应原样保留, got %+v", cmd)
	}
	// 过期后新指令照常覆盖入槽（不领取场景——过期指令留槽等覆盖）
	e.advance(21) // 越过 0.2×100=20s 死线
	if !e.d.EnqueueDshCompact(compactSID, "C:/new") {
		t.Fatal("过期槽应允许新指令覆盖入槽")
	}
	cmds := e.d.DshPoll(pollBody(compactSID))["commands"].([]map[string]any)
	if len(cmds) != 1 || cmds[0]["cwd"] != "C:/new" {
		t.Fatalf("过期旧槽应被新指令覆盖, got %v", cmds)
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

// TestDshCompactPassLineTable 放行线纯函数表（v0.9.3 票1 解耦）：max(地板,
// ratio×压前峰值)。min_peak_tokens 不在公式内——解耦实锚（首单事故 60562 压到
// 20562 撞 min_peak 20000 线照样拦）。
func TestDshCompactPassLineTable(t *testing.T) {
	e := newCompactEnv(t, 100)
	cases := []struct {
		name    string
		prePeak int
		floor   int
		ratio   float64
		want    int
	}{
		{"缺省:压前不可得→地板", 0, 12000, 0.5, 12000},
		{"缺省:肥会话50K→比例腿25K", 50000, 12000, 0.5, 25000},
		{"缺省:首单实锚60562→30281", 60562, 12000, 0.5, 30281},
		{"缺省:瘦会话10K→比例腿5K不敌地板", 10000, 12000, 0.5, 12000},
		{"可配:地板抬高独走", 50000, 40000, 0.1, 40000},
		{"可配:比例1.0=压多少放多少", 50000, 0, 1.0, 50000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e.d.Cfg.DshCompact.PassFloorTokens = tc.floor
			e.d.Cfg.DshCompact.PassRatio = tc.ratio
			if got := e.d.dshCompactPassLine(tc.prePeak); got != tc.want {
				t.Fatalf("passLine(%d) = %d, want %d", tc.prePeak, got, tc.want)
			}
		})
	}
}

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
	if prefix, _, ok := e.d.DshCompressedActive(compactSID); !ok || prefix != 1234 {
		t.Fatalf("DshCompressedActive = (%d,%v), want (1234,true)", prefix, ok)
	}
	if got := e.compactMark(compactSID).PrePeak; got != 50000 {
		t.Fatalf("标记 PrePeak = %d, want 50000（压前峰值入标记——v0.9.3 放行线比例腿基准）", got)
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
			if _, _, ok := e.d.DshCompressedActive(compactSID); ok != tc.wantMark {
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
	if _, _, ok := e.d.DshCompressedActive(compactSID); !ok {
		t.Fatal("死线内应有效")
	}
	e.advance(2) // 越过 200s 死线
	if _, _, ok := e.d.DshCompressedActive(compactSID); ok {
		t.Fatal("过死线应无效（不再续期）")
	}
}

func TestDshCompressedFlagVoidedByTraffic(t *testing.T) {
	e := newCompactEnv(t, 100)
	path := filepath.Join(e.tmp, compactSID+".jsonl.zstd")
	e.led.TouchFull("dsh", compactSID, path, e.t0-100, 10, "C:/proj", "", 50000, 0)
	e.d.DshCompacted(map[string]any{"session_id": compactSID, "ok": true, "prefix_tokens": 1234})
	// 标记后无流量：同刻 LastWrite（== 标记 TS，压缩自身落盘写不越过）仍有效
	if _, _, ok := e.d.DshCompressedActive(compactSID); !ok {
		t.Fatal("无流量应有效")
	}
	// 新流量（机器产出 Touch 推进 LastWrite 越过标记时刻）→ 立即作废（红利只领一次）
	e.led.TouchFull("dsh", compactSID, path, e.t0+10, 10, "C:/proj", "", 50001, 0)
	if _, _, ok := e.d.DshCompressedActive(compactSID); ok {
		t.Fatal("标记后有流量应作废")
	}
}

func TestDshCompressedActiveNoState(t *testing.T) {
	e := newCompactEnv(t, 100)
	if _, _, ok := e.d.DshCompressedActive("nobody"); ok {
		t.Fatal("无台账/无标记应无效")
	}
}

func TestDshCompressedFlagZeroTTLInvalid(t *testing.T) {
	e := newCompactEnv(t, 0) // ttl_s 未配置：expires 钉死在置位时刻 → 恒无效
	e.regCompact(compactSID, 10, 50000)
	e.d.DshCompacted(map[string]any{"session_id": compactSID, "ok": true, "prefix_tokens": 1234})
	if _, _, ok := e.d.DshCompressedActive(compactSID); ok {
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

// ---- 守护可见性票01：过期无人领取逐轮日志＋一次性告警＋重置钩子 ----

// captureCompactLog 捕获 compactLogf 缝输出（用毕 t.Cleanup 还原；freezeClock
// 同纪律）。
func captureCompactLog(t *testing.T) *strings.Builder {
	t.Helper()
	var sb strings.Builder
	orig := compactLogf
	compactLogf = func(format string, args ...any) {
		fmt.Fprintf(&sb, format, args...)
	}
	t.Cleanup(func() { compactLogf = orig })
	return &sb
}

// resetCompactMiss 清空包级连续无人领取计数（newCompactEnv 开头调用，包级态
// 跨用例渗漏防护）。dsh-host-guard 票01：同锁下另清退避轮计数（claim-miss）。
func resetCompactMiss() {
	dshCompactMissMu.Lock()
	dshCompactMissN = map[string]int{}
	dshCompactClaimMissN = map[string]int{}
	dshCompactMissMu.Unlock()
}

// dropOnce 真实循环构造一轮"过期无人领取"（票01 修正版：入槽不清零——入槽、
// 越过指令死线 0.2×100=20s、poll 丢弃，即触发器自然形态）。N1 不派发，此处
// 不断言——派发面已有 TestDshCompactPollDropsExpired 钉住。env 固定 ttls=100
// enabled 缺省开，入槽必成（返回值不查：本组用例只关心过期丢弃）。
func (e *compactEnv) dropOnce(sid string) {
	e.d.EnqueueDshCompact(sid, "C:/proj")
	e.advance(21)
	e.d.DshPoll(pollBody(sid))
}

// claimOnce 入槽一条新鲜指令并立即 poll 领取（票01 修正版清零钩子①：应答
// commands 非空＝有人来领）。返回领取条数（恒 1）。
func (e *compactEnv) claimOnce(sid string) int {
	e.d.EnqueueDshCompact(sid, "C:/proj")
	return len(e.d.DshPoll(pollBody(sid))["commands"].([]map[string]any))
}

// undeliveredAlerts 沙箱 gate.log 里 mode=compact-undelivered 行数（无文件＝
// 零告警的正常形态，返回空串）。
func (e *compactEnv) undeliveredAlerts() (int, string) {
	b, err := os.ReadFile(filepath.Join(e.tmp, "data", "gate.log"))
	s := string(b)
	if err != nil {
		return 0, s
	}
	return strings.Count(s, "mode=compact-undelivered"), s
}

// TestCompactUndeliveredRounds 轮数→告警数映射表（票01）：1、2 轮只日志；
// 第 3 轮恰告警一行；4、5 轮不再叠加（每会话一次防刷屏）。逐轮日志行数＝
// 轮数，行文带轮号与 sid16。
func TestCompactUndeliveredRounds(t *testing.T) {
	cases := []struct {
		name       string
		rounds     int
		wantAlerts int
	}{
		{"第1轮只日志不告警", 1, 0},
		{"第2轮只日志不告警", 2, 0},
		{"第3轮告警恰一行", 3, 1},
		{"第4轮不叠加", 4, 1},
		{"第5轮不叠加", 5, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newCompactEnv(t, 100)
			log := captureCompactLog(t)
			for i := 0; i < tc.rounds; i++ {
				e.dropOnce(compactSID)
			}
			if got, s := e.undeliveredAlerts(); got != tc.wantAlerts {
				t.Fatalf("gate.log compact-undelivered 行 = %d, want %d\n%s", got, tc.wantAlerts, s)
			}
			lines := strings.Split(strings.TrimSpace(log.String()), "\n")
			if len(lines) != tc.rounds {
				t.Fatalf("日志行 = %d, want %d（每丢一行）\n%q", len(lines), tc.rounds, log.String())
			}
			for i, ln := range lines {
				want := fmt.Sprintf("[compact] dsh 指令过期无人领取(第 %d 轮):%s",
					i+1, runeCap16(compactSID))
				if ln != want {
					t.Fatalf("日志行 %d = %q, want %q", i+1, ln, want)
				}
			}
		})
	}
}

// TestCompactUndeliveredResetHooks 清零钩子表（票01 修订版：清零只认 ok=true
// 上报——领取不再清零，bb5d5e37 实锚"领取即清零"把告警永久消音；入槽照旧
// 不清零）。wantLastRound 是计数状态变化的直接证据——清零后轮号从 1 重数；
// 不清零的对照形轮号照旧累大。领取后走在飞执行窗（窗内不重入槽），故 claim
// 形在续丢轮前推过窗口。
func TestCompactUndeliveredResetHooks(t *testing.T) {
	cases := []struct {
		name          string
		preDrops      int    // 首段连续轮数
		resetKind     string // "": 无（对照）| "claim" 领取（不清零,票01修订）| "ok-true" | "ok-false"
		postDrops     int    // 清零（或对照）后再丢轮数
		wantAlerts    int    // 全程 gate.log 告警行总数
		wantLastRound int    // 尾条日志轮号（0＝不查）
	}{
		{"ok=true上报清零:再3轮再告警轮号重数", 3, "ok-true", 3, 2, 3},
		{"入槽不清零:真实循环过期-重灌3轮即告警", 0, "", 3, 1, 3},
		{"领取不清零(票01修订):累计满3轮照告警", 2, "claim", 1, 1, 3},
		{"领取不清零(票01修订):对照形轮号累大", 1, "claim", 1, 0, 2},
		{"ok=false不清零:第3轮照告警", 2, "ok-false", 1, 1, 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newCompactEnv(t, 100)
			e.regCompact(compactSID, 100, 50000) // 上报钩子要落账本行（Acct 面）
			log := captureCompactLog(t)
			for i := 0; i < tc.preDrops; i++ {
				e.dropOnce(compactSID)
			}
			switch tc.resetKind {
			case "ok-true":
				e.d.DshCompacted(map[string]any{"session_id": compactSID, "ok": true})
			case "ok-false":
				e.d.DshCompacted(map[string]any{"session_id": compactSID, "ok": false})
			case "claim":
				if got := e.claimOnce(compactSID); got != 1 {
					t.Fatalf("领取应得 1 条指令, got %d", got)
				}
				// 票01：领取起在飞执行窗，窗内不重入槽——推过窗口再续丢轮
				e.advance(dshCompactExecWindowS + 1)
			}
			for i := 0; i < tc.postDrops; i++ {
				e.dropOnce(compactSID)
			}
			if got, s := e.undeliveredAlerts(); got != tc.wantAlerts {
				t.Fatalf("gate.log 告警 = %d, want %d\n%s", got, tc.wantAlerts, s)
			}
			if tc.wantLastRound > 0 {
				lines := strings.Split(strings.TrimSpace(log.String()), "\n")
				wantLast := fmt.Sprintf("[compact] dsh 指令过期无人领取(第 %d 轮):%s",
					tc.wantLastRound, runeCap16(compactSID))
				if last := lines[len(lines)-1]; last != wantLast {
					t.Fatalf("尾条日志 = %q, want %q（轮号重数＝计数被清零的直接证据）",
						last, wantLast)
				}
			}
		})
	}
}

// TestCompactUndeliveredClaimSilent 正常领取路径零声（票01：被领走不算无人
// 领取）——日志零行、gate.log 零告警。
func TestCompactUndeliveredClaimSilent(t *testing.T) {
	e := newCompactEnv(t, 100)
	log := captureCompactLog(t)
	if !e.d.EnqueueDshCompact(compactSID, "C:/proj") {
		t.Fatal("入槽应成功")
	}
	r := e.d.DshPoll(pollBody(compactSID))
	if got := len(r["commands"].([]map[string]any)); got != 1 {
		t.Fatalf("指令应被领取, got %d", got)
	}
	if log.String() != "" {
		t.Fatalf("领取路径应零日志, got %q", log.String())
	}
	if n, s := e.undeliveredAlerts(); n != 0 {
		t.Fatalf("领取路径应零告警, got %d\n%s", n, s)
	}
}

// TestCompactUndeliveredPerSession 每会话独立计数（票01）：两会话各满 3 轮
// 各告警一行，互不顶替、互不串账。
func TestCompactUndeliveredPerSession(t *testing.T) {
	e := newCompactEnv(t, 100)
	captureCompactLog(t)
	for i := 0; i < 3; i++ {
		e.dropOnce(compactSID)
		e.dropOnce(compactSID2)
	}
	n, s := e.undeliveredAlerts()
	if n != 2 {
		t.Fatalf("两会话应各告警一行, got %d\n%s", n, s)
	}
}

// ---- 票01：领取后执行窗——"领取不执行"空转环的钉子 ----

// resetCompactClaims 清空包级在飞领取窗（票01 包级态测试卫生，resetCompactMiss
// 同款纪律）。
func resetCompactClaims() {
	dshCompactClaimMu.Lock()
	dshCompactClaimAt = map[string]float64{}
	dshCompactClaimMu.Unlock()
}

// claimExecSweep 构造并结算一轮"领取后执行窗超时"（票01）：入槽→领取（在飞
// 窗起点）→推过执行窗（180s+60s=240s）→sweep 结算（计一轮＋清领取位）。
// 各步断言防空转假绿。
func (e *compactEnv) claimExecSweep(t *testing.T, sid string) {
	t.Helper()
	if !e.d.EnqueueDshCompact(sid, "C:/proj") {
		t.Fatal("入槽应成功（上一轮 sweep 已放行重入槽）")
	}
	if got := len(e.d.DshPoll(pollBody(sid))["commands"].([]map[string]any)); got != 1 {
		t.Fatalf("指令应被领取, got %d", got)
	}
	e.advance(dshCompactExecWindowS + 1)
	e.d.dshCompactClaimSweep(sid)
}

// TestDshCompactClaimWindowThrottlesEnqueue 在飞领取窗内不重入槽（票01 结构性
// 兜底；"领取后 30s 重触发被拒"的 daemon 面钉子）。窗走满即放行（结算归守望
// sweep，入槽侧只认窗口几何）。
func TestDshCompactClaimWindowThrottlesEnqueue(t *testing.T) {
	e := newCompactEnv(t, 100)
	if !e.d.EnqueueDshCompact(compactSID, "C:/proj") {
		t.Fatal("入槽应成功")
	}
	if got := len(e.d.DshPoll(pollBody(compactSID))["commands"].([]map[string]any)); got != 1 {
		t.Fatalf("指令应被领取, got %d", got)
	}
	e.advance(30) // 领取后 30s：执行窗（240s）内
	if e.d.EnqueueDshCompact(compactSID, "C:/proj") {
		t.Fatal("在飞领取窗内重入槽应拒（30s 重发环治点）")
	}
	e.advance(dshCompactExecWindowS) // 越过执行窗
	if !e.d.EnqueueDshCompact(compactSID, "C:/proj") {
		t.Fatal("执行窗走满后应放行重入槽")
	}
}

// TestDshCompactClaimOkClosesWindow ok=true 收口在飞窗：成功上报即刻放行重入
// 槽（真开会话秒领取秒执行→再触发不受 240s 窗拖累）。
func TestDshCompactClaimOkClosesWindow(t *testing.T) {
	e := newCompactEnv(t, 100)
	e.regCompact(compactSID, 100, 50000) // ok=true 落账本行要台账（Acct 面）
	if !e.d.EnqueueDshCompact(compactSID, "C:/proj") {
		t.Fatal("入槽应成功")
	}
	if got := len(e.d.DshPoll(pollBody(compactSID))["commands"].([]map[string]any)); got != 1 {
		t.Fatalf("指令应被领取, got %d", got)
	}
	if r := e.d.DshCompacted(map[string]any{"session_id": compactSID, "ok": true}); r["ok"] != true {
		t.Fatalf("上报响应 = %v", r)
	}
	if !e.d.EnqueueDshCompact(compactSID, "C:/proj") {
		t.Fatal("ok=true 应收口在飞窗,重入槽立即放行")
	}
}

// TestDshCompactClaimOkFalseKeepsWindow ok=false 不收口在飞窗（票01）：no-agent/
// busy/timeout/error 一律不算送达——窗照走满，30s 重发环不复活。
func TestDshCompactClaimOkFalseKeepsWindow(t *testing.T) {
	e := newCompactEnv(t, 100)
	if !e.d.EnqueueDshCompact(compactSID, "C:/proj") {
		t.Fatal("入槽应成功")
	}
	if got := len(e.d.DshPoll(pollBody(compactSID))["commands"].([]map[string]any)); got != 1 {
		t.Fatalf("指令应被领取, got %d", got)
	}
	if r := e.d.DshCompacted(map[string]any{"session_id": compactSID,
		"ok": false, "reason": "no-agent"}); r["ok"] != true {
		t.Fatalf("上报响应 = %v", r)
	}
	e.advance(30)
	if e.d.EnqueueDshCompact(compactSID, "C:/proj") {
		t.Fatal("ok=false 不收口在飞窗,窗内重入槽应拒")
	}
	e.advance(dshCompactExecWindowS)
	if !e.d.EnqueueDshCompact(compactSID, "C:/proj") {
		t.Fatal("窗走满后应放行重入槽")
	}
}

// TestCompactClaimTimeoutRounds 领取后执行窗超时计轮表（票01）：与"过期无人
// 领取"同计数同告警路径——1、2 轮只日志；第 3 轮恰 gateWarn 一行；4 轮不再
// 叠加；日志文案带"领取后执行窗"与轮号、sid16。
func TestCompactClaimTimeoutRounds(t *testing.T) {
	cases := []struct {
		name       string
		rounds     int
		wantAlerts int
	}{
		{"第1轮只日志不告警", 1, 0},
		{"第2轮只日志不告警", 2, 0},
		{"第3轮告警恰一行", 3, 1},
		{"第4轮不叠加", 4, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newCompactEnv(t, 100)
			log := captureCompactLog(t)
			for i := 0; i < tc.rounds; i++ {
				e.claimExecSweep(t, compactSID)
			}
			if got, s := e.undeliveredAlerts(); got != tc.wantAlerts {
				t.Fatalf("gate.log compact-undelivered 行 = %d, want %d\n%s", got, tc.wantAlerts, s)
			}
			lines := strings.Split(strings.TrimSpace(log.String()), "\n")
			if len(lines) != tc.rounds {
				t.Fatalf("日志行 = %d, want %d（每结算一行）\n%q", len(lines), tc.rounds, log.String())
			}
			for i, ln := range lines {
				want := fmt.Sprintf("[compact] dsh 指令领取后执行窗内无成功上报(第 %d 轮):%s",
					i+1, runeCap16(compactSID))
				if ln != want {
					t.Fatalf("日志行 %d = %q, want %q", i+1, ln, want)
				}
			}
		})
	}
}

// TestCompactMissCounterSharedAcrossPaths 两路未送达同计数（票01"同权同告警
// 路径"）：过期无人领取、领取后执行窗超时交替累计——第 3 轮照告警，日志两路
// 文案各形、轮号连续。
func TestCompactMissCounterSharedAcrossPaths(t *testing.T) {
	e := newCompactEnv(t, 100)
	log := captureCompactLog(t)
	e.dropOnce(compactSID)          // 过期无人领取 第1轮
	e.claimExecSweep(t, compactSID) // 领取后执行窗超时 第2轮
	e.dropOnce(compactSID)          // 过期无人领取 第3轮 → 告警
	if n, s := e.undeliveredAlerts(); n != 1 {
		t.Fatalf("两路同计数:第3轮应告警一行, got %d\n%s", n, s)
	}
	lines := strings.Split(strings.TrimSpace(log.String()), "\n")
	wantMsg := []string{
		fmt.Sprintf("[compact] dsh 指令过期无人领取(第 1 轮):%s", runeCap16(compactSID)),
		fmt.Sprintf("[compact] dsh 指令领取后执行窗内无成功上报(第 2 轮):%s", runeCap16(compactSID)),
		fmt.Sprintf("[compact] dsh 指令过期无人领取(第 3 轮):%s", runeCap16(compactSID)),
	}
	if len(lines) != len(wantMsg) {
		t.Fatalf("日志行 = %d, want %d\n%q", len(lines), len(wantMsg), log.String())
	}
	for i, ln := range lines {
		if ln != wantMsg[i] {
			t.Fatalf("日志行 %d = %q, want %q", i+1, ln, wantMsg[i])
		}
	}
}

// ---- dsh-host-guard 票01：live 三值派发路由（spec C 逐字） ----
//
// 指令在槽内时 DshPoll 应答从「谁把 sid 列进清单就给谁」收口为：本次轮询对该
// sid 报 live=true → 可领取；报 live=false → 不派给（即便列了该 sid）；键缺失
//（旧协议体）→ 可领取（兼容例外，维持现状）。派发判定只看本次轮询体里的
// live 值，不依赖跨轮询记忆。

// TestDshCompactPollLiveRouting 派发路由三分支＋交错领取：false 宿主先轮询不
// 得（指令留槽），活宿主/旧体宿主后到照领。
func TestDshCompactPollLiveRouting(t *testing.T) {
	tt, ff := true, false
	t.Run("live=false不派留槽,live=true可领", func(t *testing.T) {
		e := newCompactEnv(t, 100)
		if !e.d.EnqueueDshCompact(compactSID, "C:/proj") {
			t.Fatal("入槽应成功")
		}
		if cmds := e.d.DshPoll(pollBodyLive(compactSID, &ff))["commands"].([]map[string]any); len(cmds) != 0 {
			t.Fatalf("live=false 宿主列了该 sid 也不派, got %v", cmds)
		}
		if e.slotPeek(compactSID) == nil {
			t.Fatal("false 宿主轮询不得清槽——指令留给持活 agent 的宿主")
		}
		if cmds := e.d.DshPoll(pollBodyLive(compactSID, &tt))["commands"].([]map[string]any); len(cmds) != 1 {
			t.Fatalf("live=true 宿主应领取, got %v", cmds)
		}
		if e.slotPeek(compactSID) != nil {
			t.Fatal("领取即清槽（既有语义）")
		}
	})
	t.Run("缺键旧体可领取(false宿主之后旧体宿主照领)", func(t *testing.T) {
		e := newCompactEnv(t, 100)
		if !e.d.EnqueueDshCompact(compactSID, "C:/proj") {
			t.Fatal("入槽应成功")
		}
		if cmds := e.d.DshPoll(pollBodyLive(compactSID, &ff))["commands"].([]map[string]any); len(cmds) != 0 {
			t.Fatalf("live=false 不派, got %v", cmds)
		}
		// 前向兼容硬约束：旧协议体（无 live 键）与今天行为完全一致——可领取
		if cmds := e.d.DshPoll(pollBodyLive(compactSID, nil))["commands"].([]map[string]any); len(cmds) != 1 {
			t.Fatalf("缺键（旧协议体）宿主应可领取（兼容例外）, got %v", cmds)
		}
	})
	t.Run("非布尔live坏形按缺键宽容领取", func(t *testing.T) {
		e := newCompactEnv(t, 100)
		e.d.EnqueueDshCompact(compactSID, "C:/proj")
		body := map[string]any{"agent": "dsh", "sessions": []any{
			map[string]any{"sid": compactSID, "idle_s": 3.0, "live": "yes"}}}
		if cmds := e.d.DshPoll(body)["commands"].([]map[string]any); len(cmds) != 1 {
			t.Fatalf("非布尔 live＝非显式上报,按缺键宽容领取（坏形静默收窄）, got %v", cmds)
		}
	})
}
