package main

// restore_test.go —— 票39：`ferryman restore [--cwd 目录]`（显式恢复薄客户端）。
//
// 纪律（status_test 同款）：绝不触碰真实守护——请求形态用 httptest 桩钉
// （daemonRestoreGet 直打假端点，断言 /restore 路径与 explicit=1）；决策树用
// 注桩 deps（零网络）；不可达用真闭口（连接拒绝路径，不发真请求到生产口）。
// 帮助契约经 run() 分发走真路径（-h 退 0 打印 usage、用法错退 2）。

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// ---- 帮助契约（票01 同款：-h/--help 退 0 打印 usage；用法错退 2） ----

func TestRestoreCLIHelpContract(t *testing.T) {
	helps := []struct {
		name string
		args []string
	}{
		{"restore -h", []string{"restore", "-h"}},
		{"restore --help", []string{"restore", "--help"}},
	}
	for _, tc := range helps {
		var code int
		stdout, _ := captureStd(t, func() { code = run(tc.args) })
		if code != 0 {
			t.Fatalf("%s: 退出码 = %d, want 0", tc.name, code)
		}
		if stdout != restoreUsage {
			t.Fatalf("%s: 帮助输出应恰为 usage 常量:\ngot  %q\nwant %q", tc.name, stdout, restoreUsage)
		}
	}
	errs := []struct {
		name string
		args []string
	}{
		{"restore 位置参数", []string{"restore", "extra"}},
		{"restore 未知旗标", []string{"restore", "--nope"}},
	}
	for _, tc := range errs {
		var code int
		_, stderr := captureStd(t, func() { code = run(tc.args) })
		if code != 2 {
			t.Fatalf("%s: 退出码 = %d, want 2（用法错）", tc.name, code)
		}
		if !strings.Contains(stderr, "用法:") {
			t.Fatalf("%s: 用法错应打印 usage 到 stderr, got %q", tc.name, stderr)
		}
	}
}

// TestTopLevelUsageMentionsRestore 顶层 usage 收录命令（票面：进顶层帮助）。
func TestTopLevelUsageMentionsRestore(t *testing.T) {
	if !strings.Contains(usage, "ferryman restore") {
		t.Errorf("顶层 usage 缺 %q", "ferryman restore")
	}
}

// ---- 请求形态（httptest 桩：/restore + explicit=1 + agent=cc） ----

func TestDaemonRestoreGetRequestShape(t *testing.T) {
	var gotPath string
	var gotQuery url.Values
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.Query()
		gotAuth = r.Header.Get("Authorization")
		fmt.Fprint(w, `{"context":"交接文本"}`)
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	port := 0
	fmt.Sscanf(u.Port(), "%d", &port)
	r, err := daemonRestoreGet(port, "tok-x", `C:\proj`, "sid-1")
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/restore" {
		t.Fatalf("路径 = %q, want /restore", gotPath)
	}
	if gotAuth != "Bearer tok-x" {
		t.Fatalf("鉴权头 = %q", gotAuth)
	}
	if gotQuery.Get("agent") != "cc" {
		t.Fatalf("agent = %q, want cc（CLI 是 CC 显式入口）", gotQuery.Get("agent"))
	}
	if gotQuery.Get("explicit") != "1" {
		t.Fatalf("explicit = %q, want 1（显式恢复契约）", gotQuery.Get("explicit"))
	}
	if gotQuery.Get("cwd") != `C:\proj` {
		t.Fatalf("cwd = %q", gotQuery.Get("cwd"))
	}
	if gotQuery.Get("session_id") != "sid-1" {
		t.Fatalf("session_id = %q", gotQuery.Get("session_id"))
	}
	if ctx, _ := r["context"].(string); ctx != "交接文本" {
		t.Fatalf("context = %v", r["context"])
	}
	// --session 缺省（空）＝不设：请求不带 session_id（守护侧合成 cli-restore-*，
	// 合成单源；spec #34 评审 F5）。
	r2, err := daemonRestoreGet(port, "tok-x", `C:\proj`, "")
	if err != nil {
		t.Fatal(err)
	}
	if gotQuery.Get("session_id") != "" {
		t.Fatalf("空 session 不应携带 session_id 参数: %q", gotQuery.Get("session_id"))
	}
	if ctx, _ := r2["context"].(string); ctx != "交接文本" {
		t.Fatalf("context = %v", r2["context"])
	}
}

// ---- 决策树（注桩 deps，零网络） ----

