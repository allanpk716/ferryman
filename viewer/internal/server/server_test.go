package server

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// seedLedger 在临时目录写一份两 lineage 的小账本。
// 同 lineage 行故意乱序写入（usage 200 在 usage 100 之前），考验 timeline 的 ts 升序排序。
func seedLedger(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	var b strings.Builder
	// lin-t1：usage×2 + window×1 + beat/handoff/block 各 1
	b.WriteString(`{"v":1,"kind":"usage","ts":200,"agent":"cc","lineage_id":"lin-t1","project":"P","title":"T1","input_tokens":7,"output_tokens":3}` + "\n")
	b.WriteString(`{"v":1,"kind":"window","ts":110,"lineage_id":"lin-t1","opened_ts":1000,"closed_ts":2500,"dur_s":1500,"prefix_tokens":150000,"close_reason":"limit"}` + "\n")
	b.WriteString(`{"v":1,"kind":"beat","ts":112,"lineage_id":"lin-t1","hit":true,"price_ver":"v2026-09"}` + "\n")
	b.WriteString(`{"v":1,"kind":"usage","ts":100,"agent":"cc","lineage_id":"lin-t1","project":"P","title":"T1","input_tokens":5,"output_tokens":2}` + "\n")
	b.WriteString(`{"v":1,"kind":"handoff","ts":105,"lineage_id":"lin-t1","provider":"glm","price_ver":"v2026-09"}` + "\n")
	b.WriteString(`{"v":1,"kind":"block","ts":104,"lineage_id":"lin-t1","idle_s":30}` + "\n")
	// lin-t2：仅 usage×1（LastTS=300 比 lin-t1 大 → sessions 列表排在前）
	b.WriteString(`{"v":1,"kind":"usage","ts":300,"agent":"codex","lineage_id":"lin-t2","project":"P","title":"T2","input_tokens":1}` + "\n")
	if err := os.WriteFile(filepath.Join(dir, "202609.jsonl"), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func serve(t *testing.T, dir string) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(New(dir).Routes())
	t.Cleanup(ts.Close)
	return ts
}

func call(t *testing.T, ts *httptest.Server, method, path, body string) (int, []byte) {
	t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, ts.URL+path, rd)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, b
}

func decode(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("响应不是 JSON 对象: %v\n%s", err, body)
	}
	return m
}

func arr(t *testing.T, m map[string]any, key string) []any {
	t.Helper()
	v, ok := m[key].([]any)
	if !ok {
		t.Fatalf("键 %q 不是数组（可能为 null）: %v", key, m[key])
	}
	return v
}

func fnum(t *testing.T, m map[string]any, key string) float64 {
	t.Helper()
	v, ok := m[key].(float64)
	if !ok {
		t.Fatalf("键 %q 不是数字: %v", key, m[key])
	}
	return v
}

func obj(t *testing.T, m map[string]any, key string) map[string]any {
	t.Helper()
	v, ok := m[key].(map[string]any)
	if !ok {
		t.Fatalf("键 %q 不是对象: %v", key, m[key])
	}
	return v
}

func assertNear(t *testing.T, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1e-6 {
		t.Fatalf("got %v want %v", got, want)
	}
}

