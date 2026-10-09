// Package config 的测试：规格 tests/test_config.py 9 例 1:1，另加票面验收
// 要求的补充例（各节覆盖 / lead 夹取与两类告警打印 / 全部校验分支文案逐字 /
// 路径优先级 / 坏 TOML / DataDir / ThresholdFor）。
package config

import (
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// capturePipe 换掉 *os.Stderr / *os.Stdout，返回读回已捕获输出的函数。
// 与 internal/accounts/accounts_test.go 同款。
func capturePipe(t *testing.T, target **os.File) (read func() string) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := *target
	*target = w
	done := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
		_ = r.Close()
	}()
	return func() string {
		_ = w.Close()
		*target = old
		return <-done
	}
}

// th 对应 Python 的 ThresholdCfg(summarize_s=..., block_s=...)——dataclass
// 未指定的字段保持默认值（min_ctx_tokens=20000, cache_warn_s=720）。
func th(s, b float64) ThresholdCfg {
	return ThresholdCfg{SummarizeS: s, BlockS: b, MinCtxTokens: 20000, CacheWarnS: 720}
}

// ---- 以下 9 例 = tests/test_config.py 1:1 ----

func TestDefaultsPass(t *testing.T) { // test_defaults_pass
	if err := Validate(Default(), false); err != nil {
		t.Fatalf("Validate(Default()) = %v, want nil", err)
	}
}

func TestFerryProviderDefaultsToUnconfigured(t *testing.T) { // test_ferry_provider_defaults_to_unconfigured
	// T39 去硬编码：默认未配置（曾默认 "local" 指向作者内网网关）
	if got := Default().FerryProvider; got != "" {
		t.Fatalf("FerryProvider = %q, want 空", got)
	}
}

func TestEqualThresholdsRejected(t *testing.T) { // test_equal_thresholds_rejected
	c := Default()
	c.Thresholds = th(100, 100)
	err := Validate(c, false)
	if err == nil || !strings.Contains(err.Error(), "严格小于") {
		t.Fatalf("err = %v, want 含 严格小于", err)
	}
}

func TestSummarizeAboveBlockRejected(t *testing.T) { // test_summarize_above_block_rejected
	c := Default()
	c.Thresholds = th(200, 100)
	if err := Validate(c, false); err == nil {
		t.Fatal("err = nil, want 非空")
	}
}

func TestGapBelow120sRejected(t *testing.T) { // test_gap_below_120s_rejected
	c := Default()
	c.Thresholds = th(60, 100)
	err := Validate(c, false)
	if err == nil || !strings.Contains(err.Error(), "120s") {
		t.Fatalf("err = %v, want 含 120s", err)
	}
	// 锁 %.0f 语义（Python {diff:.0f}）：40.0 → "40"
	if err == nil || !strings.Contains(err.Error(), "阈值差须 ≥120s（当前 40s）") {
		t.Fatalf("err = %v, want 含 阈值差须 ≥120s（当前 40s）", err)
	}
}

func TestRelaxMinGapOnlyRelaxesGap(t *testing.T) { // test_relax_min_gap_only_relaxes_gap
	c := Default()
	c.Thresholds = th(60, 100)
	if err := Validate(c, true); err != nil { // 差值放宽
		t.Fatalf("Validate(relax) = %v, want nil", err)
	}
	eq := Default()
	eq.Thresholds = th(100, 100)
	if err := Validate(eq, true); err == nil { // 相等仍拒
		t.Fatal("Validate(relax, 相等阈值) = nil, want 非空")
	}
}

func TestBadGateModeRejected(t *testing.T) { // test_bad_gate_mode_rejected
	c := Default()
	c.GateCC = "bogus"
	err := Validate(c, false)
	if err == nil || !strings.Contains(err.Error(), "cc_mode") {
		t.Fatalf("err = %v, want 含 cc_mode", err)
	}
	// 锁可选项渲染 = Python 元组 repr 逐字
	if err == nil || !strings.Contains(err.Error(),
		"gate.cc_mode 非法: bogus（可选 ('off', 'observe', 'enforce')）") {
		t.Fatalf("err = %v, want 文案逐字", err)
	}
	c2 := Default()
	c2.GateCodex = "always"
	err = Validate(c2, false)
	if err == nil || !strings.Contains(err.Error(), "codex_mode") {
		t.Fatalf("err = %v, want 含 codex_mode", err)
	}
}

