package mcp

// e2e_closeout_test.go — 票06 验收收口：
//   - 六工具全链 E2E：临时 daemon（FERRYMAN_CONFIG 指向临时 config、临时
//     端口、显式断言非生产端口），initialize → tools/list → 六件 tools/call
//     逐一走通；
//   - 反向断言总集：全部响应无消息内容样本、无凭据字段、无 token 串；
//     MCP 工具面无写操作工具；
//   - doctor"MCP 注册在位"经 MCP 面装/卸两态各断言一次（卸态＝删掉用户级
//     MCP 配置后独立重算）。
//
// 哨兵设计（让反向断言有真实暴露面，不做空洞断言）：
//   - 凭据哨兵：临时 config 追加假 provider（api_key 假值）——经
//     DoctorStructured 的 LoadProviders 真加载进内存；
//   - 消息内容哨兵：样本串埋进配置文件（文件内容若被任何层回显即触发）；
//   - token 哨兵：夹具 token（daemon 真鉴权材料）。

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ferryman/internal/installer"
)

// closeoutCredCanary / closeoutMsgCanary 哨兵常量（出现即泄漏）。
const (
	closeoutCredCanary = "sk-closeout-fake-9f3c2a"
	closeoutMsgCanary  = "对话原文样本-closeout-7b1d"
)

// closeoutDeniedKeys 键名禁出清单（递归扫响应 JSON 的键，精确匹配——
// input_tokens/cache_read_tokens 等计数字段名不在其中）：凭据字段名＋
// 消息内容字段名（daemon 响应内层；MCP 信封的 content 键是协议形状，
// 已在取 text 时剥掉）。
var closeoutDeniedKeys = map[string]string{
	"api_key": "凭据", "apikey": "凭据", "token": "凭据", "secret": "凭据",
	"password": "凭据", "authorization": "凭据", "headers": "凭据", "env": "凭据",
	"message": "消息内容", "messages": "消息内容", "conversation": "消息内容",
	"transcript": "消息内容", "prompt": "消息内容",
}

// closeoutScanKeys 递归扫响应 JSON 全部键名：禁出清单命中即泄漏。
func closeoutScanKeys(t *testing.T, where, text string) {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(text), &v); err != nil {
		t.Fatalf("%s 响应非 JSON: %v（%s）", where, err, text)
	}
	var walk func(v any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			for k, val := range x {
				if kind, bad := closeoutDeniedKeys[k]; bad {
					t.Fatalf("%s 含禁出字段名 %q（%s类）: %s", where, k, kind, text)
				}
				walk(val)
			}
		case []any:
			for _, val := range x {
				walk(val)
			}
		}
	}
	walk(v)
}

