package daemon

// settings_switch_test.go — 设置视图票06：POST /settings/dock/switch（复用既有
// provider_switch 热换绑＋票03 单写者锁＋票05 写前快照＋审计行）验收钉子。
//
// 验收对照（票面）：
//   - 切到已加载条目：内存换绑（dockState）＋盘上 active 落盘（重读往返）；
//     响应 {switched:true, needs_restart:false}；
//   - 切到启动后新增条目（盘上有、启动表没有）：409 + needs_restart:true +
//     文案含「重启」（票面二选一已定 409 形态，此处钉死）；
//   - codex 不可用条目按既有规则拒绝（CLI provider switch 默认拒语义）；
//   - 写前自动快照在位：seam reason="switch"（观察点）＋真实现落盘文件类
//     auto（可清单可滚动）；
//   - 审计行 section=dock.switch 落盘（saved/rejected+error）；
//   - 守门序同管理端点族：loopback → 方法 POST → Bearer；替身 404；钩子未装
//     404（落既有分派）；坏 JSON 400 且不落审计；
//   - 与节级 PUT 共用单写者锁（持锁期间 switch 排队）。
//
// 夹具：swSwitchEnv 自建 dock 双条目配置（[server].data_dir 钉沙箱——审计/
// 快照落 t.TempDir()），真 Daemon（newQueryEnv：冻结钟）＋真 switchTo 钩子的
// makeHandler 服务器；审计/快照读取复用票03 测试帮手（同包编译面）。

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ferryman/internal/config"
)

// ---- 夹具 ----

