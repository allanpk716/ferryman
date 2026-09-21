package main

// backtest_test.go — 票04 验收钉子：`ferryman backtest` 子命令端到端（进程内
// 驱动 cmdBacktest，stdout/stderr 注入，零子进程零外呼）。
//
// 夹具纪律：临时 data_dir（accounts 包写路径造 window/usage JSONL，与生产行
// 同形）+ 临时 config.toml（--config / FERRYMAN_CONFIG 指入）——绝不读真实
// ~/ferryman（真实账本首跑是票面手工步骤，不进单测）。
// 断言数字全部来自夹具设计值，无任何规格文本时点快照。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ferryman/internal/accounts"
	"ferryman/internal/backtest"
)

// btBase = 2026-09-21 00:00 UTC（落 202609.jsonl；晚于夹具价格表生效日）。
const btBase = 1789948800.0

// btWin 夹具窗设计值（session/project/dur_s/prefix_tokens）。
type btWin struct {
	session string
	project string
	dur     float64
	prefix  int
}

// btMakeData 临时账本目录：逐窗写 kind=window 行（生产同形）。
func btMakeData(t *testing.T, wins []btWin) string {
	t.Helper()
	dir := t.TempDir()
	acc, err := accounts.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	for i, w := range wins {
		ts := btBase + float64(i)*600
		if _, err := acc.Record("window", ts, accounts.Fields{
			"agent": "cc", "session_id": w.session, "lineage_id": "L-" + w.session,
			"project": w.project, "opened_ts": ts - w.dur, "closed_ts": ts,
			"dur_s": w.dur, "prefix_tokens": w.prefix, "close_reason": "subagents_done",
		}); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// btMakeConfig 临时 config.toml：data_dir 指账本 + [heartbeat] ttl_s（ttlS<=0
// 缺省不写节 = 未配置语义）+ [prices.econ]（pCache=false = 版本缺 P_cache）。
func btMakeConfig(t *testing.T, dataDir string, ttlS float64, pCache bool) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	var b strings.Builder
	b.WriteString("[server]\ndata_dir = '" + dataDir + "'\n\n")
	if ttlS > 0 {
		b.WriteString("[heartbeat]\nttl_s = 600\n\n")
	}
	b.WriteString("[prices.econ]\nunit = 'u'\nper = 10000\n" +
		"[[prices.econ.versions]]\neffective_from = '2026-01-01'\np_in = 1.0\n")
	if pCache {
		b.WriteString("p_cache = 0.5\n")
	}
	b.WriteString("p_out = 2.0\n")
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// btRun 进程内跑一轮 backtest，回 (退出码, stdout, stderr)。缺 --out 时自动
// 补临时目录落点——测试绝不依赖 cwd 相对缺省路径（不在源码树撒报告文件）。
func btRun(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	hasOut := false
	for _, a := range args {
		if a == "--out" {
			hasOut = true
			break
		}
	}
	if !hasOut {
		args = append(args, "--out", filepath.Join(t.TempDir(), "report.md"))
	}
	var out, errBuf strings.Builder
	code := cmdBacktest(args, &out, &errBuf)
	return code, out.String(), errBuf.String()
}

// btDecodeJSON --json stdout → SweepView（复用包类型，键名漂移即编译失败）。
func btDecodeJSON(t *testing.T, raw string) backtest.SweepView {
	t.Helper()
	var v backtest.SweepView
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		t.Fatalf("--json 输出非合法 JSON: %v\n%s", err, raw)
	}
	return v
}

// ---- 验收 1：临时 data_dir 夹具端到端（--json 退出码 0 + 报告落 --out + 计数） ----

func TestBacktestE2E(t *testing.T) {
	dataDir := btMakeData(t, []btWin{
		{"s1", "C:/work/alpha", 600, 100000},
		{"s2", "C:/work/beta", 1800, 80000},
	})
	cfgPath := btMakeConfig(t, dataDir, 600, true)
	outPath := filepath.Join(t.TempDir(), "rep", "report.md") // 父目录不存在也要能落

	code, stdout, stderr := btRun(t, "--config", cfgPath, "--out", outPath, "--json")
	if code != 0 {
		t.Fatalf("退出码 = %d, want 0\nstdout=%s\nstderr=%s", code, stdout, stderr)
	}
	v := btDecodeJSON(t, stdout)
	if v.Counts.TotalWindows != 2 || v.Counts.ReplayWindows != 2 {
		t.Fatalf("计数 = total %d / replay %d, want 2/2", v.Counts.TotalWindows, v.Counts.ReplayWindows)
	}
	if v.Counts.AfterFilterWindows != 2 {
		t.Fatalf("AfterFilterWindows = %d, want 2（默认全量）", v.Counts.AfterFilterWindows)
	}
	if v.LoadedAtISO == "" {
		t.Fatal("--json 缺装载时点戳 loaded_at_iso")
	}
	if v.Gap.Recommend == "" || strings.Contains(v.Gap.Recommend, "无法给出") {
		t.Fatalf("Recommend = %q, want 行动出口文案", v.Gap.Recommend)
	}
	// 档数：引擎场景键与报告规范档名同值单源（grid.go 常量别名 report.go
	// ScenarioName*，78e8c50 首跑暴露的接缝瑕疵经协调者修复）——恰规范三档，
	// 三档全带数据，无占位行。
	if len(v.Tiers) != 3 {
		t.Fatalf("Tiers 档数 = %d, want 3（规范三档，键名单源后无额外档）", len(v.Tiers))
	}
	withData := 0
	for _, tv := range v.Tiers {
		if tv.Best != nil || tv.Baseline != nil {
			withData++
		}
	}
	if withData != 3 {
		t.Fatalf("带数据档数 = %d, want 3", withData)
	}

	b, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("markdown 报告未落 --out: %v", err)
	}
	md := string(b)
	if !strings.Contains(md, "# 等待窗扫参实验报告") || !strings.Contains(md, "装载时点戳") {
		t.Fatalf("报告缺标题/时点戳节：\n%s", md[:min(400, len(md))])
	}
	if !strings.Contains(md, "| 全部窗（过滤前） | 2 |") {
		t.Fatalf("报告计数与夹具不符：\n%s", md[:min(600, len(md))])
	}
}

