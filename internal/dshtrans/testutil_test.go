package dshtrans

// 测试夹具助手：用 klauspost 编码器造 dsh 形状的会话文件——
// 物质化＝首帧仅头行＋首批事件一帧；后续每批事件各一帧（checksummed、
// 帧拼接，与 dsh encodeMaterialization/appendLines 同构）。

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/klauspost/compress/zstd"
)

// zstdFrames 每批一帧，帧拼接。
func zstdFrames(t *testing.T, batches ...string) []byte {
	t.Helper()
	var out bytes.Buffer
	for _, b := range batches {
		w, err := zstd.NewWriter(&out, zstd.WithEncoderCRC(true), // dsh CHECKSUM_OPTIONS 同款
			zstd.WithEncoderConcurrency(1))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(b)); err != nil {
			t.Fatal(err)
		}
		if err := w.Close(); err != nil { // Close 收帧
			t.Fatal(err)
		}
	}
	return out.Bytes()
}

// writeGen 落一个代文件（返回路径）。
func writeGen(t *testing.T, dir, name string, content []byte) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, content, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// 真机夹具（2026-10-03 ~/.dsh/sessions 实物摘录，脱敏压缩）。

// realHeaderV4 真机 v4 头行（支持-deepseek-harness-服务商配置 会话）。
const realHeaderV4 = `{"type":"session","version":4,"id":"session-6e360520-09b8-46b6-9d82-cc9446ec8bd9","createdAt":1790905220031,"cwd":"C:\\Users\\allan716\\orca\\workspaces\\Ferryman\\支持-deepseek-harness-服务商配置","isSeeded":false,"delegationDepth":0}` + "\n"

// realHeaderV0 真机 v0 头行（ca_things 会话，session.jsonl.zstd 无名代）。
const realHeaderV0 = `{"type":"session","version":0,"id":"session-5e78cf1d-e357-40f6-bae7-dda2e846fe8e","createdAt":1788397646083,"cwd":"C:\\WorkSpace\\ca_things","delegationDepth":0,"agentPreset":"standard"}` + "\n"

// realUsageLine 真机 assistant/message（ferryman-dock→GLM-5.3 一跳；正文/
// stream 截短，usage 与 source 结构保持原样）。
const realUsageLine = `{"type":"assistant/message","seq":16,"time":1790905227524,"data":{"turn":1,"step":1,"message":{"role":"assistant","content":[{"type":"reasoning","text":"…"},{"type":"text","text":"成功"}],"source":{"kind":"model","provider":"ferryman-dock","model":"claude-opus-5","replayState":{"response":{"kind":"pi-ai","version":2,"api":"anthropic-messages","provider":"ferryman-dock","model":"claude-opus-5","responseModel":"GLM-5.3","responseId":"msg_…","stopReason":"stop"},"blocks":[{"type":"reasoning","thinkingSignature":"…"},{"type":"text"}]}}},"usage":{"inputTokens":9169,"outputTokens":21,"totalTokens":9190},"stream":[]},"surfaceOp":"append"}` + "\n"
