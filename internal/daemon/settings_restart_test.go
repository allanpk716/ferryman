package daemon

// settings_restart_test.go — 设置视图票07：POST /settings/restart 安全重启
// 端点验收钉子（F2 回滚源盖章 + F7 全程持锁 + 预检拒绝面）。
//
// 验收对照（票面五条之 1/3/4/5 + 盖章/快照滚动钉子）：
//   1. 预检失败拒重启并报因：a) 盘上 config 坏 TOML → 400 报因+审计 rejected；
//      b) server.port 指向测试预占端口 → 400 文案含「绑不上/占用」；
//      c) settingsRestartUpdateLockHeld 注桩回真 → 400 文案含「升级」；
//   3. 多写场景回归（F2 原案）：盖章好底（端口 A+旧值）→ PUT server.port=B
//      成功 → PUT gate 成功 → last-healthy.toml 仍是启动时内容 → POST restart
//      被预检拒（B 占用）；
//   4. restart 与写并发=串行+全程持锁（F7）：hold 缝进位后节级写 200ms 内不得
//      完成，release 后完成且回话正常；在飞写（快照桩拖 300ms）未排干前
//      restart 预检不得开始；
//   5. provider_switch 入锁（票07 随行小修）：持 settingsWriteMu 期间
//      POST /provider_switch 不得完成，放锁后完成；
//   6. 盖章单测：字节逐位相等；源缺失=报错；
//   8. 快照滚动不收 last-healthy：21 份 auto + last-healthy → takeSnapshot 触发
//      prune 后 last-healthy 仍在，且清单不含它。
//
// 夹具复用 newSettingsEnv（FERRYMAN_CONFIG 钉沙箱 + 冻结钟）＋票03/05/06 测试
// 帮手（swPut/swAuditLines/swReqNF，同包编译面）。restart 四缝
// （updateLockHeld/spawnHelper/hold 等）逐测试注桩、t.Cleanup 还原。

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
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

// srstOccupyPort 占住一个临时端口（保持监听到测试结束），返回端口号——
// 预检③「新端口绑不上」的占口夹具。
func srstOccupyPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	return ln.Addr().(*net.TCPAddr).Port
}

// srstPost 打真监听端口的 restart 端点（body 空串 = 无 body）。
func srstPost(t *testing.T, e *queryEnv, body string) (int, []byte) {
	t.Helper()
	var raw []byte
	if body != "" {
		raw = []byte(body)
	}
	return swReqNF(http.MethodPost, e.port, e.token, "/settings/restart", raw)
}

// srstSeams restart 缝注桩录制器（mutex 护读写——handler 在独立 goroutine）。
type srstSeams struct {
	mu         sync.Mutex
	spawnFrom  int
	spawnTo    int
	spawnCalls int
	holdCalled bool
}

func (s *srstSeams) spawn() (int, int, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.spawnFrom, s.spawnTo, s.spawnCalls
}

func (s *srstSeams) held() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.holdCalled
}

// srstStubSeams 注桩三缝（updateLockHeld/spawnHelper/hold），还原挂
// t.Cleanup。held=nil → 恒不持有（安全缺省）；spawn 桩记参数并成功；hold 桩
// 记调用并立即返回（测试世界 hold 不得挂死）。
func srstStubSeams(t *testing.T, held func(string, string, func(int) bool, func(int) (string, error)) (int, bool)) *srstSeams {
	t.Helper()
	oHeld, oSpawn := settingsRestartUpdateLockHeld, settingsRestartSpawnHelper
	if held == nil {
		held = func(string, string, func(int) bool, func(int) (string, error)) (int, bool) {
			return 0, false
		}
	}
	s := &srstSeams{}
	settingsRestartUpdateLockHeld = held
	settingsRestartSpawnHelper = func(from, to int) error {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.spawnCalls++
		s.spawnFrom, s.spawnTo = from, to
		return nil
	}
	settingsRestartHoldUntilExit = func() {
		s.mu.Lock()
		s.holdCalled = true
		s.mu.Unlock()
	}
	t.Cleanup(func() {
		settingsRestartUpdateLockHeld, settingsRestartSpawnHelper = oHeld, oSpawn
		// hold 不还原真缺省（select{} 永不返回）：迟到的泄漏请求若在恢复边界
		// 之后才走到终局（2026-10-09 全仓跑挂死实锚——某测试的在飞 restart
		// POST 跨过 t.Cleanup，读到恢复后的真 hold → 持全局 settingsWriteMu
		// 永不还 → 后续一切 settings 测试等锁到 10m 包超时），真 hold 会把
		// 锁绞死整包。测试世界 hold 恒为返回形（文件头「测试世界 hold 不得
		// 挂死」纪律的缺口收口）；真缺省行为由生产二进制承载，测试不钉它。
		settingsRestartHoldUntilExit = func() {}
	})
	return s
}

