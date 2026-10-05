package daemon

// settings_write_test.go — 设置视图票03：daemon 单写者锁 + 节级 PUT + 审计行
// 验收钉子。
//
// 验收对照（票面）：
//   - 两并发 PUT 不同节：两节都写入、无交错损坏（多轮放大丢写窗口）；
//   - PUT 期间 GET 不被阻塞（锁只罩写路径；白盒占锁 + 排队 PUT 放行）；
//   - 校验失败的 PUT：config 字节不动、审计行记 attempt（after=试图值）
//     + rejected（outcome/error）；
//   - 改 [server].port 的响应含 new_port（同值再写不附）；
//   - 审计行密钥字段为 <masked>，真钥（改前/改后）都不出现，真钥照常落盘；
//   - 守门：无/错 Bearer 401；未知节/实体路径 404（auth 前）；坏 JSON 400
//     且不落审计；替身（非 *Daemon）404。
//
// 环境夹具复用 settings_read_test.go 的 newSettingsEnv（同包编译面）：
// FERRYMAN_CONFIG 指临时配置（data_dir 钉沙箱 → 审计文件也落沙箱），守护
// 内存 cfg 自该文件装载，时钟冻结在 e.t0（审计 ts 可确定性断言）。

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"ferryman/internal/config"
)

// ---- 小帮手 ----

// swReqNF 非致命 HTTP 请求（goroutine 安全——t.Fatalf 只许测试主协程）。
func swReqNF(method string, port int, token, path string, body []byte) (int, []byte) {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, fmt.Sprintf("http://127.0.0.1:%d%s", port, path), rd)
	if err != nil {
		return 0, []byte(err.Error())
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		return 0, []byte(err.Error())
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, raw
}

// swPut 主协程 PUT 助手：body 为节完整 JSON 对象。
func swPut(t *testing.T, e *queryEnv, section string, body map[string]any) (int, []byte) {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return swReqNF(http.MethodPut, e.port, e.token, "/settings/"+section, raw)
}

// swAuditPath 审计文件路径（= <DataDir>/settings-audit.log，沙箱内）。
func swAuditPath(e *queryEnv) string {
	return filepath.Join(e.d.Cfg.DataDir(), "settings-audit.log")
}

// swAuditLines 读审计文件逐行解 JSON（文件缺席=零行）。
func swAuditLines(t *testing.T, e *queryEnv) []map[string]any {
	t.Helper()
	raw, err := os.ReadFile(swAuditPath(e))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("读审计文件: %v", err)
	}
	var out []map[string]any
	for _, ln := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if ln == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(ln), &m); err != nil {
			t.Fatalf("审计行非单行 JSON: %q: %v", ln, err)
		}
		out = append(out, m)
	}
	return out
}

// swSnapRec 快照桩录制替身（票05 接线点在位的钉子）：记录写前调用 reason。
type swSnapRec struct {
	mu    sync.Mutex
	calls []string
}

func (r *swSnapRec) get() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.calls...)
}

// swSwapSnapshot 把 snapshotBeforeWrite 桩换成录制替身（用毕还原），返回
// 录制器。
func swSwapSnapshot(t *testing.T) *swSnapRec {
	t.Helper()
	orig := snapshotBeforeWrite
	rec := &swSnapRec{}
	snapshotBeforeWrite = func(reason string) error {
		rec.mu.Lock()
		rec.calls = append(rec.calls, reason)
		rec.mu.Unlock()
		return nil
	}
	t.Cleanup(func() { snapshotBeforeWrite = orig })
	return rec
}

// ---- 验收钉子 ----

