package mcp

// tool_doctor_test.go — 票05 验收钉子：第六件只读工具 doctor。
//
//   - 工具注册在位（tools/list 可发现）；
//   - 响应逐项结构化三要素（名称/状态/说明）＋汇总计数；
//   - daemon 在线（临时 daemon）活性项 pass；daemon 不可达时活性项 fail 且
//     仍返回完整结构化结果（不是工具错误——doctor 例外语义，规格「错误面」节）；
//   - 两次调用独立重算（不缓存）；全程无新进程/监听（不自举）；
//   - 真实装配面向临时 HOME/临时 config（residency=false，零子进程/零注册表
//     读——绝不读真实用户目录）；测试端口非生产端口（夹具 assertNotProdPort
//     ＋响应文本反向断言）；
//   - 反向断言：响应不含 token、不含凭据字段。
//
// doctor 进程内复用 installer 的结构化体检（D6），不经 HTTP——真实链路用
// installer.DoctorStructured 面向临时环境装配（Server.doctor 可注入缝）。

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ferryman/internal/config"
	"ferryman/internal/installer"
)

// startPipesServer 与 mcp_test.go 的 startPipes 同形，但收现成 Server
// （票05：Server.doctor 可注入缝须在 Serve 前替换；原夹具不动——路径外只读）。
func startPipesServer(t *testing.T, s *Server) *mcpPipes {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	p := &mcpPipes{t: t, inW: inW, out: bufio.NewReader(outR), done: make(chan error, 1)}
	go func() { p.done <- s.Serve(context.Background(), inR, outW) }()
	t.Cleanup(func() { _ = inW.Close() })
	return p
}

// doctorResp doctor 工具响应形状（checks 逐项三要素 + summary 计数）。
type doctorResp struct {
	Checks []struct {
		Name   string `json:"name"`
		Status string `json:"status"`
		Detail string `json:"detail"`
	} `json:"checks"`
	Summary struct {
		Total      int `json:"total"`
		Pass       int `json:"pass"`
		Fail       int `json:"fail"`
		NotChecked int `json:"not_checked"`
	} `json:"summary"`
}

// parseDoctorResp 工具 text → doctorResp（形状钉子）。
func parseDoctorResp(t *testing.T, text string) doctorResp {
	t.Helper()
	var dr doctorResp
	mustJSON(t, text, &dr)
	if len(dr.Checks) == 0 {
		t.Fatalf("checks 为空: %s", text)
	}
	legal := map[string]bool{"pass": true, "fail": true, "not_checked": true}
	pass, fail, nc := 0, 0, 0
	for _, c := range dr.Checks {
		if c.Name == "" || c.Detail == "" || !legal[c.Status] {
			t.Fatalf("三要素不齐: %+v (text=%s)", c, text)
		}
		switch c.Status {
		case "pass":
			pass++
		case "fail":
			fail++
		default:
			nc++
		}
	}
	if dr.Summary.Total != len(dr.Checks) || dr.Summary.Pass != pass ||
		dr.Summary.Fail != fail || dr.Summary.NotChecked != nc {
		t.Fatalf("summary 计数不符: %+v (逐项重算 pass=%d fail=%d not_checked=%d)",
			dr.Summary, pass, fail, nc)
	}
	return dr
}

// findCheck 按名取检查项（缺失即 fatal）。
func findCheck(t *testing.T, dr doctorResp, name string) (status, detail string) {
	t.Helper()
	for _, c := range dr.Checks {
		if c.Name == name {
			return c.Status, c.Detail
		}
	}
	t.Fatalf("缺检查项 %s: %+v", name, dr.Checks)
	return "", ""
}

// noSecrets 反向断言：文本不含夹具 token、凭据字段、生产端口串。
func noSecrets(t *testing.T, e *mcpEnv, where, text string) {
	t.Helper()
	for _, bad := range []string{e.token, "api_key", "Bearer", "Authorization",
		"15721", "15722", "15724", "7311", "15900"} {
		if strings.Contains(text, bad) {
			t.Fatalf("%s 响应含禁出串 %q: %s", where, bad, text)
		}
	}
}