// srstLastHealthy 沙箱里 last-healthy.toml 的路径。
func srstLastHealthy(e *queryEnv) string {
	return filepath.Join(e.d.Cfg.DataDir(), "backups", "config", "last-healthy.toml")
}

// srstNewEnv restart 路径专用夹具（newSettingsEnv 同构，唯 [server].port 钉
// 25xxx 沙箱段——闪窗事故返工②：restart 测试不碰生产口 15700，缺口径一致；
// 预检③/帮手参数/审计 before 端口随夹具=25700）。
func srstNewEnv(t *testing.T) (*queryEnv, string) {
	t.Helper()
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "config.toml")
	fixture := strings.Replace(srFixtureTOML(filepath.Join(tmp, "data")),
		"port = 15700", "port = 25700", 1)
	if err := os.WriteFile(cfgPath, []byte(fixture), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FERRYMAN_CONFIG", cfgPath)
	cfg, err := config.Load(cfgPath, true)
	if err != nil {
		t.Fatalf("Load restart 夹具: %v", err)
	}
	e := newQueryEnv(t)
	e.d.Cfg = cfg
	return e, cfgPath
}

// srstRealSpawnHelper 包初始化时快照的真缺省拉起缝（先于任何测试的
// stub/restore——钉子测试用它而非当前值，别的测试漏还原也不陪葬）。
var srstRealSpawnHelper = settingsRestartSpawnHelper

// TestSettingsRestartSpawnHelperRefusesTestBinary 递归熔断钉子（闪窗事故
// 返工①主防线）：go test 进程里缺省拉起缝必须拒绝真拉起——否则测试二进制
// 会以「restart --from N --to M」位置参数自我再执行＝整套套件递归重跑。
func TestSettingsRestartSpawnHelperRefusesTestBinary(t *testing.T) {
	base := filepath.Base(os.Args[0])
	if !strings.HasSuffix(base, ".test") && !strings.HasSuffix(base, ".test.exe") {
		t.Skipf("非测试二进制形态（%s），熔断分支不可达，跳过", base)
	}
	if err := srstRealSpawnHelper(25700, 25700); err == nil {
		t.Fatal("测试二进制内缺省拉起缝应拒绝真拉起（递归熔断）")
	}
}

// ---- 验收钉子 ----

