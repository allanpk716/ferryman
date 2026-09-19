// ccswitch_test.go — 票20：CC Switch 快照注入验收（规格 tests/test_ccswitch.py 1:1）。
//
// 机制（2026-09-17 T21 三轮实测定案，DESIGN §3）：CC Switch 切换供应商 =
// 把 providers.settings_config 快照**逐字写入** ~/.claude/settings.json——
// 不在快照里的键（hooks）每次切换 / Live 模式重写都会被抹（Orca 同样会灭）。
// 修复 = ferryman 钩子注进全部 claude 供应商快照：幂等、保留既有条目（Orca
// 共存）、改库前自动备份。sqlite 用 modernc.org/sqlite 真库临时文件。

package installer

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// makeDB 建真 sqlite 库 + providers 表（test_ccswitch.py _make_db 同形）。
// providers: list of (app_type, name, settings_config_json)。
func makeDB(t *testing.T, path string, providers [][3]string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("CREATE TABLE providers (id INTEGER PRIMARY KEY, app_type TEXT, " +
		"name TEXT, settings_config TEXT)"); err != nil {
		t.Fatal(err)
	}
	for _, p := range providers {
		if _, err := db.Exec("INSERT INTO providers (app_type, name, settings_config) VALUES (?,?,?)",
			p[0], p[1], p[2]); err != nil {
			t.Fatal(err)
		}
	}
}

// mustJSON 序列化辅助（makeDB 入参用）。
func mustJSON(t *testing.T, v any) string {
	t.Helper()
	return marshalCompact(v)
}

// queryScalar 单值查询。
func queryScalar(t *testing.T, dbPath, query string) string {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var s string
	if err := db.QueryRow(query).Scan(&s); err != nil {
		t.Fatalf("query %q: %v", query, err)
	}
	return s
}

// queryNameRaw name → settings_config 映射（claude 行）。
func queryNameRaw(t *testing.T, dbPath string) map[string]string {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query("SELECT name, settings_config FROM providers WHERE app_type='claude'")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var name, raw string
		if err := rows.Scan(&name, &raw); err != nil {
			t.Fatal(err)
		}
		out[name] = raw
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// parseCfg settings_config JSON → map。
func parseCfg(t *testing.T, raw string) map[string]any {
	t.Helper()
	var v map[string]any
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		t.Fatalf("settings_config 解析失败: %v", err)
	}
	return v
}

// cfgHooks 取快照 hooks 表。
func cfgHooks(t *testing.T, cfg map[string]any) map[string]any {
	t.Helper()
	hooks, _ := cfg["hooks"].(map[string]any)
	if hooks == nil {
		t.Fatal("快照 hooks 缺失")
	}
	return hooks
}

const (
	orcaEntry = `{"hooks":[{"type":"command","command":"C:/Users/allan716/.orca/agent-hooks/claude-hook.cmd || echo {}","timeout":10}]}`
)

// ---- test_inject_all_claude_providers_preserves_existing ----

func TestInjectAllClaudeProvidersPreservesExisting(t *testing.T) {
	tmp := t.TempDir()
	db := filepath.Join(tmp, "cc-switch.db")
	makeDB(t, db, [][3]string{
		{"claude", "有Orca的", mustJSON(t, map[string]any{"env": map[string]any{"A": "1"}, "hooks": map[string]any{
			"UserPromptSubmit": []any{json.RawMessage(orcaEntry)},
			"Stop":             []any{json.RawMessage(orcaEntry)},
		}})},
		{"claude", "干净的", mustJSON(t, map[string]any{"env": map[string]any{"B": "2"}})},
		{"codex", "Codex家", mustJSON(t, map[string]any{"auth": map[string]any{}, "config": "x = 1"})},
	})

	read := captureStdout(t)
	n := InjectCCSwitch(db, tmp, nil)
	read()
	if n != 2 { // 只动 claude
		t.Fatalf("注入数 = %d, want 2", n)
	}

	rows := queryNameRaw(t, db)
	codexRaw := queryScalar(t, db, "SELECT settings_config FROM providers WHERE name='Codex家'")

	for name, raw := range rows {
		cfg := parseCfg(t, raw)
		if _, ok := cfg["env"]; !ok { // 原有内容不动
			t.Fatalf("%s: env 丢失", name)
		}
		hooks := cfgHooks(t, cfg)
		for _, evt := range FerryEvents {
			list := asList(hooks[evt])
			count := 0
			for _, e := range list {
				if strings.Contains(marshalCompact(e), "ferryman") {
					count++
				}
			}
			if count != 1 {
				t.Fatalf("(%s, %s) ferryman 条目数 = %d, want 1", name, evt, count)
			}
		}
	}

	// Orca 共存（同一事件数组里两条并存）
	cfg := parseCfg(t, rows["有Orca的"])
	ups := asList(cfgHooks(t, cfg)["UserPromptSubmit"])
	anyOrca, anyFerry := false, false
	for _, e := range ups {
		blob := marshalCompact(e)
		if strings.Contains(blob, "orca") {
			anyOrca = true
		}
		if strings.Contains(blob, "ferryman") {
			anyFerry = true
		}
	}
	if !anyOrca || !anyFerry {
		t.Fatalf("Orca/ferryman 未共存: orca=%v ferryman=%v", anyOrca, anyFerry)
	}
	// 不归我们管的事件（Stop）原样保留
	stop := asList(cfgHooks(t, cfg)["Stop"])
	if len(stop) != 1 || !strings.Contains(marshalCompact(stop[0]), "orca") {
		t.Fatalf("Stop 应原样保留一条 orca: %s", marshalCompact(stop))
	}
	// SessionStart 带 matcher
	ss := firstMap(t, asList(cfgHooks(t, cfg)["SessionStart"]), "ferryman")
	if m, _ := ss["matcher"].(string); m != "clear|startup" {
		t.Fatalf("SessionStart matcher = %v, want clear|startup", m)
	}
	// codex 供应商不动
	if strings.Contains(codexRaw, "ferryman") {
		t.Fatal("codex 供应商快照被动了")
	}
}

