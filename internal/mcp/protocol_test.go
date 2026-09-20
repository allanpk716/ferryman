package mcp

// protocol_test.go — 票04 协议面验收钉子：initialize（capabilities.tools 与
// serverInfo）、tools/list（恰好五件只读工具＋反向断言无写操作工具）、描述
// 中文且直接引用 CONTEXT.md 词条原文（D12）、通知静默、协议错误码、参数校验。

import (
	"strings"
	"testing"
)

// TestInitializeAdvertisesToolsCapability initialize 三方法发现面：协议版本
// 回显（客户端版本受支持时）、capabilities.tools 在位、serverInfo 完整。
func TestInitializeAdvertisesToolsCapability(t *testing.T) {
	e := newEnv(t, false, false) // 协议面不依赖 daemon
	p := startPipes(t, e.cfg)
	resp := p.call("initialize", map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "test-client", "version": "1"},
	})
	res := resMap(t, resp)
	if pv, _ := res["protocolVersion"].(string); pv != "2024-11-05" {
		t.Fatalf("protocolVersion = %q, want 回显受支持版本 2024-11-05", pv)
	}
	caps, ok := res["capabilities"].(map[string]any)
	if !ok {
		t.Fatalf("capabilities 缺失: %v", res)
	}
	if _, ok := caps["tools"]; !ok {
		t.Fatalf("capabilities.tools 缺失（工具能力未声明）: %v", caps)
	}
	si, ok := res["serverInfo"].(map[string]any)
	if !ok || si["name"] != "ferryman" {
		t.Fatalf("serverInfo.name 应为 ferryman: %v", si)
	}
	if v, _ := si["version"].(string); v == "" {
		t.Fatalf("serverInfo.version 为空: %v", si)
	}
}

// TestInitializeUnknownVersionFallsBack 客户端版本不受支持 → 回服务端基线版本。
func TestInitializeUnknownVersionFallsBack(t *testing.T) {
	e := newEnv(t, false, false)
	p := startPipes(t, e.cfg)
	res := resMap(t, p.call("initialize", map[string]any{"protocolVersion": "1999-01-01"}))
	if pv, _ := res["protocolVersion"].(string); pv != "2024-11-05" {
		t.Fatalf("未知版本应回落 2024-11-05, got %q", pv)
	}
}

// TestInitializeWithoutParams 缺省 initialize（params 空）也应可答。
func TestInitializeWithoutParams(t *testing.T) {
	e := newEnv(t, false, false)
	p := startPipes(t, e.cfg)
	res := resMap(t, p.call("initialize", nil))
	caps, ok := res["capabilities"].(map[string]any)
	if !ok {
		t.Fatalf("capabilities 缺失: %v", res)
	}
	if _, ok := caps["tools"]; !ok {
		t.Fatalf("capabilities.tools 缺失: %v", caps)
	}
}

// TestToolsListExactlyFiveReadOnlyTools 验收钉子：恰好五件（doctor 在票05），
// 每件有描述与 object inputSchema；反向断言无任何写操作工具。
func TestToolsListExactlyFiveReadOnlyTools(t *testing.T) {
	e := newEnv(t, false, false)
	p := startPipes(t, e.cfg)
	tools := listedTools(t, p)
	want := []string{"sessions", "session_detail", "gate_check", "cost_report",
		"heartbeat_status"}
	if len(tools) != len(want) {
		t.Fatalf("工具数 = %d, want %d（doctor 在票05 不在本票）: %v",
			len(tools), len(want), tools)
	}
	for _, w := range want {
		tm, ok := tools[w]
		if !ok {
			t.Fatalf("缺工具 %s: %v", w, tools)
		}
		if d, _ := tm["description"].(string); d == "" {
			t.Fatalf("工具 %s 无描述", w)
		}
		sch, ok := tm["inputSchema"].(map[string]any)
		if !ok || sch["type"] != "object" {
			t.Fatalf("工具 %s inputSchema 非 object: %v", w, tm["inputSchema"])
		}
	}
	// 反向断言：doctor 不在本票；工具面不存在写操作工具。
	if _, ok := tools["doctor"]; ok {
		t.Fatal("doctor 不应在本票出现（票05）")
	}
	for name := range tools {
		for _, verb := range []string{"write", "create", "update", "delete",
			"remove", "install", "stop", "kill", "restart", "push", "patch",
			"save", "ctl"} {
			if strings.Contains(name, verb) {
				t.Fatalf("疑似写操作工具 %q（工具面必须纯只读）", name)
			}
		}
	}
}

