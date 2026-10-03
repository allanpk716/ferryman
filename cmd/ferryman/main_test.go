package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"ferryman/internal/update"
)

// TestResolveDataDir 数据根自动探测：根（无 *.jsonl、有 accounts/）下钻一层；
// 其余情况原样返回（accounts 直传、平铺 jsonl、两者皆无的空目录）。
// （cmd/viewer 迁入，票22 附录#3：viewer 测试不删。）
func TestResolveDataDir(t *testing.T) {
	root := t.TempDir()
	acc := filepath.Join(root, "accounts")
	if err := os.MkdirAll(acc, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(acc, "202609.jsonl"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if got := resolveDataDir(root); got != acc {
		t.Fatalf("数据根 → %q, want 自动下钻 %q", got, acc)
	}
	if got := resolveDataDir(acc); got != acc {
		t.Fatalf("accounts 直传 → %q, want 原样 %q", got, acc)
	}

	// 根里直接放 *.jsonl：本身就是账本目录，不下钻
	flat := t.TempDir()
	if err := os.WriteFile(filepath.Join(flat, "a.jsonl"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := resolveDataDir(flat); got != flat {
		t.Fatalf("平铺 jsonl 目录 → %q, want 原样 %q", got, flat)
	}

	// 无 jsonl 也无 accounts/：原样返回，交服务端报「数据目录不存在」
	empty := t.TempDir()
	if got := resolveDataDir(empty); got != empty {
		t.Fatalf("空目录 → %q, want 原样 %q", got, empty)
	}
}

// TestParseEvents --events 逗号列表解析：空白裁剪、空段丢弃；
// 空/缺省 = nil（= installer.normalizeEvents 的全集语义）。
func TestParseEvents(t *testing.T) {
	got := parseEvents("SessionStart, SubagentStart ,SubagentStop")
	want := []string{"SessionStart", "SubagentStart", "SubagentStop"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseEvents 子集 = %v, want %v", got, want)
	}
	if v := parseEvents(""); v != nil {
		t.Fatalf("空串应得 nil（全集语义）, got %v", v)
	}
	if v := parseEvents(" , ,"); v != nil {
		t.Fatalf("全空段应得 nil（全集语义）, got %v", v)
	}
	if v := parseEvents("UserPromptSubmit"); !reflect.DeepEqual(v, []string{"UserPromptSubmit"}) {
		t.Fatalf("单事件 = %v, want [UserPromptSubmit]", v)
	}
}

// TestCmdVersion version 子命令（发布链票01）：缺省 dev 带「非 release 构建」
// 提示；注入值原样输出（测试直接改包级 version 变量——ldflags
// "-X main.version=…" 与之等价，exe 冒烟另行证明）。
func TestCmdVersion(t *testing.T) {
	orig := version
	defer func() { version = orig }()

	version = "dev"
	var buf bytes.Buffer
	if code := cmdVersion(nil, &buf); code != 0 {
		t.Fatalf("version 退出码 = %d, want 0", code)
	}
	if out := buf.String(); !strings.Contains(out, "dev") || !strings.Contains(out, "非 release 构建") {
		t.Fatalf("dev 输出 = %q, want 含版本值与「非 release 构建」提示", out)
	}

	version = "v0.1.0"
	buf.Reset()
	if code := cmdVersion(nil, &buf); code != 0 {
		t.Fatalf("version 退出码 = %d, want 0", code)
	}
	if got := strings.TrimSpace(buf.String()); got != "v0.1.0" {
		t.Fatalf("注入后输出 = %q, want v0.1.0（注入值原样透出）", got)
	}
}

// TestPanelMuxAPIVersion 版本 API（票02，规格 §A）：面板 GET /api/version 回
// {"version": <main.version>}——页脚版本号的数据源（前端运行时取，不烘焙进
// 静态资源；测试直接改包级 version 变量，与 TestCmdVersion 同法）。
func TestPanelMuxAPIVersion(t *testing.T) {
	orig := version
	defer func() { version = orig }()
	version = "v9.9.9-test"

	mux := panelMux(t.TempDir(), "")
	srv := httptest.NewServer(mux)
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/api/version")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/version = %d, want 200", resp.StatusCode)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("响应非 JSON: %v", err)
	}
	if v, _ := body["version"].(string); v != "v9.9.9-test" {
		t.Fatalf("version = %v, want v9.9.9-test", body["version"])
	}
}

// TestCmdUpdate update 子命令（票03 只读路径 + 票05 执行路径）：无 --check /
// --supervise 都走监督者执行（stub 注入缝断言，不真升级）；--supervise 行为与
// 无参一致（规格 §C 统一监督者）；多余位置参数退 2。--check 的联网行为在
// internal/update 里用 httptest 全覆盖，这里只测分发边界，不外呼。
func TestCmdUpdate(t *testing.T) {
	orig := runUpdateExecute
	defer func() { runUpdateExecute = orig }()

	var gotSpec string
	var gotPre bool
	var gotWQ int
	var gotForce bool
	var calls int
	runUpdateExecute = func(spec string, pre bool, waitQuiet int, force bool, _ io.Writer) int {
		calls++
		gotSpec, gotPre = spec, pre
		gotWQ, gotForce = waitQuiet, force
		return 0
	}

	// 无 --check = 执行（顺带传显式版本与 prerelease）
	var buf bytes.Buffer
	if code := cmdUpdate([]string{"v0.2.0", "--prerelease"}, &buf); code != 0 {
		t.Fatalf("执行路径退出码 = %d, want 0（stub）", code)
	}
	if calls != 1 || gotSpec != "v0.2.0" || !gotPre {
		t.Fatalf("执行路径参数透传: calls=%d spec=%q pre=%v", calls, gotSpec, gotPre)
	}
	// 静默门旗标缺省（票02）：预算 60s、不强切
	if gotWQ != 60 || gotForce {
		t.Fatalf("静默门旗标缺省透传: waitQuiet=%d force=%v, want 60/false", gotWQ, gotForce)
	}

	// --supervise 内部旗标：行为与无参一致（同走执行路径）
	calls = 0
	if code := cmdUpdate([]string{"--supervise"}, &buf); code != 0 {
		t.Fatalf("--supervise 退出码 = %d, want 0（stub）", code)
	}
	if calls != 1 || gotSpec != "" {
		t.Fatalf("--supervise 应同无参走执行路径: calls=%d spec=%q", calls, gotSpec)
	}

	buf.Reset()
	if code := cmdUpdate([]string{"--check", "v0.1.0", "extra"}, &buf); code != 2 {
		t.Fatalf("多余位置参数退出码 = %d, want 2", code)
	}
}

// ---- 静默门旗标(票02):--wait-quiet / --force 解析与透传 ----

// TestCmdUpdateQuietGateFlags 缺省 60s 不强切;--wait-quiet=0(不等)/显式秒数/
// --force 原样进执行路径;带值旗标空格形态(`--wait-quiet 30`)与位置参数混排
// 不串位。
func TestCmdUpdateQuietGateFlags(t *testing.T) {
	orig := runUpdateExecute
	defer func() { runUpdateExecute = orig }()

	var gotSpec string
	var gotWQ int
	var gotForce bool
	runUpdateExecute = func(spec string, pre bool, waitQuiet int, force bool, _ io.Writer) int {
		gotSpec, gotWQ, gotForce = spec, waitQuiet, force
		return 0
	}
	var buf bytes.Buffer

	if code := cmdUpdate(nil, &buf); code != 0 {
		t.Fatalf("缺省退出码 = %d, want 0", code)
	}
	if gotWQ != 60 || gotForce || gotSpec != "" {
		t.Fatalf("缺省透传: wq=%d force=%v spec=%q, want 60/false/\"\"", gotWQ, gotForce, gotSpec)
	}

	if code := cmdUpdate([]string{"--wait-quiet=0", "--force"}, &buf); code != 0 {
		t.Fatalf("自包含旗标形态退出码 = %d", code)
	}
	if gotWQ != 0 || !gotForce {
		t.Fatalf("--wait-quiet=0 --force 透传: wq=%d force=%v", gotWQ, gotForce)
	}

	if code := cmdUpdate([]string{"v0.2.0", "--wait-quiet", "30"}, &buf); code != 0 {
		t.Fatalf("空格形态退出码 = %d", code)
	}
	if gotWQ != 30 || gotSpec != "v0.2.0" {
		t.Fatalf("空格形态透传: wq=%d spec=%q, want 30/v0.2.0", gotWQ, gotSpec)
	}
}

// TestQuietWaitFromFlag 旗标秒数 → update.Config.WaitQuiet 映射:0 = 不等
// (负值哨兵);其余秒→时长(缺省 60 直接透传即缺省语义)。
func TestQuietWaitFromFlag(t *testing.T) {
	if got := quietWaitFromFlag(0); got >= 0 {
		t.Fatalf("0 应映射为不等待哨兵(负值), got %v", got)
	}
	if got := quietWaitFromFlag(30); got != 30*time.Second {
		t.Fatalf("30 → %v, want 30s", got)
	}
	if got := quietWaitFromFlag(60); got != 60*time.Second {
		t.Fatalf("60 → %v, want 60s", got)
	}
}

// ---- 托盘菜单(票07):装配纯函数 + 检查/升级动作流 ----

// TestTrayMenuItems 托盘菜单装配(票07,规格 §D):五项序——版本(disabled
// 展示)→打开面板→检查更新→立即升级→退出;版本项文本随入参(dev 显 dev);
// 打开面板 tooltip = 面板地址。systray 本体不可单测,只测装配纯函数。
func TestTrayMenuItems(t *testing.T) {
	url := "http://127.0.0.1:15900"
	items := trayMenuItems("v0.1.0", url)
	want := []string{"版本 v0.1.0", "打开面板", "检查更新", "立即升级", "退出"}
	if len(items) != len(want) {
		t.Fatalf("菜单项数 = %d, want %d", len(items), len(want))
	}
	for i, it := range items {
		if it.title != want[i] {
			t.Fatalf("第 %d 项 = %q, want %q(五项序)", i, it.title, want[i])
		}
	}
	if !items[0].disabled {
		t.Fatal("版本项应为 disabled 展示项")
	}
	for i := 1; i < len(items); i++ {
		if items[i].disabled {
			t.Fatalf("第 %d 项 %q 不应 disabled", i, items[i].title)
		}
	}
	if items[1].tooltip != url {
		t.Fatalf("打开面板 tooltip = %q, want 面板地址 %q", items[1].tooltip, url)
	}
	// dev 显 dev:本地开发构建的版本项如实显示 dev
	if got := trayMenuItems("dev", url)[0].title; got != "版本 dev" {
		t.Fatalf("dev 版本项 = %q, want \"版本 dev\"", got)
	}
}

// TestTrayCheckUpdateFlow 「检查更新」动作流(票07):只读——进程内调
// update.Check(注入 httptest 假端点,零外呼),结论以 Check 人话串经注入的
// 通知函数推送(桩断言,不真弹窗);release 查询之外零端点触碰 = 无下载等
// 写操作面;查询失败也推一次失败文案(用户点了要有回音)。
func TestTrayCheckUpdateFlow(t *testing.T) {
	var otherHits int
	mux := http.NewServeMux()
	// 假 GitHub latest 端点(repoSlug = allanpk716/ferryman,internal/update 硬编码)
	mux.HandleFunc("/repos/allanpk716/ferryman/releases/latest", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"tag_name":"v0.2.0"}`)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { // 其余路径 = 不该被碰
		otherHits++
		http.NotFound(w, r)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	eps := update.Endpoints{APIBase: srv.URL, DLBase: srv.URL}

	var pushes int
	var gotTitle, gotMsg string
	updateCheckFlow(eps, "v0.1.0", func(title, msg string) {
		pushes++
		gotTitle, gotMsg = title, msg
	})
	if pushes != 1 {
		t.Fatalf("通知推送次数 = %d, want 1", pushes)
	}
	if gotTitle != "Ferryman 检查更新" {
		t.Fatalf("通知标题 = %q, want Ferryman 检查更新", gotTitle)
	}
	if !strings.Contains(gotMsg, "v0.2.0") || !strings.Contains(gotMsg, "v0.1.0") {
		t.Fatalf("通知正文 = %q, want 含目标 v0.2.0 与当前 v0.1.0(Check 人话串)", gotMsg)
	}
	if otherHits != 0 {
		t.Fatalf("只读红线:release 查询之外被碰 %d 次", otherHits)
	}

	// 查询失败(404):仍推一次,文案为失败说明
	pushes = 0
	bad := update.Endpoints{APIBase: srv.URL + "/nowhere", DLBase: srv.URL}
	updateCheckFlow(bad, "v0.1.0", func(title, msg string) {
		pushes++
		gotTitle, gotMsg = title, msg
	})
	if pushes != 1 || !strings.Contains(gotMsg, "检查更新失败") {
		t.Fatalf("失败路径应推一次失败文案: pushes=%d msg=%q", pushes, gotMsg)
	}
}

// TestTrayUpgradeLaunchFlow 「立即升级」动作流(票07):detached 隐藏拉起的
// 参数形态 = 本 exe 原样 + `update --supervise`(票05 内部旗标 = 监督者执行);
// 启动函数可注入——单测用桩断言形态,真实 spawn 不在单测里跑;启动失败原样
// 上抛。
func TestTrayUpgradeLaunchFlow(t *testing.T) {
	var gotExe string
	var gotArgs []string
	start := func(exe string, args ...string) error {
		gotExe, gotArgs = exe, args
		return nil
	}
	if err := upgradeLaunchFlow(`C:\apps\ferryman.exe`, start); err != nil {
		t.Fatalf("拉起流不应失败: %v", err)
	}
	if gotExe != `C:\apps\ferryman.exe` {
		t.Fatalf("拉起 exe = %q, want 本 exe 原样", gotExe)
	}
	if !reflect.DeepEqual(gotArgs, []string{"update", "--supervise"}) {
		t.Fatalf("拉起参数 = %v, want [update --supervise](监督者形态)", gotArgs)
	}

	wantErr := errors.New("boom")
	if err := upgradeLaunchFlow(`ferryman.exe`, func(string, ...string) error { return wantErr }); !errors.Is(err, wantErr) {
		t.Fatalf("启动失败应原样上抛: got %v", err)
	}
}

// ---- help 安全契约（本票）：-h/--help 永不触发真实动作 ----

// captureStd 捕获进程级 stdout/stderr（usage/帮助面直写 os.Stdout/os.Stderr，
// 无注入缝——测试以管道换底取回，两路输出按流分桶；测试串行跑，无并发互踩）。
func captureStd(t *testing.T, fn func()) (string, string) {
	t.Helper()
	oldOut, oldErr := os.Stdout, os.Stderr
	rOut, wOut, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	rErr, wErr, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout, os.Stderr = wOut, wErr
	defer func() { os.Stdout, os.Stderr = oldOut, oldErr }()

	outC := make(chan string, 1)
	errC := make(chan string, 1)
	go func() { b, _ := io.ReadAll(rOut); outC <- string(b) }()
	go func() { b, _ := io.ReadAll(rErr); errC <- string(b) }()

	fn()

	wOut.Close()
	wErr.Close()
	return <-outC, <-errC
}

// TestNoArgCommandsRejectArgs doctor/install-ccswitch 零参数契约（本票①）：
// 任何非空参数（含 -h/--help/任意词）= 用法错退 2，且注入桩计数为零——拒绝
// 先于执行（旧缺陷：参数整体丢弃照跑，install-ccswitch -h 会真写宿主配置）。
// 桩缝参照 runUpdateExecute 的 var 注入先例；真跑 RunDoctor/InjectCCSwitch 的
// 行为在 internal 包各有单测，这里绝不触。
func TestNoArgCommandsRejectArgs(t *testing.T) {
	origDoctor, origInject := runDoctorEntry, injectCCSwitchEntry
	defer func() { runDoctorEntry, injectCCSwitchEntry = origDoctor, origInject }()

	var doctorCalls, injectCalls int
	runDoctorEntry = func(string) int { doctorCalls++; return 0 }
	injectCCSwitchEntry = func(string, string, []string) int { injectCalls++; return 0 }

	cases := []struct {
		name string
		args []string
	}{
		{"doctor -h", []string{"doctor", "-h"}},
		{"doctor --help", []string{"doctor", "--help"}},
		{"doctor extra", []string{"doctor", "extra"}},
		{"install-ccswitch -h", []string{"install-ccswitch", "-h"}},
		{"install-ccswitch --help", []string{"install-ccswitch", "--help"}},
		{"install-ccswitch extra", []string{"install-ccswitch", "extra"}},
	}
	for _, tc := range cases {
		var code int
		_, stderr := captureStd(t, func() { code = run(tc.args) })
		if code != 2 {
			t.Fatalf("%s: 退出码 = %d, want 2（用法错）", tc.name, code)
		}
		if !strings.Contains(stderr, "用法:") {
			t.Fatalf("%s: 应打印用法到 stderr, got %q", tc.name, stderr)
		}
	}
	if doctorCalls != 0 {
		t.Fatalf("doctor 注入桩被调 %d 次, want 0（拒绝先于执行）", doctorCalls)
	}
	if injectCalls != 0 {
		t.Fatalf("install-ccswitch 注入桩被调 %d 次, want 0（拒绝先于执行）", injectCalls)
	}

	// 零参数形态照常进桩（缝接线健全性；桩返回 0 = 成功退出码）
	if code := run([]string{"doctor"}); code != 0 || doctorCalls != 1 {
		t.Fatalf("doctor 零参应进桩: code=%d calls=%d", code, doctorCalls)
	}
	if code := run([]string{"install-ccswitch"}); code != 0 || injectCalls != 1 {
		t.Fatalf("install-ccswitch 零参应进桩: code=%d calls=%d", code, injectCalls)
	}
}

// TestFamilyHelpContract 七族 -h/--help（本票②）：account/tuning/autostart/
// watchdog/upstream/provider/cutover 在分发层识别 -h/--help——打印各自 usage
// 常量退 0（帮助走 stdout）；用法错分支（缺参/未知子命令）依旧退 2 且输出含
// usage。经 run() 分发走真路径，钉住"分发层识别"而非仅 cmd 函数本面。
func TestFamilyHelpContract(t *testing.T) {
	// 常量不可寻址——各族 usage 以名取值对照
	usages := map[string]string{
		"account":   accountUsage,
		"autostart": autostartUsage,
		"watchdog":  watchdogUsage,
		"cutover":   cutoverUsage,
		"tuning":    tuningUsage,
		"upstream":  upstreamUsage,
		"provider":  providerUsage,
	}
	helps := []struct {
		name   string
		args   []string
		family string
	}{
		{"account -h", []string{"account", "-h"}, "account"},
		{"account --help", []string{"account", "--help"}, "account"},
		{"autostart -h", []string{"autostart", "-h"}, "autostart"},
		{"autostart --help", []string{"autostart", "--help"}, "autostart"},
		{"watchdog -h", []string{"watchdog", "-h"}, "watchdog"},
		{"watchdog --help", []string{"watchdog", "--help"}, "watchdog"},
		{"cutover -h", []string{"cutover", "-h"}, "cutover"},
		{"cutover --help", []string{"cutover", "--help"}, "cutover"},
		{"tuning -h", []string{"tuning", "-h"}, "tuning"},
		{"tuning --help", []string{"tuning", "--help"}, "tuning"},
		{"upstream -h", []string{"upstream", "-h"}, "upstream"},
		{"upstream --help", []string{"upstream", "--help"}, "upstream"},
		{"provider -h", []string{"provider", "-h"}, "provider"},
		{"provider --help", []string{"provider", "--help"}, "provider"},
	}
	for _, tc := range helps {
		var code int
		stdout, _ := captureStd(t, func() { code = run(tc.args) })
		if code != 0 {
			t.Fatalf("%s: 退出码 = %d, want 0", tc.name, code)
		}
		if want := usages[tc.family]; stdout != want {
			t.Fatalf("%s: 帮助输出应恰为该族 usage 常量:\ngot  %q\nwant %q", tc.name, stdout, want)
		}
	}

	errs := []struct {
		name string
		args []string
	}{
		{"account 无参", []string{"account"}},
		{"account 未知子命令", []string{"account", "nope"}},
		{"autostart 未知子命令", []string{"autostart", "nope"}},
		{"watchdog 未知子命令", []string{"watchdog", "nope"}},
		{"cutover 无参", []string{"cutover"}},
		{"cutover 未知子命令", []string{"cutover", "nope"}},
		{"tuning 无参", []string{"tuning"}},
		{"tuning 未知子命令", []string{"tuning", "nope"}},
		{"upstream 无参", []string{"upstream"}},
		{"upstream 未知子命令", []string{"upstream", "nope"}},
		{"provider 无参", []string{"provider"}},
		{"provider 未知子命令", []string{"provider", "nope"}},
	}
	for _, tc := range errs {
		var code int
		_, stderr := captureStd(t, func() { code = run(tc.args) })
		if code != 2 {
			t.Fatalf("%s: 退出码 = %d, want 2", tc.name, code)
		}
		if !strings.Contains(stderr, "用法:") {
			t.Fatalf("%s: 用法错应打印 usage, got %q", tc.name, stderr)
		}
	}
}

// TestTopLevelUsageContract 顶层用法面（本票③）：-h/--help/help 三入口退 0 且
// 输出恰为顶层 usage；usage 含 update 条目（--check/--prerelease/--wait-quiet/
// --force/版本参数，照 main.go 文件头注释形态）与八个分组标题；总行数不增
// （钉 ≤ 重构前 69 行基线——"篇幅明显缩短"的防线，防将来静默回涨）。
func TestTopLevelUsageContract(t *testing.T) {
	for _, entry := range []string{"-h", "--help", "help"} {
		var code int
		stdout, _ := captureStd(t, func() { code = run([]string{entry}) })
		if code != 0 {
			t.Fatalf("%s: 退出码 = %d, want 0", entry, code)
		}
		if stdout != usage {
			t.Fatalf("%s: 输出应恰为顶层 usage 常量", entry)
		}
	}
	for _, want := range []string{
		"ferryman update [--check] [vX.Y.Z] [--prerelease]",
		"--wait-quiet", "--force",
		"serve", "体检", "安装", "账本", "渡口与供应商", "调参", "换装", "工具",
	} {
		if !strings.Contains(usage, want) {
			t.Fatalf("顶层 usage 缺条目/分组: %q", want)
		}
	}
	if n := strings.Count(strings.TrimRight(usage, "\n"), "\n") + 1; n > 69 {
		t.Fatalf("顶层 usage %d 行, want ≤ 69（重构前基线，总行数不增）", n)
	}
}
