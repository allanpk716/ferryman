package dshtrans

// dshtrans_test.go — dsh-heartbeat 票03「检测态」：最后说话人累积态
//（dsh-heartbeat 规格「dsh 等答复窗·检测态」节）。
//
// 语义钉死：
//   - turn/start、user/message＝用户侧（合成注入回合也是 user/message 类型，
//     注入即用户侧——规格：注入即活动、缓存刚被使用，无害）；
//   - assistant/message＝dsh 侧（不要求带 usage——与出行门槛分离）；
//   - "dsh 后说"＝最后一条用户侧事件早于最后一条 assistant/message；
//   - 跨 chunk 累积（同 title 的携带形：态随调用传入传回）；
//   - 明文与 zstd 两形态都过（TailText 解码后同一 ParseChunkDetect 消费）。

import (
	"os"
	"testing"
)

func TestSpeakerStateTruthTable(t *testing.T) {
	cases := []struct {
		name               string
		sp                 SpeakerState
		wantDshSpokeLast   bool
	}{
		{"零值（未见任何事件）", SpeakerState{}, false},
		{"仅用户侧", SpeakerState{LastUserSeq: 5}, false},
		{"仅 assistant 侧", SpeakerState{LastAssistantSeq: 5}, true},
		{"用户后说", SpeakerState{LastUserSeq: 7, LastAssistantSeq: 5}, false},
		{"dsh 后说", SpeakerState{LastUserSeq: 5, LastAssistantSeq: 7}, true},
	}
	for _, c := range cases {
		if got := c.sp.DshSpokeLast(); got != c.wantDshSpokeLast {
			t.Errorf("%s: DshSpokeLast() = %v, want %v", c.name, got, c.wantDshSpokeLast)
		}
	}
}

func TestParseChunkDetectSpeakerClassification(t *testing.T) {
	// 单段分类：turn/start 与 user/message＝用户侧；assistant/message（无
	// usage 也算）＝dsh 侧；未知/ignorable 事件不动说话人态。
	text := `{"type":"user/message","seq":10,"time":1790905210000,"data":{"message":{"role":"user"}}}` + "\n" +
		`{"type":"assistant/message","seq":11,"time":1790905220000,"data":{"message":{"role":"assistant"},"usage":{"inputTokens":1,"outputTokens":1}}}` + "\n" +
		`{"type":"session/title","seq":12,"time":1790905230000,"data":{"title":"机器侧"}}` + "\n" + // 机器侧：不动说话人
		`{"type":"turn/start","seq":13,"time":1790905240000,"data":{"turn":3}}` + "\n" + // 用户侧（认领排队输入）
		`{"type":"assistant/message","seq":14,"time":1790905250000,"data":{"message":{"role":"assistant"}}}` + "\n" + // 无 usage 仍 dsh 侧
		`{"type":"todo/write","ignorable":true,"seq":15,"time":1790905260000,"data":{}}` + "\n" +
		`{"type":"whatever/unknown","seq":16,"time":1790905270000,"data":{}}` + "\n" +
		"{broken json\n"
	r := ParseChunkDetect(text, "", SpeakerState{})
	if r.Speaker.LastUserSeq != 13 || r.Speaker.LastAssistantSeq != 14 {
		t.Fatalf("Speaker = %+v, want user=13 assistant=14", r.Speaker)
	}
	if !r.Speaker.DshSpokeLast() {
		t.Fatal("末条为 assistant（seq14>seq13）应 dsh 后说")
	}
	// 活动时刻：白名单活动事件（turn/start、assistant/message）的最后时刻
	//（毫秒→秒）。
	if !r.HasActivity || r.LastActivityTS != 1790905250.0 {
		t.Fatalf("LastActivityTS = %v has=%v, want 1790905250.0/true", r.LastActivityTS, r.HasActivity)
	}
	// 出行门槛不受检测影响：带 usage 的 assistant/message 恰一行。
	if len(r.Rows) != 1 || r.Rows[0].Seq != 11 {
		t.Fatalf("Rows = %+v, want 1 行 seq=11", r.Rows)
	}
}

func TestParseChunkDetectSeqlessSkipped(t *testing.T) {
	// 无 seq 的事件无法排序（会话内单调唯一序的根基），防御式跳过——绝不让
	// 无序证据翻转"最后说话人"。
	text := `{"type":"user/message","time":1790905210000,"data":{}}` + "\n" +
		`{"type":"assistant/message","seq":3,"time":1790905220000,"data":{}}` + "\n"
	r := ParseChunkDetect(text, "", SpeakerState{})
	if r.Speaker.LastUserSeq != 0 {
		t.Fatalf("无 seq 用户事件不应登记: %+v", r.Speaker)
	}
	if r.Speaker.LastAssistantSeq != 3 || !r.Speaker.DshSpokeLast() {
		t.Fatalf("Speaker = %+v, want assistant=3 dsh-last", r.Speaker)
	}
}

