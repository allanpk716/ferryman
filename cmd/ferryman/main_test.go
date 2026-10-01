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

	// --self-relay 已废弃（自中继副本机制 v0.5.2/票02 删除）：解析但忽略
	// （次版删旗标）——旗标在场不报错,执行路径照常走,值不再透传任何面。
	calls = 0
	if code := cmdUpdate([]string{"--supervise", "--self-relay"}, &buf); code != 0 {
		t.Fatalf("--self-relay 退出码 = %d, want 0（解析但忽略）", code)
	}
	if calls != 1 {
		t.Fatalf("--self-relay 应照常走执行路径（解析但忽略）: calls=%d", calls)
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