func TestRestoreRunPrintsContext(t *testing.T) {
	var stdout, stderr strings.Builder
	code := restoreRun(&stdout, &stderr, "C:/proj", "", &restoreDeps{
		Port: 1, Token: "t",
		Get: func(port int, token, cwd, sid string) (map[string]any, error) {
			return map[string]any{"context": "[Ferryman 交接 · …] 注入文本"}, nil
		},
	})
	if code != 0 {
		t.Fatalf("退出码 = %d, want 0", code)
	}
	if !strings.Contains(stdout.String(), "注入文本") {
		t.Fatalf("stdout 应含注入文本: %q", stdout.String())
	}
	if stderr.String() != "" {
		t.Fatalf("成功路径 stderr 应为空: %q", stderr.String())
	}
}

func TestRestoreRunUnreachableReadableNoStack(t *testing.T) {
	// 守护不可达：可读报错（人话 + 处置提示）、退 1、绝无堆栈。
	var stdout, stderr strings.Builder
	code := restoreRun(&stdout, &stderr, "C:/proj", "", &restoreDeps{
		Port: 1, Token: "t",
		Get: func(port int, token, cwd, sid string) (map[string]any, error) {
			return nil, fmt.Errorf("%w（127.0.0.1:1 无应答）", errDaemonOffline)
		},
	})
	if code != 1 {
		t.Fatalf("退出码 = %d, want 1", code)
	}
	msg := stderr.String()
	if !strings.Contains(msg, "守护") || !strings.Contains(msg, "不在线") {
		t.Fatalf("报错应可读（守护不在线）: %q", msg)
	}
	if !strings.Contains(msg, "ferryman") { // 处置提示：怎么把守护拉起来
		t.Fatalf("报错应给处置提示: %q", msg)
	}
	if strings.Contains(msg, "goroutine") || strings.Contains(stdout.String(), "goroutine") {
		t.Fatalf("不得出现堆栈: %q / %q", msg, stdout.String())
	}
	if stdout.String() != "" {
		t.Fatalf("失败路径 stdout 应为空: %q", stdout.String())
	}
}

func TestRestoreRunAuthFailedReadable(t *testing.T) {
	var stdout, stderr strings.Builder
	code := restoreRun(&stdout, &stderr, "C:/proj", "", &restoreDeps{
		Port: 1, Token: "t",
		Get: func(port int, token, cwd, sid string) (map[string]any, error) {
			return nil, errAuth
		},
	})
	if code != 1 {
		t.Fatalf("退出码 = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "鉴权") {
		t.Fatalf("报错应点名鉴权失败: %q", stderr.String())
	}
}

func TestRestoreRunPassesSessionThrough(t *testing.T) {
	// --session 非空：原样传给端点缝（inject 账与 mark_injected 的登记主体＝
	// 真实会话 id，上一代交接判定可据此认出被接替的会话——US31）。
	var got string
	var stdout, stderr strings.Builder
	code := restoreRun(&stdout, &stderr, "C:/proj", "sess-42", &restoreDeps{
		Port: 1, Token: "t",
		Get: func(port int, token, cwd, sid string) (map[string]any, error) {
			got = sid
			return map[string]any{"context": "x"}, nil
		},
	})
	if code != 0 || got != "sess-42" {
		t.Fatalf("--session 应原样透传: code=%d sid=%q", code, got)
	}
}

func TestRestoreRunEmptySessionMeansUnset(t *testing.T) {
	// --session 空（缺省）＝不设：CLI 不再合成 cli-restore-*（合成单源在守护侧
	// RestoreExplicit——空 session_id 请求到达后由守护合成；spec #34 评审 F5）。
	var got []string
	call := func() {
		var stdout, stderr strings.Builder
		restoreRun(&stdout, &stderr, "C:/proj", "", &restoreDeps{
			Port: 1, Token: "t",
			Get: func(port int, token, cwd, sid string) (map[string]any, error) {
				got = append(got, sid)
				return map[string]any{"context": "x"}, nil
			},
		})
	}
	call()
	call()
	for _, sid := range got {
		if sid != "" {
			t.Fatalf("空 --session 应以未设传端（守护侧合成），CLI 不预合成: %q", sid)
		}
	}
}

// ---- --cwd 缺省 = 当前目录（纯函数钉子） ----

func TestRestoreCwdDefaultUsesGetwd(t *testing.T) {
	if got := restoreCwdOrDefault("", func() (string, error) { return "W:/here", nil }); got != "W:/here" {
		t.Fatalf("缺省 cwd 应取当前目录: %q", got)
	}
	if got := restoreCwdOrDefault("X:/given", func() (string, error) { return "W:/here", nil }); got != "X:/given" {
		t.Fatalf("显式 --cwd 优先: %q", got)
	}
	// getwd 失败 → 空串照传（守护侧对空 cwd 返回空候选——CLI 输出“无可用交接”明说）
	if got := restoreCwdOrDefault("", func() (string, error) { return "", fmt.Errorf("boom") }); got != "" {
		t.Fatalf("getwd 失败应空串照传: %q", got)
	}
}