// swWriteDockCfg 票06 基础夹具：单条目 a（active=a）＋[server].data_dir 钉沙箱。
func swWriteDockCfg(t *testing.T, dataDir string) string {
	t.Helper()
	f := filepath.Join(t.TempDir(), "config.toml")
	src := `[server]
port = 15700
data_dir = "` + filepath.ToSlash(dataDir) + `"

[dock]
listen = "127.0.0.1:15923"
active = "a"

[dock.upstreams.a]
base_url = "https://a.example/api"
api_key = "sk-dock-a-1111"

[dock.upstreams.a.model_map]
default = "glm-a"
`
	if err := os.WriteFile(f, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	return f
}

// swAppendUpstream 向盘上 config 追加 [dock.upstreams.b] 块（extra 为块内追加
// 键行，落在 b 主键与 model_map 子表之间）——模拟票04 实体写：只落盘、内存
// 不重载（守护启动表仍是加载时快照）。
func swAppendUpstream(t *testing.T, path, extra string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	block := "\n[dock.upstreams.b]\nbase_url = \"https://b.example/api\"\napi_key = \"sk-dock-b-2222\"\n" +
		extra + "\n[dock.upstreams.b.model_map]\ndefault = \"glm-b\"\n"
	if _, err := f.WriteString(block); err != nil {
		t.Fatal(err)
	}
}

// swSwitchEnv 票06 测试环境：dock 夹具钉 FERRYMAN_CONFIG；真 Daemon（冻结钟、
// 沙箱 store/审计/快照目录）＋真 switchTo 钩子的 makeHandler 服务器。
// withB=true 时盘上先有 b（extra 追加进 b 块）。
func swSwitchEnv(t *testing.T, withB bool, bExtra string) (*queryEnv, *dockUpstreamState, string, *httptest.Server) {
	t.Helper()
	tmp := t.TempDir()
	dataDir := filepath.Join(tmp, "data")
	cfgPath := swWriteDockCfg(t, dataDir)
	if withB {
		swAppendUpstream(t, cfgPath, bExtra)
	}
	t.Setenv("FERRYMAN_CONFIG", cfgPath)
	cfg, err := config.Load(cfgPath, true)
	if err != nil {
		t.Fatalf("Load dock 夹具: %v", err)
	}
	e := newQueryEnv(t)
	e.d.Cfg = cfg
	state := newDockUpstreamState(cfg.Dock, cfgPath)
	ts := httptest.NewServer(makeHandler(e.d, e.token, nil, state.switchTo))
	t.Cleanup(ts.Close)
	return e, state, cfgPath, ts
}

// swSwitchPost 打真监听 server 的 switch 端点。
func swSwitchPost(t *testing.T, ts *httptest.Server, token, body string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/settings/dock/switch", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, raw
}

// ---- 验收钉子 ----

// TestSettingsSwitchLoadedEntryPersistsAndRebinds 基本面：切到已加载条目 b——
// 200 {switched,needs_restart:false,active}；内存即刻换绑；盘上 active 落盘
// （重读往返）；审计行 saved（section=dock.switch，before/after=active 换挡）；
// 写前快照真落盘且文件名可解析（类 auto＝可清单可滚动）。
func TestSettingsSwitchLoadedEntryPersistsAndRebinds(t *testing.T) {
	e, state, cfgPath, ts := swSwitchEnv(t, true, "")

	code, raw := swSwitchPost(t, ts, e.token, `{"name":"b"}`)
	if code != http.StatusOK {
		t.Fatalf("POST /settings/dock/switch = %d %q, want 200", code, raw)
	}
	var resp map[string]any
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("响应非 JSON: %v (%q)", err, raw)
	}
	if resp["switched"] != true || resp["needs_restart"] != false {
		t.Fatalf("响应 = %v, want switched=true needs_restart=false", resp)
	}
	if resp["active"] != "b" {
		t.Fatalf("active = %v, want b", resp["active"])
	}

	// 内存即刻换绑（新请求即刻新上游的既有语义）。
	if name, up := state.ActiveUpstream(); name != "b" || up.APIKey != "sk-dock-b-2222" {
		t.Fatalf("换绑后内存 = %q/%v, want b/sk-dock-b-2222", name, up)
	}
	// 盘上 active 落盘（重读往返）。
	cfg2, err := config.Load(cfgPath, false)
	if err != nil || cfg2.Dock.Active != "b" {
		t.Fatalf("盘上 active = %q err=%v, want b", cfg2.Dock.Active, err)
	}

	// 审计行：恰一行 saved，section=dock.switch，before/after=active 换挡。
	lines := swAuditLines(t, e)
	if len(lines) != 1 {
		t.Fatalf("审计行数 = %d, want 1", len(lines))
	}
	ln := lines[0]
	if ln["ts"] != e.t0 {
		t.Fatalf("审计 ts = %v, want 冻结钟 %v", ln["ts"], e.t0)
	}
	if ln["entry"] != "settings-ui" || ln["section"] != "dock.switch" || ln["outcome"] != "saved" {
		t.Fatalf("审计行头 = %v", ln)
	}
	if b, _ := ln["before"].(map[string]any); b == nil || b["active"] != "a" {
		t.Fatalf("审计 before = %v, want {active:a}", ln["before"])
	}
	if a, _ := ln["after"].(map[string]any); a == nil || a["active"] != "b" {
		t.Fatalf("审计 after = %v, want {active:b}", ln["after"])
	}

	// 写前快照真落盘：文件存在且类 auto（parseSnapshotName 过＝GET 清单可见、
	// 滚动可收——异类名孤儿在此即红）。
	snapDir := filepath.Join(e.d.Cfg.DataDir(), "backups", "config")
	ents, err := os.ReadDir(snapDir)
	if err != nil || len(ents) == 0 {
		t.Fatalf("写前快照未落盘: %v（dir=%s）", err, snapDir)
	}
	cls, _, ok := parseSnapshotName(ents[0].Name())
	if !ok || cls != "auto" {
		t.Fatalf("快照名 %q 不可解析或类非 auto", ents[0].Name())
	}
}

