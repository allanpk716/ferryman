package daemon

// 票01/ADR-0008 守望分流与子代理 usage 入账的 daemon 面钉子：
//   - pollCC 对 subagents 路径只喂 usage 采集——不 Touch、不入摆渡队（T32 既有
//     跳过语义零变动，TestWatcherSkipsSubagentTranscriptPaths 继续钉住登记面）；
//   - 子代理 usage 行形状：session_id=父 sid、lineage_id=父转录归一键、
//     subagent=文件 stem、四列取自该子代理自己的 assistant 记录；
//   - 主会话 usage 行 subagent=空串；
//   - lineage 键来源（rev1·F4/F9）：台账有父会话以台账 TranscriptPath 为准；
//     父不在台账用路径剥离构造，且与父转录后续 Touch 的归一键一致。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"ferryman/internal/accounts"
	"ferryman/internal/config"
	"ferryman/internal/ledger"
	"ferryman/internal/pathsx"
)

// writeSubagentTranscript 造一份子代理转录（真实布局：<munged>/<父sid>/subagents/
// agent-*.jsonl；行内 sessionId=父会话、agentId、isSidechain、cwd 自带）。
func writeSubagentTranscript(t *testing.T, projects, parentSid, stem string, inp, out int) string {
	t.Helper()
	user, _ := json.Marshal(map[string]any{
		"type": "user", "timestamp": "2026-09-18T12:00:00.000Z",
		"isSidechain": true, "agentId": strings.TrimPrefix(stem, "agent-"),
		"sessionId": parentSid, "cwd": "C:/proj",
		"message": map[string]any{"role": "user", "content": "子任务原话（不入账）"},
	})
	asst, _ := json.Marshal(map[string]any{
		"type": "assistant", "timestamp": "2026-09-18T12:00:05.000Z",
		"isSidechain": true, "agentId": strings.TrimPrefix(stem, "agent-"),
		"sessionId": parentSid, "cwd": "C:/proj",
		"message": map[string]any{"role": "assistant", "id": "msg_sub_" + stem,
			"model": "glm-5.3",
			"content": []any{map[string]any{"type": "text", "text": "子代理回答原话（不入账）"}},
			"usage": map[string]any{"input_tokens": inp,
				"cache_read_input_tokens": 4000, "cache_creation_input_tokens": 0,
				"output_tokens": out}},
	})
	return writeSubTranscriptLines(t, projects, parentSid, stem, string(user), string(asst))
}

// writeSubTranscriptLines 在 <projects>/C--proj/<父sid>/subagents/<stem>.jsonl 落行。
func writeSubTranscriptLines(t *testing.T, projects, parentSid, stem string, lines ...string) string {
	t.Helper()
	f := filepath.Join(projects, "C--proj", parentSid, "subagents", stem+".jsonl")
	if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return f
}

// newHarvestWatcher 最小守望装配：cc 目录指临时 projects、accounts 接线
// （Default 的 HarvestUsage=true 使 harvest 随建）、入队记录器。
func newHarvestWatcher(t *testing.T, projects string) (*Watcher, *accounts.Accounts, *[]string, *sync.Mutex) {
	t.Helper()
	acc, err := accounts.New(filepath.Join(t.TempDir(), "acc"))
	if err != nil {
		t.Fatal(err)
	}
	led := ledger.New()
	var mu sync.Mutex
	ids := []string{}
	cfg := config.Default()
	cfg.Watch.CCProjectsDir = projects
	w := NewWatcher(cfg, led, nil, func(st *ledger.SessionState) bool {
		mu.Lock()
		ids = append(ids, st.SessionID)
		mu.Unlock()
		return true
	}, 0, acc, nil, nil, nil)
	w.cxDirs = []string{filepath.Join(projects, "no-codex")}
	return w, acc, &ids, &mu
}

func usageRows(t *testing.T, acc *accounts.Accounts) (main, sub map[string]any) {
	t.Helper()
	for _, r := range acc.Read(accounts.ReadOpts{Kind: "usage"}) {
		if s, _ := r["subagent"].(string); s == "" {
			main = r
		} else {
			sub = r
		}
	}
	return main, sub
}

