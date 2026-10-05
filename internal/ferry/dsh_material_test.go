package ferry

// dsh_material_test.go — 票02：dsh 摆渡材料分支测试（快照替身 → 材料/交接）。
//
// 覆盖票面验收：
//   - 快照替身（多条 user/assistant 消息的请求体）→ dsh 分支提取的材料含近期
//     对话内容（语义级断言，非仅骨架/标题）；tool_result/thinking/system 不入
//     正文（防注入面与 CC 提取器同位）。
//   - 覆盖截止：coversAt（入队时台账 lastWrite）→ facts.LastTS / meta
//     covers_until_iso 可回读同值；「对同时刻 coversBar 判覆盖」的完整命中
//     断言（store.ValidHandoff 真判）在 daemon 侧 worker 测试。
//   - 退化体（nil/坏 JSON/零消息/null）零 items 不 panic（fail-open，D7）；
//     coversAt<=0 → LastTS 空（缺值如实缺，不伪造 1970 覆盖）。

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"

	"ferryman/internal/extract"
)

// dshSnapshotBody anthropic-messages 请求体替身（渡口主快照形态）：system
// blocks 形 + 五条 messages——user 字符串 content / assistant text+thinking
// blocks / user tool_result（整条须丢弃）/ assistant tool_use+text / user 末条。
func dshSnapshotBody(t *testing.T) []byte {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"model":  "deepseek-chat",
		"system": []any{map[string]any{"type": "text", "text": "你是系统提示（不得进材料）"}},
		"messages": []any{
			map[string]any{"role": "user", "content": "修一下登录页的 bug"},
			map[string]any{"role": "assistant", "content": []any{
				map[string]any{"type": "thinking", "thinking": "思考中（不得进材料）"},
				map[string]any{"type": "text", "text": "好的，我先看下 auth.py 的登录分支"},
			}},
			map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "tool_result", "tool_use_id": "t1",
					"content": "工具输出（不得进材料）"},
			}},
			map[string]any{"role": "assistant", "content": []any{
				map[string]any{"type": "tool_use", "id": "t2", "name": "Edit",
					"input": map[string]any{"file_path": "C:/proj/auth.py"}},
				map[string]any{"type": "text", "text": "改完了登录分支"},
			}},
			map[string]any{"role": "user", "content": "继续跑一下登录回归测试"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestDshMaterialFromSnapshotBody：材料构造单测——messages 逐条成 items
// （user/assistant 分角色；tool_result 整条丢弃）；facts.Title 恒空（台账现行
// 标题在上游）；LastTS＝coversAt 的 ISO 形（回读同值）；末段定格三件套与 CC
// 提取器同语义（末条 user 文本/末条 assistant 文本/末条助手工具名）。
func TestDshMaterialFromSnapshotBody(t *testing.T) {
	coversAt := 1759600000.5
	facts, items := DshMaterial("session-material-0001", dshSnapshotBody(t), coversAt)
	want := []extract.Item{
		{Role: "user", Text: "修一下登录页的 bug"},
		{Role: "assistant", Text: "好的，我先看下 auth.py 的登录分支"},
		{Role: "assistant", Text: "改完了登录分支"},
		{Role: "user", Text: "继续跑一下登录回归测试"},
	}
	if len(items) != len(want) {
		t.Fatalf("items 数 = %d, want %d: %+v", len(items), len(want), items)
	}
	for i, w := range want {
		if items[i] != w {
			t.Fatalf("items[%d] = %+v, want %+v", i, items[i], w)
		}
	}
	if facts.Title != "" {
		t.Fatalf("Title = %q, want 空（回落在上游）", facts.Title)
	}
	pt, err := time.Parse(time.RFC3339, facts.LastTS)
	if err != nil {
		t.Fatalf("LastTS 非 ISO 形: %q (%v)", facts.LastTS, err)
	}
	if got := float64(pt.Unix()) + float64(pt.Nanosecond())/1e9; math.Abs(got-coversAt) > 0.001 {
		t.Fatalf("LastTS 回读 = %v, want %v", got, coversAt)
	}
	if facts.FreezeUser != "继续跑一下登录回归测试" {
		t.Fatalf("FreezeUser = %q", facts.FreezeUser)
	}
	if facts.FreezeAsstText != "改完了登录分支" {
		t.Fatalf("FreezeAsstText = %q", facts.FreezeAsstText)
	}
	if len(facts.FreezeTools) != 1 || facts.FreezeTools[0] != "Edit" {
		t.Fatalf("FreezeTools = %v, want [Edit]", facts.FreezeTools)
	}
	if facts.NTurns != 2 {
		t.Fatalf("NTurns = %d, want 2（assistant 条数）", facts.NTurns)
	}
	// 防注入面：thinking/tool_result/系统提示不得混进正文与定格
	for _, banned := range []string{"思考中", "工具输出", "系统提示"} {
		for _, it := range items {
			if strings.Contains(it.Text, banned) {
				t.Fatalf("正文混入 %q: %+v", banned, it)
			}
		}
		if strings.Contains(facts.FreezeUser, banned) ||
			strings.Contains(facts.FreezeAsstText, banned) {
			t.Fatalf("定格混入 %q", banned)
		}
	}
}

// TestDshMaterialDegenerateBodiesFailOpen：体缺/体坏/零消息 → 零 items 不
// panic（D7 fail-open——骨架-only 材料照常走模型）；覆盖截止不随体缺丢。
func TestDshMaterialDegenerateBodiesFailOpen(t *testing.T) {
	coversAt := 1759600000.0
	for name, body := range map[string][]byte{
		"nil 体":  nil,
		"坏 JSON": []byte("{not json"),
		"零消息":    []byte(`{"system":"s","messages":[]}`),
		"空对象":    []byte(`{}`),
		"null 体": []byte("null"),
	} {
		facts, items := DshMaterial("session-degenerate", body, coversAt)
		if len(items) != 0 {
			t.Fatalf("%s: items = %+v, want 空", name, items)
		}
		if facts.LastTS == "" {
			t.Fatalf("%s: LastTS 应仍为 coversAt 的 ISO 形（覆盖截止不随体缺丢）", name)
		}
	}
	// coversAt<=0：缺值如实缺——不伪造 1970 覆盖（新鲜窗自然判弃）
	if facts, _ := DshMaterial("session-zero-covers", dshSnapshotBody(t), 0); facts.LastTS != "" {
		t.Fatalf("coversAt=0 → LastTS = %q, want 空", facts.LastTS)
	}
}

// TestDshFerrySessionMaterialContainsConversation：快照替身 → dsh 分支真摆渡
// ——材料（发往模型的载荷）含近期对话内容（末轮原话在场，非仅骨架/标题）；
// meta covers_until_iso 回读＝入队 covers 值；标题空回落 (无标题)。
func TestDshFerrySessionMaterialContainsConversation(t *testing.T) {
	srv := newChatSrv(t, []string{chatBody(t,
		"<<<INJECT>>>\n注入层：登录分支已修\n<<</INJECT>>>\n\n# 全文\n登录页收尾")})
	coversAt := 1759600000.25
	md, meta, err := DshFerrySession("session-ferry-0001", dshSnapshotBody(t), coversAt,
		Provider{Name: "fake", BaseURL: srv.srv.URL, Model: "fake"}, 10)
	if err != nil {
		t.Fatal(err)
	}
	_, _, payloads := srv.snap()
	if len(payloads) != 1 {
		t.Fatalf("小材料应 L1 单发, got %d 次调用", len(payloads))
	}
	material := userContent(t, payloads[0])
	for _, want := range []string{"修一下登录页的 bug", "auth.py 的登录分支",
		"继续跑一下登录回归测试"} {
		if !strings.Contains(material, want) {
			t.Fatalf("材料缺近期对话 %q:\n%s", want, material)
		}
	}
	if strings.Contains(material, "不得进材料") { // 三替身敏感串共用尾标（system/thinking/tool_result）
		t.Fatalf("材料混入 system/thinking/工具输出:\n%s", material[:min(400, len(material))])
	}
	iso, _ := meta["covers_until_iso"].(string)
	pt, err := time.Parse(time.RFC3339, iso)
	if err != nil {
		t.Fatalf("covers_until_iso 非 ISO: %q (%v)", iso, err)
	}
	if got := float64(pt.Unix()) + float64(pt.Nanosecond())/1e9; math.Abs(got-coversAt) > 0.001 {
		t.Fatalf("covers 回读 = %v, want %v（入队口径）", got, coversAt)
	}
	if meta["mode"] != "L1" || meta["source"] != "session-ferry-0001" {
		t.Fatalf("mode/source = %v/%v", meta["mode"], meta["source"])
	}
	if !strings.Contains(md, "[Ferryman 交接 · 会话 (无标题)]") {
		t.Fatalf("无标题回落缺失:\n%s", md)
	}
	if !strings.Contains(md, "<<<INJECT>>>\n注入层：登录分支已修") {
		t.Fatalf("注入层缺失:\n%s", md)
	}
}
