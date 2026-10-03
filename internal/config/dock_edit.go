// dock_edit.go — 票06：`ferryman provider add/remove/import-ccswitch` 的上游
// 表增删写回（AddDockUpstream / RemoveDockUpstream / MergeDockUpstreams）。
//
// 语义（与 dock_setactive.go / dock_migrate.go 同纪律）：
//   - 文本手术只增删目标条目：删除＝整段摘掉该条目行跨度（表头到下一表头前），
//     新增＝渲染条目块插到 dock 节跨度末尾；既有条目字节、[dock] 主表其余键
//     （drain_timeout_s 等迁移后新键）与其余节逐字保留；
//   - 前置守卫：[dock] 节与 [dock.upstreams] 表必须在（旧单值形态无表可编，
//     由守护首启迁移）；重名拒增；active 拒删；
//   - 前置校验（与 Validate 同单源 validateDockUpstreams）：改后的表必须仍过
//     校验——写回会让守护拒启的先拦下；
//   - 自校验（rename 前跑）：新全文可解码；dock 节经生产解析器回读与预期逐字
//     段相等；其余节解码等价。任何不符即拒写，原文件字节不动；
//   - 原子写：同目录临时文件＋rename（MoveFileEx 语义），权限沿用原文件。
//
// 条目块渲染（renderUpstreamEntry）字段序与 dock_migrate.go renderDockSection
// 的条目段同序（base_url/api_key/model_map/text_only/balance_url），另补票02
// 的 dialect/codex 两键（迁移条目恒为 anthropic/空，渲染不涉；两处渲染若日後
// 需合流，以本函数为准回填）。dialect 恒显式落盘（空值在写入前归一 anthropic
// ——避免「缺省还是有意」歧义）；codex 仅在否决位在位时落盘；pi 行同款（票12
// 补，插在 codex 后——票09 否决位写入面移交至此落地）。
package config