// TestAPI 全端点表驱动：status + 逐用例校验函数。
func TestAPI(t *testing.T) {
	dir := seedLedger(t)
	ts := serve(t, dir)
	missing := serve(t, filepath.Join(dir, "no-such-dir"))

	golden := `{"lineage":"lin-t1","opened_ts":1000,"closed_ts":2500,"prefix_tokens":150000,` +
		`"params":{"p_in":6.9,"p_cache":1.7,"p_out":24,"per":10000,"ttl_s":600,"safety":0.8,"beat_out_tokens":300,"max_wait_s":0}}`
	noCache := strings.Replace(golden, `"p_cache":1.7`, `"p_cache":0`, 1)
	noTTL := strings.Replace(golden, `"ttl_s":600`, `"ttl_s":0`, 1)
	manual := strings.Replace(golden, `"max_wait_s":0`, `"max_wait_s":900`, 1)
	trailing := golden + "{}"                                     // 合法 JSON 后跟第二条：尾随垃圾必须拒绝
	oversize := `{"lineage":"` + strings.Repeat("a", 1<<20) + `"` // >1 MiB，MaxBytesReader 拒收

	cases := []struct {
		name   string
		srv    *httptest.Server
		method string
		path   string
		body   string
		status int
		check  func(t *testing.T, body []byte)
	}{
		{
			name: "sessions 聚合两 lineage 且 LastTS 倒序", srv: ts, method: http.MethodGet, path: "/api/sessions", status: 200,
			check: func(t *testing.T, body []byte) {
				m := decode(t, body)
				ss := arr(t, m, "sessions")
				if len(ss) != 2 {
					t.Fatalf("sessions 行数 = %d, want 2", len(ss))
				}
				if fnum(t, m, "generated_at") <= 0 {
					t.Fatalf("generated_at = %v, want > 0", m["generated_at"])
				}
				s0 := ss[0].(map[string]any)
				if s0["lineage_id"] != "lin-t2" {
					t.Fatalf("sessions[0].lineage_id = %v, want lin-t2（LastTS 倒序）", s0["lineage_id"])
				}
				if s0["requests"] != float64(1) || s0["input"] != float64(1) {
					t.Fatalf("lin-t2 汇总 = %v, want requests=1 input=1", s0)
				}
				s1 := ss[1].(map[string]any)
				if s1["lineage_id"] != "lin-t1" || s1["requests"] != float64(2) || s1["windows"] != float64(1) || s1["beats"] != float64(1) {
					t.Fatalf("lin-t1 汇总 = %v, want requests=2 windows=1 beats=1", s1)
				}
			},
		},
		{
			name: "timeline 三数组过滤+升序", srv: ts, method: http.MethodGet, path: "/api/timeline?lineage=lin-t1", status: 200,
			check: func(t *testing.T, body []byte) {
				m := decode(t, body)
				reqs := arr(t, m, "requests")
				events := arr(t, m, "events")
				wins := arr(t, m, "windows")
				if len(reqs) != 2 || len(events) != 3 || len(wins) != 1 {
					t.Fatalf("数组长度 = (req %d, evt %d, win %d), want (2, 3, 1)", len(reqs), len(events), len(wins))
				}
				// requests：只含 usage，ts 升序 [100, 200]（账本里 200 行写在前）
				for i, want := range []float64{100, 200} {
					e := reqs[i].(map[string]any)
					if e["kind"] != "usage" || e["ts"] != want {
						t.Fatalf("requests[%d] = %v, want kind=usage ts=%v", i, e, want)
					}
				}
				// events：非 usage 且非 window，ts 升序 [104 block, 105 handoff, 112 beat]
				for i, w := range [][2]any{{"block", float64(104)}, {"handoff", float64(105)}, {"beat", float64(112)}} {
					e := events[i].(map[string]any)
					if e["kind"] != w[0] || e["ts"] != w[1] {
						t.Fatalf("events[%d] = %v, want kind=%v ts=%v", i, e, w[0], w[1])
					}
				}
				// windows：仅 window 行
				w0 := wins[0].(map[string]any)
				if w0["kind"] != "window" || w0["opened_ts"] != float64(1000) || w0["closed_ts"] != float64(2500) {
					t.Fatalf("windows[0] = %v, want window opened=1000 closed=2500", w0)
				}
			},
		},
		{
			name: "timeline 未知 lineage → 三个空数组（非 null）", srv: ts, method: http.MethodGet, path: "/api/timeline?lineage=nope", status: 200,
			check: func(t *testing.T, body []byte) {
				m := decode(t, body)
				for _, k := range []string{"requests", "events", "windows"} {
					if got := arr(t, m, k); len(got) != 0 {
						t.Fatalf("%s = %v, want 空数组", k, got)
					}
				}
			},
		},
		{
			name: "backtest 黄金参数", srv: ts, method: http.MethodPost, path: "/api/backtest", body: golden, status: 200,
			check: func(t *testing.T, body []byte) {
				m := decode(t, body)
				if m["ok"] != true {
					t.Fatalf("ok = %v, body=%s", m["ok"], body)
				}
				r := obj(t, m, "result")
				assertNear(t, fnum(t, r, "tau_s"), 480)
				assertNear(t, fnum(t, r, "per_beat"), 26.22)
				assertNear(t, fnum(t, r, "expire"), 103.5)
				// beats 绝对时间戳：opened_ts=1000 起算 T0+τ → [1480, 1960]
				beats := arr(t, m, "beats")
				if len(beats) != 2 {
					t.Fatalf("beats = %v, want [1480 1960]", m["beats"])
				}
				assertNear(t, beats[0].(float64), 1480)
				assertNear(t, beats[1].(float64), 1960)
				assertNear(t, fnum(t, m, "beats_cost"), 52.44)
				assertNear(t, fnum(t, m, "do_nothing_cost"), 103.5) // dur=1500 > ttl 600 → 过期一次
				note, _ := m["note"].(string)
				if !strings.Contains(note, "手动上限只能往下收") || !strings.Contains(note, "1427.9") {
					t.Fatalf("note = %q, want 含「手动上限只能往下收」与 auto≈1427.9", note)
				}
			},
		},
		{
			name: "backtest 手动上限 900 收窄 cap 且截跳", srv: ts, method: http.MethodPost, path: "/api/backtest", body: manual, status: 200,
			check: func(t *testing.T, body []byte) {
				m := decode(t, body)
				if m["ok"] != true {
					t.Fatalf("ok = %v, body=%s", m["ok"], body)
				}
				assertNear(t, fnum(t, obj(t, m, "result"), "cap_s"), 900)
				beats := arr(t, m, "beats")
				if len(beats) != 1 || !near(beats[0].(float64), 1480) {
					t.Fatalf("beats = %v, want 仅 [1480]（1960 超 cap 1900 被截）", beats)
				}
				note, _ := m["note"].(string)
				if !strings.Contains(note, "900") || !strings.Contains(note, "1427.9") {
					t.Fatalf("note = %q, want 同时写明本次 cap 与 auto 值", note)
				}
			},
		},
		{
			name: "backtest p_cache=0 → 200 ok:false", srv: ts, method: http.MethodPost, path: "/api/backtest", body: noCache, status: 200,
			check: func(t *testing.T, body []byte) {
				m := decode(t, body)
				if m["ok"] != false {
					t.Fatalf("ok = %v, body=%s", m["ok"], body)
				}
				if !strings.Contains(m["error"].(string), "拒绝推导") {
					t.Fatalf("error = %v, want 含「拒绝推导」", m["error"])
				}
			},
		},
		{
			name: "backtest ttl_s=0 → 200 ok:false", srv: ts, method: http.MethodPost, path: "/api/backtest", body: noTTL, status: 200,
			check: func(t *testing.T, body []byte) {
				m := decode(t, body)
				if m["ok"] != false {
					t.Fatalf("ok = %v, body=%s", m["ok"], body)
				}
				if !strings.Contains(m["error"].(string), "拒绝推导") {
					t.Fatalf("error = %v, want 含「拒绝推导」", m["error"])
				}
			},
		},
		{
			name: "backtest 坏 JSON → 400", srv: ts, method: http.MethodPost, path: "/api/backtest", body: "{oops", status: 400,
			check: func(t *testing.T, body []byte) {
				if decode(t, body)["ok"] != false {
					t.Fatalf("坏请求体应返回 ok:false, body=%s", body)
				}
			},
		},
		{
			name: "backtest 尾随垃圾 → 400", srv: ts, method: http.MethodPost, path: "/api/backtest", body: trailing, status: 400,
			check: func(t *testing.T, body []byte) {
				m := decode(t, body)
				if m["ok"] != false {
					t.Fatalf("尾随垃圾应返回 ok:false, body=%s", body)
				}
				if !strings.Contains(m["error"].(string), "多余内容") {
					t.Fatalf("error = %v, want 含「多余内容」", m["error"])
				}
			},
		},
		{
			name: "backtest 超 1MiB 请求体 → 400", srv: ts, method: http.MethodPost, path: "/api/backtest", body: oversize, status: 400,
			check: func(t *testing.T, body []byte) {
				m := decode(t, body)
				if m["ok"] != false {
					t.Fatalf("超限请求体应返回 ok:false, body=%s", body)
				}
				if !strings.Contains(m["error"].(string), "too large") {
					t.Fatalf("error = %v, want 含 too large（MaxBytesReader 拒收）", m["error"])
				}
			},
		},
		{
			name: "数据目录不存在 → 200 空列表+note", srv: missing, method: http.MethodGet, path: "/api/sessions", status: 200,
			check: func(t *testing.T, body []byte) {
				m := decode(t, body)
				if got := arr(t, m, "sessions"); len(got) != 0 {
					t.Fatalf("sessions = %v, want 空数组", got)
				}
				if !strings.Contains(m["note"].(string), "数据目录不存在") {
					t.Fatalf("note = %v, want 含「数据目录不存在」", m["note"])
				}
			},
		},
		{
			name: "方法不符 → 405", srv: ts, method: http.MethodPost, path: "/api/sessions", status: 405,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, body := call(t, tc.srv, tc.method, tc.path, tc.body)
			if status != tc.status {
				t.Fatalf("status = %d, want %d\n%s", status, tc.status, body)
			}
			if tc.check != nil {
				tc.check(t, body)
			}
		})
	}
}

