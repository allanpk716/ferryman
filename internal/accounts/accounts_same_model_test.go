package accounts

// accounts_same_model_test.go — 票03：handoff.lane 标注与 same_model_skip
// 科目钉子（ADR-0015 决定六）。
//
// 验收对照：
//   - handoff 记账带 lane 标注，账本测试覆盖三档（same_model/third_party/
//     skeleton 逐档入账且值持久化）；
//   - 存量生产者形态（无 lane 的 handoff 行，worker 产线）照常入账——lane
//     当前为可选字段（kindOptional），后续票接线补写后恢复必填；
//   - 白名单外字段照拒（隐私铁律不因可选机制松动）；
//   - same_model_skip 科目：六字段白名单、缺必填拒行、未知科目文案收录。

import (
	"strings"
	"testing"
)

func handoffFields(lane string) Fields {
	f := Fields{
		"agent":             "cc",
		"session_id":        "s-lane",
		"lineage_id":        "L",
		"project":           "C:/p",
		"provider":          "zhipu",
		"model":             "glm-5.3",
		"price_ver":         nil,
		"prompt_tokens":     1000,
		"completion_tokens": 200,
		"outcome":           "fresh",
		"wall_s":            3.5,
	}
	if lane != "" {
		f["lane"] = lane
	}
	return f
}

func TestHandoffLaneThreeLanesRecorded(t *testing.T) {
	acc, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, lane := range []string{"same_model", "third_party", "skeleton"} {
		if _, err := acc.Record("handoff", -1, handoffFields(lane)); err != nil {
			t.Fatalf("lane=%s 入账失败: %v", lane, err)
		}
	}
	rows := acc.Read(ReadOpts{Kind: "handoff"})
	if len(rows) != 3 {
		t.Fatalf("rows = %d, want 3（三档各一行）", len(rows))
	}
	got := map[string]bool{}
	for _, r := range rows {
		got[r["lane"].(string)] = true
	}
	for _, lane := range []string{"same_model", "third_party", "skeleton"} {
		if !got[lane] {
			t.Fatalf("缺 %s 档行: %v", lane, got)
		}
	}
}

func TestHandoffLaneOptionalForLegacyProducers(t *testing.T) {
	acc, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// worker 存量产线形态：无 lane 字段——照常入账（可选字段豁免必填）。
	if _, err := acc.Record("handoff", -1, handoffFields("")); err != nil {
		t.Fatalf("存量无 lane 行应入账: %v", err)
	}
	// 白名单外字段照拒（隐私铁律不动摇）。
	f := handoffFields("same_model")
	f["messages"] = "正文永不入账"
	if _, err := acc.Record("handoff", -1, f); err == nil ||
		!strings.Contains(err.Error(), "messages") {
		t.Fatalf("未知字段应拒: %v", err)
	}
}

func TestSameModelSkipKind(t *testing.T) {
	acc, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := acc.Record("same_model_skip", -1, Fields{
		"agent": "cc", "session_id": "s-skip", "lineage_id": "L", "project": "C:/p",
		"reason": "cold", "upstream": "zhipu", "idle_s": 1200.5,
		"clock_s": -1.0, "ttl_s": 1800.0, "threshold_min": 20.0,
	}); err != nil {
		t.Fatalf("same_model_skip 入账失败: %v", err)
	}
	rows := acc.Read(ReadOpts{Kind: "same_model_skip"})
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	if rows[0]["reason"] != "cold" || rows[0]["upstream"] != "zhipu" {
		t.Fatalf("行字段 = %v", rows[0])
	}
	// 缺必填拒行（六字段白名单全必填）。
	f := Fields{"reason": "cold", "upstream": "zhipu", "idle_s": 1.0,
		"clock_s": 1.0, "ttl_s": 1.0}
	if _, err := acc.Record("same_model_skip", -1, f); err == nil {
		t.Fatal("缺 threshold_min 应拒行")
	}
	// 未知科目文案收录新科目（kindOrder 追加）。
	_, err = acc.Record("nope", -1, Fields{})
	if err == nil || !strings.Contains(err.Error(), "same_model_skip") {
		t.Fatalf("未知科目文案应含 same_model_skip: %v", err)
	}
}
