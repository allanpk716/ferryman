// upstream_test.go — 票02：`ferryman upstream list / use` 验收钉子。
//
// 验收口径（票 02）：
//   - list：全部条目 + active 标注 + base_url + model_map 概要 + 缺 key 状态
//   - 密钥脱敏（只露尾 4 位，绝不整钥出站）；
//   - use：不存在条目/缺 key 条目正确拒绝（含可用条目清单/补 key 指引）；
//   - use 成功路径：在途请求中断提示 → active 原子写回 → 停旧→拉起→健康检查
//     （顺序钉死）→ 成功输出冷启动提示；
//   - use 失败路径：健康检查超时 → 非零退出 + 如实报告（配置已切/守护未起）
//   - 手动拉起与回退指引；不回滚、不重试（拉起恰好一次）；
//   - 重启机制可注入（启/停/健康检查接口替换，测试不拉长驻进程）。
//
// 长跑纪律：全部走注入桩/httptest，健康轮询上限由测试给小预算，绝不无限等。
package main

import (
	"bytes"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// upstreamCfgSrc list/use 共用底稿：带 key 的本地回退、带 key 的直连条目、
// 缺 key 的预置各一；其余节用于"写回不动它"验收。
const upstreamCfgSrc = `
[server]
port = 7399

[dock]
listen = "127.0.0.1:15722"
active = "cc-switch"

[dock.upstreams."cc-switch"]
base_url = "http://127.0.0.1:15721"
api_key = "sk-local-1593574628abcd"

[dock.upstreams.zhipu]
base_url = "https://open.bigmodel.cn/api/anthropic"
api_key = "sk-zhipu-00001234ef12"
model_map = { default = "glm-5.3", haiku = "glm-5.3-flash" }
balance_url = "https://open.bigmodel.cn/api/user/balance"

[dock.upstreams.kimi]
base_url = "https://api.kimi.com/coding/"
model_map = { default = "kimi-for-coding" }

[ferry]
provider = "deepseek"
`

func writeUpstreamCfg(t *testing.T, src string) string {
	t.Helper()
	f := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(f, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	return f
}

// fakeRestart 重启桩：记录调用序；healthOK/launchErr 可设。
func fakeRestart(healthOK bool, launchErr error) (*upstreamRestartDeps, *[]string) {
	var order []string
	return &upstreamRestartDeps{
		Port:  7311,
		Token: "tok",
		Shutdown: func() error {
			order = append(order, "shutdown")
			return nil
		},
		Launch: func() error {
			order = append(order, "launch")
			return launchErr
		},
		Health: func(time.Duration) bool {
			order = append(order, "health")
			return healthOK
		},
	}, &order
}

// ---- list ----

func TestUpstreamListShowsAllEntriesStatusAndMasking(t *testing.T) {
	f := writeUpstreamCfg(t, upstreamCfgSrc)
	var buf bytes.Buffer
	if code := upstreamList(f, &buf); code != 0 {
		t.Fatalf("list 退出码 = %d, want 0\n%s", code, buf.String())
	}
	out := buf.String()
	for _, want := range []string{
		"cc-switch", "zhipu", "kimi", // 全部条目
		"（active）",                               // active 标注
		"http://127.0.0.1:15721",                 // base_url ×2
		"https://open.bigmodel.cn/api/anthropic", //
		"glm-5.3", "glm-5.3-flash",               // model_map 概要
		"未配置",      // 缺 key 状态
		"api_key",  //
		"****abcd", // 脱敏：只露尾 4 位
		"https://open.bigmodel.cn/api/user/balance", // balance_url 配了才显示
	} {
		if !strings.Contains(out, want) {
			t.Errorf("list 输出缺 %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "sk-local-1593574628abcd") ||
		strings.Contains(out, "sk-zhipu-00001234ef12") {
		t.Errorf("list 泄漏整钥:\n%s", out)
	}
}

func TestUpstreamListLegacyAndNoDockForms(t *testing.T) {
	// 旧单值形态（无上游表）：如实说明＋兜底条目可见，退出 0
	f := writeUpstreamCfg(t, "[dock]\nupstream_base_url = \"http://127.0.0.1:15721\"\napi_key = \"sk-old-9999abcd\"\n")
	var buf bytes.Buffer
	if code := upstreamList(f, &buf); code != 0 {
		t.Fatalf("legacy list 退出码 = %d\n%s", code, buf.String())
	}
	out := buf.String()
	if !strings.Contains(out, "旧单值") || !strings.Contains(out, "****abcd") {
		t.Errorf("legacy list 输出缺旧单值说明/脱敏:\n%s", out)
	}
	if strings.Contains(out, "sk-old-9999abcd") {
		t.Errorf("legacy list 泄漏整钥:\n%s", out)
	}

	// 无 [dock] 节：如实说明，退出 0
	f2 := writeUpstreamCfg(t, "[server]\nport = 7399\n")
	buf.Reset()
	if code := upstreamList(f2, &buf); code != 0 {
		t.Fatalf("no-dock list 退出码 = %d\n%s", code, buf.String())
	}
	if out := buf.String(); !strings.Contains(out, "[dock]") {
		t.Errorf("no-dock list 输出缺说明:\n%s", out)
	}
}

func TestUpstreamListBadConfigErrors(t *testing.T) {
	f := writeUpstreamCfg(t, "[dock]\nactive = \"ghost\"\n\n[dock.upstreams.a]\nbase_url = \"https://a.example\"\nmodel_map = { default = \"m\" }\n")
	var buf bytes.Buffer
	if code := upstreamList(f, &buf); code == 0 {
		t.Fatalf("坏配置（active 悬空）应非零退出:\n%s", buf.String())
	}
}

// ---- use：拒绝分支 ----

func TestUpstreamUseUnknownEntryRejectedListsAvailable(t *testing.T) {
	f := writeUpstreamCfg(t, upstreamCfgSrc)
	var buf bytes.Buffer
	if code := upstreamUse(f, "nope", &buf, nil); code == 0 {
		t.Fatal("未知条目应非零退出")
	}
	out := buf.String()
	if !strings.Contains(out, "nope") || !strings.Contains(out, "cc-switch") || !strings.Contains(out, "zhipu") {
		t.Errorf("拒绝输出应含条目名与可用清单:\n%s", out)
	}
	if strings.Contains(readFileUp(t, f), `active = "nope"`) {
		t.Fatal("拒绝路径不得写文件")
	}
}

func TestUpstreamUseMissingKeyRejectedWithFillHint(t *testing.T) {
	f := writeUpstreamCfg(t, upstreamCfgSrc)
	var buf bytes.Buffer
	if code := upstreamUse(f, "kimi", &buf, nil); code == 0 {
		t.Fatal("缺 key 条目应非零退出")
	}
	out := buf.String()
	if !strings.Contains(out, "kimi") || !strings.Contains(out, "api_key") {
		t.Errorf("拒绝输出应含条目名与补 key 指引:\n%s", out)
	}
}

func TestUpstreamUseInvalidConfigRejectedBeforeWrite(t *testing.T) {
	src := upstreamCfgSrc + `
[dock.upstreams.broken]
base_url = "https://api.deepseek.com/anthropic"
api_key = "sk-broken"
`
	f := writeUpstreamCfg(t, src)
	before := readFileUp(t, f)
	var buf bytes.Buffer
	// broken 有 key 但非本地条目缺 model_map.default——写回会让守护拒启，
	// 必须在校验层拒绝（写前拦截）。
	if code := upstreamUse(f, "broken", &buf, nil); code == 0 {
		t.Fatalf("校验不过的条目应非零退出:\n%s", buf.String())
	}
	if out := buf.String(); !strings.Contains(out, "校验") {
		t.Errorf("拒绝输出应说明校验失败:\n%s", out)
	}
	if got := readFileUp(t, f); got != before {
		t.Fatal("校验拒绝不得写文件")
	}
}

func TestUpstreamUseNoUpstreamTableRejected(t *testing.T) {
	f := writeUpstreamCfg(t, "[dock]\napi_key = \"k\"\n")
	var buf bytes.Buffer
	if code := upstreamUse(f, "cc-switch", &buf, nil); code == 0 {
		t.Fatal("无上游表应非零退出")
	}
}

// ---- use：成功路径 ----

func TestUpstreamUseSuccessWritesStopsRelaunchesChecksHealth(t *testing.T) {
	f := writeUpstreamCfg(t, upstreamCfgSrc)
	deps, order := fakeRestart(true, nil)
	var buf bytes.Buffer
	if code := upstreamUse(f, "zhipu", &buf, deps); code != 0 {
		t.Fatalf("use 退出码 = %d, want 0\n%s", code, buf.String())
	}
	out := buf.String()
	// 在途请求中断提示必须在输出里
	if !strings.Contains(out, "在途请求") || !strings.Contains(out, "中断") {
		t.Errorf("缺在途请求中断提示:\n%s", out)
	}
	// 成功输出：新 active + 冷启动提示
	if !strings.Contains(out, "zhipu") ||
		!strings.Contains(out, "内存缓存快照已清空") || !strings.Contains(out, "冷启动") {
		t.Errorf("成功输出缺新 active/冷启动提示:\n%s", out)
	}
	// 重启顺序：停旧 → 拉起 → 健康检查
	if got := *order; len(got) != 3 || got[0] != "shutdown" || got[1] != "launch" || got[2] != "health" {
		t.Fatalf("重启调用序 = %v, want [shutdown launch health]", got)
	}
	// 配置写回：active 变、其余节逐字保留
	after := readFileUp(t, f)
	if !strings.Contains(after, `active = "zhipu"`) {
		t.Fatalf("active 未写回:\n%s", after)
	}
	for _, keep := range []string{"[ferry]", `provider = "deepseek"`, "sk-local-1593574628abcd"} {
		if !strings.Contains(after, keep) {
			t.Fatalf("写回丢了应保留内容 %q:\n%s", keep, after)
		}
	}
}

// ---- use：失败路径（健康检查超时） ----

func TestUpstreamUseHealthTimeoutHonestReportNoRollbackNoRetry(t *testing.T) {
	f := writeUpstreamCfg(t, upstreamCfgSrc)
	deps, order := fakeRestart(false, nil)
	var buf bytes.Buffer
	if code := upstreamUse(f, "zhipu", &buf, deps); code == 0 {
		t.Fatalf("健康检查失败应非零退出:\n%s", buf.String())
	}
	out := buf.String()
	// 如实报告：配置已切换 + 守护未起来
	if !strings.Contains(out, "配置已切换为") || !strings.Contains(out, "zhipu") ||
		!strings.Contains(out, "守护进程未起来") {
		t.Errorf("失败输出缺如实状态报告:\n%s", out)
	}
	// 手动拉起与回退指引
	if !strings.Contains(out, "ferryman serve") || !strings.Contains(out, "upstream use cc-switch") {
		t.Errorf("失败输出缺手动拉起/回退指引:\n%s", out)
	}
	// 不自动回滚：配置保持已写状态
	if after := readFileUp(t, f); !strings.Contains(after, `active = "zhipu"`) {
		t.Fatalf("失败路径不得回滚配置:\n%s", after)
	}
	// 不自动重试：拉起恰好一次
	if n := countStr(*order, "launch"); n != 1 {
		t.Fatalf("launch 调用次数 = %d, want 1（不自动重试）", n)
	}
}

func TestUpstreamUseLaunchFailureReported(t *testing.T) {
	f := writeUpstreamCfg(t, upstreamCfgSrc)
	deps, _ := fakeRestart(true, os.ErrPermission)
	var buf bytes.Buffer
	if code := upstreamUse(f, "zhipu", &buf, deps); code == 0 {
		t.Fatalf("拉起失败应非零退出:\n%s", buf.String())
	}
	if out := buf.String(); !strings.Contains(out, "拉起") || !strings.Contains(out, "ferryman serve") {
		t.Errorf("拉起失败输出缺报告与指引:\n%s", out)
	}
}

func TestUpstreamUseMissingNameAndBadConfig(t *testing.T) {
	var buf bytes.Buffer
	if code := upstreamUse(writeUpstreamCfg(t, upstreamCfgSrc), "", &buf, nil); code != 2 {
		t.Fatalf("缺条目名应退出 2, got %d", code)
	}
	buf.Reset()
	if code := upstreamUse(filepath.Join(t.TempDir(), "absent.toml"), "a", &buf, nil); code == 0 {
		t.Fatal("配置不存在应非零退出")
	}
}

// ---- cmdUpstream 分发与 flag ----

func TestCmdUpstreamDispatch(t *testing.T) {
	f := writeUpstreamCfg(t, upstreamCfgSrc)
	var buf bytes.Buffer
	if code := cmdUpstream([]string{"list", "--config", f}, &buf); code != 0 {
		t.Fatalf("cmdUpstream list = %d\n%s", code, buf.String())
	}
	if out := buf.String(); !strings.Contains(out, "cc-switch") {
		t.Errorf("--config 未生效:\n%s", out)
	}
	buf.Reset()
	if code := cmdUpstream([]string{"bogus"}, &buf); code != 2 {
		t.Fatalf("未知子命令应退出 2, got %d", code)
	}
	buf.Reset()
	if code := cmdUpstream(nil, &buf); code != 2 {
		t.Fatalf("无子命令应退出 2, got %d", code)
	}
}

// ---- 真实 HTTP 层（停旧/健康检查探针；拉起仍注入，不真拉进程） ----

func TestUpstreamShutdownRealHTTPReleasesPort(t *testing.T) {
	var gotAuthed bool
	var srv *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("POST /shutdown", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tk" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		gotAuthed = true
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
		go srv.Close() // 同真守护：shutdown 后监听口关闭（端口释放）
	})
	srv = httptest.NewServer(mux)
	port := portOfURL(t, srv.URL)
	t.Cleanup(srv.Close)

	if err := upstreamShutdown(port, "tk"); err != nil {
		t.Fatalf("upstreamShutdown: %v", err)
	}
	if !gotAuthed {
		t.Fatal("POST /shutdown 未按 Bearer 发出")
	}
}

func TestUpstreamShutdownWrongTokenErrors(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /shutdown", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	if err := upstreamShutdown(portOfURL(t, srv.URL), "WRONG"); err == nil {
		t.Fatal("非 200 应报错")
	}
}

func TestUpstreamHealthRealHTTP(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /stats", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tk" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"version":"dev"}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	if !upstreamHealth(portOfURL(t, srv.URL), "tk", 2*time.Second) {
		t.Fatal("健康检查应通过")
	}
}

func TestUpstreamHealthDeadPortTimesOut(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close() // 拿一个确定无监听的口
	if upstreamHealth(port, "tk", 400*time.Millisecond) {
		t.Fatal("无监听口健康检查应为 false")
	}
}

func TestUpstreamWaitPortFree(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	if upstreamWaitPortFree(port, 300*time.Millisecond) {
		t.Fatal("监听中应判未释放")
	}
	_ = ln.Close()
	if !upstreamWaitPortFree(port, 2*time.Second) {
		t.Fatal("关闭后应判已释放")
	}
}

// ---- 工具 ----

func readFileUp(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func countStr(list []string, s string) int {
	n := 0
	for _, v := range list {
		if v == s {
			n++
		}
	}
	return n
}

func portOfURL(t *testing.T, raw string) int {
	t.Helper()
	i := strings.LastIndex(raw, ":")
	if i < 0 {
		t.Fatalf("URL 无端口: %q", raw)
	}
	p, err := strconv.Atoi(raw[i+1:])
	if err != nil {
		t.Fatal(err)
	}
	return p
}