// TestSettingsSwitchAddedAfterStartupConflict409 切到启动后新增条目（盘上有、
// 启动表没有＝本会话经设置面新增、未重启）：409 + needs_restart:true + 文案含
// 「重启」（票面二选一的 409 形态钉死）；内存/盘均不动；审计 rejected。
func TestSettingsSwitchAddedAfterStartupConflict409(t *testing.T) {
	e, state, cfgPath, ts := swSwitchEnv(t, false, "")

	// 本会话经设置面新增（票04 实体写=只落盘，内存不重载）。
	swAppendUpstream(t, cfgPath, "")

	code, raw := swSwitchPost(t, ts, e.token, `{"name":"b"}`)
	if code != http.StatusConflict {
		t.Fatalf("POST = %d %q, want 409", code, raw)
	}
	var resp map[string]any
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("响应非 JSON: %v (%q)", err, raw)
	}
	if resp["needs_restart"] != true {
		t.Fatalf("响应 = %v, want needs_restart:true", resp)
	}
	if msg, _ := resp["error"].(string); !strings.Contains(msg, "重启") {
		t.Fatalf("错误文案须含「重启」: %q", msg)
	}

	// 内存与盘都没动。
	if name, _ := state.ActiveUpstream(); name != "a" {
		t.Fatalf("拒绝后内存 = %q, want a（不得换绑）", name)
	}
	cfg2, err := config.Load(cfgPath, false)
	if err != nil || cfg2.Dock.Active != "a" {
		t.Fatalf("拒绝后盘上 active = %q err=%v, want a", cfg2.Dock.Active, err)
	}

	// 审计行 rejected + error 点名重启。
	lines := swAuditLines(t, e)
	if len(lines) != 1 {
		t.Fatalf("审计行数 = %d, want 1", len(lines))
	}
	ln := lines[0]
	if ln["section"] != "dock.switch" || ln["outcome"] != "rejected" {
		t.Fatalf("审计行头 = %v", ln)
	}
	if errStr, _ := ln["error"].(string); !strings.Contains(errStr, "重启") {
		t.Fatalf("审计 error = %v, want 含 重启", ln["error"])
	}
}