func TestInjectIdempotent(t *testing.T) {
	tmp := t.TempDir()
	db := filepath.Join(tmp, "cc-switch.db")
	makeDB(t, db, [][3]string{{"claude", "P1", mustJSON(t, map[string]any{"env": map[string]any{}})}})
	read := captureStdout(t)
	InjectCCSwitch(db, tmp, nil)
	InjectCCSwitch(db, tmp, nil) // 第二次不重复
	read()
	raw := queryScalar(t, db, "SELECT settings_config FROM providers")
	hooks := cfgHooks(t, parseCfg(t, raw))
	n := 0
	for _, e := range asList(hooks["SubagentStart"]) {
		if strings.Contains(marshalCompact(e), "ferryman") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("幂等后 SubagentStart ferryman 条目数 = %d, want 1", n)
	}
}

func TestInjectReplacesStaleFerrymanEntries(t *testing.T) {
	// 快照里已有旧 ferryman 条目（如手工注入过）→ 替换为最新版，不叠加。
	tmp := t.TempDir()
	db := filepath.Join(tmp, "cc-switch.db")
	stale := mustJSON(t, map[string]any{"env": map[string]any{}, "hooks": map[string]any{
		"SubagentStart": []any{map[string]any{"hooks": []any{
			map[string]any{"type": "command", "command": "ferryman-old.ps1", "timeout": 9}}}},
	}})
	makeDB(t, db, [][3]string{{"claude", "P1", stale}})
	read := captureStdout(t)
	InjectCCSwitch(db, tmp, nil)
	read()
	raw := queryScalar(t, db, "SELECT settings_config FROM providers")
	subs := asList(cfgHooks(t, parseCfg(t, raw))["SubagentStart"])
	if len(subs) != 1 || !strings.Contains(marshalCompact(subs), "ferryman-subagent.ps1") {
		t.Fatalf("旧条目应被替换: %s", marshalCompact(subs))
	}
}

func TestNoDBIsSilentNoop(t *testing.T) {
	tmp := t.TempDir()
	read := captureStdout(t)
	n := InjectCCSwitch(filepath.Join(tmp, "nope.db"), tmp, nil)
	out := read()
	if n != 0 {
		t.Fatalf("无库应返回 0, got %d", n)
	}
	if !strings.Contains(out, "未检测到") {
		t.Fatalf("缺『未检测到』文案: %s", out)
	}
}

func TestBackupCreatedBeforeModifying(t *testing.T) {
	tmp := t.TempDir()
	db := filepath.Join(tmp, "cc-switch.db")
	makeDB(t, db, [][3]string{{"claude", "P1", mustJSON(t, map[string]any{"env": map[string]any{}})}})
	read := captureStdout(t)
	InjectCCSwitch(db, tmp, nil)
	read()
	baks, err := filepath.Glob(filepath.Join(tmp, "cc-switch.db.bak-ferryman-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(baks) != 1 {
		t.Fatalf("备份数 = %d, want 1", len(baks))
	}
	// 备份是修改前的内容（无 ferryman）
	bakRaw := queryScalar(t, baks[0], "SELECT settings_config FROM providers")
	if strings.Contains(bakRaw, "ferryman") {
		t.Fatal("备份含 ferryman——不是修改前的内容")
	}
}

// ---- 备份留 3（删旧失败不抛） ----

func TestPruneCCSwitchBaksKeepsThree(t *testing.T) {
	tmp := t.TempDir()
	db := filepath.Join(tmp, "cc-switch.db")
	// 造 5 份旧备份（时间戳名保证排序）
	for _, ts := range []string{"20260101_000001", "20260101_000002", "20260101_000003", "20260101_000004", "20260101_000005"} {
		name := filepath.Join(tmp, "cc-switch.db.bak-ferryman-"+ts)
		if err := os.WriteFile(name, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	pruneCCSwitchBaks(db, 3)
	got, err := filepath.Glob(filepath.Join(tmp, "cc-switch.db.bak-ferryman-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("备份留 %d 份, want 3: %v", len(got), got)
	}
	for _, g := range got { // 留的应是最新三份
		if strings.HasSuffix(g, "000001") || strings.HasSuffix(g, "000002") {
			t.Fatalf("旧备份未删: %s", g)
		}
	}
}

func TestInstallCCAutoInjectsCCswitch(t *testing.T) {
	tmp := t.TempDir()
	db := filepath.Join(tmp, "cc-switch.db")
	makeDB(t, db, [][3]string{{"claude", "P1", mustJSON(t, map[string]any{"env": map[string]any{}})}})
	p := filepath.Join(tmp, "settings.json")
	if err := os.WriteFile(p, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	read := captureStdout(t)
	InstallCC(p, db, filepath.Join(tmp, "data"), tmp, nil)
	read()
	raw := queryScalar(t, db, "SELECT settings_config FROM providers")
	if !strings.Contains(raw, "ferryman") {
		t.Fatal("install-cc 未自动注入快照")
	}
}
