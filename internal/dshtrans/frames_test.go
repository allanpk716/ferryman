package dshtrans

// P2-1 第一块砖：帧扫描器与增量尾读。klauspost 造的 checksummed 帧拼接
// 为夹具（与 dsh 写方同构）；真机文件另见 real_test.go。

import (
	"bytes"
	"os"
	"testing"
)

func TestScanZstdFramesMultiFrame(t *testing.T) {
	data := zstdFrames(t, "header\n", "batch-1 line-a\nline-b\n", "batch-2\n")
	frames, torn, err := ScanZstdFrames(data)
	if err != nil || torn != -1 {
		t.Fatalf("scan: frames=%d torn=%d err=%v", len(frames), torn, err)
	}
	if len(frames) != 3 {
		t.Fatalf("want 3 frames, got %d", len(frames))
	}
	if frames[0].Start != 0 || frames[2].End != len(data) {
		t.Errorf("帧区间应铺满: %+v", frames)
	}
	for i := 0; i+1 < len(frames); i++ {
		if frames[i].End != frames[i+1].Start {
			t.Errorf("帧 %d/%d 不相接", i, i+1)
		}
	}
	// 逐帧解码与原文一致。
	wants := []string{"header\n", "batch-1 line-a\nline-b\n", "batch-2\n"}
	for i, fr := range frames {
		plain, err := decodeFrame(data[fr.Start:fr.End])
		if err != nil {
			t.Fatalf("frame %d decode: %v", i, err)
		}
		if string(plain) != wants[i] {
			t.Errorf("frame %d = %q, want %q", i, plain, wants[i])
		}
	}
}

func TestScanZstdFramesTornTail(t *testing.T) {
	data := zstdFrames(t, "header\n", "batch\n")
	frames, _, err := ScanZstdFrames(data)
	if err != nil || len(frames) != 2 {
		t.Fatalf("完帧扫描: %v %d", err, len(frames))
	}
	// 逐级截断尾帧：截到只剩 1 字节，凡未完整 → torn=尾帧起点、前帧完好。
	for cut := 1; cut < len(data)-frames[0].End; cut++ {
		trunc := data[:len(data)-cut]
		fs, torn, err := ScanZstdFrames(trunc)
		if err != nil {
			t.Fatalf("截 %d 字节: %v", cut, err)
		}
		if len(fs) != 1 || torn != frames[0].End {
			t.Errorf("截 %d 字节: frames=%d torn=%d, want 1/%d", cut, len(fs), torn, frames[0].End)
		}
	}
	// 空/近空输入：空输入无残帧（torn=-1，dsh undefined 同义）；魔数前缀
	// 字节不足 → 残帧起点 0。
	if fs, torn, err := ScanZstdFrames(nil); err != nil || len(fs) != 0 || torn != -1 {
		t.Errorf("空输入: frames=%d torn=%d err=%v", len(fs), torn, err)
	}
	for _, b := range [][]byte{{0x28}, {0x28, 0xB5}, {0x28, 0xB5, 0x2F}} {
		fs, torn, err := ScanZstdFrames(b)
		if err != nil || len(fs) != 0 || torn != 0 {
			t.Errorf("残输入 %v: frames=%d torn=%d err=%v", b, len(fs), torn, err)
		}
	}
}

func TestScanZstdFramesBadMagic(t *testing.T) {
	data := zstdFrames(t, "header\n")
	data[0] ^= 0xFF // 魔数损坏
	if _, _, err := ScanZstdFrames(data); err == nil {
		t.Fatal("坏魔数应报错")
	}
	// 第二帧魔数坏：首帧仍完整交付。
	d2 := zstdFrames(t, "h1\n", "h2\n")
	d2[len(d2)-1] = 0x00 // 破坏尾字节（校验和域）——魔数完好
	frames, _, err := ScanZstdFrames(d2)
	if err != nil || len(frames) != 2 {
		t.Fatalf("只破校验和域: %v %d", err, len(frames))
	}
}

