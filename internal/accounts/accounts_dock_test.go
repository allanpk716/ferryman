// accounts_dock_test.go — 票06：dock 科目验收钉子（spec F13）。
// 透传与改写两模式行形状齐全；纯元数据红线（消息内容字段被白名单拒）。
package accounts

import (
	"strings"
	"testing"
)

func dockFields(mode, mIn, mOut string) Fields {
	return Fields{
		"agent": "cc", "session_id": "sess-dock",
		"mode": mode, "model_in": mIn, "model_out": mOut,
		"input_tokens": 12, "cache_read_tokens": 34, "cache_creation_tokens": 5,
		"output_tokens": 6, "latency_s": 0.25, "status": 200,
	}
}

func TestDockKindPassthroughAndRewriteRows(t *testing.T) {
	acc, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// 透传行：前后模型名同值
	if _, err := acc.Record("dock", -1, dockFields("passthrough", "claude-opus-5", "claude-opus-5")); err != nil {
		t.Fatalf("透传行: %v", err)
	}
	// 改写行：别名换档
	if _, err := acc.Record("dock", -1, dockFields("rewrite", "claude-opus-5", "glm-5.5")); err != nil {
		t.Fatalf("改写行: %v", err)
	}
	rows := acc.Read(ReadOpts{Kind: "dock"})
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	pt, rw := rows[0], rows[1]
	if pt["model_in"] != pt["model_out"] {
		t.Fatalf("透传行前后模型名须同值: %v/%v", pt["model_in"], pt["model_out"])
	}
	if rw["model_in"] == rw["model_out"] {
		t.Fatal("改写行前后模型名应不同")
	}
	for k, want := range map[string]float64{
		"input_tokens": 12, "cache_read_tokens": 34, "cache_creation_tokens": 5,
		"output_tokens": 6, "status": 200,
	} {
		if got := pt[k].(float64); got != want {
			t.Fatalf("透传行 %s = %v, want %v", k, pt[k], want)
		}
	}
	if pt["session_id"] != "sess-dock" {
		t.Fatalf("session_id = %v", pt["session_id"])
	}
	if _, ok := pt["ts"]; !ok {
		t.Fatal("公共章 ts 缺失")
	}
	if _, ok := pt["latency_s"]; !ok {
		t.Fatal("latency_s 缺失")
	}
}

func TestDockKindRejectsMessageContent(t *testing.T) {
	acc, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := dockFields("passthrough", "m", "m")
	f["messages"] = []any{map[string]any{"role": "user", "content": "用户原话"}}
	if _, err := acc.Record("dock", -1, f); err == nil || !strings.Contains(err.Error(), "隐私") {
		t.Fatalf("消息内容字段应被白名单拒: %v", err)
	}
}

func TestDockKindMissingFieldRejected(t *testing.T) {
	acc, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := dockFields("passthrough", "m", "m")
	delete(f, "status")
	if _, err := acc.Record("dock", -1, f); err == nil || !strings.Contains(err.Error(), "缺必填") {
		t.Fatalf("缺 status 应报缺必填: %v", err)
	}
}