// TestSettingsWritePutsSectionAndAudits 基本面：合法 PUT 落盘（其余节不殃及）
// + 审计单行 JSON（ts/entry/section/outcome/before=盘上改前值/after）+ 快照
// 桩写前在位调用（票05 接线点）。
func TestSettingsWritePutsSectionAndAudits(t *testing.T) {
	e, cfgPath := newSettingsEnv(t)
	snap := swSwapSnapshot(t)

	code, raw := swPut(t, e, "gate", map[string]any{"cc_mode": "enforce", "codex_mode": "off", "dsh_mode": ""})
	if code != http.StatusOK {
		t.Fatalf("PUT /settings/gate = %d %q, want 200", code, raw)
	}
	var resp map[string]any
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("响应非 JSON: %v (%q)", err, raw)
	}
	if resp["saved"] != true || resp["needs_restart"] != true {
		t.Fatalf("响应 = %v, want saved=true needs_restart=true", resp)
	}

	// 盘上节值真的换了；其余节未殃及（节级整写不动别人）。
	cfg, err := config.Load(cfgPath, true)
	if err != nil {
		t.Fatalf("写后 Load: %v", err)
	}
	if cfg.GateCC != "enforce" || cfg.GateCodex != "off" {
		t.Fatalf("盘上 gate = %q/%q, want enforce/off", cfg.GateCC, cfg.GateCodex)
	}
	if cfg.Thresholds.SummarizeS != 1500 || cfg.Thresholds.BlockS != 2100 {
		t.Fatalf("thresholds 被殃及: %v", cfg.Thresholds)
	}

	// 审计行：恰一行单行 JSON，字段齐全；before 取盘上改前值。
	lines := swAuditLines(t, e)
	if len(lines) != 1 {
		t.Fatalf("审计行数 = %d, want 1", len(lines))
	}
	ln := lines[0]
	if ln["ts"] != e.t0 {
		t.Fatalf("审计 ts = %v, want 冻结钟 %v", ln["ts"], e.t0)
	}
	if ln["entry"] != "settings-ui" || ln["section"] != "gate" || ln["outcome"] != "saved" {
		t.Fatalf("审计行头 = %v", ln)
	}
	before, ok := ln["before"].(map[string]any)
	if !ok || before["cc_mode"] != "observe" {
		t.Fatalf("审计 before = %v, want 盘上改前 {cc_mode:observe}", ln["before"])
	}
	after, ok := ln["after"].(map[string]any)
	if !ok || after["cc_mode"] != "enforce" {
		t.Fatalf("审计 after = %v", ln["after"])
	}

	// 快照桩：写前恰调用一次，reason 点名节（票05 接线点在位）。
	calls := snap.get()
	if len(calls) != 1 || !strings.Contains(calls[0], "gate") {
		t.Fatalf("快照桩调用 = %v, want 1 次含 gate", calls)
	}
}

// TestSettingsWriteConcurrentSectionsSerialized 两并发 PUT 不同节：串行化后
// 两节都写入、无交错损坏。多轮放大丢写窗口——无锁实现是整文件读改写，
// 并发必有一方节改动被后写者覆盖（红灯可复现）。
func TestSettingsWriteConcurrentSectionsSerialized(t *testing.T) {
	e, cfgPath := newSettingsEnv(t)
	const rounds = 3
	for round := 0; round < rounds; round++ {
		wantCC := "enforce"
		wantEnabled := true
		if round%2 == 1 {
			wantCC = "observe"
			wantEnabled = false
		}
		gateBody, _ := json.Marshal(map[string]any{"cc_mode": wantCC, "codex_mode": "off", "dsh_mode": ""})
		notifyBody, _ := json.Marshal(map[string]any{"enabled": wantEnabled, "pushover": false,
			"pushover_token": "pushover-token-abcdefgh", "pushover_user": "u1", "toast": false})

		start := make(chan struct{})
		codes := make(chan int, 2)
		var wg sync.WaitGroup
		for i := 0; i < 2; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-start
				if i == 0 {
					c, _ := swReqNF(http.MethodPut, e.port, e.token, "/settings/gate", gateBody)
					codes <- c
				} else {
					c, _ := swReqNF(http.MethodPut, e.port, e.token, "/settings/notify", notifyBody)
					codes <- c
				}
			}(i)
		}
		close(start)
		wg.Wait()
		close(codes)
		for c := range codes {
			if c != http.StatusOK {
				t.Fatalf("第 %d 轮并发 PUT 状态码 = %d, want 200", round, c)
			}
		}

		// 无交错损坏：文件仍可整装 Load；两节改动都在（丢写=缺一即红）；
		// 旁观节不动。
		cfg, err := config.Load(cfgPath, true)
		if err != nil {
			t.Fatalf("第 %d 轮后 Load 失败（交错损坏?）: %v", round, err)
		}
		if cfg.GateCC != wantCC {
			t.Fatalf("第 %d 轮后 gate = %q, want %q（并发丢写）", round, cfg.GateCC, wantCC)
		}
		if cfg.Notify.Enabled != wantEnabled {
			t.Fatalf("第 %d 轮后 notify.enabled = %v, want %v（并发丢写）", round, cfg.Notify.Enabled, wantEnabled)
		}
		if cfg.Thresholds.SummarizeS != 1500 || cfg.Thresholds.BlockS != 2100 {
			t.Fatalf("第 %d 轮后 thresholds 被殃及: %v", round, cfg.Thresholds)
		}
	}
	// 审计：每写一行，全部 saved。
	lines := swAuditLines(t, e)
	if len(lines) != rounds*2 {
		t.Fatalf("审计行数 = %d, want %d", len(lines), rounds*2)
	}
	for i, ln := range lines {
		if ln["outcome"] != "saved" {
			t.Fatalf("审计行 %d outcome = %v, want saved", i, ln["outcome"])
		}
	}
}