// TestSettingsRestartHappyPath 基本面：空 body 与 {} 皆收；缝全注桩下 200
// {restarting:true}（端口未变不附 new_port）；帮手以 (旧口,新口)=(15700,15700)
// 拉起；审计恰一行 outcome=restarting、section=restart、before/after={port:…}；
// hold 缝被调（编排走到终局）。
func TestSettingsRestartHappyPath(t *testing.T) {
	for _, body := range []string{"", "{}"} {
		e, _ := srstNewEnv(t)
		seams := srstStubSeams(t, nil)

		code, raw := srstPost(t, e, body)
		if code != http.StatusOK {
			t.Fatalf("body %q POST /settings/restart = %d %q, want 200", body, code, raw)
		}
		var resp map[string]any
		if err := json.Unmarshal(raw, &resp); err != nil {
			t.Fatalf("body %q 响应非 JSON: %v (%q)", body, err, raw)
		}
		if resp["restarting"] != true {
			t.Fatalf("body %q 响应 = %v, want restarting:true", body, resp)
		}
		if _, has := resp["new_port"]; has {
			t.Fatalf("body %q 端口未变不应附 new_port: %v", body, resp)
		}
		from, to, calls := seams.spawn()
		if calls != 1 || from != 25700 || to != 25700 {
			t.Fatalf("body %q 帮手拉起 = (%d,%d)×%d, want (25700,25700)×1", body, from, to, calls)
		}
		if !seams.held() {
			t.Fatalf("body %q hold 缝未被调（编排未走到终局）", body)
		}

		lines := swAuditLines(t, e)
		if len(lines) != 1 {
			t.Fatalf("body %q 审计行数 = %d, want 1", body, len(lines))
		}
		ln := lines[0]
		if ln["section"] != "restart" || ln["outcome"] != "restarting" {
			t.Fatalf("body %q 审计行头 = %v, want section=restart outcome=restarting", body, ln)
		}
		if b, _ := ln["before"].(map[string]any); b == nil || b["port"] != float64(25700) {
			t.Fatalf("body %q 审计 before = %v, want {port:25700}", body, ln["before"])
		}
		if a, _ := ln["after"].(map[string]any); a == nil || a["port"] != float64(25700) {
			t.Fatalf("body %q 审计 after = %v, want {port:25700}", body, ln["after"])
		}
	}
}

// TestSettingsRestartPortChangeResponse 端口改动形态：PUT server.port=B（自由
// 口）后 POST restart → 200 {restarting:true, new_port:B}；帮手以 (15700,B)
// 拉起；审计 before/after={port:15700→B}（换挡记录）。
func TestSettingsRestartPortChangeResponse(t *testing.T) {
	e, _ := srstNewEnv(t)
	seams := srstStubSeams(t, nil)

	b := freePort(t)
	dataDir := filepath.ToSlash(e.d.Cfg.DataDir())
	code, raw := swPut(t, e, "server", map[string]any{"port": b, "data_dir": dataDir})
	if code != http.StatusOK {
		t.Fatalf("PUT /settings/server = %d %q, want 200", code, raw)
	}

	code, raw = srstPost(t, e, "")
	if code != http.StatusOK {
		t.Fatalf("POST /settings/restart = %d %q, want 200", code, raw)
	}
	var resp map[string]any
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("响应非 JSON: %v (%q)", err, raw)
	}
	if resp["restarting"] != true || resp["new_port"] != float64(b) {
		t.Fatalf("响应 = %v, want restarting:true new_port:%d", resp, b)
	}
	if from, to, calls := seams.spawn(); calls != 1 || from != 25700 || to != b {
		t.Fatalf("帮手拉起 = (%d,%d)×%d, want (25700,%d)×1", from, to, calls, b)
	}
	lines := swAuditLines(t, e)
	// 两行：PUT server（saved）+ restart（restarting）——restart 行 before/after
	// 端口换挡。
	var restartLn map[string]any
	for _, ln := range lines {
		if ln["section"] == "restart" {
			restartLn = ln
		}
	}
	if restartLn == nil {
		t.Fatalf("审计行缺 restart: %v", lines)
	}
	if b4, _ := restartLn["before"].(map[string]any); b4 == nil || b4["port"] != float64(25700) {
		t.Fatalf("restart 审计 before = %v, want {port:25700}", restartLn["before"])
	}
	if af, _ := restartLn["after"].(map[string]any); af == nil || af["port"] != float64(b) {
		t.Fatalf("restart 审计 after = %v, want {port:%d}", restartLn["after"], b)
	}
}

