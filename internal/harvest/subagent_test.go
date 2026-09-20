package harvest

// 票01/ADR-0008 子代理采集面钉子：父域复合键不串扰、行形状归父、首见全量
// 回填、断点恢复（键成分全取账本行内字段）与旧账本无标记字段的兼容路径。
// 夹具按真实子代理转录形态（t32 实测样例）：行内 sessionId=父会话、agentId、
// isSidechain:true、cwd 自带。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ferryman/internal/accounts"
)

// subUserLine 子代理转录 user 行（真实形态：父 sessionId/agentId/isSidechain/cwd 行内自带）。
func subUserLine(sid, agentID, cwd, ts string) string {
	return mustJSON(map[string]any{
		"parentUuid": nil, "isSidechain": true, "agentId": agentID,
		"type": "user", "timestamp": ts, "cwd": cwd, "sessionId": sid,
		"message": map[string]any{"role": "user", "content": "子任务原话"},
	})
}

// subAsstLine 子代理转录 assistant 行（usage 官方四列与主转录同构；消息 id 按
// ts 区分——现实每条助手消息 id 唯一，同 id 重复行由去重语义管）。
func subAsstLine(sid, agentID, cwd, ts, model string, inp, cr, cc, out int) string {
	return mustJSON(map[string]any{
		"parentUuid": "u0", "isSidechain": true, "agentId": agentID,
		"type": "assistant", "timestamp": ts, "cwd": cwd, "sessionId": sid,
		"message": map[string]any{
			"role": "assistant", "id": "msg_" + agentID + "_" + ts, "model": model,
			"content": []any{map[string]any{"type": "text", "text": "子回答原话"}},
			"usage": map[string]any{
				"input_tokens": inp, "cache_read_input_tokens": cr,
				"cache_creation_input_tokens": cc, "output_tokens": out,
			},
		},
	})
}

// mkSubFile 造真实布局的子代理转录：<projects>/C--proj/<父sid>/subagents/<stem>.jsonl。
func mkSubFile(t *testing.T, projects, parentSid, stem string, lines ...string) string {
	t.Helper()
	f := filepath.Join(projects, "C--proj", parentSid, "subagents", stem+".jsonl")
	if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, f, strings.Join(lines, "\n")+"\n")
	return f
}