// TestSettingsWriteGetNotBlockedWhilePutInFlight 锁只罩写路径：白盒占住
// 单写者锁（=模拟在飞 PUT 的临界区），GET /settings 照常回话；排队的 PUT
// 阻塞至放锁才放行（并发请求阻塞排队）。
func TestSettingsWriteGetNotBlockedWhilePutInFlight(t *testing.T) {
	e, _ := newSettingsEnv(t)

	settingsWriteMu.Lock()
	unlocked := false
	release := func() {
		if !unlocked {
			unlocked = true
			settingsWriteMu.Unlock()
		}
	}
	t.Cleanup(release) // 失败路径也放锁，不拖垮后续测试

	// 持锁期间 GET 不被阻塞（读面不取写锁）。
	got := make(chan int, 1)
	go func() {
		c, _ := swReqNF(http.MethodGet, e.port, e.token, "/settings", nil)
		got <- c
	}()
	select {
	case c := <-got:
		if c != http.StatusOK {
			t.Fatalf("持锁期间 GET /settings = %d, want 200", c)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("持锁期间 GET /settings 被阻塞（锁只应罩写路径）")
	}

	// 排队 PUT：持锁期间不得完成；放锁后放行。
	putDone := make(chan int, 1)
	go func() {
		b, _ := json.Marshal(map[string]any{"cc_mode": "enforce"})
		c, _ := swReqNF(http.MethodPut, e.port, e.token, "/settings/gate", b)
		putDone <- c
	}()
	select {
	case c := <-putDone:
		t.Fatalf("持锁期间 PUT 不应完成 = %d", c)
	case <-time.After(300 * time.Millisecond):
	}
	release()
	select {
	case c := <-putDone:
		if c != http.StatusOK {
			t.Fatalf("放锁后排队 PUT = %d, want 200", c)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("放锁后排队 PUT 未放行")
	}
}

// TestSettingsWriteRejectedKeepsConfigAndAuditsAttempt 校验失败的 PUT
// （阈值差<120s，票01 写前全量校验拒写）：config 字节不动；审计行记
// attempt（after=试图值）+rejected（outcome/error）。
func TestSettingsWriteRejectedKeepsConfigAndAuditsAttempt(t *testing.T) {
	e, cfgPath := newSettingsEnv(t)
	beforeBytes, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}

	code, raw := swPut(t, e, "thresholds", map[string]any{
		"summarize_s": 2000, "block_s": 2100, "min_ctx_tokens": 100, "cache_warn_s": 720})
	if code != http.StatusBadRequest {
		t.Fatalf("非法 PUT = %d %q, want 400", code, raw)
	}
	if !strings.Contains(string(raw), "阈值差") {
		t.Fatalf("400 文案应点名校验规则: %q", raw)
	}

	// config 字节不动（票01 原语拒写语义的端到端面）。
	afterBytes, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(beforeBytes, afterBytes) {
		t.Fatal("校验失败的 PUT 改动了 config 字节")
	}

	// 审计行：rejected + attempt（before=盘上现值, after=试图值）+error。
	lines := swAuditLines(t, e)
	if len(lines) != 1 {
		t.Fatalf("审计行数 = %d, want 1", len(lines))
	}
	ln := lines[0]
	if ln["outcome"] != "rejected" {
		t.Fatalf("outcome = %v, want rejected", ln["outcome"])
	}
	if errStr, _ := ln["error"].(string); errStr == "" || !strings.Contains(errStr, "阈值差") {
		t.Fatalf("error = %v, want 含 阈值差", ln["error"])
	}
	if b, _ := ln["before"].(map[string]any); b == nil || b["summarize_s"] != float64(1500) {
		t.Fatalf("before = %v, want 盘上现值 summarize_s=1500", ln["before"])
	}
	if a, _ := ln["after"].(map[string]any); a == nil || a["summarize_s"] != float64(2000) {
		t.Fatalf("after = %v, want 试图值 summarize_s=2000（attempt 记录）", ln["after"])
	}
}

// TestSettingsWriteServerPortChangeReturnsNewPort [server].port 改动：响应附
// new_port + needs_restart；同值再写不附；盘上真换、守护内存不换（换挡在
// 重启——needs_restart 语义）。
func TestSettingsWriteServerPortChangeReturnsNewPort(t *testing.T) {
	e, cfgPath := newSettingsEnv(t)
	dataDir := filepath.ToSlash(e.d.Cfg.DataDir())
	body := map[string]any{"port": 15999, "data_dir": dataDir}

	code, raw := swPut(t, e, "server", body)
	if code != http.StatusOK {
		t.Fatalf("PUT /settings/server = %d %q, want 200", code, raw)
	}
	var resp map[string]any
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatal(err)
	}
	if resp["new_port"] != float64(15999) {
		t.Fatalf("new_port = %v, want 15999", resp["new_port"])
	}
	if resp["needs_restart"] != true {
		t.Fatalf("needs_restart = %v, want true", resp["needs_restart"])
	}
	cfg, err := config.Load(cfgPath, true)
	if err != nil || cfg.Server.Port != 15999 {
		t.Fatalf("盘上 port = %d err=%v, want 15999", cfg.Server.Port, err)
	}
	if e.d.Cfg.Server.Port != 15700 {
		t.Fatalf("守护内存 port = %d, want 15700（写面只落盘，换挡在重启）", e.d.Cfg.Server.Port)
	}

	// 同值再写：不附 new_port。
	code, raw = swPut(t, e, "server", body)
	if code != http.StatusOK {
		t.Fatalf("同值再写 = %d %q, want 200", code, raw)
	}
	resp = nil
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatal(err)
	}
	if _, has := resp["new_port"]; has {
		t.Fatalf("同值再写不应附 new_port: %v", resp)
	}
}