func TestTailTextZstdIncremental(t *testing.T) {
	p := writeGen(t, t.TempDir(), "session.v4.jsonl.zstd",
		zstdFrames(t, realHeaderV4, realUsageLine))
	// 首轮：全量。
	r1 := TailText(p, true, 0)
	if r1.Err != nil {
		t.Fatal(r1.Err)
	}
	if !bytes.HasPrefix([]byte(r1.Text), []byte(`{"type":"session"`)) {
		t.Errorf("首段应含头行: %.60s", r1.Text)
	}
	if r1.NewOffset <= 0 || r1.NewOffset > fileSize(t, p) {
		t.Errorf("偏移 %d 越界（size=%d）", r1.NewOffset, fileSize(t, p))
	}
	// 次轮：无新增。
	r2 := TailText(p, true, r1.NewOffset)
	if r2.Err != nil || r2.Text != "" || r2.NewOffset != r1.NewOffset {
		t.Errorf("无新增轮: %+v", r2)
	}
	// 追加一批：只出新批（含 usage 行）。
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	f.Write(zstdFrames(t, `{"type":"session/title","seq":17,"time":1790905230000,"data":{"title":"真机回钉标题","messageSeqs":[8],"source":"user"}}`+"\n"))
	f.Close()
	r3 := TailText(p, true, r1.NewOffset)
	if r3.Err != nil {
		t.Fatal(r3.Err)
	}
	if got := r3.Text; got != `{"type":"session/title","seq":17,"time":1790905230000,"data":{"title":"真机回钉标题","messageSeqs":[8],"source":"user"}}`+"\n" {
		t.Errorf("增量段不符: %q", got)
	}
	if r3.NewOffset != fileSize(t, p) {
		t.Errorf("增量偏移 %d ≠ size %d", r3.NewOffset, fileSize(t, p))
	}
}

func TestTailTextZstdTornAppendNotConsumed(t *testing.T) {
	full := zstdFrames(t, realHeaderV4, "batch2\n")
	p := writeGen(t, t.TempDir(), "session.v4.jsonl.zstd", full[:len(full)-3]) // 尾帧撕 3 字节
	r := TailText(p, true, 0)
	if r.Err != nil {
		t.Fatalf("残帧不应报错（首帧完好）: %v", r.Err)
	}
	if !bytes.Contains([]byte(r.Text), []byte(realHeaderV4[:20])) {
		t.Errorf("应消费首帧: %.40s", r.Text)
	}
	if r.NewOffset >= fileSize(t, p) {
		t.Errorf("残帧偏移不应越过文件尾: %d >= %d", r.NewOffset, fileSize(t, p))
	}
	// 修复（写方 truncateTornTail 语义）后从头重采。
	os.WriteFile(p, full, 0o644)
	r2 := TailText(p, true, r.NewOffset)
	if r2.Err != nil {
		t.Fatal(r2.Err)
	}
	if r2.Text == "" || r2.NewOffset != int64(len(full)) {
		t.Errorf("补全后应消费余量: text空=%v off=%d", r2.Text == "", r2.NewOffset)
	}
}

func TestTailTextPlainAndShrink(t *testing.T) {
	p := writeGen(t, t.TempDir(), "session.jsonl", []byte(realHeaderV0+realUsageLine))
	r := TailText(p, false, 0)
	if r.Err != nil || r.NewOffset != int64(len(realHeaderV0+realUsageLine)) {
		t.Fatalf("明文全量: %+v", r)
	}
	// 残行（无 \n 结尾）扣留。
	os.WriteFile(p, []byte(realHeaderV0+"partial-no-newline"), 0o644)
	r2 := TailText(p, false, int64(len(realHeaderV0)))
	if r2.Err != nil || r2.Text != "" || r2.NewOffset != int64(len(realHeaderV0)) {
		t.Errorf("残行应扣留: %+v", r2)
	}
	// 收缩：偏移清零从头重采。
	os.WriteFile(p, []byte(realHeaderV0), 0o644)
	r3 := TailText(p, false, r.NewOffset) // 偏移 > size
	if r3.Err != nil || r3.NewOffset != int64(len(realHeaderV0)) {
		t.Errorf("收缩重采: %+v", r3)
	}
}

func fileSize(t *testing.T, p string) int64 {
	t.Helper()
	info, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	return info.Size()
}