// TestDoctorToolListedAndDescribed 工具注册在位：tools/list 可发现 doctor，
// 有中文描述（点名 daemon 活性语义）与 object inputSchema。
func TestDoctorToolListedAndDescribed(t *testing.T) {
	e := newEnv(t, false, false)
	p := startPipes(t, e.cfg)
	tools := listedTools(t, p)
	tm, ok := tools["doctor"]
	if !ok {
		t.Fatalf("doctor 未注册: %v", tools)
	}
	desc, _ := tm["description"].(string)
	if !strings.Contains(desc, "daemon 活性") || !strings.Contains(desc, "不缓存") {
		t.Fatalf("doctor 描述应点名 daemon 活性与不缓存语义: %s", desc)
	}
	sch, ok := tm["inputSchema"].(map[string]any)
	if !ok || sch["type"] != "object" {
		t.Fatalf("doctor inputSchema 非 object: %v", tm["inputSchema"])
	}
	if ap, ok := sch["additionalProperties"].(bool); !ok || ap {
		t.Fatalf("doctor inputSchema 应 additionalProperties=false: %v", sch)
	}
}

// TestDoctorToolStructuredOutputStub 进程内调用注入桩：逐项三要素原样透传
// （pass/fail/not_checked 三态都要能过工具面）、summary 计数推导一致、响应
// 非工具错误。
func TestDoctorToolStructuredOutputStub(t *testing.T) {
	e := newEnv(t, false, false)
	s := New(e.cfg)
	s.doctor = func() []installer.CheckResult {
		return []installer.CheckResult{
			{Name: "cc_hooks", Status: installer.StatusPass, Detail: "settings.json 四钩子在位"},
			{Name: "daemon_liveness", Status: installer.StatusFail, Detail: "daemon 未运行（钩子自举会拉起，或手动 start-daemon.cmd）"},
			{Name: "autostart", Status: installer.StatusNotChecked, Detail: "Run 键自启未检查（检查目标未装配——如实标注不伪造）"},
		}
	}
	p := startPipesServer(t, s)
	text, isErr, errObj := p.callTool("doctor", nil)
	if errObj != nil {
		t.Fatalf("doctor 不应产生协议级 error: %v", errObj)
	}
	if isErr {
		t.Fatalf("doctor 正常路径不应 isError: %s", text)
	}
	dr := parseDoctorResp(t, text)
	if st, _ := findCheck(t, dr, "daemon_liveness"); st != "fail" {
		t.Fatalf("daemon_liveness 应 fail, got %s", st)
	}
	if st, _ := findCheck(t, dr, "autostart"); st != "not_checked" {
		t.Fatalf("autostart 应 not_checked, got %s", st)
	}
	noSecrets(t, e, "doctor", text)
}

// TestDoctorToolNoCacheTwoCallsRecompute 不缓存：两次调用独立重算——桩按
// 调用次数吐不同 Detail，第二次必须看到第二次的计算结果。
func TestDoctorToolNoCacheTwoCallsRecompute(t *testing.T) {
	e := newEnv(t, false, false)
	s := New(e.cfg)
	n := 0
	s.doctor = func() []installer.CheckResult {
		n++
		return []installer.CheckResult{{
			Name: "daemon_liveness", Status: installer.StatusPass,
			Detail: fmt.Sprintf("第 %d 次独立重算", n)}}
	}
	p := startPipesServer(t, s)
	text1, isErr, _ := p.callTool("doctor", nil)
	if isErr || !strings.Contains(text1, "第 1 次独立重算") {
		t.Fatalf("第一次调用应现算第 1 次: isErr=%v text=%s", isErr, text1)
	}
	text2, isErr, _ := p.callTool("doctor", nil)
	if isErr || !strings.Contains(text2, "第 2 次独立重算") {
		t.Fatalf("第二次调用应独立重算（不得回放缓存）: isErr=%v text=%s", isErr, text2)
	}
}

