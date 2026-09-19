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
		w.HarvestUsage {
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
		d.Watch.CodexSessionsDir != "" || len(d.Watch.CodexExtraDirs) != 0 || !d.Watch.HarvestUsage {
		t.Fatalf("watch 默认 = %+v", d.Watch)
	}
	if d.Server.Port != 7311 || d.Server.DataDir != "" {
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