func TestPollCCSubagentFeedsUsageOnly(t *testing.T) {
	// 分流：subagents 文件不 Touch/不入队（既有 T32 面），唯一新增行为是喂
	// usage 采集——子代理行归父会话、标记=stem、四列取自该文件；主行空串。
	tmp := t.TempDir()
	projects := filepath.Join(tmp, "projects")
	main := writeMergeTranscript(t, projects, "P1", "干完了，没有问题。", nil)
	writeSubagentTranscript(t, projects, "P1", "agent-a1", 111, 22)

	w, acc, ids, mu := newHarvestWatcher(t, projects)
	w.pollCC()

	ledSessions := map[string]bool{}
	for _, st := range w.Ledger.AllSessions() {
		ledSessions[st.SessionID] = true
	}
	if !ledSessions["P1"] {
		t.Fatal("主会话应登记")
	}
	if ledSessions["agent-a1"] {
		t.Fatal("子代理转录不得登记为独立会话（T32）")
	}
	mu.Lock()
	for _, id := range *ids {
		if id == "agent-a1" {
			t.Fatalf("子代理不得入摆渡队, enqueued = %v", *ids)
		}
	}
	mu.Unlock()

	mainRow, subRow := usageRows(t, acc)
	if mainRow == nil || subRow == nil {
		t.Fatalf("usage 行缺失: main=%v sub=%v", mainRow, subRow)
	}
	if mainRow["session_id"] != "P1" || mainRow["subagent"] != "" {
		t.Fatalf("主行 = %v, want session_id=P1 subagent=空串", mainRow)
	}
	if mainRow["lineage_id"] != pathsx.NormPath(main) {
		t.Fatalf("主行 lineage = %v, want %s", mainRow["lineage_id"], pathsx.NormPath(main))
	}
	if subRow["session_id"] != "P1" {
		t.Fatalf("子行 session_id = %v, want 父会话 P1（行内 sessionId）", subRow["session_id"])
	}
	if subRow["subagent"] != "agent-a1" {
		t.Fatalf("子行 subagent = %v, want 文件 stem agent-a1", subRow["subagent"])
	}
	if subRow["lineage_id"] != pathsx.NormPath(main) {
		t.Fatalf("子行 lineage = %v, want 父转录归一键 %s", subRow["lineage_id"], pathsx.NormPath(main))
	}
	// 四列取自该子代理自己的 assistant 记录（非主转录行）
	if subRow["input_tokens"] != float64(111) || subRow["output_tokens"] != float64(22) {
		t.Fatalf("子行四列 = %v/%v, want 111/22", subRow["input_tokens"], subRow["output_tokens"])
	}
	if subRow["cache_read_tokens"] != float64(4000) {
		t.Fatalf("子行 cache_read = %v, want 4000", subRow["cache_read_tokens"])
	}
	// 隐私不变量（结构性）：账本行只含数字与标识字段——子代理消息内容字段
	// 不在白名单，Record 侧拒绝（形态断言见 accounts 包 TestUsageSubagentMarkerField）。
	for k := range subRow {
		if k == "content" || k == "message" {
			t.Fatalf("子行不得携带内容字段: %s", k)
		}
	}
}

func TestSubagentLineageLedgerPriority(t *testing.T) {
	// rev1·F9：台账父会话 TranscriptPath 与路径剥离结果不一致 → 以台账为准。
	tmp := t.TempDir()
	projects := filepath.Join(tmp, "projects")
	writeSubagentTranscript(t, projects, "P1", "agent-a1", 111, 22)
	w, acc, _, _ := newHarvestWatcher(t, projects)
	elsewhere := filepath.Join(tmp, "elsewhere", "P1.jsonl")
	if err := os.MkdirAll(filepath.Dir(elsewhere), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(elsewhere, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	w.Ledger.Touch("cc", "P1", elsewhere, 1_800_000_000.0, 3, 0)

	w.pollCC()
	_, subRow := usageRows(t, acc)
	if subRow == nil {
		t.Fatal("子代理 usage 行缺失")
	}
	if subRow["lineage_id"] != pathsx.NormPath(elsewhere) {
		t.Fatalf("子行 lineage = %v, want 台账值 %s", subRow["lineage_id"], pathsx.NormPath(elsewhere))
	}
}

func TestSubagentLineageDerivedMatchesLaterTouch(t *testing.T) {
	// 父会话不在台账：从子代理路径剥离构造的 lineage 键 == 父转录文件落盘后
	// 后续 Touch 赋值的归一键（验收#3；F4 的一致性钉子）。
	tmp := t.TempDir()
	projects := filepath.Join(tmp, "projects")
	writeSubagentTranscript(t, projects, "P1", "agent-a1", 111, 22)
	w, acc, _, _ := newHarvestWatcher(t, projects)

	w.pollCC() // 父转录文件尚不存在——父不在台账，用剥离结果
	_, subRow := usageRows(t, acc)
	if subRow == nil {
		t.Fatal("子代理 usage 行缺失")
	}
	derived := pathsx.NormPath(filepath.Join(projects, "C--proj", "P1.jsonl"))
	if subRow["lineage_id"] != derived {
		t.Fatalf("子行 lineage = %v, want 剥离键 %s", subRow["lineage_id"], derived)
	}

	// 父转录随后落盘并被 Touch：主行归一键与子行一致
	writeMergeTranscript(t, projects, "P1", "干完了，没有问题。", nil)
	w.pollCC()
	mainRow, _ := usageRows(t, acc)
	if mainRow == nil {
		t.Fatal("主行 usage 缺失")
	}
	if mainRow["lineage_id"] != subRow["lineage_id"] {
		t.Fatalf("族系键分裂: 主=%v 子=%v", mainRow["lineage_id"], subRow["lineage_id"])
	}
}