func TestBadPollIntervalRejected(t *testing.T) { // test_bad_poll_interval_rejected
	c := Default()
	c.Watch = WatchCfg{PollIntervalS: 0, HarvestUsage: true} // 其余字段 = Python dataclass 默认
	err := Validate(c, false)
	if err == nil || !strings.Contains(err.Error(), "poll") {
		t.Fatalf("err = %v, want 含 poll", err)
	}
}

func TestEnvConfigPath(t *testing.T) { // test_env_config_path
	f := filepath.Join(t.TempDir(), "my.toml")
	if err := os.WriteFile(f, []byte("[server]\nport = 7399\n[thresholds]\nsummarize_s = 10\nblock_s = 200\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FERRYMAN_CONFIG", f)
	cfg, err := Load("", false)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Server.Port != 7399 {
		t.Fatalf("port = %d, want 7399", cfg.Server.Port)
	}
	if cfg.Thresholds.SummarizeS != 10 {
		t.Fatalf("summarize_s = %v, want 10", cfg.Thresholds.SummarizeS)
	}
}

// ---- 以下为票面验收补充例（lead 夹取/两类告警/全分支文案/各节覆盖等）----

func TestValidateClampsLeadWithWarnings(t *testing.T) {
	c := Default()
	c.QuestionWatch.Mode = "enforce"
	c.QuestionWatch.FerryDeadlineLeadS = 30
	readOut := capturePipe(t, &os.Stdout)
	err := Validate(c, false)
	out := readOut()
	if err != nil { // 30→夹取 60 后 60+1500=1560 ≤ 2100，合法
		t.Fatalf("Validate = %v, want nil", err)
	}
	// 告警文案逐字（config.py:182-184）：原值 %g、夹取值 %g
	want1 := "[config] ⚠ question_watch.ferry_deadline_lead_s 30s 低于下限，已夹取为 60s"
	if !strings.Contains(out, want1) {
		t.Fatalf("stdout = %q, want 含 %q", out, want1)
	}
	// 夹取后仍 ≤ 480 → 贴线告警跟着打（config.py:195-198）
	want2 := "[config] ⚠ question_watch.ferry_deadline_lead_s 60s 不大于摆渡墙钟 480s——死线余量不足，骨架可能贴线"
	if !strings.Contains(out, want2) {
		t.Fatalf("stdout = %q, want 含 %q", out, want2)
	}
	if c.QuestionWatch.FerryDeadlineLeadS != 60 { // 就地夹取副作用
		t.Fatalf("lead = %v, want 60", c.QuestionWatch.FerryDeadlineLeadS)
	}
}

func TestValidateSkipsLeadChecksWhenOff(t *testing.T) {
	c := Default()
	c.QuestionWatch.FerryDeadlineLeadS = 10 // 功能关闭时不校验也不夹取
	readOut := capturePipe(t, &os.Stdout)
	err := Validate(c, false)
	out := readOut()
	if err != nil {
		t.Fatalf("Validate = %v, want nil", err)
	}
	if out != "" {
		t.Fatalf("stdout = %q, want 空", out)
	}
	if c.QuestionWatch.FerryDeadlineLeadS != 10 {
		t.Fatalf("lead = %v, want 10（不夹取）", c.QuestionWatch.FerryDeadlineLeadS)
	}
}

func TestValidateLeadTooBigRejected(t *testing.T) {
	c := Default()
	c.QuestionWatch.Mode = "enforce"
	c.QuestionWatch.FerryDeadlineLeadS = 700
	readOut := capturePipe(t, &os.Stdout)
	err := Validate(c, false)
	out := readOut()
	want := "question_watch.ferry_deadline_lead_s 过大：summarize_s + lead（1500 + 700s）须 ≤ block_s（2100s）——摆渡死线必须赶在闸门拦截之前"
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("err = %v, want 含 %q", err, want)
	}
	if out != "" { // 过大分支为 if，贴线告警是 elif——不再打印
		t.Fatalf("stdout = %q, want 空", out)
	}
	if c.QuestionWatch.FerryDeadlineLeadS != 700 {
		t.Fatalf("lead = %v, want 700（过大不夹取）", c.QuestionWatch.FerryDeadlineLeadS)
	}
}

