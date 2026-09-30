package daemon

// gate_log_test.go — 警告落盘钉子：三站点接线各一行、短 id 截断、无目录静默。
// 不测格式字符串全字段（写死脆弱），测"落了、可数、含关键字段"。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAppendGateWarnWritesCountableLine(t *testing.T) {
	dir := t.TempDir()
	appendGateWarn(dir, "cc", "abcdefgh1234", "observe", 2100.4)
	appendGateWarn(dir, "cc", "abcdefgh1234", "enforce", 2160)
	b, err := os.ReadFile(filepath.Join(dir, "gate.log"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) != 2 {
		t.Fatalf("应落 2 行, got %d", len(lines))
	}
	if !strings.Contains(lines[0], "[warn] cc sid=abcdefgh mode=observe idle=2100s") {
		t.Fatalf("行 1 字段不符: %q", lines[0])
	}
	if !strings.Contains(lines[1], "mode=enforce idle=2160s") {
		t.Fatalf("行 2 字段不符: %q", lines[1])
	}
}

func TestAppendGateWarnEmptyDirSilent(t *testing.T) {
	appendGateWarn("", "cc", "x", "observe", 1) // 不 panic 不落
}

func TestShortSid(t *testing.T) {
	if got := shortSid("abcdefghijklmnop"); got != "abcdefgh" {
		t.Fatalf("shortSid = %q", got)
	}
	if got := shortSid("abc"); got != "abc" {
		t.Fatalf("shortSid 短串应原样, got %q", got)
	}
}

// TestGateEnvWarnLandsInSandboxedDataDir 测试卫生回归（2026-09-30 生产污染案：
// newGateEnv 裸用 config.Default() → DataDir 解析到 ~/ferryman，12:45~18:55 的
// s1/s2/t1 警告行全写进了生产 gate.log）：newGateEnv 的警告必须落在沙箱目录。
func TestGateEnvWarnLandsInSandboxedDataDir(t *testing.T) {
	e := newGateEnv(t)
	e.reg("sbox-warn1", filepath.Join(e.tmp, "t.jsonl"), "C:/proj", testBlockS+10, 0)
	e.d.Gate(map[string]any{"agent": "cc", "session_id": "sbox-warn1",
		"transcript_path": filepath.Join(e.tmp, "t.jsonl"), "cwd": "C:/proj",
		"prompt": "继续"})
	b, err := os.ReadFile(filepath.Join(e.tmp, "data", "gate.log"))
	if err != nil {
		t.Fatalf("警告应落沙箱 gate.log: %v", err)
	}
	if !strings.Contains(string(b), "sid=sbox-war") {
		t.Fatalf("沙箱 gate.log 应含本会话警告行: %q", string(b))
	}
}