// TestSettingsWriteAuditMasksSecretValues 审计脱敏：密钥字段（毒名单口径）
// 的 before/after 值一律 <masked>；改前/改后真钥都不出现在审计文件；非密钥
// 字段不误伤；真钥照常落盘（掩码只限审计面——读面另有掩码口径）。
func TestSettingsWriteAuditMasksSecretValues(t *testing.T) {
	e, cfgPath := newSettingsEnv(t)

	code, raw := swPut(t, e, "notify", map[string]any{"enabled": true, "pushover": false,
		"pushover_token": "sk-brand-new-secret-zzzz", "pushover_user": "user-123", "toast": false})
	if code != http.StatusOK {
		t.Fatalf("PUT /settings/notify = %d %q, want 200", code, raw)
	}

	rawAudit, err := os.ReadFile(swAuditPath(e))
	if err != nil {
		t.Fatalf("读审计文件: %v", err)
	}
	audit := string(rawAudit)
	if strings.Contains(audit, "sk-brand-new-secret-zzzz") {
		t.Fatal("改后真钥泄漏进审计行")
	}
	if strings.Contains(audit, "pushover-token-abcdefgh") {
		t.Fatal("改前真钥泄漏进审计行")
	}
	lines := swAuditLines(t, e)
	if len(lines) != 1 {
		t.Fatalf("审计行数 = %d, want 1", len(lines))
	}
	ln := lines[0]
	before, _ := ln["before"].(map[string]any)
	after, _ := ln["after"].(map[string]any)
	if before == nil || before["pushover_token"] != "<masked>" {
		t.Fatalf("before.pushover_token = %v, want <masked>", ln["before"])
	}
	if after == nil || after["pushover_token"] != "<masked>" {
		t.Fatalf("after.pushover_token = %v, want <masked>", ln["after"])
	}
	if after["pushover_user"] != "user-123" {
		t.Fatalf("after.pushover_user = %v, want 原值（非密钥不误伤）", after["pushover_user"])
	}

	// 真钥照常落盘（掩码只在审计面）。
	cfg, err := config.Load(cfgPath, true)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Notify.PushoverToken != "sk-brand-new-secret-zzzz" {
		t.Fatalf("盘上 pushover_token = %q, want 真钥照常落盘", cfg.Notify.PushoverToken)
	}
}