// ---- 验收 2：--projects / --exclude 生效（ft-lab 样式路径剔除） ----

func TestBacktestProjectFilters(t *testing.T) {
	wins := []btWin{
		{"s1", "C:/work/alpha", 600, 100000},
		{"s2", "C:/work/beta", 900, 80000},
		{"s3", "C:/lab/ft-lab", 300, 50000},
	}
	cfgPath := btMakeConfig(t, btMakeData(t, wins), 600, true)

	// --exclude 剔 ft-lab：过滤前口径仍 3，过滤后 2
	code, stdout, stderr := btRun(t, "--config", cfgPath, "--json",
		"--exclude", "*ft-lab*")
	if code != 0 {
		t.Fatalf("exclude 退出码 = %d\nstderr=%s", code, stderr)
	}
	v := btDecodeJSON(t, stdout)
	if v.Counts.TotalWindows != 3 || v.Counts.AfterFilterWindows != 2 || v.Counts.ReplayWindows != 2 {
		t.Fatalf("--exclude 计数 = total %d / after %d / replay %d, want 3/2/2",
			v.Counts.TotalWindows, v.Counts.AfterFilterWindows, v.Counts.ReplayWindows)
	}

	// --projects 正集：ft-lab 不在正集（unknown/正集外同剔除）
	code, stdout, _ = btRun(t, "--config", cfgPath, "--json",
		"--projects", "C:/work/*")
	if code != 0 {
		t.Fatalf("projects 退出码 != 0")
	}
	if v := btDecodeJSON(t, stdout); v.Counts.AfterFilterWindows != 2 {
		t.Fatalf("--projects 过滤后 = %d, want 2", v.Counts.AfterFilterWindows)
	}

	// 正集 + 排除叠加：排除恒压过正集
	code, stdout, _ = btRun(t, "--config", cfgPath, "--json",
		"--projects", "C:/work/*", "--exclude", "*beta*")
	if code != 0 {
		t.Fatalf("叠加退出码 != 0")
	}
	if v := btDecodeJSON(t, stdout); v.Counts.ReplayWindows != 1 {
		t.Fatalf("正集+排除 replay = %d, want 1", v.Counts.ReplayWindows)
	}
}

// ---- 验收 3：config 优先级与 daemon 一致（显式 > FERRYMAN_CONFIG > 默认） ----

func TestBacktestConfigPriority(t *testing.T) {
	dirA := btMakeData(t, []btWin{{"a1", "C:/work/a", 600, 100000}})
	dirB := btMakeData(t, []btWin{
		{"b1", "C:/work/b", 600, 100000},
		{"b2", "C:/work/c", 900, 80000},
	})
	cfgA := btMakeConfig(t, dirA, 600, true)
	cfgB := btMakeConfig(t, dirB, 600, true)
	t.Setenv("FERRYMAN_CONFIG", cfgA)

	// 显式 --config 压过 FERRYMAN_CONFIG：计数来自 B（2 窗）
	code, stdout, stderr := btRun(t, "--config", cfgB, "--json")
	if code != 0 {
		t.Fatalf("显式优先退出码 = %d\nstderr=%s", code, stderr)
	}
	if v := btDecodeJSON(t, stdout); v.Counts.TotalWindows != 2 {
		t.Fatalf("显式 --config 未生效：total = %d, want 2（B 的计数）", v.Counts.TotalWindows)
	}

	// 无 --config：FERRYMAN_CONFIG 生效（计数来自 A，1 窗——真实默认 config
	// 绝不掺和：其账本不可能恰好 1 窗）
	code, stdout, stderr = btRun(t, "--json")
	if code != 0 {
		t.Fatalf("env 优先退出码 = %d\nstderr=%s", code, stderr)
	}
	if v := btDecodeJSON(t, stdout); v.Counts.TotalWindows != 1 {
		t.Fatalf("FERRYMAN_CONFIG 未生效：total = %d, want 1（A 的计数）", v.Counts.TotalWindows)
	}
}