func TestValidateLeadAboveWallClockNoWarn(t *testing.T) {
	// lead=600：600+1500=2100 不大于 2100（不报错），且 600 > 480（不贴线告警）
	c := Default()
	c.QuestionWatch.Mode = "observe"
	c.QuestionWatch.FerryDeadlineLeadS = 600
	readOut := capturePipe(t, &os.Stdout)
	err := Validate(c, false)
	out := readOut()
	if err != nil {
		t.Fatalf("Validate = %v, want nil", err)
	}
	if out != "" {
		t.Fatalf("stdout = %q, want 空", out)
	}
	// 贴线边界：480.5 > 480 → 无告警；480 恰在贴线位（已知取舍）
	c2 := Default()
	c2.QuestionWatch.Mode = "observe"
	c2.QuestionWatch.FerryDeadlineLeadS = 480.5
	readOut2 := capturePipe(t, &os.Stdout)
	if err := Validate(c2, false); err != nil {
		t.Fatalf("Validate = %v, want nil", err)
	}
	if out2 := readOut2(); out2 != "" {
		t.Fatalf("stdout = %q, want 空", out2)
	}
}

func TestValidateBeatIntervalPositive(t *testing.T) {
	c := Default()
	c.QuestionWatch.BeatIntervalS = 0
	err := Validate(c, false)
	// 锁 %g 语义（Python {v:g}）：0.0 → "0"
	if err == nil || !strings.Contains(err.Error(), "question_watch.beat_interval_s 须 > 0（当前 0s）") {
		t.Fatalf("err = %v, want 含 beat_interval_s 文案", err)
	}
}

func TestValidateAllBranchesExactText(t *testing.T) {
	// 一配置踩满全部分支，锁 error 全文（含顺序）= Python ValueError 逐字
	c := Default()
	c.GateCC = "bogus"
	c.GateCodex = "always"
	c.Thresholds = th(100, 100)
	c.QuestionWatch.Mode = "weird"
	c.QuestionWatch.BeatIntervalS = 0
	c.Watch.PollIntervalS = 0
	want := `配置校验失败，拒绝启动：
  - gate.cc_mode 非法: bogus（可选 ('off', 'observe', 'enforce')）
  - gate.codex_mode 非法: always
  - question_watch.mode 非法: weird（可选 ('off', 'observe', 'enforce')）
  - question_watch.beat_interval_s 须 > 0（当前 0s）
  - question_watch.ferry_deadline_lead_s 过大：summarize_s + lead（100 + 480s）须 ≤ block_s（100s）——摆渡死线必须赶在闸门拦截之前
  - [cc] 总结阈值必须严格小于拦截阈值（当前 100.0s / 100.0s）
  - [codex] 总结阈值必须严格小于拦截阈值（当前 100.0s / 100.0s）
  - watch.poll_interval_s 须 > 0`
	err := Validate(c, false)
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v\nwant = %s", err, want)
	}
}