// TestSixToolsFullChainE2EAndReverseAssertions 六工具全链＋反向断言总集。
func TestSixToolsFullChainE2EAndReverseAssertions(t *testing.T) {
	e := newEnv(t, true, true)   // FERRYMAN_CONFIG→临时 config＋临时端口＋真 daemon
	assertNotProdPort(t, e.port) // 验收钉子：显式断言非生产端口（15722/15724 等）

	// 哨兵埋设：临时 config 追加假 provider 凭据＋消息内容样本（TOML 注释）。
	extra := fmt.Sprintf("\n# 会话内容哨兵: %s\n[providers.canary]\n"+
		"base_url = 'http://127.0.0.1:1'\nmodel = 'm'\napi_key = '%s'\n",
		closeoutMsgCanary, closeoutCredCanary)
	f, err := os.OpenFile(e.cfgPath, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(extra); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	// doctor 真实装配：临时 HOME（预装用户级 MCP 注册＝装态）＋空 repo，
	// providers 面向带哨兵的临时 config（凭据真进内存）。
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")
	repo := filepath.Join(tmp, "repo")
	for _, d := range []string{home, repo} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	homeCfg := installer.UserMCPConfigPath(home)
	exe := filepath.Join(tmp, "ferryman.exe")
	regJSON, _ := json.Marshal(map[string]any{"mcpServers": map[string]any{
		"ferryman": map[string]any{
			"type": "stdio", "command": exe, "args": []any{"mcp"},
			"env": map[string]any{},
		},
	}})
	if err := os.WriteFile(homeCfg, regJSON, 0o644); err != nil {
		t.Fatal(err)
	}
	s := New(e.cfg, testVersion)
	s.doctor = func() []installer.CheckResult {
		return installer.DoctorStructured(home, repo, e.cfg, e.cfgPath, false)
	}
	p := startPipesServer(t, s)

	// ① initialize 握手（全链入口）。
	initRes := resMap(t, p.call("initialize",
		map[string]any{"protocolVersion": "2025-06-18",
			"capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "t", "version": "1"}}))
	caps, ok := initRes["capabilities"].(map[string]any)
	if !ok {
		t.Fatalf("capabilities 缺失: %v", initRes)
	}
	if _, ok := caps["tools"]; !ok {
		t.Fatalf("capabilities.tools 缺失: %v", caps)
	}
	p.send(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)

	// ② tools/list：恰好六件只读工具；反向断言——无写操作工具（白名单集合
	// 之外的任何工具（含写动词命名）都过不了集合等值关；写动词扫描双保险）。
	tools := listedTools(t, p)
	want := map[string]bool{"sessions": true, "session_detail": true, "gate_check": true,
		"cost_report": true, "heartbeat_status": true, "doctor": true}
	if len(tools) != len(want) {
		t.Fatalf("工具数 = %d, want %d: %v", len(tools), len(want), tools)
	}
	for n := range tools {
		if !want[n] {
			t.Fatalf("非白名单工具 %q（工具面应只含六件只读工具）: %v", n, tools)
		}
		for _, verb := range []string{"write", "delete", "update", "create", "install",
			"stop", "kill", "remove", "push", "patch", "mutate", "drop"} {
			if strings.Contains(n, verb) {
				t.Fatalf("工具名 %q 含写动词（写操作工具禁入）", n)
			}
		}
	}

	// ③ 六件逐一调用（全链：MCP server → daemon 只读 GET / 进程内 doctor）。
	cases := []struct {
		name string
		args map[string]any
	}{
		{"sessions", nil},
		{"session_detail", map[string]any{"session_id": "sd-1"}},
		{"gate_check", nil}, // 无 session_id＝汇总模式
		{"cost_report", map[string]any{"scope": "project", "key": "C:/proj"}},
		{"heartbeat_status", nil},
		{"doctor", nil},
	}
	texts := map[string]string{}
	for _, tc := range cases {
		text, isErr, errObj := p.callTool(tc.name, tc.args)
		if errObj != nil {
			t.Fatalf("%s 产生协议级 error: %v", tc.name, errObj)
		}
		if isErr {
			t.Fatalf("%s 工具报错: %s", tc.name, text)
		}
		texts[tc.name] = text
	}

	// ④ 反向断言总集：无 token 串、无凭据类字样、无哨兵（凭据/消息内容）、
	// 无禁出字段名。
	for name, text := range texts {
		if strings.Contains(text, e.token) {
			t.Fatalf("%s 响应泄漏 token 串: %s", name, text)
		}
		if strings.Contains(text, closeoutCredCanary) {
			t.Fatalf("%s 响应泄漏凭据哨兵: %s", name, text)
		}
		if strings.Contains(text, closeoutMsgCanary) {
			t.Fatalf("%s 响应泄漏消息内容样本: %s", name, text)
		}
		for _, bad := range []string{"api_key", "Bearer", "Authorization"} {
			if strings.Contains(text, bad) {
				t.Fatalf("%s 响应含凭据类字样 %q: %s", name, bad, text)
			}
		}
		closeoutScanKeys(t, name, text)
	}

	// ⑤ doctor 装态：mcp_registration＝pass（经 MCP 面的装态断言）。
	dr := parseDoctorResp(t, texts["doctor"])
	if st, detail := findCheck(t, dr, "mcp_registration"); st != "pass" {
		t.Fatalf("装态 mcp_registration 应 pass: %s（%s）", st, detail)
	}

	// ⑥ 卸态：删掉用户级 MCP 配置 → doctor 独立重算（不缓存）→ fail 并说明。
	if err := os.Remove(homeCfg); err != nil {
		t.Fatal(err)
	}
	text, isErr, errObj := p.callTool("doctor", nil)
	if errObj != nil {
		t.Fatalf("卸态 doctor 不应产生协议级 error: %v", errObj)
	}
	if isErr {
		t.Fatalf("卸态 doctor 仍应返回结构化结果（不是工具错误）: %s", text)
	}
	dr2 := parseDoctorResp(t, text)
	if st, detail := findCheck(t, dr2, "mcp_registration"); st != "fail" || !strings.Contains(detail, "不在位") {
		t.Fatalf("卸态 mcp_registration 应 fail 并说明: %s（%s）", st, detail)
	}
	if strings.Contains(text, e.token) || strings.Contains(text, closeoutCredCanary) ||
		strings.Contains(text, closeoutMsgCanary) {
		t.Fatalf("卸态 doctor 响应泄漏哨兵: %s", text)
	}
}