// TestSettingsWriteSecretMergeKeepsDiskValue F3 密钥合并（spec 全局规则，
// 返工①）：毒名单密钥字段——省略、等于读面掩码占位串（"••••"+尾四位）、
// 空串、读面对象形 {masked,has_key} → 盘上真钥逐字节保留；仅显式非空新值
// 覆盖；怪类型（数字等）拒写。
func TestSettingsWriteSecretMergeKeepsDiskValue(t *testing.T) {
	e, cfgPath := newSettingsEnv(t)
	const diskToken = "pushover-token-abcdefgh" // 夹具盘上真钥

	assertKept := func(stage string, want string) {
		t.Helper()
		cfgBytes, err := os.ReadFile(cfgPath)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(cfgBytes), want) {
			t.Fatalf("%s: 盘上真钥 %q 未逐字节保留", stage, want)
		}
		cfg, err := config.Load(cfgPath, true)
		if err != nil || cfg.Notify.PushoverToken != want {
			t.Fatalf("%s: Load 后 token = %q err=%v, want %q", stage, cfg.Notify.PushoverToken, err, want)
		}
	}

	// ① 省略密钥字段：节级整写不得误清真钥。
	code, raw := swPut(t, e, "notify", map[string]any{"enabled": true})
	if code != http.StatusOK {
		t.Fatalf("省略密钥 PUT = %d %q, want 200", code, raw)
	}
	assertKept("省略", diskToken)
	// 审计面照常 <masked>（注入盘值不进审计明文）。
	lines := swAuditLines(t, e)
	if len(lines) != 1 {
		t.Fatalf("审计行数 = %d, want 1", len(lines))
	}
	if a, _ := lines[0]["after"].(map[string]any); a == nil || a["pushover_token"] != "<masked>" {
		t.Fatalf("after.pushover_token = %v, want <masked>", lines[0]["after"])
	}

	// ② 掩码占位串（读面回显形）：同样保留现值。
	code, raw = swPut(t, e, "notify", map[string]any{"enabled": true, "pushover_token": "••••efgh"})
	if code != http.StatusOK {
		t.Fatalf("掩码占位 PUT = %d %q, want 200", code, raw)
	}
	assertKept("掩码占位串", diskToken)

	// ③ 显式非空新值：覆盖。
	code, raw = swPut(t, e, "notify", map[string]any{"enabled": true, "pushover_token": "sk-rotate-new-4321"})
	if code != http.StatusOK {
		t.Fatalf("显式新值 PUT = %d %q, want 200", code, raw)
	}
	cfgBytes, _ := os.ReadFile(cfgPath)
	if strings.Contains(string(cfgBytes), diskToken) {
		t.Fatal("显式新值覆盖后旧真钥仍在盘上")
	}
	assertKept("显式新值", "sk-rotate-new-4321")

	// ④ 空串：保留现值（清钥走删除整条目——spec）。
	code, raw = swPut(t, e, "notify", map[string]any{"enabled": true, "pushover_token": ""})
	if code != http.StatusOK {
		t.Fatalf("空串 PUT = %d %q, want 200", code, raw)
	}
	assertKept("空串", "sk-rotate-new-4321")

	// ⑤ 读面对象形占位 {masked,has_key}：按占位处理=保留现值。
	code, raw = swPut(t, e, "notify", map[string]any{"enabled": true,
		"pushover_token": map[string]any{"masked": "••••4321", "has_key": true}})
	if code != http.StatusOK {
		t.Fatalf("对象形占位 PUT = %d %q, want 200", code, raw)
	}
	assertKept("对象形占位", "sk-rotate-new-4321")

	// ⑥ 怪类型（数字）：密钥字段拒写。
	code, raw = swPut(t, e, "notify", map[string]any{"enabled": true, "pushover_token": 12345})
	if code != http.StatusBadRequest {
		t.Fatalf("怪类型 PUT = %d %q, want 400", code, raw)
	}
	assertKept("怪类型拒写", "sk-rotate-new-4321")
}