// TestDoctorToolRejectsUnknownArg doctor 无参数：带未知参数 → isError 明确报错。
func TestDoctorToolRejectsUnknownArg(t *testing.T) {
	e := newEnv(t, false, false)
	s := New(e.cfg)
	s.doctor = func() []installer.CheckResult {
		t.Fatal("带未知参数不应执行检查")
		return nil
	}
	p := startPipesServer(t, s)
	text, isErr, _ := p.callTool("doctor", map[string]any{"verbose": true})
	if !isErr {
		t.Fatalf("未知参数应 isError, got: %s", text)
	}
	if !strings.Contains(text, "verbose") {
		t.Fatalf("错误文案应点名未知参数: %s", text)
	}
}

// realDepsDoctor 真实装配面向临时环境：临时 HOME/空 repo/夹具临时 config
// （providers 无配置 → 该项确定性 fail）；residency=false 零子进程零注册表。
func realDepsDoctor(t *testing.T, cfg *config.Config, cfgPath string) func() []installer.CheckResult {
	t.Helper()
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")
	repo := filepath.Join(tmp, "repo") // 空：脚本/钩子缺失 → 确定性 fail
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	return func() []installer.CheckResult {
		return installer.DoctorStructured(home, repo, cfg, cfgPath, false)
	}
}

// TestDoctorToolRealDepsDaemonOnline 真实链路 E2E（daemon 在线）：临时 daemon
// ＋真实 DoctorStructured 装配——daemon_liveness pass、完整结构化结果、
// 无 token/凭据/生产端口串。
func TestDoctorToolRealDepsDaemonOnline(t *testing.T) {
	e := newEnv(t, true, true)
	s := New(e.cfg)
	s.doctor = realDepsDoctor(t, e.cfg, e.cfgPath)
	p := startPipesServer(t, s)
	text, isErr, errObj := p.callTool("doctor", nil)
	if errObj != nil {
		t.Fatalf("doctor 不应产生协议级 error: %v", errObj)
	}
	if isErr {
		t.Fatalf("daemon 在线时不应 isError: %s", text)
	}
	dr := parseDoctorResp(t, text)
	if st, _ := findCheck(t, dr, "daemon_liveness"); st != "pass" {
		t.Fatalf("临时 daemon 在线活性应 pass: %s", text)
	}
	noSecrets(t, e, "doctor-online", text)
}

// TestDoctorToolRealDepsDaemonOffline doctor 例外语义（daemon 不可达）：仍返回
// 完整结构化结果（不是工具错误）、daemon_liveness＝fail；两次调用结果一致
// （不缓存不伪造）；端口全程无监听（不自举）。
func TestDoctorToolRealDepsDaemonOffline(t *testing.T) {
	e := newEnv(t, false, true) // token 在、无 daemon——不可达而非 token 缺失
	s := New(e.cfg)
	s.doctor = realDepsDoctor(t, e.cfg, e.cfgPath)
	p := startPipesServer(t, s)
	for i := 1; i <= 2; i++ {
		text, isErr, errObj := p.callTool("doctor", nil)
		if errObj != nil {
			t.Fatalf("doctor 不可达时也不产生协议级 error（例外语义）: %v", errObj)
		}
		if isErr {
			t.Fatalf("第 %d 次：daemon 不可达应照常返回结构化结果（不是工具错误）: %s", i, text)
		}
		dr := parseDoctorResp(t, text)
		st, detail := findCheck(t, dr, "daemon_liveness")
		if st != "fail" {
			t.Fatalf("daemon 不可达活性应 fail, got %s", st)
		}
		if !strings.Contains(detail, "未运行") {
			t.Fatalf("活性项说明应如实: %s", detail)
		}
		noSecrets(t, e, "doctor-offline", text)
	}
	// 不自举：全程无新监听产生（端口仍无人听）。
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", e.port),
		500*time.Millisecond)
	if err == nil {
		conn.Close()
		t.Fatalf("端口 %d 出现监听——doctor 不得自举 daemon", e.port)
	}
}