// TestToolDescriptionsQuoteContextTerms D12：描述中文且直接引用 CONTEXT.md
// 词条原文（不造第二套翻译）——逐工具钉引用片段。
func TestToolDescriptionsQuoteContextTerms(t *testing.T) {
	e := newEnv(t, false, false)
	p := startPipes(t, e.cfg)
	desc := map[string]string{}
	for name, tm := range listedTools(t, p) {
		desc[name], _ = tm["description"].(string)
	}
	quotes := map[string][]string{
		"sessions": { // 台账＋凉会话
			"闲置判定的唯一事实源，判定时不解析 jsonl",
			"闲置时长超过拦截阈值的会话；缓存大概率已失效，继续使用即全量重付",
		},
		"session_detail": { // 有效交接
			"与被拦会话同 Agent 且同项目目录、状态为 fresh 或骨架、覆盖截止不早于台账最后写入的交接；闸门 block 的唯一依据",
		},
		"gate_check": { // 拦截阈值
			"判定会话变凉、闸门开始拒绝的闲置时长；默认 35 分钟（E0a 实测拐点 +5min 余量），按 Agent/服务商可配",
		},
		"cost_report": { // 成效账
			"report 层按版本化反事实公式算出的节省额；只有真实兑现的避免才计节省，bypass 绕过与无效保温单列",
		},
		"heartbeat_status": { // 心跳＋等待窗口＋等答复窗口（问询窗）
			"等待窗口内对主会话缓存前缀的定期体外重放",
			"主会话因等待在飞子代理而闲置的起止区间",
			"问询守望命中后挂在会话上的窗口态",
		},
	}
	for name, qs := range quotes {
		for _, q := range qs {
			if !strings.Contains(desc[name], q) {
				t.Fatalf("工具 %s 描述未引用 CONTEXT.md 词条原文片段 %q", name, q)
			}
		}
	}
}

// TestNotificationSilentlyConsumed 通知（无 id）不产生任何回话——随后 ping 的
// 响应是第一条到达的响应（id 校验即证据）。
func TestNotificationSilentlyConsumed(t *testing.T) {
	e := newEnv(t, false, false)
	p := startPipes(t, e.cfg)
	p.send(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	p.send(`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":1}}`)
	res := resMap(t, p.call("ping", nil)) // 若通知被误答，id 校验在此炸出
	if len(res) != 0 {
		t.Fatalf("ping result 应为空对象: %v", res)
	}
}

// TestProtocolErrors 协议级错误码：坏 JSON -32700、未知方法 -32601、
// 未知工具（含 doctor）-32602。
func TestProtocolErrors(t *testing.T) {
	e := newEnv(t, false, false)
	p := startPipes(t, e.cfg)
	p.send("{not json")
	if code := errCode(t, p.recv()); code != -32700 {
		t.Fatalf("坏 JSON 应 -32700, got %d", code)
	}
	if code := errCode(t, p.call("resources/list", nil)); code != -32601 {
		t.Fatalf("未知方法应 -32601, got %d", code)
	}
	_, _, errObj := p.callTool("doctor", nil)
	if errObj == nil {
		t.Fatal("未知工具应有协议级 error")
	}
	if code := errCode(t, map[string]any{"error": errObj}); code != -32602 {
		t.Fatalf("未知工具应 -32602, got %d", code)
	}
}

// TestToolBadArgsIsError 参数面：类型错/未知参数/缺必需参数 → MCP isError
// 语义的明确错误（转发前拦下，不触 daemon）。
func TestToolBadArgsIsError(t *testing.T) {
	e := newEnv(t, false, false) // 无 daemon：参数错误若误走 HTTP 会变成不可达错
	p := startPipes(t, e.cfg)
	for _, tc := range []struct {
		name string
		args map[string]any
		want string
	}{
		{"sessions", map[string]any{"limit": "abc"}, "limit"},
		{"sessions", map[string]any{"limit": 1.5}, "limit"},
		{"sessions", map[string]any{"who": "x"}, "who"},
		{"session_detail", map[string]any{}, "session_id"},
		{"cost_report", map[string]any{"scope": "project"}, "key"},
	} {
		text, isErr, _ := p.callTool(tc.name, tc.args)
		if !isErr {
			t.Fatalf("%s(%v) 应报参数错误, got: %s", tc.name, tc.args, text)
		}
		if !strings.Contains(text, tc.want) {
			t.Fatalf("%s(%v) 错误文案应含 %q: %s", tc.name, tc.args, tc.want, text)
		}
	}
}