// TestSettingsRestartPrecheckRejectsBadConfig 验收1a：盘上 config 坏 TOML →
// 400 报因 + 审计 rejected（error 落行）；帮手不拉起、hold 不进；config 字节
// 不被触碰。
func TestSettingsRestartPrecheckRejectsBadConfig(t *testing.T) {
	e, cfgPath := srstNewEnv(t)
	seams := srstStubSeams(t, nil)

	if err := os.WriteFile(cfgPath, []byte("= 这是 [坏 TOML\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, raw := srstPost(t, e, "")
	if code != http.StatusBadRequest {
		t.Fatalf("坏 TOML POST = %d %q, want 400", code, raw)
	}
	if msg := string(raw); !strings.Contains(msg, "拒重启") {
		t.Fatalf("400 文案应报因（含 拒重启）: %q", msg)
	}
	lines := swAuditLines(t, e)
	if len(lines) != 1 || lines[0]["section"] != "restart" || lines[0]["outcome"] != "rejected" {
		t.Fatalf("审计行 = %v, want 1 行 restart/rejected", lines)
	}
	if errStr, _ := lines[0]["error"].(string); errStr == "" {
		t.Fatalf("审计 error 缺席: %v", lines[0])
	}
	if _, _, calls := seams.spawn(); calls != 0 {
		t.Fatal("预检拒绝不得拉起帮手")
	}
	if seams.held() {
		t.Fatal("预检拒绝不得进 hold")
	}
}

// TestSettingsRestartPrecheckRejectsOccupiedPort 验收1b：盘上 server.port 指向
// 测试预占端口（Load 合法）→ 400 文案含「绑不上/占用」+ 审计 rejected；帮手
// 不拉起。
func TestSettingsRestartPrecheckRejectsOccupiedPort(t *testing.T) {
	e, _ := srstNewEnv(t)
	seams := srstStubSeams(t, nil)

	b := srstOccupyPort(t)
	dataDir := filepath.ToSlash(e.d.Cfg.DataDir())
	code, raw := swPut(t, e, "server", map[string]any{"port": b, "data_dir": dataDir})
	if code != http.StatusOK {
		t.Fatalf("PUT /settings/server = %d %q, want 200（占口不影响落盘）", code, raw)
	}

	code, raw = srstPost(t, e, "")
	if code != http.StatusBadRequest {
		t.Fatalf("占口 POST = %d %q, want 400", code, raw)
	}
	if msg := string(raw); !strings.Contains(msg, "绑不上") && !strings.Contains(msg, "占用") {
		t.Fatalf("400 文案应点名绑不上/占用: %q", msg)
	}
	if !strings.Contains(string(raw), fmt.Sprint(b)) {
		t.Fatalf("400 文案应含新端口号 %d: %q", b, raw)
	}
	lines := swAuditLines(t, e)
	var rejectLn map[string]any
	for _, ln := range lines {
		if ln["section"] == "restart" && ln["outcome"] == "rejected" {
			rejectLn = ln
		}
	}
	if rejectLn == nil {
		t.Fatalf("审计行缺 restart/rejected: %v", lines)
	}
	if _, _, calls := seams.spawn(); calls != 0 {
		t.Fatal("预检拒绝不得拉起帮手")
	}
	if seams.held() {
		t.Fatal("预检拒绝不得进 hold")
	}
}

// TestSettingsRestartPrecheckRejectsUpdateLock 验收1c：升级锁缝注桩回真 →
// 400 文案含「升级」+ 审计 rejected；预检序①先于配置读取（盘上仍是占口配置
// 也按升级拒，不落端口文案）。
func TestSettingsRestartPrecheckRejectsUpdateLock(t *testing.T) {
	e, _ := srstNewEnv(t)
	seams := srstStubSeams(t, func(string, string, func(int) bool, func(int) (string, error)) (int, bool) {
		return 4242, true
	})

	// 盘上留一个占口配置，证明预检①先于③（同占口下若序颠倒会报端口文案）。
	b := srstOccupyPort(t)
	dataDir := filepath.ToSlash(e.d.Cfg.DataDir())
	if code, raw := swPut(t, e, "server", map[string]any{"port": b, "data_dir": dataDir}); code != http.StatusOK {
		t.Fatalf("PUT /settings/server = %d %q, want 200", code, raw)
	}

	code, raw := srstPost(t, e, "")
	if code != http.StatusBadRequest {
		t.Fatalf("升级锁 POST = %d %q, want 400", code, raw)
	}
	if msg := string(raw); !strings.Contains(msg, "升级") {
		t.Fatalf("400 文案应含 升级: %q", msg)
	}
	if strings.Contains(string(raw), "绑不上") {
		t.Fatalf("预检①应先于③（报升级而非端口）: %q", raw)
	}
	lines := swAuditLines(t, e)
	found := false
	for _, ln := range lines {
		if ln["section"] == "restart" && ln["outcome"] == "rejected" {
			found = true
		}
	}
	if !found {
		t.Fatalf("审计行缺 restart/rejected: %v", lines)
	}
	if _, _, calls := seams.spawn(); calls != 0 {
		t.Fatal("预检拒绝不得拉起帮手")
	}
}

// TestSettingsRestartLastHealthyStableAcrossWrites 验收3（F2 原案多写回归）：
// 盖章好底（端口 A+旧值）→ PUT server.port=B（预占口，Load 合法落盘）成功 →
// PUT gate 新值成功 → last-healthy.toml 仍是启动时内容（端口 A+旧值，未被写
// 路径触碰）→ POST restart 被预检拒（B 占用）。与验收2 合起来覆盖「坏端口写
// →其他节写→restart 失败→回滚到健康配置后成功拉起」。
func TestSettingsRestartLastHealthyStableAcrossWrites(t *testing.T) {
	e, cfgPath := srstNewEnv(t)
	dataDir := e.d.Cfg.DataDir()

	// 盖章好底＝启动成功点的配置原字节。
	if err := stampLastHealthyConfig(cfgPath, dataDir); err != nil {
		t.Fatalf("盖章: %v", err)
	}
	base, err := os.ReadFile(srstLastHealthy(e))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(base), "port = 25700") || !strings.Contains(string(base), `cc_mode = "observe"`) {
		t.Fatalf("盖章底应含端口 A 与旧 gate 值: %q", base)
	}

	// 两笔写：端口换 B（预占口）＋ gate 新值——都成功。
	b := srstOccupyPort(t)
	code, raw := swPut(t, e, "server", map[string]any{"port": b, "data_dir": filepath.ToSlash(dataDir)})
	if code != http.StatusOK {
		t.Fatalf("PUT /settings/server = %d %q, want 200", code, raw)
	}
	code, raw = swPut(t, e, "gate", map[string]any{"cc_mode": "enforce", "codex_mode": "off", "dsh_mode": ""})
	if code != http.StatusOK {
		t.Fatalf("PUT /settings/gate = %d %q, want 200", code, raw)
	}

	// last-healthy 逐字节仍是启动时内容（写路径只进快照滚动，不碰盖章）。
	now, err := os.ReadFile(srstLastHealthy(e))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(base, now) {
		t.Fatal("last-healthy.toml 被写路径触碰（F2 回滚源必须钉在启动态）")
	}

	// POST restart 被预检③拒（B 占用）。
	srstStubSeams(t, nil)
	code, raw = srstPost(t, e, "")
	if code != http.StatusBadRequest {
		t.Fatalf("POST /settings/restart = %d %q, want 400（B 占用）", code, raw)
	}
	if msg := string(raw); !strings.Contains(msg, "绑不上") && !strings.Contains(msg, "占用") {
		t.Fatalf("400 文案应点名绑不上/占用: %q", msg)
	}
}

// TestSettingsRestartPrecheckRejectsDataDirChange 预检④（评审高·返工）：
// PUT server.data_dir 改成别的目录（Load 合法落盘、端口不动）→ POST restart
// 400，文案点名「数据目录…暂不支持随安全重启生效/迁移」；帮手不拉起、hold
// 不进；审计 rejected。
func TestSettingsRestartPrecheckRejectsDataDirChange(t *testing.T) {
	e, _ := srstNewEnv(t)
	seams := srstStubSeams(t, nil)

	other := filepath.ToSlash(filepath.Join(t.TempDir(), "other-data"))
	code, raw := swPut(t, e, "server", map[string]any{"port": 25700, "data_dir": other})
	if code != http.StatusOK {
		t.Fatalf("PUT /settings/server = %d %q, want 200（data_dir 改动落盘合法）", code, raw)
	}

	code, raw = srstPost(t, e, "")
	if code != http.StatusBadRequest {
		t.Fatalf("data_dir 改动 POST = %d %q, want 400", code, raw)
	}
	msg := string(raw)
	if !strings.Contains(msg, "数据目录") ||
		!strings.Contains(msg, "暂不支持随安全重启生效") ||
		!strings.Contains(msg, "迁移") {
		t.Fatalf("400 文案应点名数据目录迁移暂不支持: %q", msg)
	}
	lines := swAuditLines(t, e)
	found := false
	for _, ln := range lines {
		if ln["section"] == "restart" && ln["outcome"] == "rejected" {
			found = true
			if errStr, _ := ln["error"].(string); !strings.Contains(errStr, "数据目录") {
				t.Fatalf("审计 error 应含 数据目录: %v", ln["error"])
			}
		}
	}
	if !found {
		t.Fatalf("审计行缺 restart/rejected: %v", lines)
	}
	if _, _, calls := seams.spawn(); calls != 0 {
		t.Fatal("预检拒绝不得拉起帮手")
	}
	if seams.held() {
		t.Fatal("预检拒绝不得进 hold")
	}
}

// TestSettingsRestartSerializesWithWritesAndHoldsLock 验收4（F7 主面）：hold 缝
// 置位后（=响应已出网、编排持锁进终局），节级写 200ms 内不得完成；release 后
// 完成且回话正常（200）。
func TestSettingsRestartSerializesWithWritesAndHoldsLock(t *testing.T) {
	e, _ := srstNewEnv(t)
	srstStubSeams(t, nil)
	startedCh := make(chan struct{})
	releaseCh := make(chan struct{})
	oHold := settingsRestartHoldUntilExit
	settingsRestartHoldUntilExit = func() {
		close(startedCh)
		<-releaseCh
	}
	t.Cleanup(func() { settingsRestartHoldUntilExit = oHold })

	respCh := make(chan int, 1)
	releaseOnce := sync.Once{}
	releaseHold := func() { releaseOnce.Do(func() { close(releaseCh) }) }
	t.Cleanup(releaseHold) // 失败路径也放行 hold，不漏 goroutine 卡死测试进程
	go func() {
		c, raw := srstPost(t, e, "")
		if c == http.StatusOK && !strings.Contains(string(raw), `"restarting":true`) {
			c = -1 // 200 但体不对，视作失败码传回
		}
		respCh <- c
	}()
	select {
	case <-startedCh: // hold 已进入=编排已持锁进终局
	case <-time.After(5 * time.Second):
		t.Fatal("restart 编排未进入 hold")
	}
	// 响应已到客户端（writeJSON+Flush 先于 hold）。
	select {
	case c := <-respCh:
		if c != http.StatusOK {
			t.Fatalf("restart 回话 = %d, want 200", c)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("hold 期间响应未出网（须 Flush）")
	}

	// 持锁期间节级写不得完成。
	putDone := make(chan int, 1)
	go func() {
		resp, err := e.d.settingsPutSection("gate", map[string]any{"cc_mode": "enforce", "codex_mode": "off", "dsh_mode": ""})
		if err != nil || resp["saved"] != true {
			putDone <- 0
			return
		}
		putDone <- 200
	}()
	select {
	case c := <-putDone:
		t.Fatalf("持锁期间写不应完成 = %d", c)
	case <-time.After(200 * time.Millisecond):
	}
	releaseHold()
	select {
	case c := <-putDone:
		if c != 200 {
			t.Fatalf("release 后写未成功 = %d", c)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("release 后写未放行")
	}
}

// TestSettingsRestartWaitsForInFlightWrite 验收4（F7 排干面）：快照缝注桩睡眠
// 300ms 拖住在飞写的临界区；restart 的预检①（观测缝）须等写完成（锁释放后）
// 才开始——无锁实现会在写临界区内抢跑预检，此刻早于 250ms 即红。
func TestSettingsRestartWaitsForInFlightWrite(t *testing.T) {
	e, _ := srstNewEnv(t)

	var snapMu sync.Mutex
	var snapStart time.Time
	oSnap := snapshotBeforeWrite
	snapshotBeforeWrite = func(string) error {
		snapMu.Lock()
		snapStart = time.Now()
		snapMu.Unlock()
		time.Sleep(300 * time.Millisecond)
		return nil
	}
	t.Cleanup(func() { snapshotBeforeWrite = oSnap })

	precheckAt := make(chan time.Time, 1)
	srstStubSeams(t, func(string, string, func(int) bool, func(int) (string, error)) (int, bool) {
		precheckAt <- time.Now()
		return 0, false
	})

	go func() {
		_, _ = e.d.settingsPutSection("gate", map[string]any{"cc_mode": "enforce", "codex_mode": "off", "dsh_mode": ""})
	}()
	time.Sleep(50 * time.Millisecond) // 让写先进临界区
	go func() {
		_, _ = srstPost(t, e, "")
	}()

	select {
	case pa := <-precheckAt:
		snapMu.Lock()
		ss := snapStart
		snapMu.Unlock()
		if pa.Before(ss.Add(250 * time.Millisecond)) {
			t.Fatalf("restart 预检未等在飞写排干: 预检 %v, 写临界区始 %v", pa, ss)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("restart 预检未开始")
	}
}

// TestProviderSwitchTakesSettingsWriteMu 验收5（票07 随行小修）：白盒持
// settingsWriteMu 期间经 makeHandler 打 POST /provider_switch 不得完成；释放后
// 完成（200）。锁内调 onSwitch（switchTo 只嵌套其自有 h.mu，无死锁环）。
func TestProviderSwitchTakesSettingsWriteMu(t *testing.T) {
	e, _ := newSettingsEnv(t)
	onSwitch := func(name string) (map[string]any, error) {
		return map[string]any{"ok": true, "active": name}, nil
	}
	h := makeHandler(e.d, e.token, nil, onSwitch)

	settingsWriteMu.Lock()
	unlocked := false
	release := func() {
		if !unlocked {
			unlocked = true
			settingsWriteMu.Unlock()
		}
	}
	t.Cleanup(release)

	done := make(chan int, 1)
	go func() {
		req := httptest.NewRequest(http.MethodPost, "/provider_switch", strings.NewReader(`{"name":"b"}`))
		req.Header.Set("Authorization", "Bearer "+e.token)
		req.RemoteAddr = "127.0.0.1:5555"
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		done <- rec.Code
	}()
	select {
	case c := <-done:
		t.Fatalf("持锁期间 provider_switch 不应完成 = %d", c)
	case <-time.After(300 * time.Millisecond):
	}
	release()
	select {
	case c := <-done:
		if c != http.StatusOK {
			t.Fatalf("放锁后 provider_switch = %d, want 200", c)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("放锁后 provider_switch 未放行")
	}
}

// TestStampLastHealthyConfig 验收6：盖章字节逐位相等；源缺失=报错；目录递归
// 建（backups/config 不在场时）。
func TestStampLastHealthyConfig(t *testing.T) {
	tmp := t.TempDir()
	src := filepath.Join(tmp, "config.toml")
	content := []byte("[server]\nport = 15700\n# 中文与转义 \"引号\"\n")
	if err := os.WriteFile(src, content, 0o600); err != nil {
		t.Fatal(err)
	}
	dataDir := filepath.Join(tmp, "data", "nested") // backups/config 递归建
	if err := stampLastHealthyConfig(src, dataDir); err != nil {
		t.Fatalf("盖章: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dataDir, "backups", "config", "last-healthy.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, content) {
		t.Fatalf("盖章字节不等: got %q want %q", got, content)
	}
	if err := stampLastHealthyConfig(filepath.Join(tmp, "nope.toml"), dataDir); err == nil {
		t.Fatal("源缺失应报错")
	}
}

// TestSnapshotPruneKeepsLastHealthy 验收8：目录里放 last-healthy.toml + 21 份
// config-*auto 快照 → takeSnapshot 触发 prune 后 last-healthy 仍在；快照清单
// 不含它（异类名 parseSnapshotName 不认）。
func TestSnapshotPruneKeepsLastHealthy(t *testing.T) {
	e, _ := newSettingsEnv(t)
	snapDir := filepath.Join(e.d.Cfg.DataDir(), "backups", "config")
	if err := os.MkdirAll(snapDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(snapDir, "last-healthy.toml"), []byte("[server]\nport = 15700\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 21; i++ {
		ts := base.Add(time.Duration(i) * time.Second)
		p := filepath.Join(snapDir, "config-"+ts.UTC().Format("20060102-150405")+"-auto.toml")
		if err := os.WriteFile(p, []byte("# snap"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, ts, ts); err != nil {
			t.Fatal(err)
		}
	}

	// takeSnapshot（锁内单线程直调——单写者测试形态，settings_snapshot_test
	// 同惯例）落第 22 份并触发 prune（>20）。
	if _, err := takeSnapshot("manual"); err != nil {
		t.Fatalf("takeSnapshot: %v", err)
	}

	if _, err := os.Stat(filepath.Join(snapDir, "last-healthy.toml")); err != nil {
		t.Fatalf("prune 收走了 last-healthy.toml: %v", err)
	}
	snaps := 0
	ents, err := os.ReadDir(snapDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, en := range ents {
		if strings.HasPrefix(en.Name(), "config-") {
			snaps++
		}
	}
	if snaps > 20 {
		t.Fatalf("滚动后快照数 = %d, want ≤20", snaps)
	}
	for _, m := range listSnapshots(snapDir) {
		if m["id"] == "last-healthy.toml" {
			t.Fatal("快照清单不得含 last-healthy.toml")
		}
	}
}

// TestSettingsRestartGuards 守门面（管理端点族同序）：非 loopback 403；GET
// 405（带 Allow）；错 token 401；替身（非 *Daemon）404；非对象 body 400。
func TestSettingsRestartGuards(t *testing.T) {
	e, _ := srstNewEnv(t)
	srstStubSeams(t, nil)
	h := makeHandler(e.d, e.token, nil, nil)

	rec := func(method, remote, token, body string) *httptest.ResponseRecorder {
		var rd *strings.Reader
		if body == "" {
			rd = strings.NewReader("")
		} else {
			rd = strings.NewReader(body)
		}
		req := httptest.NewRequest(method, "/settings/restart", rd)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		req.RemoteAddr = remote
		r := httptest.NewRecorder()
		h.ServeHTTP(r, req)
		return r
	}

	if c := rec(http.MethodPost, "203.0.113.7:443", e.token, "").Code; c != http.StatusForbidden {
		t.Fatalf("非 loopback = %d, want 403", c)
	}
	g := rec(http.MethodGet, "127.0.0.1:5555", e.token, "")
	if g.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET = %d, want 405", g.Code)
	}
	if g.Header().Get("Allow") != http.MethodPost {
		t.Fatalf("405 应带 Allow: %v", g.Header())
	}
	if c := rec(http.MethodPost, "127.0.0.1:5555", "WRONG", "").Code; c != http.StatusUnauthorized {
		t.Fatalf("错 token = %d, want 401", c)
	}
	if c := rec(http.MethodPost, "127.0.0.1:5555", e.token, `[1,2]`).Code; c != http.StatusBadRequest {
		t.Fatalf("非对象 body = %d, want 400", c)
	}
	if lines := swAuditLines(t, e); len(lines) != 0 {
		t.Fatalf("守门拒绝不应落审计行: %v", lines)
	}

	// 替身（非 *Daemon）：过守门仍 404。
	hSt := makeHandler(&stopDaemon{}, e.token, nil, nil)
	req := httptest.NewRequest(http.MethodPost, "/settings/restart", strings.NewReader(""))
	req.Header.Set("Authorization", "Bearer "+e.token)
	req.RemoteAddr = "127.0.0.1:5555"
	rSt := httptest.NewRecorder()
	hSt.ServeHTTP(rSt, req)
	if rSt.Code != http.StatusNotFound {
		t.Fatalf("替身 = %d %q, want 404", rSt.Code, rSt.Body.String())
	}
}