func TestLoadAllSectionsOverride(t *testing.T) {
	f := filepath.Join(t.TempDir(), "full.toml")
	tomlText := `
[gate]
cc_mode = "enforce"
codex_mode = "observe"

[thresholds]
summarize_s = 300
block_s = 1000
min_ctx_tokens = 12345
cache_warn_s = 300.5

[watch]
poll_interval_s = 7.5
cc_projects_dir = "C:/cc"
codex_sessions_dir = "C:/codex"
codex_extra_dirs = ["D:/x", "E:/y"]
dsh_sessions_dir = "C:/dsh-sessions"
harvest_usage = false

[server]
port = 7399
data_dir = "C:/data"

[notify]
enabled = true
pushover = false
pushover_token = "tok"
pushover_user = "usr"
toast = false

[heartbeat]
enabled = true
ttl_s = 610.5
ttl_measured_at = "2026-09-19T00:00:00"
ttl_source = "measured"

[question_watch]
mode = "observe"
min_questions = 3
beat_interval_s = 500.25
max_beats = 4
ferry_deadline_lead_s = 500

[ferry]
provider = "glm"
`
	if err := os.WriteFile(f, []byte(tomlText), 0o644); err != nil {
		t.Fatal(err)
	}
	readOut := capturePipe(t, &os.Stdout) // 该组合合法且不贴线，应无告警
	cfg, err := Load(f, false)
	out := readOut()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if out != "" {
		t.Fatalf("stdout = %q, want 空", out)
	}
	if cfg.GateCC != "enforce" || cfg.GateCodex != "observe" {
		t.Fatalf("gate = %q/%q", cfg.GateCC, cfg.GateCodex)
	}
	thw := cfg.Thresholds
	if thw.SummarizeS != 300 || thw.BlockS != 1000 || thw.MinCtxTokens != 12345 || thw.CacheWarnS != 300.5 {
		t.Fatalf("thresholds = %+v", thw)
	}
	w := cfg.Watch
	if w.PollIntervalS != 7.5 || w.CCProjectsDir != "C:/cc" || w.CodexSessionsDir != "C:/codex" ||
		len(w.CodexExtraDirs) != 2 || w.CodexExtraDirs[0] != "D:/x" || w.CodexExtraDirs[1] != "E:/y" ||
		w.DshSessionsDir != "C:/dsh-sessions" || w.HarvestUsage {
		t.Fatalf("watch = %+v", w)
	}
	if cfg.Server.Port != 7399 || cfg.Server.DataDir != "C:/data" {
		t.Fatalf("server = %+v", cfg.Server)
	}
	n := cfg.Notify
	if !n.Enabled || n.Pushover || n.PushoverToken != "tok" || n.PushoverUser != "usr" || n.Toast {
		t.Fatalf("notify = %+v", n)
	}
	hb := cfg.Heartbeat
	if !hb.Enabled || hb.TTLS != 610.5 || hb.TTLMeasuredAt != "2026-09-19T00:00:00" || hb.TTLSource != "measured" {
		t.Fatalf("heartbeat = %+v", hb)
	}
	q := cfg.QuestionWatch
	if q.Mode != "observe" || q.MinQuestions != 3 || q.BeatIntervalS != 500.25 || q.MaxBeats != 4 || q.FerryDeadlineLeadS != 500 {
		t.Fatalf("question_watch = %+v", q)
	}
	if cfg.FerryProvider != "glm" {
		t.Fatalf("ferry_provider = %q", cfg.FerryProvider)
	}
}