// TestSettingsSwitchCodexUnavailableRejected codex 不可用条目按既有规则拒绝
// （CLI provider switch 默认拒语义）：400 报因（对 codex 不可用），内存/盘
// 不动，审计 rejected。
func TestSettingsSwitchCodexUnavailableRejected(t *testing.T) {
	e, state, cfgPath, ts := swSwitchEnv(t, true, `codex = "unsupported"`)

	code, raw := swSwitchPost(t, ts, e.token, `{"name":"b"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("POST = %d %q, want 400", code, raw)
	}
	if msg := string(raw); !strings.Contains(msg, "对 codex 不可用") || !strings.Contains(msg, "unsupported") {
		t.Fatalf("400 文案应点名 codex 不可用: %q", msg)
	}
	if name, _ := state.ActiveUpstream(); name != "a" {
		t.Fatalf("拒绝后内存 = %q, want a", name)
	}
	cfg2, err := config.Load(cfgPath, false)
	if err != nil || cfg2.Dock.Active != "a" {
		t.Fatalf("拒绝后盘上 active = %q err=%v, want a", cfg2.Dock.Active, err)
	}
	lines := swAuditLines(t, e)
	if len(lines) != 1 || lines[0]["outcome"] != "rejected" {
		t.Fatalf("审计行 = %v, want 1 行 rejected", lines)
	}
}

// TestSettingsSwitchUnknownEntry 未知条目：400 沿用钩子报因（列可用条目），
// 不动内存/盘，审计 rejected。
func TestSettingsSwitchUnknownEntry(t *testing.T) {
	e, state, cfgPath, ts := swSwitchEnv(t, true, "")

	code, raw := swSwitchPost(t, ts, e.token, `{"name":"nope"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("POST = %d %q, want 400", code, raw)
	}
	if msg := string(raw); !strings.Contains(msg, "nope") || !strings.Contains(msg, "上游表") {
		t.Fatalf("400 应沿钩子报因（含 nope/上游表）: %q", msg)
	}
	if name, _ := state.ActiveUpstream(); name != "a" {
		t.Fatalf("拒绝后内存 = %q, want a", name)
	}
	cfg2, err := config.Load(cfgPath, false)
	if err != nil || cfg2.Dock.Active != "a" {
		t.Fatalf("拒绝后盘上 active = %q err=%v, want a", cfg2.Dock.Active, err)
	}
	lines := swAuditLines(t, e)
	if len(lines) != 1 || lines[0]["outcome"] != "rejected" {
		t.Fatalf("审计行 = %v, want 1 行 rejected", lines)
	}
}

// TestSettingsSwitchSnapshotReasonSwitch 写前快照 seam（票05 接线点）：恰调用
// 一次，reason="switch"。
func TestSettingsSwitchSnapshotReasonSwitch(t *testing.T) {
	e, _, _, ts := swSwitchEnv(t, true, "")
	snap := swSwapSnapshot(t)

	if code, raw := swSwitchPost(t, ts, e.token, `{"name":"b"}`); code != http.StatusOK {
		t.Fatalf("POST = %d %q, want 200", code, raw)
	}
	calls := snap.get()
	if len(calls) != 1 || calls[0] != "switch" {
		t.Fatalf("快照桩调用 = %v, want 恰 1 次 reason=switch", calls)
	}
}

// TestSettingsSwitchGuards 守门面（管理端点族同序）：非 loopback 403；GET
// 405；错 token 401；坏 JSON 400 且不落审计；空名 400；替身（非 *Daemon）
// 404；钩子未装（渡口关）404 落既有分派。
func TestSettingsSwitchGuards(t *testing.T) {
	e, state, _, _ := swSwitchEnv(t, true, "")
	h := makeHandler(e.d, e.token, nil, state.switchTo)

	recPost := func(remote, token, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/settings/dock/switch", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		req.RemoteAddr = remote
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	if c := recPost("203.0.113.7:443", e.token, `{"name":"b"}`).Code; c != http.StatusForbidden {
		t.Fatalf("非 loopback = %d, want 403", c)
	}
	getRec := httptest.NewRecorder()
	getReq := httptest.NewRequest(http.MethodGet, "/settings/dock/switch", nil)
	getReq.Header.Set("Authorization", "Bearer "+e.token)
	getReq.RemoteAddr = "127.0.0.1:5555"
	h.ServeHTTP(getRec, getReq)
	if getRec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET = %d %q, want 405", getRec.Code, getRec.Body.String())
	}
	if c := recPost("127.0.0.1:5555", "WRONG", `{"name":"b"}`).Code; c != http.StatusUnauthorized {
		t.Fatalf("错 token = %d, want 401", c)
	}
	if c := recPost("127.0.0.1:5555", e.token, "not-json").Code; c != http.StatusBadRequest {
		t.Fatalf("坏 JSON = %d, want 400", c)
	}
	if c := recPost("127.0.0.1:5555", e.token, `{"name":"  "}`).Code; c != http.StatusBadRequest {
		t.Fatalf("空名 = %d, want 400", c)
	}
	if lines := swAuditLines(t, e); len(lines) != 0 {
		t.Fatalf("守门拒绝/坏请求不应落审计行: %v", lines)
	}

	// 替身（非 *Daemon）：过守门仍 404（设置写面同语义）。
	hSt := makeHandler(&stopDaemon{}, e.token, nil, state.switchTo)
	req := httptest.NewRequest(http.MethodPost, "/settings/dock/switch", strings.NewReader(`{"name":"b"}`))
	req.Header.Set("Authorization", "Bearer "+e.token)
	req.RemoteAddr = "127.0.0.1:5555"
	rec := httptest.NewRecorder()
	hSt.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("替身 = %d %q, want 404", rec.Code, rec.Body.String())
	}

	// 钩子未装（渡口未启用）：落既有分派＝未知路径 404（auth 前）。
	hNil := makeHandler(e.d, e.token, nil, nil)
	req2 := httptest.NewRequest(http.MethodPost, "/settings/dock/switch", strings.NewReader(`{"name":"b"}`))
	req2.RemoteAddr = "127.0.0.1:5555"
	rec2 := httptest.NewRecorder()
	hNil.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusNotFound {
		t.Fatalf("未装钩子 = %d %q, want 404", rec2.Code, rec2.Body.String())
	}
}

// TestSettingsSwitchQueuedBehindSettingsWriteMu 与节级 PUT 共用单写者锁：
// 白盒持锁期间 switch 排队不放行，放锁后完成。
func TestSettingsSwitchQueuedBehindSettingsWriteMu(t *testing.T) {
	e, _, _, ts := swSwitchEnv(t, true, "")

	settingsWriteMu.Lock()
	unlocked := false
	release := func() {
		if !unlocked {
			unlocked = true
			settingsWriteMu.Unlock()
		}
	}
	t.Cleanup(release) // 失败路径也放锁，不拖垮后续测试

	done := make(chan int, 1)
	go func() {
		c, _ := swSwitchPost(t, ts, e.token, `{"name":"b"}`)
		done <- c
	}()
	select {
	case c := <-done:
		t.Fatalf("持锁期间 switch 不应完成（状态码 %d）", c)
	case <-time.After(300 * time.Millisecond):
	}
	release()
	select {
	case c := <-done:
		if c != http.StatusOK {
			t.Fatalf("放锁后 switch = %d, want 200", c)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("放锁后 switch 未放行")
	}
}