// TestSettingsWriteTuningNeedsRestartTrue 返工②：节级 PUT 零热应用（调参
// 热缝走 tuning apply 另路，不经本端点）——响应 needs_restart 一律 true，
// 不复用读面 effects 的操作级判定。
func TestSettingsWriteTuningNeedsRestartTrue(t *testing.T) {
	e, _ := newSettingsEnv(t)
	code, raw := swPut(t, e, "tuning", map[string]any{"mode": "recommend"})
	if code != http.StatusOK {
		t.Fatalf("PUT /settings/tuning = %d %q, want 200", code, raw)
	}
	var resp map[string]any
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatal(err)
	}
	if resp["saved"] != true || resp["needs_restart"] != true {
		t.Fatalf("响应 = %v, want saved=true needs_restart=true", resp)
	}
}

// TestSettingsWriteGuards 守门面：无/错 Bearer 401；未知节/票04 实体路径
// 404（auth 前，POST 面同序）；坏 JSON 400 且不落审计；替身 404。
func TestSettingsWriteGuards(t *testing.T) {
	e, _ := newSettingsEnv(t)
	b, _ := json.Marshal(map[string]any{"cc_mode": "enforce"})

	if c, _ := swReqNF(http.MethodPut, e.port, "", "/settings/gate", b); c != http.StatusUnauthorized {
		t.Fatalf("无 Bearer PUT = %d, want 401", c)
	}
	if c, _ := swReqNF(http.MethodPut, e.port, "wrong", "/settings/gate", b); c != http.StatusUnauthorized {
		t.Fatalf("错 Bearer PUT = %d, want 401", c)
	}
	// 未知节/实体路径（票04 面）404 在 auth 前（无 token 也 404 非 401）。
	for _, p := range []string{"/settings/unknown", "/settings/dock/upstreams/glm",
		"/settings/providers/glm", "/settings/prices/glm", "/settings/gate/deeper"} {
		if c, _ := swReqNF(http.MethodPut, e.port, "", p, b); c != http.StatusNotFound {
			t.Fatalf("PUT %s = %d, want 404（auth 前）", p, c)
		}
	}
	// 坏 JSON：400 且不落审计行（未成写尝试）。
	if c, _ := swReqNF(http.MethodPut, e.port, e.token, "/settings/gate", []byte("not-json")); c != http.StatusBadRequest {
		t.Fatalf("坏 JSON PUT = %d, want 400", c)
	}
	if lines := swAuditLines(t, e); len(lines) != 0 {
		t.Fatalf("坏 JSON 不应落审计行: %v", lines)
	}

	// 替身（非 *Daemon）：已知节+合法 Bearer 仍 404（读面同语义）。
	h := makeHandler(&stopDaemon{}, "tok-sw", nil, nil)
	req := httptest.NewRequest(http.MethodPut, "/settings/gate", strings.NewReader(`{"cc_mode":"enforce"}`))
	req.Header.Set("Authorization", "Bearer tok-sw")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("替身 PUT /settings/gate = %d %q, want 404", rec.Code, rec.Body.String())
	}
}

// TestSettingsWriteTOMLBodyRender 渲染器单元钉子：键序字典序、整数形优先
// （1500 不落 1500.0，与既有文件风格一致）、内联表/数组、字符串转义；
// null 拒渲染。
func TestSettingsWriteTOMLBodyRender(t *testing.T) {
	got, err := settingsSectionTOMLBody(map[string]any{
		"b":   true,
		"s":   `he said "hi"\` + "\n",
		"n":   1500,
		"f":   6.9,
		"arr": []any{"a", 1.5},
		"sub": map[string]any{"k": "v", "z": 2},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "arr = [\"a\", 1.5]\nb = true\nf = 6.9\nn = 1500\ns = \"he said \\\"hi\\\"\\\\\\n\"\nsub = { k = \"v\", z = 2 }\n"
	if got != want {
		t.Fatalf("渲染 = %q\nwant %q", got, want)
	}
	if _, err := settingsSectionTOMLBody(map[string]any{"x": nil}); err == nil {
		t.Fatal("null 值应拒渲染")
	}
}