// recordSubRows 测试侧把子代理出行按 watcher 记账点同形状落账。
func recordSubRows(t *testing.T, acc *accounts.Accounts, rows []Row) {
	t.Helper()
	for _, r := range rows {
		ts := -1.0
		if r.TS != nil {
			ts = *r.TS
		}
		if _, err := acc.Record("usage", ts, accounts.Fields{
			"agent": "cc", "session_id": r.SessionID, "lineage_id": "L",
			"project": r.Project, "model": r.Model, "title": r.Title,
			"input_tokens": r.InputTokens, "cache_read_tokens": r.CacheReadTokens,
			"cache_creation_tokens": r.CacheCreationTokens,
			"output_tokens":         r.OutputTokens, "offset": r.Offset,
			"subagent": r.Subagent,
		}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSubagentRowsShapedToParent(t *testing.T) {
	// 行形状：session_id=父 sid（行内 sessionId）、标记=文件 stem、四列取自
	// 该子代理自己的 assistant 记录；title/project 沿既有语义（cwd 出 project）。
	tmp := t.TempDir()
	acc := newAccounts(t, tmp)
	f := mkSubFile(t, tmp, "P1", "agent-a1",
		subUserLine("P1", "a1", "C:/proj", "2026-09-18T01:00:00Z"),
		subAsstLine("P1", "a1", "C:/proj", "2026-09-18T01:00:05Z", "glm-5.3", 111, 4000, 0, 22))
	rows := NewHarvestState(acc).MaybeHarvestSubagent(f, fileSize(t, f), "cc")
	if len(rows) != 1 {
		t.Fatalf("len(rows) = %d, want 1", len(rows))
	}
	r := rows[0]
	if r.SessionID != "P1" {
		t.Fatalf("SessionID = %q, want 父会话 P1", r.SessionID)
	}
	if r.Subagent != "agent-a1" {
		t.Fatalf("Subagent = %q, want 文件 stem agent-a1", r.Subagent)
	}
	if r.InputTokens != 111 || r.CacheReadTokens != 4000 ||
		r.CacheCreationTokens != 0 || r.OutputTokens != 22 {
		t.Fatalf("四列 = %d/%d/%d/%d, want 111/4000/0/22",
			r.InputTokens, r.CacheReadTokens, r.CacheCreationTokens, r.OutputTokens)
	}
	if r.Project != "C:/proj" {
		t.Fatalf("Project = %q, want 行内 cwd C:/proj", r.Project)
	}
	if r.Offset != fileSize(t, f) {
		t.Fatalf("Offset = %d, want %d", r.Offset, fileSize(t, f))
	}
}

func TestSubagentCompositeKeyNoCrosstalk(t *testing.T) {
	// 两个会话各带同名形态的 agent 文件（同 stem）→ 采集状态不串扰：
	// 各自 offset 独立推进，互不吞行。
	tmp := t.TempDir()
	acc := newAccounts(t, tmp)
	f1 := mkSubFile(t, tmp, "P1", "agent-a1",
		subUserLine("P1", "a1", "C:/proj", "2026-09-18T01:00:00Z"),
		subAsstLine("P1", "a1", "C:/proj", "2026-09-18T01:00:05Z", "glm-5.3", 100, 0, 0, 50))
	f2 := mkSubFile(t, tmp, "P2", "agent-a1",
		subUserLine("P2", "a1", "C:/proj", "2026-09-18T02:00:00Z"),
		subAsstLine("P2", "a1", "C:/proj", "2026-09-18T02:00:05Z", "glm-5.3", 200, 0, 0, 60))
	hs := NewHarvestState(acc)
	r1 := hs.MaybeHarvestSubagent(f1, fileSize(t, f1), "cc")
	if len(r1) != 1 || r1[0].InputTokens != 100 || r1[0].SessionID != "P1" {
		t.Fatalf("P1 首采 = %+v, want 1 行 in=100 归 P1", r1)
	}
	r2 := hs.MaybeHarvestSubagent(f2, fileSize(t, f2), "cc")
	if len(r2) != 1 || r2[0].InputTokens != 200 || r2[0].SessionID != "P2" {
		t.Fatalf("P2 首采被 P1 状态串扰 = %+v, want 1 行 in=200 归 P2", r2)
	}
	// P1 追加：只出 P1 的新行；P2 状态不受影响
	appendFile(t, f1, subAsstLine("P1", "a1", "C:/proj", "2026-09-18T01:01:00Z", "glm-5.3", 1, 2, 0, 3)+"\n")
	r1b := hs.MaybeHarvestSubagent(f1, fileSize(t, f1), "cc")
	if len(r1b) != 1 || r1b[0].InputTokens != 1 {
		t.Fatalf("P1 增量 = %+v, want 1 行 in=1", r1b)
	}
	if rows := hs.MaybeHarvestSubagent(f2, fileSize(t, f2), "cc"); len(rows) != 0 {
		t.Fatalf("P2 无新增应 0 行, got %d", len(rows))
	}
}

func TestSubagentParentSidFromLineNotPath(t *testing.T) {
	// 父 sid 从行内 sessionId 取，勿只靠路径推导：目录名与行内值不同 → 行内赢。
	tmp := t.TempDir()
	acc := newAccounts(t, tmp)
	f := mkSubFile(t, tmp, "DIRNOTSID", "agent-a1",
		subUserLine("P1", "a1", "C:/proj", "2026-09-18T01:00:00Z"),
		subAsstLine("P1", "a1", "C:/proj", "2026-09-18T01:00:05Z", "glm-5.3", 5, 0, 0, 6))
	hs := NewHarvestState(acc)
	rows := hs.MaybeHarvestSubagent(f, fileSize(t, f), "cc")
	if len(rows) != 1 || rows[0].SessionID != "P1" {
		t.Fatalf("rows = %+v, want 1 行归行内 P1", rows)
	}
	// 记忆稳定：追加后仍归同键（不因再次解析漂移）
	appendFile(t, f, subAsstLine("P1", "a1", "C:/proj", "2026-09-18T01:02:00Z", "glm-5.3", 7, 0, 0, 8)+"\n")
	rows = hs.MaybeHarvestSubagent(f, fileSize(t, f), "cc")
	if len(rows) != 1 || rows[0].SessionID != "P1" || rows[0].InputTokens != 7 {
		t.Fatalf("增量 = %+v, want 1 行 in=7 归 P1", rows)
	}
}

func TestSubagentFirstSightBackfillsAll(t *testing.T) {
	// 首见语义=从 offset 0 全量回填（历史已存在的未采集文件），不跳尾。
	tmp := t.TempDir()
	acc := newAccounts(t, tmp)
	f := mkSubFile(t, tmp, "P1", "agent-a1",
		subUserLine("P1", "a1", "C:/proj", "2026-09-18T01:00:00Z"),
		subAsstLine("P1", "a1", "C:/proj", "2026-09-18T01:00:05Z", "glm-5.3", 10, 0, 0, 1),
		subAsstLine("P1", "a1", "C:/proj", "2026-09-18T01:01:05Z", "glm-5.3", 20, 0, 0, 2))
	rows := NewHarvestState(acc).MaybeHarvestSubagent(f, fileSize(t, f), "cc")
	if len(rows) != 2 {
		t.Fatalf("首见应全量回填 2 行, got %d", len(rows))
	}
	if rows[0].Offset != fileSize(t, f) {
		t.Fatalf("Offset = %d, want 全量 %d", rows[0].Offset, fileSize(t, f))
	}
}

func TestSubagentResumeFromAccounts(t *testing.T) {
	// daemon 重启：子代理偏移从账本行恢复——键成分全部取自行内字段
	// （agent、session_id=父 sid、subagent=stem），零命名推导。
	tmp := t.TempDir()
	acc := newAccounts(t, tmp)
	f := mkSubFile(t, tmp, "P1", "agent-a1",
		subUserLine("P1", "a1", "C:/proj", "2026-09-18T01:00:00Z"),
		subAsstLine("P1", "a1", "C:/proj", "2026-09-18T01:00:05Z", "glm-5.3", 100, 0, 0, 50))
	hs := NewHarvestState(acc)
	recordSubRows(t, acc, hs.MaybeHarvestSubagent(f, fileSize(t, f), "cc"))
	appendFile(t, f, subAsstLine("P1", "a1", "C:/proj", "2026-09-18T02:00:00Z", "glm-5.3", 1, 2, 0, 3)+"\n") // 停机期间新增
	hs2 := NewHarvestState(acc)                                                                              // "重启"
	rows := hs2.MaybeHarvestSubagent(f, fileSize(t, f), "cc")
	if len(rows) != 1 || rows[0].InputTokens != 1 || rows[0].Subagent != "agent-a1" {
		t.Fatalf("重启后应只采新增 = %+v", rows)
	}
}

func TestLegacyRowWithoutMarkerRecoversMainKey(t *testing.T) {
	// 旧账本无标记字段的行按主会话键恢复（向后兼容）：手写旧格式 usage 行
	// （无 subagent 键），重启后主转录只采新增。
	tmp := t.TempDir()
	acc := newAccounts(t, tmp)
	f := filepath.Join(tmp, "s1.jsonl")
	mkSession(t, f)
	hs := NewHarvestState(acc)
	hs.MaybeHarvest(f, fileSize(t, f), "cc")
	legacy := mustJSON(map[string]any{"kind": "usage", "agent": "cc", "session_id": "s1",
		"offset": fileSize(t, f), "model": "glm-5.3",
		"input_tokens": 1, "cache_read_tokens": 0,
		"cache_creation_tokens": 0, "output_tokens": 0, "title": "", "project": ""})
	appendFile(t, filepath.Join(tmp, "accounts", "209901.jsonl"), legacy+"\n")
	appendFile(t, f, asstLine("2026-09-18T02:00:00Z", "glm-5.3", 100, 9000, 0, 50)+"\n") // 停机期间新增
	hs2 := NewHarvestState(acc)
	rows := hs2.MaybeHarvest(f, fileSize(t, f), "cc")
	if len(rows) != 1 || rows[0].Offset != fileSize(t, f) {
		t.Fatalf("旧格式行应按主会话键恢复偏移, rows = %+v", rows)
	}
	if rows[0].Subagent != "" || rows[0].SessionID != "" {
		t.Fatalf("主转录出行标记应零值, got %q/%q", rows[0].Subagent, rows[0].SessionID)
	}
}
