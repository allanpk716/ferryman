// ccswitch.go — CC Switch 快照注入（规格 ferryman/install.py inject_ccswitch +
// _prune_ccswitch_baks 1:1，票20）。
//
// 机制（2026-09-17 T21 三轮实测定案，DESIGN §3）：CC Switch 切换供应商 =
// 把 providers.settings_config 快照**逐字写入** ~/.claude/settings.json——
// 不在快照里的键（hooks）每次切换 / Live 模式重写都会被抹（Orca 同样会灭）。
// 修复 = ferryman 钩子注进全部 claude 供应商快照：幂等、保留既有条目（Orca
// 共存）、改库前自动备份。新增供应商后需重跑（ferryman install-ccswitch）。
//
// sqlite 走 modernc.org/sqlite（纯 Go 无 CGO，spec 决策）；Python connect
// timeout=10 → busy_timeout PRAGMA（单连接针定，事务同连接语义对齐）。
package installer

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	_ "modernc.org/sqlite" // 驱动名 "sqlite"（纯 Go，无 CGO）
)

// providerRow providers 表一行（id 类型放开——INTEGER PRIMARY KEY → int64）。
type providerRow struct {
	ID   any
	Name string
	Raw  sql.NullString
}

// InjectCCSwitch 把 ferryman 钩子注进 cc-switch.db 全部 claude 供应商快照。
// repo = 钩子脚本仓库根（嵌入条目路径；空 = exe 所在目录——票22 骑手 M4：
// 接参消除 InstallCC 与本函数各自求值 repoRoot() 的分叉）。
// 返回注入的供应商数；无 DB / 任何 sqlite 故障 → 0（绝不影响 settings.json 安装）。
func InjectCCSwitch(dbPath, repo string, events []string) int {
	if dbPath == "" {
		dbPath = CCSwitchDBPath(homeDir())
	}
	if repo == "" {
		repo = repoRoot()
	}
	if _, err := os.Stat(dbPath); errors.Is(err, os.ErrNotExist) {
		fmt.Printf("未检测到 CC Switch（%s 不存在），跳过供应商快照注入。\n", dbPath)
		return 0
	}
	entries := FerryHookEntries(repo, events)
	bak := filepath.Join(filepath.Dir(dbPath),
		filepath.Base(dbPath)+".bak-ferryman-"+stamp())
	if err := backupFile(dbPath, bak); err != nil {
		fmt.Printf("[!] cc-switch.db 备份失败，跳过注入（安全起见）: %v\n", err)
		return 0
	}
	pruneCCSwitchBaks(dbPath, 3) // 防复发：备份逐次 56MB，一晚多次重启曾攒 66 份 3.6GB
	n, err := injectRows(dbPath, bak, entries)
	if err != nil {
		fmt.Printf("[!] CC Switch 快照注入失败（%v）——CC Switch 可能正持有库锁。"+
			"稍后重跑 `ferryman install-ccswitch`。\n", err)
		return 0
	}
	return n
}

// injectRows 单连接事务内完成：读全表 claude 行 → 逐行幂等合并 → 一次提交
// （Python 隐式事务 + conn.commit() 同形：中途失败全量不落）。
func injectRows(dbPath, bak string, entries map[string]any) (int, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return 0, err
	}
	defer db.Close()
	ctx := context.Background()
	conn, err := db.Conn(ctx) // 单连接针定：PRAGMA 与 BEGIN/COMMIT 同连接
	if err != nil {
		return 0, err
	}
	defer conn.Close()
	// Python sqlite3.connect(db_path, timeout=10) 同位
	if _, err := conn.ExecContext(ctx, "PRAGMA busy_timeout=10000"); err != nil {
		return 0, err
	}
	if _, err := conn.ExecContext(ctx, "BEGIN"); err != nil {
		return 0, err
	}
	rows, err := conn.QueryContext(ctx,
		"SELECT id, name, settings_config FROM providers WHERE app_type='claude'")
	if err != nil {
		return 0, err
	}
	var prows []providerRow
	for rows.Next() {
		var r providerRow
		if err := rows.Scan(&r.ID, &r.Name, &r.Raw); err != nil {
			rows.Close()
			return 0, err
		}
		prows = append(prows, r)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	rows.Close()

	for _, r := range prows {
		cfg := parseSnapshot(r.Raw)
		old, _ := cfg["hooks"].(map[string]any)
		merged := map[string]any{}
		for _, evt := range sortedUnion(keysOf(old), keysOf(entries)) {
			var kept []any
			for _, e := range asList(old[evt]) {
				if _, isObj := e.(map[string]any); isObj &&
					!strings.Contains(marshalCompact(e), "ferryman") {
					kept = append(kept, e)
				}
			}
			merged[evt] = append(kept, asList(entries[evt])...) // entries.get(evt, []) 同位
		}
		cfg["hooks"] = merged
		if _, err := conn.ExecContext(ctx,
			"UPDATE providers SET settings_config=? WHERE id=?",
			string(dumpJSON(cfg)), r.ID); err != nil {
			return 0, err
		}
		fmt.Printf("[ccswitch] ✓ %s: ferryman 四钩子已入快照"+
			"（既有条目保留，备份 %s）\n", r.Name, filepath.Base(bak))
	}
	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil { // conn.commit()
		return 0, err
	}
	return len(prows), nil
}

// parseSnapshot settings_config 列 → dict（Python json.loads + isinstance 守卫
// 逐字：坏 JSON / 非 dict / NULL → 空表）。
func parseSnapshot(raw sql.NullString) map[string]any {
	var cfg map[string]any
	if !raw.Valid || json.Unmarshal([]byte(raw.String), &cfg) != nil || cfg == nil {
		return map[string]any{}
	}
	return cfg
}

// sortedUnion 两键集并集（Python set(old) | set(entries) 的确定性形——排序
// 只为输出稳定，JSON 对象键序本无语义）。
func sortedUnion(a, b []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, k := range append(append([]string{}, a...), b...) {
		if !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// readClaudeProviders 读 providers 表 claude 行（CheckCCSwitch 用，只读不写；
// busyTimeoutMS 对齐 Python connect(timeout=5)）。
func readClaudeProviders(dbPath string, busyTimeoutMS int) ([]providerRow, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx,
		fmt.Sprintf("PRAGMA busy_timeout=%d", busyTimeoutMS)); err != nil {
		return nil, err
	}
	rows, err := conn.QueryContext(ctx,
		"SELECT id, name, settings_config FROM providers WHERE app_type='claude'")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []providerRow
	for rows.Next() {
		var r providerRow
		if err := rows.Scan(&r.ID, &r.Name, &r.Raw); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// pruneCCSwitchBaks 删旧备份只留最近 keep 份（含刚写的那份）。删失败不抛——
// 备份多余只是占盘，不影响注入（install.py _prune_ccswitch_baks 逐字）。
func pruneCCSwitchBaks(dbPath string, keep int) {
	baks, err := filepath.Glob(filepath.Join(filepath.Dir(dbPath),
		filepath.Base(dbPath)+".bak-ferryman-*"))
	if err != nil {
		return
	}
	sort.Strings(baks)
	var olds []string
	if keep > 0 {
		if len(baks) > keep {
			olds = baks[:len(baks)-keep]
		}
	} else {
		olds = baks
	}
	for _, old := range olds {
		_ = os.Remove(old) // OSError 不抛同位
	}
}
