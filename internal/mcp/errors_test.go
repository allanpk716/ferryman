package mcp

// errors_test.go — 票04 错误面验收钉子：daemon 不可达 → 五工具均明确报错
// （MCP isError 语义，文案含拉起途径提示）；连续两次调用均报错（不缓存）；
// 全程无新 daemon 进程/监听产生（不自举）；token 缺失同样明确报错；任何
// 错误文本不回显 token。

import (
	"fmt"
	"net"
	"strings"
	"testing"
	"time"
)

// TestDaemonUnreachableAllToolsErrorNoCacheNoBootstrap daemon 不在（token 在、
// 端口无人听）：五工具各一发均明确报错；sessions 连打两次仍报错（不缓存）；
// 端口全程无监听（不自举）。
func TestDaemonUnreachableAllToolsErrorNoCacheNoBootstrap(t *testing.T) {
	e := newEnv(t, false, true)
	p := startPipes(t, e.cfg)
	tools := []struct {
		name string
		args map[string]any
	}{
		{"sessions", nil},
		{"session_detail", map[string]any{"session_id": "sd-1"}},
		{"gate_check", nil},
		{"cost_report", map[string]any{"scope": "month", "key": "2026-09"}},
		{"heartbeat_status", nil},
	}
	for _, tc := range tools {
		text, isErr, _ := p.callTool(tc.name, tc.args)
		if !isErr {
			t.Fatalf("%s 在 daemon 不可达时应报错（isError）, got: %s", tc.name, text)
		}
		if !strings.Contains(text, pullUpHint) {
			t.Fatalf("%s 错误文案缺拉起途径提示 %q: %s", tc.name, pullUpHint, text)
		}
		if strings.Contains(text, e.token) {
			t.Fatalf("%s 错误文案泄漏 token: %s", tc.name, text)
		}
	}
	// 不缓存：同一工具连续两次调用均报错（缓存了失败态或成功态都会露馅）。
	for i := 1; i <= 2; i++ {
		text, isErr, _ := p.callTool("sessions", nil)
		if !isErr || !strings.Contains(text, pullUpHint) {
			t.Fatalf("sessions 第 %d 次调用应仍报错且含提示: isErr=%v text=%s",
				i, isErr, text)
		}
	}
	// 不自举：全程无新监听产生（端口仍无人听）。
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", e.port),
		500*time.Millisecond)
	if err == nil {
		conn.Close()
		t.Fatalf("端口 %d 出现监听——MCP server 不得自举 daemon", e.port)
	}
}

// TestDaemonTokenMissingIsClearError token 文件不存在（daemon 从未在此数据目录
// 启动过）：明确报错＋提示拉起途径，不回显任何密物。
func TestDaemonTokenMissingIsClearError(t *testing.T) {
	e := newEnv(t, false, false)
	p := startPipes(t, e.cfg)
	text, isErr, _ := p.callTool("sessions", nil)
	if !isErr {
		t.Fatalf("token 缺失应报错, got: %s", text)
	}
	if !strings.Contains(text, "daemon.token") {
		t.Fatalf("错误文案应指明 daemon.token 缺失: %s", text)
	}
	if !strings.Contains(text, pullUpHint) {
		t.Fatalf("错误文案缺拉起途径提示: %s", text)
	}
}

// TestDaemonHTTPErrorSurfacesIsError daemon 活着但端点回非 2xx（如 session
// 不存在 404）：工具面如实转错误，不伪造成成功。
func TestDaemonHTTPErrorSurfacesIsError(t *testing.T) {
	e := newEnv(t, true, true)
	p := startPipes(t, e.cfg)
	text, isErr, _ := p.callTool("session_detail",
		map[string]any{"session_id": "no-such-session"})
	if !isErr {
		t.Fatalf("404 应转 isError, got: %s", text)
	}
	if !strings.Contains(text, "session not found") {
		t.Fatalf("错误文案应含 daemon 原始错误: %s", text)
	}
}