import (
	"fmt"
	"os"
	"reflect"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

// AddDockUpstream 原子新增一条上游条目（票06 provider add）。重名/非法条目/
// 无上游表＝报错且原文件不动。
func AddDockUpstream(path, name string, up DockUpstream) error {
	return editDockUpstreams(path, nil, map[string]DockUpstream{name: up})
}

// RemoveDockUpstream 原子删除一条上游条目（票06 provider remove）。active
// 条目拒删（删了 active 会悬空、守护下次启动拒启）；未知条目＝报错不动。
func RemoveDockUpstream(path, name string) error {
	return editDockUpstreams(path, []string{name}, nil)
}

// MergeDockUpstreams 批量原子新增（票06 import-ccswitch）：任一名与既有表
// 冲突＝整体拒绝（不部分写入，调用方先做逐名冲突检查可得到逐条报告）。
func MergeDockUpstreams(path string, ups map[string]DockUpstream) error {
	return editDockUpstreams(path, nil, ups)
}

// CheckDockUpstreamEntry 单条目可写性检查（票06 CLI 新增/导入前用，借既有表
// 作上下文）：把条目并入副本后跑 validateDockUpstreams，返回问题列表（空＝
// 可写）。注意列表可能含既有表的存量问题（它们本就让守护拒启，如实带出）。
func CheckDockUpstreamEntry(d *DockCfg, name string, up DockUpstream) []string {
	if d == nil {
		return []string{"[dock] 节缺失"}
	}
	if _, ok := d.Upstreams[name]; ok {
		return []string{fmt.Sprintf("条目 %q 已存在", name)}
	}
	next := cloneUpstreams(d.Upstreams)
	c := cloneDockUpstream(up)
	if c.Dialect == "" {
		c.Dialect = DialectAnthropic
	}
	next[name] = c
	return validateDockUpstreams(&DockCfg{Listen: d.Listen, Active: d.Active, Upstreams: next})
}

// cloneUpstreams 表深拷贝＋dialect 归一（空 → anthropic，与解析层同语义——
// 预期值与写回产物解析值逐字段相等的前提）。
func cloneUpstreams(m map[string]DockUpstream) map[string]DockUpstream {
	out := make(map[string]DockUpstream, len(m)+1)
	for k, v := range m {
		c := cloneDockUpstream(v)
		if c.Dialect == "" {
			c.Dialect = DialectAnthropic
		}
		out[k] = c
	}
	return out
}

// editDockUpstreams 增删一体核心：remove 与 add 同一批处理（先删后增，名不
// 相交由调用语义保证——同批同名先删后增也是合法用形）。全拒或全成，无部分写。
func editDockUpstreams(path string, remove []string, add map[string]DockUpstream) error {
	p := resolveConfigPath(path)
	raw, err := os.ReadFile(p)
	if err != nil {
		return err
	}
	var data map[string]any
	if _, err := toml.DecodeFile(p, &data); err != nil {
		return fmt.Errorf("config: 原文件解析失败（不动）: %w", err)
	}
	rawDock, ok := data["dock"]
	if !ok {
		return fmt.Errorf("config: 无 [dock] 节，上游表无从增删（原文件未动）")
	}
	dk, err := asTable(rawDock, "dock")
	if err != nil {
		return err
	}
	cur, err := parseDockSection(dk) // 形态合法性（上游表/子表类型错早暴露）
	if err != nil {
		return err
	}
	if len(cur.Upstreams) == 0 {
		return fmt.Errorf("config: 无 [dock.upstreams] 上游表（旧单值形态——守护首启迁移后才有；原文件未动）")
	}
	next := cloneUpstreams(cur.Upstreams)
	for _, name := range remove {
		if _, ok := next[name]; !ok {
			return fmt.Errorf("config: 条目 %q 不在上游表内（原文件未动）", name)
		}
		if name == cur.Active {
			return fmt.Errorf("config: 条目 %q 是当前 active，拒删（先切到其他条目再删；原文件未动）", name)
		}
		delete(next, name)
	}
	for name, up := range add {
		if _, ok := next[name]; ok {
			return fmt.Errorf("config: 条目 %q 已存在（原文件未动）", name)
		}
		c := cloneDockUpstream(up)
		if c.Dialect == "" {
			c.Dialect = DialectAnthropic
		}
		next[name] = c
	}
	// 前置校验（与 Validate 同单源）：改后的表必须仍过校验——写回会让守护
	// 拒启的条目（缺 base_url/非本地缺 default/dialect 非法等）在这里就拒绝。
	probe := &DockCfg{Listen: cur.Listen, Active: cur.Active, Upstreams: next}
	if probs := validateDockUpstreams(probe); len(probs) > 0 {
		return fmt.Errorf("config: 写回前置校验失败（新表会让守护拒启，原文件未动）: %s",
			strings.Join(probs, "; "))
	}
	newRaw, err := rewriteUpstreamsTable(raw, remove, add)
	if err != nil {
		return err
	}
	if err := verifyDockEdit(raw, newRaw, probe); err != nil {
		return err
	}
	mode := os.FileMode(0o600)
	if fi, err := os.Stat(p); err == nil {
		mode = fi.Mode().Perm() // 保留原文件权限（含真钥的敏感能级不放宽）
	}
	tmp := p + ".edit-tmp"
	if err := os.WriteFile(tmp, newRaw, mode); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, p); err != nil { // 原子替换（Windows MoveFileEx 语义）
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// rewriteUpstreamsTable 文本手术：删除目标条目行跨度、在 dock 节跨度末尾插入
// 新条目块；其余字节逐字保留。行尾跟随原文件主流行尾（CRLF/LF）。
func rewriteUpstreamsTable(raw []byte, remove []string, add map[string]DockUpstream) ([]byte, error) {
	text := string(raw)
	lines := strings.SplitAfter(text, "\n")
	nl := "\n"
	if i := strings.Index(text, "\n"); i > 0 && text[i-1] == '\r' {
		nl = "\r\n"
	}
	// 删除：逐名定位条目表头行（引号键经 tomlKey 同形渲染），整段摘除
	// [表头, 下一表头) 跨度；表头前恰为空行时连空行一并摘（防空行堆积）。
	for _, name := range remove {
		header := "[dock.upstreams." + tomlKey(name) + "]"
		idx := -1
		for i, ln := range lines {
			if strings.TrimSpace(ln) == header {
				idx = i
				break
			}
		}
		if idx < 0 {
			return nil, fmt.Errorf("config: 未定位到条目 %q 的表头行 %s（引号/异形形态不支持），保守拒写",
				name, header)
		}
		end := nextHeaderAfter(lines, idx)
		start := idx
		if start > 0 && strings.TrimSpace(lines[start-1]) == "" {
			start--
		}
		lines = append(lines[:start], lines[end:]...)
	}
	// 插入：渲染新增条目块（名排序——写入确定性），插到 dock 节跨度末尾
	//（下一个非 dock 表头前/文件尾）。
	if len(add) > 0 {
		_, end, found := findDockSpan(lines) // 删除后行号已变，重定位（跨度末尾插入）
		if !found {
			return nil, fmt.Errorf("config: 无法定位 [dock] 表头（内联/异形形态），保守拒写")
		}
		names := make([]string, 0, len(add))
		for name := range add {
			names = append(names, name)
		}
		sort.Strings(names)
		var blocks strings.Builder
		for _, name := range names {
			blocks.WriteString(renderUpstreamEntry(name, add[name], nl))
		}
		// 块首不再补空行：前一行非空才补分隔空行（前一行已空则直接续）。
		sep := ""
		if end > 0 && strings.TrimSpace(lines[end-1]) != "" {
			sep = nl
		}
		// dock 跨度到文件尾且末行无行尾：先补行尾，避免新条目表头与末行粘连。
		if end == len(lines) && len(lines) > 0 && !strings.HasSuffix(lines[len(lines)-1], "\n") {
			lines[len(lines)-1] += nl
		}
		lines = append(lines[:end], append([]string{sep + blocks.String()}, lines[end:]...)...)
	}
	return []byte(strings.Join(lines, "")), nil
}

// renderUpstreamEntry 渲染一条上游条目块（首行表头、尾随一空行；字段序见
// 文件头注释）。dialect 恒落盘（调用方已归一非空）；codex/pi 仅否决位在位时
// 落（pi 行票12 补，插在 codex 后——对标 codex 的显式否决位写入面）。
func renderUpstreamEntry(name string, up DockUpstream, nl string) string {
	var b strings.Builder
	w := func(format string, args ...any) {
		b.WriteString(fmt.Sprintf(format, args...))
		b.WriteString(nl)
	}
	w("[dock.upstreams.%s]", tomlKey(name))
	w("base_url = %s", tomlString(up.BaseURL))
	w("api_key = %s", tomlString(up.APIKey))
	if len(up.ModelMap) > 0 {
		w("model_map = { %s }", renderModelMapInline(up.ModelMap))
	}
	if len(up.TextOnly) > 0 {
		quoted := make([]string, len(up.TextOnly))
		for i, m := range up.TextOnly {
			quoted[i] = tomlString(m)
		}
		w("text_only = [%s]", strings.Join(quoted, ", "))
	}
	if up.BalanceURL != "" {
		w("balance_url = %s", tomlString(up.BalanceURL))
	}
	if up.Dialect != "" {
		w("dialect = %s", tomlString(up.Dialect))
	}
	if up.Codex != "" {
		w("codex = %s", tomlString(up.Codex))
	}
	if up.Pi != "" {
		w("pi = %s", tomlString(up.Pi))
	}
	w("") // 与后续条目/节保持一空行间隔
	return b.String()
}

// verifyDockEdit rename 前自校验：新全文可解码；dock 节经生产解析器回读与
// 预期（listen/active/上游表）逐字段相等；其余节解码等价。
func verifyDockEdit(oldRaw, newRaw []byte, want *DockCfg) error {
	var m1, m2 map[string]any
	if err := toml.Unmarshal(newRaw, &m2); err != nil {
		return fmt.Errorf("config: 写回产物非合法 TOML（不落盘）: %w", err)
	}
	if err := toml.Unmarshal(oldRaw, &m1); err != nil {
		return err
	}
	d2, err := asTable(m2["dock"], "dock")
	if err != nil {
		return fmt.Errorf("config: 写回产物缺 [dock] 节（不落盘）: %w", err)
	}
	parsed, err := parseDockSection(d2)
	if err != nil {
		return fmt.Errorf("config: 写回产物 dock 节解析失败（不落盘）: %w", err)
	}
	if parsed.Listen != want.Listen || parsed.Active != want.Active ||
		!reflect.DeepEqual(parsed.Upstreams, want.Upstreams) {
		return fmt.Errorf("config: 写回产物与预期不符（不落盘）: got active=%q 条目数=%d",
			parsed.Active, len(parsed.Upstreams))
	}
	delete(m1, "dock")
	delete(m2, "dock")
	if !reflect.DeepEqual(m1, m2) {
		return fmt.Errorf("config: 写回改动了 [dock] 之外的节（不落盘）")
	}
	return nil
}