func TestParseChunkDetectCarriesAcrossChunks(t *testing.T) {
	// 跨 chunk 携带（同 title 的携带形）：机器侧事件段不翻转态；用户侧事件
	// 段翻转为用户后说。
	sp := SpeakerState{}
	chunk1 := `{"type":"user/message","seq":2,"time":1790905210000,"data":{}}` + "\n" +
		`{"type":"assistant/message","seq":3,"time":1790905220000,"data":{}}` + "\n"
	r1 := ParseChunkDetect(chunk1, "旧标题", sp)
	sp = r1.Speaker
	if !sp.DshSpokeLast() {
		t.Fatalf("段1后应 dsh 后说: %+v", sp)
	}
	if r1.Title != "旧标题" {
		t.Fatalf("title 携带失效: %q", r1.Title)
	}
	// 段2：纯机器侧事件（title/turn/end/catalog/compaction）——态不动。
	chunk2 := `{"type":"session/title","seq":4,"time":1790905230000,"data":{"title":"新标题"}}` + "\n" +
		`{"type":"turn/end","seq":5,"time":1790905240000,"data":{}}` + "\n" +
		`{"type":"subagent/catalog","seq":6,"time":1790905250000,"data":{"childId":"c1","childCreatedAt":1,"mode":"one-shot"}}` + "\n" +
		`{"type":"compaction/start","seq":7,"time":1790905260000,"data":{}}` + "\n"
	r2 := ParseChunkDetect(chunk2, r1.Title, sp)
	if r2.Speaker != sp || !r2.Speaker.DshSpokeLast() {
		t.Fatalf("机器侧事件段不应翻转说话人: %+v → %+v", sp, r2.Speaker)
	}
	if r2.Title != "新标题" {
		t.Fatalf("title 更新失效: %q", r2.Title)
	}
	// 段3：turn/start（用户侧）→ 翻转为用户后说。
	chunk3 := `{"type":"turn/start","seq":8,"time":1790905270000,"data":{"turn":4}}` + "\n"
	r3 := ParseChunkDetect(chunk3, "", r2.Speaker)
	if r3.Speaker.DshSpokeLast() {
		t.Fatalf("turn/start 后应用户后说: %+v", r3.Speaker)
	}
}

func TestParseChunkDetectParityWithParseChunk(t *testing.T) {
	// 三件产出（rows/title/children）与 ParseChunk 逐位一致（ParseChunk 是
	// ParseChunkDetect 的三件套包装——票外既有测试依赖其行为不变）。
	text := `{"type":"session/title","seq":2,"time":1,"data":{"title":"T"}}` + "\n" +
		`{"type":"assistant/message","seq":3,"time":2,"data":{"message":{"source":{"model":"m"}},"usage":{"inputTokens":1,"outputTokens":1}}}` + "\n" +
		`{"type":"subagent/catalog","seq":4,"time":3,"data":{"childId":"c1","childCreatedAt":9,"mode":"continuable","label":"L"}}` + "\n"
	rowsA, titleA, kidsA := ParseChunk(text, "")
	rB := ParseChunkDetect(text, "", SpeakerState{})
	if len(rowsA) != len(rB.Rows) || (len(rowsA) > 0 && rowsA[0] != rB.Rows[0]) {
		t.Fatalf("rows 不一致: %+v vs %+v", rowsA, rB.Rows)
	}
	if titleA != rB.Title {
		t.Fatalf("title 不一致: %q vs %q", titleA, rB.Title)
	}
	if len(kidsA) != len(rB.Children) || (len(kidsA) > 0 && kidsA[0] != rB.Children[0]) {
		t.Fatalf("children 不一致: %+v vs %+v", kidsA, rB.Children)
	}
}

func TestDetectEndToEndZstdAndPlaintext(t *testing.T) {
	// zstd 形态：代文件追加式（每批一帧），TailText 增量尾读 + 检测态跨批累积。
	dir := t.TempDir()
	p := writeGen(t, dir, "session.v4.jsonl.zstd", zstdFrames(t,
		realHeaderV4,
		`{"type":"user/message","seq":2,"time":1790905210000,"data":{"message":{"role":"user"}}}`+"\n"))
	res := TailText(p, true, 0)
	if res.Err != nil {
		t.Fatalf("首读: %v", res.Err)
	}
	r1 := ParseChunkDetect(res.Text, "", SpeakerState{})
	if r1.Speaker.DshSpokeLast() {
		t.Fatalf("仅用户事件应用户后说: %+v", r1.Speaker)
	}
	off := res.NewOffset
	// 追加一批 assistant（无 usage）→ dsh 后说。
	f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(zstdFrames(t,
		`{"type":"assistant/message","seq":3,"time":1790905220000,"data":{"message":{"role":"assistant"}}}`+"\n")); err != nil {
		t.Fatal(err)
	}
	f.Close()
	res2 := TailText(p, true, off)
	r2 := ParseChunkDetect(res2.Text, "", r1.Speaker)
	if res2.Err != nil || r2.Speaker.LastAssistantSeq != 3 || !r2.Speaker.DshSpokeLast() {
		t.Fatalf("zstd 追加批后: err=%v speaker=%+v", res2.Err, r2.Speaker)
	}

	// 明文形态（compression:'none'）。
	dir2 := t.TempDir()
	pp := writeGen(t, dir2, "session.jsonl", []byte(realHeaderV0+
		`{"type":"turn/start","seq":2,"time":1790905210000,"data":{"turn":1}}`+"\n"))
	resP := TailText(pp, false, 0)
	rP := ParseChunkDetect(resP.Text, "", SpeakerState{})
	if rP.Speaker.LastUserSeq != 2 || rP.Speaker.DshSpokeLast() {
		t.Fatalf("明文 turn/start 应用户后说: %+v", rP.Speaker)
	}
	// 追加残行（无行尾）——扣留下轮，态不动。
	f2, err := os.OpenFile(pp, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f2.WriteString(`{"type":"assistant/message","seq":3,"time":1790905220000,"data":{"message":{"role":"assistant"}}}`); err != nil {
		t.Fatal(err)
	}
	f2.Close()
	resP2 := TailText(pp, false, resP.NewOffset)
	if resP2.Text != "" {
		t.Fatalf("残行应扣留: %q", resP2.Text)
	}
	rP2 := ParseChunkDetect(resP2.Text, "", rP.Speaker)
	if rP2.Speaker != rP.Speaker {
		t.Fatalf("残行不应推进态: %+v", rP2.Speaker)
	}
}