func near(a, b float64) bool { return math.Abs(a-b) <= 1e-6 }

// TestNote 服务端提示条：SetNote 后 sessions/timeline 都带 note 键且值正确；
// 未 SetNote 且目录存在时不得出现 note 键；目录缺失提示与演示标注并存时用
// "；"连接、演示标注在后（前端展示顺序：先讲数据为何为空，再讲这是演示）。
func TestNote(t *testing.T) {
	dir := seedLedger(t)
	demoNote := "演示模式：当前数据为合成账本（含未来心跳事件的预演），非真实流水"

	noted := New(dir)
	noted.SetNote(demoNote)
	tsNoted := httptest.NewServer(noted.Routes())
	t.Cleanup(tsNoted.Close)

	missing := New(filepath.Join(dir, "no-such-dir"))
	missing.SetNote(demoNote)
	tsMissing := httptest.NewServer(missing.Routes())
	t.Cleanup(tsMissing.Close)

	cases := []struct {
		name string
		srv  *httptest.Server
		path string
		note any    // 期望 note 值；nil = 不应出现 note 键
	}{
		{name: "sessions 带演示标注", srv: tsNoted, path: "/api/sessions", note: demoNote},
		{name: "timeline 带演示标注", srv: tsNoted, path: "/api/timeline?lineage=lin-t1", note: demoNote},
		{
			name: "未 SetNote 且目录存在 → 无 note 键", srv: serve(t, dir), path: "/api/sessions", note: nil,
		},
		{
			name: "未 SetNote 且目录存在 → timeline 无 note 键", srv: serve(t, dir), path: "/api/timeline?lineage=lin-t1", note: nil,
		},
		{
			name: "目录缺失 + 演示标注 → 「；」连接且演示在后", srv: tsMissing, path: "/api/sessions",
			note: fmt.Sprintf("数据目录不存在：%s；%s", filepath.Join(dir, "no-such-dir"), demoNote),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, body := call(t, tc.srv, http.MethodGet, tc.path, "")
			if status != 200 {
				t.Fatalf("status = %d\n%s", status, body)
			}
			m := decode(t, body)
			got, ok := m["note"]
			if tc.note == nil {
				if ok {
					t.Fatalf("不应出现 note 键，got %v", got)
				}
				return
			}
			if !ok || got != tc.note {
				t.Fatalf("note = %v, want %q", got, tc.note)
			}
		})
	}
}
