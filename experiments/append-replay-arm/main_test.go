// main_test.go — 票04:实跳臂工具的端到端测试(loopback 假渡口,不触外网)。
//
// 覆盖验收口径:
//   - 真实发送全流程(construct→send→evaluate→writeback)四步齐、结论 passed
//     落状态文件,缝同形可用;
//   - dry-run 不联网走通全流程:两跳按未真发留档,评估 inconclusive,
//     writeback 拒写(状态文件不产生);
//   - 上游 5xx:发送步照常完成(只报事实),评估 inconclusive,writeback 拒写;
//   - failed 结论可回写且启用=false(状态机"未过→保持未启用");
//   - 证据留档形状:四列 token + stop_reason + 输出全文(缓存写列如实 null)。
package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"ferryman/internal/ferry"
)

const armTestBody = `{"model":"glm-5.3","max_tokens":32000,"messages":[{"role":"user","content":"测试会话正文"}]}`

const armTestGoodMD = "开场\n<<<INJECT>>>\n目标:过臂\n<<</INJECT>>>\n# 目标\n过臂\n# 续接第一句话\n接着干。"

// writeArmSnapshot 落一份快照文件(body 内联形态)。
func writeArmSnapshot(t *testing.T, dir string) string {
	t.Helper()
	p := filepath.Join(dir, "snap.json")
	raw, err := json.Marshal(armSnapshot{
		SessionID: "arm-e2e",
		Body:      armTestBody,
		Headers:   map[string]string{"content-type": "application/json"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// sseData 一条 SSE data 事件(空行收尾)。
func sseData(v any) string {
	b, _ := json.Marshal(v)
	return "data: " + string(b) + "\n\n"
}

// newFakeDock 假渡口:按请求体 max_tokens 分流(1=基线跳,其他=追加跳)。
// mode: "good" = 两跳都成、追加跳产出合格交接 MD;"err500" = 恒 500。
func newFakeDock(t *testing.T, mode string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if mode == "err500" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		var body struct {
			MaxTokens int `json:"max_tokens"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if body.MaxTokens == 1 { // 基线跳:心跳式原样重放(parseSSEUsage 口径)
			sse := sseData(map[string]any{"type": "message_start", "message": map[string]any{
				"usage": map[string]any{"input_tokens": 10, "cache_read_input_tokens": 1990, "output_tokens": 1}}}) +
				sseData(map[string]any{"type": "message_delta",
					"delta": map[string]any{"stop_reason": "end_turn"},
					"usage": map[string]any{"input_tokens": 10, "cache_read_input_tokens": 1990, "output_tokens": 1}})
			_, _ = w.Write([]byte(sse))
			return
		}
		// 追加跳:完整消息(parseSSEMessage 口径)+ 合格交接 MD。
		sse := sseData(map[string]any{"type": "message_start", "message": map[string]any{
			"usage": map[string]any{"input_tokens": 10, "cache_read_input_tokens": 1990, "output_tokens": 0}}}) +
			sseData(map[string]any{"type": "content_block_delta",
				"delta": map[string]any{"type": "text_delta", "text": armTestGoodMD}}) +
			sseData(map[string]any{"type": "message_delta",
				"delta": map[string]any{"stop_reason": "end_turn"},
				"usage": map[string]any{"input_tokens": 10, "cache_read_input_tokens": 1990, "output_tokens": 800}})
		_, _ = w.Write([]byte(sse))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func armRun(t *testing.T, args ...string) int {
	t.Helper()
	return run(args, os.Stdout, os.Stderr)
}

func readJSON(t *testing.T, p string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("读证据 %s: %v", p, err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("证据非 JSON: %v", err)
	}
	return m
}

func TestFullFlowRealSendPassed(t *testing.T) {
	dir := t.TempDir()
	snap := writeArmSnapshot(t, dir)
	srv := newFakeDock(t, "good")
	state := filepath.Join(dir, "arm_verdict.jsonl")

	if rc := armRun(t, "construct", "-snapshot", snap, "-workdir", dir, "-upstream", "zhipu"); rc != 0 {
		t.Fatalf("construct 退出码 %d", rc)
	}
	if rc := armRun(t, "send", "-dock-url", srv.URL, "-snapshot", snap, "-workdir", dir); rc != 0 {
		t.Fatalf("send 退出码 %d", rc)
	}
	if rc := armRun(t, "evaluate", "-snapshot", snap, "-workdir", dir, "-upstream", "zhipu"); rc != 0 {
		t.Fatalf("evaluate 退出码 %d", rc)
	}
	// 评估证据:passed、四条全过。
	ev := readJSON(t, filepath.Join(dir, evaluateName))
	evv := ev["evaluation"].(map[string]any)
	if evv["verdict"] != ferry.ArmVerdictPassed {
		t.Fatalf("verdict = %v, want passed(细节: %v)", evv["verdict"], evv["criteria"])
	}
	// 发送证据:四列 token + stop_reason + 输出全文留档;缓存写列如实 null。
	sr := readJSON(t, filepath.Join(dir, sendName))
	app := sr["append"].(map[string]any)
	if _, ok := app["cache_write_tokens"]; !ok {
		t.Fatal("发送证据缺 cache_write_tokens 列(应留档为 null)")
	}
	if app["cache_write_tokens"] != nil {
		t.Fatalf("cache_write_tokens 应为 null(发送器未暴露), got %v", app["cache_write_tokens"])
	}
	if app["stop_reason"] != "end_turn" || app["text"] == "" {
		t.Fatalf("stop_reason/输出全文应留档: %v", app)
	}
	if app["input_tokens"].(float64) != 10 || app["cache_read_tokens"].(float64) != 1990 {
		t.Fatalf("usage 列应原样留档: %v", app)
	}
	// 回写:passed → 状态文件生效,缝同形可查。
	if rc := armRun(t, "writeback", "-workdir", dir, "-state", state, "-note", "e2e"); rc != 0 {
		t.Fatalf("writeback 退出码 %d", rc)
	}
	recs, err := ferry.LoadArmVerdicts(state)
	if err != nil {
		t.Fatal(err)
	}
	rec, ok := recs["zhipu"]
	if !ok || rec.Verdict != ferry.ArmStatePassed {
		t.Fatalf("zhipu 结论 = %v,%v; want passed", ok, rec.Verdict)
	}
	has, enabled := ferry.ArmVerdictResolverFor(state)("zhipu")
	if !has || !enabled {
		t.Fatalf("缝查询: has=%v enabled=%v, want true true", has, enabled)
	}
	// status 子命令可读。
	if rc := armRun(t, "status", "-state", state); rc != 0 {
		t.Fatalf("status 退出码 %d", rc)
	}
}

func TestDryRunWalksFullFlowWithoutNetwork(t *testing.T) {
	dir := t.TempDir()
	snap := writeArmSnapshot(t, dir)
	state := filepath.Join(dir, "arm_verdict.jsonl")

	if rc := armRun(t, "construct", "-snapshot", snap, "-workdir", dir, "-upstream", "zhipu"); rc != 0 {
		t.Fatalf("construct 退出码 %d", rc)
	}
	if rc := armRun(t, "send", "-dry-run", "-snapshot", snap, "-workdir", dir); rc != 0 {
		t.Fatalf("send(dry-run) 退出码 %d", rc)
	}
	sr := readJSON(t, filepath.Join(dir, sendName))
	if sr["dry_run"] != true {
		t.Fatal("发送证据应标记 dry_run")
	}
	// dry-run 一步都不触网:不传 -dock-url(默认地址不应被连接)。
	if rc := armRun(t, "evaluate", "-snapshot", snap, "-workdir", dir, "-upstream", "zhipu"); rc != 0 {
		t.Fatalf("evaluate 退出码 %d", rc)
	}
	ev := readJSON(t, filepath.Join(dir, evaluateName))
	evv := ev["evaluation"].(map[string]any)
	if evv["verdict"] != ferry.ArmVerdictInconclusive {
		t.Fatalf("dry-run 判定应 inconclusive, got %v", evv["verdict"])
	}
	// ②不依赖网络:照常判过。
	for _, c := range evv["criteria"].([]any) {
		cm := c.(map[string]any)
		if cm["name"] == ferry.ArmCritDiff && cm["status"] != ferry.ArmStatusPass {
			t.Fatalf("②应照判过, got %v", cm["status"])
		}
	}
	// writeback 拒写:退出码 1,状态文件不产生。
	if rc := armRun(t, "writeback", "-workdir", dir, "-state", state); rc != 1 {
		t.Fatalf("writeback 应拒写(退出码 1), got %d", rc)
	}
	if _, err := os.Stat(state); !os.IsNotExist(err) {
		t.Fatal("拒写后状态文件不应存在")
	}
}

func TestUpstreamErrorInconclusiveNoWriteback(t *testing.T) {
	dir := t.TempDir()
	snap := writeArmSnapshot(t, dir)
	srv := newFakeDock(t, "err500")
	state := filepath.Join(dir, "arm_verdict.jsonl")

	if rc := armRun(t, "construct", "-snapshot", snap, "-workdir", dir, "-upstream", "zhipu"); rc != 0 {
		t.Fatalf("construct 退出码 %d", rc)
	}
	// 发送步本身完成(发送器只报事实,不炸步骤)。
	if rc := armRun(t, "send", "-dock-url", srv.URL, "-snapshot", snap, "-workdir", dir); rc != 0 {
		t.Fatalf("send 退出码 %d", rc)
	}
	if rc := armRun(t, "evaluate", "-snapshot", snap, "-workdir", dir, "-upstream", "zhipu"); rc != 0 {
		t.Fatalf("evaluate 退出码 %d", rc)
	}
	ev := readJSON(t, filepath.Join(dir, evaluateName))
	if ev["evaluation"].(map[string]any)["verdict"] != ferry.ArmVerdictInconclusive {
		t.Fatalf("上游全败应 inconclusive, got %v", ev["evaluation"].(map[string]any)["verdict"])
	}
	if rc := armRun(t, "writeback", "-workdir", dir, "-state", state); rc != 1 {
		t.Fatalf("writeback 应拒写, got %d", rc)
	}
}

func TestFailedVerdictWritebackKeepsDisabled(t *testing.T) {
	dir := t.TempDir()
	snap := writeArmSnapshot(t, dir)
	state := filepath.Join(dir, "arm_verdict.jsonl")

	if rc := armRun(t, "construct", "-snapshot", snap, "-workdir", dir, "-upstream", "zhipu"); rc != 0 {
		t.Fatalf("construct 退出码 %d", rc)
	}
	tsrv := newFakeDockToolUse(t)
	if rc := armRun(t, "send", "-dock-url", tsrv.URL, "-snapshot", snap, "-workdir", dir); rc != 0 {
		t.Fatalf("send 退出码 %d", rc)
	}
	if rc := armRun(t, "evaluate", "-snapshot", snap, "-workdir", dir, "-upstream", "zhipu"); rc != 0 {
		t.Fatalf("evaluate 退出码 %d", rc)
	}
	if rc := armRun(t, "writeback", "-workdir", dir, "-state", state); rc != 0 {
		t.Fatalf("failed 结论应可回写, 退出码 %d", rc)
	}
	recs, _ := ferry.LoadArmVerdicts(state)
	if st := ferry.ArmState(recs, "zhipu"); st != ferry.ArmStateFailed {
		t.Fatalf("未过状态 = %q, want failed", st)
	}
	_, enabled := ferry.ArmVerdictResolverFor(state)("zhipu")
	if enabled {
		t.Fatal("未过必须保持未启用")
	}
}

// newFakeDockToolUse 追加跳回 tool_use(标准③判否路径)。
func newFakeDockToolUse(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			MaxTokens int `json:"max_tokens"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "text/event-stream")
		if body.MaxTokens == 1 {
			sse := sseData(map[string]any{"type": "message_start", "message": map[string]any{
				"usage": map[string]any{"input_tokens": 10, "cache_read_input_tokens": 1990, "output_tokens": 1}}}) +
				sseData(map[string]any{"type": "message_delta",
					"delta": map[string]any{"stop_reason": "end_turn"},
					"usage": map[string]any{"input_tokens": 10, "cache_read_input_tokens": 1990, "output_tokens": 1}})
			_, _ = w.Write([]byte(sse))
			return
		}
		sse := sseData(map[string]any{"type": "message_start", "message": map[string]any{
			"usage": map[string]any{"input_tokens": 10, "cache_read_input_tokens": 1990, "output_tokens": 0}}}) +
			sseData(map[string]any{"type": "message_delta",
				"delta": map[string]any{"stop_reason": "tool_use"},
				"usage": map[string]any{"input_tokens": 10, "cache_read_input_tokens": 1990, "output_tokens": 50}})
		_, _ = w.Write([]byte(sse))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestStatusEmptyState(t *testing.T) {
	if rc := armRun(t, "status", "-state", filepath.Join(t.TempDir(), "none.jsonl")); rc != 0 {
		t.Fatalf("空状态 status 退出码 %d", rc)
	}
}