func TestLoadPartialSectionKeepsDefaults(t *testing.T) {
	// .get 语义：节内缺字段回落默认值，其余字段不串节
	f := filepath.Join(t.TempDir(), "partial.toml")
	if err := os.WriteFile(f, []byte(`
[gate]
cc_mode = "enforce"

[thresholds]
summarize_s = 100

[watch]
poll_interval_s = 10
`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(f, false)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.GateCC != "enforce" || cfg.GateCodex != "off" { // codex_mode 缺省回落
		t.Fatalf("gate = %q/%q", cfg.GateCC, cfg.GateCodex)
	}
	if cfg.Thresholds.SummarizeS != 100 || cfg.Thresholds.BlockS != 2100 ||
		cfg.Thresholds.MinCtxTokens != 20000 || cfg.Thresholds.CacheWarnS != 720 {
		t.Fatalf("thresholds = %+v", cfg.Thresholds)
	}
	if cfg.Watch.PollIntervalS != 10 || cfg.Watch.CCProjectsDir != "" || !cfg.Watch.HarvestUsage {
		t.Fatalf("watch = %+v", cfg.Watch)
	}
}

func TestLoadMissingFileReturnsDefaults(t *testing.T) {
	f := filepath.Join(t.TempDir(), "absent.toml")
	cfg, err := Load(f, false)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	d := Default()
	if !reflect.DeepEqual(cfg, d) {
		t.Fatalf("cfg = %+v, want 全默认 %+v", cfg, d)
	}
}

func TestLoadBadTomlFails(t *testing.T) {
	f := filepath.Join(t.TempDir(), "bad.toml")
	if err := os.WriteFile(f, []byte("not [valid"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(f, false); err == nil {
		t.Fatal("Load(bad toml) err = nil, want 非空")
	}
}

func TestLoadExplicitPathBeatsEnv(t *testing.T) {
	envFile := filepath.Join(t.TempDir(), "env.toml")
	if err := os.WriteFile(envFile, []byte("[server]\nport = 7401\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	explicit := filepath.Join(t.TempDir(), "explicit.toml")
	if err := os.WriteFile(explicit, []byte("[server]\nport = 7402\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FERRYMAN_CONFIG", envFile)
	cfg, err := Load(explicit, false)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Server.Port != 7402 {
		t.Fatalf("port = %d, want 7402（显式 > 环境变量）", cfg.Server.Port)
	}
}

func TestDataDir(t *testing.T) {
	c := Default()
	c.Server.DataDir = filepath.Join("X:", "data")
	if got := c.DataDir(); got != filepath.Join("X:", "data") {
		t.Fatalf("DataDir = %q, want 显式值", got)
	}
	c2 := Default()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("无 HOME：%v", err)
	}
	if got := c2.DataDir(); got != filepath.Join(home, "ferryman") {
		t.Fatalf("DataDir = %q, want %q", got, filepath.Join(home, "ferryman"))
	}
}

func TestThresholdForReturnsGlobal(t *testing.T) {
	c := Default()
	c.Thresholds = th(111, 222)
	if c.ThresholdFor("cc") != c.Thresholds || c.ThresholdFor("codex") != c.Thresholds {
		t.Fatalf("ThresholdFor 未返回全局阈值")
	}
}

func TestDefaultValuesVerbatim(t *testing.T) {
	// 默认值逐字（config.py dataclass 逐字段）
	d := Default()
	if d.GateCC != "observe" || d.GateCodex != "off" {
		t.Fatalf("gate 默认 = %q/%q", d.GateCC, d.GateCodex)
	}
	if d.Thresholds.SummarizeS != 1500 || d.Thresholds.BlockS != 2100 ||
		d.Thresholds.MinCtxTokens != 20000 || d.Thresholds.CacheWarnS != 720 {
		t.Fatalf("thresholds 默认 = %+v", d.Thresholds)
	}
	if d.Watch.PollIntervalS != 3.0 || d.Watch.CCProjectsDir != "" ||
		d.Watch.CodexSessionsDir != "" || len(d.Watch.CodexExtraDirs) != 0 ||
		d.Watch.DshSessionsDir != "" || !d.Watch.HarvestUsage {
		t.Fatalf("watch 默认 = %+v", d.Watch)
	}
	if d.Server.Port != 15700 || d.Server.DataDir != "" {
		t.Fatalf("server 默认 = %+v", d.Server)
	}
	if d.Notify.Enabled || !d.Notify.Pushover || d.Notify.PushoverToken != "" ||
		d.Notify.PushoverUser != "" || !d.Notify.Toast {
		t.Fatalf("notify 默认 = %+v", d.Notify)
	}
	if d.Heartbeat.Enabled || d.Heartbeat.TTLS != 0 || d.Heartbeat.TTLMeasuredAt != "" || d.Heartbeat.TTLSource != "" {
		t.Fatalf("heartbeat 默认 = %+v", d.Heartbeat)
	}
	q := d.QuestionWatch
	if q.Mode != "off" || q.MinQuestions != 5 || q.BeatIntervalS != 420 || q.MaxBeats != 2 || q.FerryDeadlineLeadS != 480 {
		t.Fatalf("question_watch 默认 = %+v", q)
	}
	if FerryWallTimeoutS != 480 || QwatchMinLeadS != 60 {
		t.Fatalf("常量 = %v/%v", FerryWallTimeoutS, QwatchMinLeadS)
	}
}

// ---- dsh 等答复窗配置（dsh-heartbeat 票03）：[question_watch] dsh_mode ----

func TestDshModeDefaultOff(t *testing.T) {
	// 缺省 off：未配置时 dsh 心跳零行为（验收底线——生产首启 observe 由
	// 运维者显式配置，规格「分档开关」）。
	d := Default()
	if d.QuestionWatch.DshMode != "off" {
		t.Fatalf("dsh_mode 默认 = %q, want off", d.QuestionWatch.DshMode)
	}
	if err := Validate(d, false); err != nil {
		t.Fatalf("默认配置应通过校验: %v", err)
	}
}

func TestDshModeTOMLOverride(t *testing.T) {
	// [question_watch] 同节新增键 dsh_mode；缺字段回落默认（节内 .get 语义）。
	dir := t.TempDir()
	p := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(p, []byte(`
[question_watch]
mode = "observe"
dsh_mode = "enforce"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(p, true)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.QuestionWatch.DshMode != "enforce" {
		t.Fatalf("dsh_mode = %q, want enforce", cfg.QuestionWatch.DshMode)
	}
	if cfg.QuestionWatch.Mode != "observe" {
		t.Fatalf("mode = %q（同节既有键不受影响）", cfg.QuestionWatch.Mode)
	}
	// 只配 mode：dsh_mode 回落默认 off。
	p2 := filepath.Join(dir, "c2.toml")
	if err := os.WriteFile(p2, []byte("[question_watch]\nmode = \"observe\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg2, err := Load(p2, true)
	if err != nil {
		t.Fatal(err)
	}
	if cfg2.QuestionWatch.DshMode != "off" {
		t.Fatalf("缺省 dsh_mode = %q, want off", cfg2.QuestionWatch.DshMode)
	}
}

func TestDshModeBadValueRejected(t *testing.T) {
	cfg := Default()
	cfg.QuestionWatch.DshMode = "loud"
	err := Validate(cfg, false)
	if err == nil || !strings.Contains(err.Error(), "question_watch.dsh_mode") {
		t.Fatalf("非法 dsh_mode 应拒启（文案含 question_watch.dsh_mode）, err = %v", err)
	}
}

func TestDshModeBeatSpanAssertion(t *testing.T) {
	// 参数相容断言（规格「参数相容断言」）：BeatIntervalS × MaxBeats < BlockS
	//——全部心跳跳完须赶在 block_s 到期关窗之前。只在 dsh_mode != off 时校验
	//（qw.Mode lead 的同款先例：存量小阈值配置零影响）。
	cfg := Default()
	cfg.Thresholds = ThresholdCfg{SummarizeS: 900, BlockS: 1000, MinCtxTokens: 100}
	cfg.QuestionWatch.DshMode = "observe"
	// 420×2=840 ≥ 1000？否——840 < 1000 过；改大间隔到 600：1200 ≥ 1000 拒。
	cfg.QuestionWatch.BeatIntervalS = 600
	err := Validate(cfg, true) // relaxMinGap：阈值差由既有分支管，本例只看心跳计划断言
	if err == nil || !strings.Contains(err.Error(), "max_beats") || !strings.Contains(err.Error(), "block_s") {
		t.Fatalf("心跳计划超 block_s 应拒启（文案含 max_beats 与 block_s）, err = %v", err)
	}
	// dsh_mode=off：同参数不校验（休眠键不拦启动）。
	cfg.QuestionWatch.DshMode = "off"
	if err := Validate(cfg, true); err != nil {
		t.Fatalf("dsh_mode=off 时不应校验心跳计划: %v", err)
	}
}

func TestDshProductionParamsSatisfyBeatSpan(t *testing.T) {
	// 生产参数钉死：420×2=840 < 2100（35min）成立——dsh_mode 拨 observe 即
	// 通过校验（上线票 D 的配置前提）。
	cfg := Default()
	cfg.QuestionWatch.DshMode = "observe"
	if cfg.QuestionWatch.BeatIntervalS*float64(cfg.QuestionWatch.MaxBeats) >= cfg.Thresholds.BlockS {
		t.Fatalf("生产参数断言不成立: %g×%d ≥ %g",
			cfg.QuestionWatch.BeatIntervalS, cfg.QuestionWatch.MaxBeats, cfg.Thresholds.BlockS)
	}
	if err := Validate(cfg, false); err != nil {
		t.Fatalf("生产参数 + dsh_mode=observe 应通过: %v", err)
	}
}

// TestGateDshModeKey gate.dsh_mode 独立键（dsh-gate-ux P3 前置）：缺省回落
// codex_mode（老配置零变化），显式设置独立生效（dsh 升档不连坐 codex），
// 非法值同款校验拒绝。
func TestGateDshModeKey(t *testing.T) {
	f := filepath.Join(t.TempDir(), "dshmode.toml")

	// ① 缺省＝未设置：只写 codex_mode → GateDsh 留空（闸门处决点回落
	// codex 道，行为等价旧版；回落路径由 daemon 侧旧 dsh 测试覆盖）
	if err := os.WriteFile(f, []byte("[gate]\ncodex_mode = \"observe\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(f, false)
	if err != nil {
		t.Fatalf("Load(缺省): %v", err)
	}
	if cfg.GateDsh != "" || cfg.GateCodex != "observe" {
		t.Fatalf("缺省 = %q/%q, want \"\"/observe", cfg.GateDsh, cfg.GateCodex)
	}

	// ② 显式覆盖：dsh 独立于 codex
	if err := os.WriteFile(f, []byte("[gate]\ncodex_mode = \"observe\"\ndsh_mode = \"enforce\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err = Load(f, false)
	if err != nil {
		t.Fatalf("Load(覆盖): %v", err)
	}
	if cfg.GateDsh != "enforce" || cfg.GateCodex != "observe" {
		t.Fatalf("独立档 = %q/%q, want enforce/observe", cfg.GateDsh, cfg.GateCodex)
	}

	// ③ 非法值：与 codex_mode 同款拒绝
	if err := os.WriteFile(f, []byte("[gate]\ndsh_mode = \"bogus\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err = Load(f, false); err == nil || !strings.Contains(err.Error(), "dsh_mode") {
		t.Fatalf("非法值 err = %v, want 含 dsh_mode", err)
	}
}

// ---- 通知分级（票01 通知分级，D2/D5）：[notify] events 内联表键 ----

func TestNotifyEventsDefaultTableVerbatim(t *testing.T) {
	// 缺省表逐字（D2）：block/tray_reply=toast，tuning=off，其余六键=both；
	// Default() 集成九键全量。
	d := Default()
	want := map[string]NotifyEventTier{
		"block":          NotifyEventToast,
		"chain_degrade":  NotifyEventBoth,
		"chain_skeleton": NotifyEventBoth,
		"breaker":        NotifyEventBoth,
		"upgrade":        NotifyEventBoth,
		"hard_cut":       NotifyEventBoth,
		"drift":          NotifyEventBoth,
		"tuning":         NotifyEventOff,
		"tray_reply":     NotifyEventToast,
	}
	if !reflect.DeepEqual(d.Notify.Events, want) {
		t.Fatalf("默认 events = %+v, want %+v", d.Notify.Events, want)
	}
	// 防御拷贝：改一份返回值不得污染另一份（票04 等值比对复用同一单源）。
	a := DefaultNotifyEvents()
	b := DefaultNotifyEvents()
	a["block"] = NotifyEventBoth
	if b["block"] != NotifyEventToast {
		t.Fatalf("DefaultNotifyEvents 未返回防御拷贝: b[block] = %q", b["block"])
	}
}

func TestNotifyEventsKeyOmittedAllDefaults(t *testing.T) {
	// events 整键省略 = 九键全缺省；与既有五键共存互不干扰。
	f := filepath.Join(t.TempDir(), "noev.toml")
	if err := os.WriteFile(f, []byte(`
[notify]
enabled = true
pushover = false
toast = false
`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(f, false)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !reflect.DeepEqual(cfg.Notify.Events, DefaultNotifyEvents()) {
		t.Fatalf("缺省 events = %+v, want 全缺省表", cfg.Notify.Events)
	}
	n := cfg.Notify
	if !n.Enabled || n.Pushover || n.Toast { // 五键原样解析
		t.Fatalf("notify = %+v", n)
	}
}

func TestNotifyEventsPartialOverride(t *testing.T) {
	// 显式键覆盖缺省，未配置键回落缺省；加载完成后恒为九键全量。
	f := filepath.Join(t.TempDir(), "partial.toml")
	if err := os.WriteFile(f, []byte(`
[notify]
enabled = true
events = { block = "both", tuning = "toast" }
`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(f, false)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	ev := cfg.Notify.Events
	if ev["block"] != NotifyEventBoth || ev["tuning"] != NotifyEventToast {
		t.Fatalf("显式键 = block %q / tuning %q, want both/toast", ev["block"], ev["tuning"])
	}
	d := DefaultNotifyEvents()
	for _, name := range NotifyEventNames {
		if name == "block" || name == "tuning" {
			continue
		}
		if ev[name] != d[name] {
			t.Fatalf("未配置键 %s = %q, want 回落缺省 %q", name, ev[name], d[name])
		}
	}
	if len(ev) != 9 {
		t.Fatalf("events 键数 = %d, want 9", len(ev))
	}
}

func TestNotifyEventsFullOverride(t *testing.T) {
	// 九键全显式（三值混合）→ 全部生效。
	f := filepath.Join(t.TempDir(), "full.toml")
	if err := os.WriteFile(f, []byte(`
[notify]
events = { block = "off", chain_degrade = "toast", chain_skeleton = "off", breaker = "toast", upgrade = "off", hard_cut = "toast", drift = "off", tuning = "both", tray_reply = "both" }
`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(f, false)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := map[string]NotifyEventTier{
		"block": NotifyEventOff, "chain_degrade": NotifyEventToast, "chain_skeleton": NotifyEventOff,
		"breaker": NotifyEventToast, "upgrade": NotifyEventOff, "hard_cut": NotifyEventToast,
		"drift": NotifyEventOff, "tuning": NotifyEventBoth, "tray_reply": NotifyEventBoth,
	}
	if !reflect.DeepEqual(cfg.Notify.Events, want) {
		t.Fatalf("events = %+v, want %+v", cfg.Notify.Events, want)
	}
}

func TestNotifyEventsBadValueRejected(t *testing.T) {
	// 非法值（非 off/toast/both）配置加载报错拒载，文案带键名与原值。
	f := filepath.Join(t.TempDir(), "badval.toml")
	if err := os.WriteFile(f, []byte(`
[notify]
events = { block = "loud", tuning = "off" }
`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Load(f, false)
	if err == nil || !strings.Contains(err.Error(), "notify.events.block") || !strings.Contains(err.Error(), "loud") {
		t.Fatalf("err = %v, want 含 notify.events.block 与 loud", err)
	}
}

func TestNotifyEventsUnknownEventRejected(t *testing.T) {
	// 未知事件名（D2 封闭集合，笔误防护）配置加载报错拒载。
	f := filepath.Join(t.TempDir(), "badkey.toml")
	if err := os.WriteFile(f, []byte(`
[notify]
events = { blocks = "toast" }
`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Load(f, false)
	if err == nil || !strings.Contains(err.Error(), "notify.events.blocks") {
		t.Fatalf("err = %v, want 含 notify.events.blocks", err)
	}
}

func TestNotifyEventsSubTableHeaderForm(t *testing.T) {
	// [notify.events] 子表头形态：TOML 数据模型上与内联表解码结果同构
	//（BurntSushi v1.6.0 实测 DeepEqual 相同、MetaData 相同），加载层无从
	// 区分亦不区分——本解析只认 notify 节内的 events 键，不存在也不得引入
	// 独立的 "notify.events 节" 解析路径（不误读 = 五键不被扰动 + 行为有
	// 定义）。子表头的禁用是写面纪律：设置视图对带子表节的整写保守拒，
	// 由 config.example 注记与设置视图写面（票04）执行。
	f := filepath.Join(t.TempDir(), "subtable.toml")
	if err := os.WriteFile(f, []byte(`
[notify]
enabled = true
pushover = false
[notify.events]
block = "both"
tuning = "toast"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(f, false)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.Notify.Enabled || cfg.Notify.Pushover || !cfg.Notify.Toast {
		t.Fatalf("notify 五键 = %+v, want enabled=true/pushover=false/toast=true（不被子表头扰动）", cfg.Notify)
	}
	ev := cfg.Notify.Events
	if ev["block"] != NotifyEventBoth || ev["tuning"] != NotifyEventToast || ev["upgrade"] != NotifyEventBoth {
		t.Fatalf("events = %+v, want 与内联表同结果", ev)
	}
}

func TestEconProviderParseAndFallback(t *testing.T) {
	// 独立票（统计卡顿）：[ferry] econ_provider 显式优先；缺省回落 provider。
	f := filepath.Join(t.TempDir(), "econ.toml")
	if err := os.WriteFile(f, []byte("[ferry]\nprovider = \"local\"\necon_provider = \"glm\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(f, true)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.FerryProvider != "local" || cfg.EconProvider != "glm" || cfg.EconKey() != "glm" {
		t.Fatalf("provider=%q econ=%q key=%q", cfg.FerryProvider, cfg.EconProvider, cfg.EconKey())
	}

	// 缺省：EconKey 回落 FerryProvider（原行为不变）。
	f2 := filepath.Join(t.TempDir(), "econ2.toml")
	if err := os.WriteFile(f2, []byte("[ferry]\nprovider = \"glm\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg2, err := Load(f2, true)
	if err != nil {
		t.Fatal(err)
	}
	if cfg2.EconProvider != "" || cfg2.EconKey() != "glm" {
		t.Fatalf("econ=%q key=%q（缺省应回落 provider）", cfg2.EconProvider, cfg2.EconKey())
	}
}