// ---- 验收 4：价格表缺 P_cache 的 provider → 不可算桶如实标注 ----
// （夹具书仅缺 p_cache 版本 → 全部窗入不可算桶、可重放零窗：引擎按「宁可空表
// 不造数」空集短路成功——退出码 0，报告如实登桶计数与「引擎未产出」占位。）

func TestBacktestUncomputableNoPCache(t *testing.T) {
	cfgPath := btMakeConfig(t, btMakeData(t, []btWin{
		{"s1", "C:/work/alpha", 600, 100000},
		{"s2", "C:/work/beta", 900, 80000},
	}), 600, false)

	outPath := filepath.Join(t.TempDir(), "report.md")
	code, stdout, _ := btRun(t, "--config", cfgPath, "--out", outPath, "--json")
	if code != 0 {
		t.Fatalf("退出码 = %d, want 0（空集短路非失败）", code)
	}
	v := btDecodeJSON(t, stdout)
	if v.Counts.UncomputableCount != 2 || v.Counts.ReplayWindows != 0 {
		t.Fatalf("不可算桶 = %d / replay = %d, want 2/0",
			v.Counts.UncomputableCount, v.Counts.ReplayWindows)
	}
	b, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("报告应照常落盘: %v", err)
	}
	if !strings.Contains(string(b), "不可算桶") || !strings.Contains(string(b), "（引擎未产出）") {
		t.Fatal("报告缺不可算桶标注/引擎未产出占位")
	}
}

// ---- 验收 5：零窗数据集 → 明确空态，退出码 0，不 panic ----

func TestBacktestZeroWindows(t *testing.T) {
	cfgPath := btMakeConfig(t, btMakeData(t, nil), 600, true)

	outPath := filepath.Join(t.TempDir(), "report.md")
	code, stdout, stderr := btRun(t, "--config", cfgPath, "--out", outPath)
	if code != 0 {
		t.Fatalf("零窗退出码 = %d, want 0\nstderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, "空态") {
		t.Fatalf("stdout 缺空态标注: %q", stdout)
	}
	b, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "| 全部窗（过滤前） | 0 |") {
		t.Fatal("报告应如实登零计数")
	}

	// --json 同样零计数、零 panic
	code, stdout, _ = btRun(t, "--config", cfgPath, "--json")
	if code != 0 {
		t.Fatalf("--json 零窗退出码 = %d, want 0", code)
	}
	if v := btDecodeJSON(t, stdout); v.Counts.TotalWindows != 0 {
		t.Fatalf("零窗 total = %d, want 0", v.Counts.TotalWindows)
	}
}

// ---- 验收 6：ttl_s 未配置 → ErrTTLUnset 如实空态（退出码 1，报告照落） ----

func TestBacktestTTLUnset(t *testing.T) {
	cfgPath := btMakeConfig(t, btMakeData(t, []btWin{
		{"s1", "C:/work/alpha", 600, 100000},
	}), 0, true) // 无 [heartbeat] 节 = ttl_s 未配置

	code, _, stderr := btRun(t, "--config", cfgPath, "--json")
	if code != 1 {
		t.Fatalf("ttl 未配置退出码 = %d, want 1", code)
	}
	if !strings.Contains(stderr, "ttl_s") {
		t.Fatalf("stderr 应含 ErrTTLUnset 语义（ttl_s）: %q", stderr)
	}
}

// ---- 旗标边界：未知参数退 2；--help 退 0；glob 旗标可多次 ----

func TestBacktestFlagEdges(t *testing.T) {
	if code, _, _ := btRun(t, "--nope"); code != 2 {
		t.Fatalf("未知旗标退出码 = %d, want 2", code)
	}
	if code, _, _ := btRun(t, "positional"); code != 2 {
		t.Fatalf("多余位置参数退出码 = %d, want 2", code)
	}
	var out, helpErr strings.Builder
	if code := cmdBacktest([]string{"--help"}, &out, &helpErr); code != 0 {
		t.Fatalf("--help 退出码 = %d, want 0", code)
	}
	// 旗标说明走 flag 包输出 = stderr（fs.SetOutput 指向）
	if !strings.Contains(helpErr.String(), "projects") {
		t.Fatal("--help 应打印旗标说明")
	}
}

// TestGlobList 可多次 glob 旗标（flag.Value 语义）：Set 追加、String 连接、空值忽略。
func TestGlobList(t *testing.T) {
	var g globList
	for _, v := range []string{"*ft-lab*", "", "*压测*"} {
		if err := g.Set(v); err != nil {
			t.Fatalf("Set(%q) 不应失败: %v", v, err)
		}
	}
	if len(g) != 2 || g[0] != "*ft-lab*" || g[1] != "*压测*" {
		t.Fatalf("globList = %v, want 追加两值（空段丢弃）", g)
	}
	if got := g.String(); got != "*ft-lab*,*压测*" {
		t.Fatalf("String() = %q", got)
	}
}
