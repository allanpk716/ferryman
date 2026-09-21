// dock_setactive.go — 票02：`upstream use` 的配置写回（只改 [dock].active 键）。
//
// 语义（票面钉死：只改 active 键、其余节逐字保留）：
//   - 文本手术：定位 [dock] 主表跨度（裸 [dock] 表头行到下一个任意表头行前），
//     原地替换 active 行；无 active 行则插到 [dock] 表头之后。跨度外字节逐字
//     保留——用户注释与未知节不可丢（dock_migrate.go 同纪律）。
//   - 前置守卫：目标名必须已在上游表内（无表＝旧单值形态，use 本就无可切，
//     绝不凭空写 active 指向不存在条目——那会让下次启动拒启）。
//   - 自校验（rename 前跑）：新全文可解码；dock 节经生产解析器回读 active==
//     目标名、listen/上游表与原文件一致；其余节解码等价。任何不符即拒写，
//     原文件字节不动。
//   - 原子写：同目录临时文件＋rename（MoveFileEx 语义），权限沿用原文件。
package config

import (
	"fmt"
	"os"
	"reflect"
	"strings"

	"github.com/BurntSushi/toml"
)

// SetActiveUpstream 把 [dock].active 原子改写为 name（票02 `upstream use`）。
// 目标名必须已存在于上游表；文件不存在/异形/自校验不过＝原样报错且原文件不动。
func SetActiveUpstream(path, name string) error {
	p := resolveConfigPath(path)
	raw, err := os.ReadFile(p)
	if err != nil {
		return err
	}

	// 前置守卫：目标名必须在既有上游表内（解析层单源 parseDockSection）。
	var data map[string]any
	if _, err := toml.DecodeFile(p, &data); err != nil {
		return fmt.Errorf("config: 原文件解析失败（不动）: %w", err)
	}
	rawDock, ok := data["dock"]
	if !ok {
		return fmt.Errorf("config: 无 [dock] 节，无上游可切（原文件未动）")
	}
	dk, err := asTable(rawDock, "dock")
	if err != nil {
		return err
	}
	_, err = parseDockSection(dk) // 形态合法性（上游表/子表类型错早暴露）
	if err != nil {
		return err
	}
	rawUps, ok := dk["upstreams"]
	if !ok {
		return fmt.Errorf("config: 无 [dock.upstreams] 上游表（旧单值形态/未迁移），无可切换条目（原文件未动）")
	}
	ups, err := asTable(rawUps, "dock.upstreams")
	if err != nil {
		return err
	}
	if _, ok := ups[name]; !ok {
		return fmt.Errorf("config: 条目 %q 不在上游表内（原文件未动）", name)
	}

	newRaw, err := rewriteDockActive(raw, name)
	if err != nil {
		return err
	}
	if err := verifyActiveRewrite(raw, newRaw, name); err != nil {
		return err
	}
	mode := os.FileMode(0o600)
	if fi, err := os.Stat(p); err == nil {
		mode = fi.Mode().Perm() // 保留原文件权限（含真钥的敏感能级不放宽）
	}
	tmp := p + ".active-tmp"
	if err := os.WriteFile(tmp, newRaw, mode); err != nil {
		return err
	}
	if err := os.Rename(tmp, p); err != nil { // 原子替换（Windows MoveFileEx 语义）
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// ResolveConfigPath 配置路径解析面（显式参数 > FERRYMAN_CONFIG > ~/ferryman/
// config.toml；CLI 提示语与写回共用同一解析，避免两处漂移）。
func ResolveConfigPath(path string) string { return resolveConfigPath(path) }

// rewriteDockActive 文本手术：只替换 [dock] 主表跨度内的 active 行（无则插
// 到 [dock] 表头后），其余字节逐字保留。行尾跟随原行（CRLF/LF/无行尾）。
func rewriteDockActive(raw []byte, name string) ([]byte, error) {
	lines := strings.SplitAfter(string(raw), "\n")
	start, ok := findBareDockHeader(lines)
	if !ok {
		return nil, fmt.Errorf("config: 无法定位裸 [dock] 表头（内联/异形形态），保守拒写")
	}
	end := nextHeaderAfter(lines, start) // 主表跨度 = 表头后到下一个任意表头前

	newLine := "active = " + tomlString(name)
	for i := start + 1; i < end; i++ {
		t := strings.TrimLeft(lines[i], " \t")
		if isActiveKeyLine(t) {
			lines[i] = newLine + lineEnding(lines[i])
			return []byte(strings.Join(lines, "")), nil
		}
		if strings.HasPrefix(t, `"active"`) { // 引号键异形：替换无从保证唯一，保守拒
			return nil, fmt.Errorf("config: [dock] 含引号键 \"active\"（异形形态），保守拒写")
		}
	}
	// 无 active 行：插到 [dock] 表头之后（跟随表头行行尾）
	insert := newLine + lineEnding(lines[start])
	out := strings.Join(lines[:start+1], "") + insert + strings.Join(lines[start+1:], "")
	return []byte(out), nil
}

// findBareDockHeader 恰是 [dock]（非子表）的表头行索引。子表（[dock.xxx]）
// 不算——active 只在主表。
func findBareDockHeader(lines []string) (int, bool) {
	for i, ln := range lines {
		t := strings.TrimLeft(ln, " \t\r")
		if strings.HasPrefix(t, "[dock]") {
			return i, true
		}
	}
	return 0, false
}

// nextHeaderAfter start 之后第一个表头行（任意节）索引；无则到文件尾。
func nextHeaderAfter(lines []string, start int) int {
	for i := start + 1; i < len(lines); i++ {
		if strings.HasPrefix(strings.TrimLeft(lines[i], " \t\r"), "[") {
			return i
		}
	}
	return len(lines)
}

// isActiveKeyLine 判裸键 active 的赋值行：active 后只许空白到 '='（注释行/
// active_xxx 键不误伤）。
func isActiveKeyLine(trimmed string) bool {
	rest := strings.TrimPrefix(trimmed, "active")
	if rest == trimmed { // 前缀就没对上
		return false
	}
	rest = strings.TrimLeft(rest, " \t")
	return strings.HasPrefix(rest, "=")
}

// lineEnding 提取一行自己的行尾符（CRLF/LF/无行尾——文件末行可能不带）。
func lineEnding(line string) string {
	if strings.HasSuffix(line, "\r\n") {
		return "\r\n"
	}
	if strings.HasSuffix(line, "\n") {
		return "\n"
	}
	return ""
}

// verifyActiveRewrite rename 前自校验：新全文可解码；dock 节经生产解析器回
// 读 active==目标名、listen 与上游表逐字段等于原文件；其余节解码等价。
func verifyActiveRewrite(oldRaw, newRaw []byte, name string) error {
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
	p2, err := parseDockSection(d2)
	if err != nil {
		return fmt.Errorf("config: 写回产物 dock 节解析失败（不落盘）: %w", err)
	}
	d1, err := asTable(m1["dock"], "dock")
	if err != nil {
		return err
	}
	p1, err := parseDockSection(d1)
	if err != nil {
		return err
	}
	if p2.Active != name || p1.Listen != p2.Listen || !reflect.DeepEqual(p1.Upstreams, p2.Upstreams) {
		return fmt.Errorf("config: 写回产物与预期不符（不落盘）: got active=%q", p2.Active)
	}
	delete(m1, "dock")
	delete(m2, "dock")
	if !reflect.DeepEqual(m1, m2) {
		return fmt.Errorf("config: 写回改动了 [dock] 之外的节（不落盘）")
	}
	return nil
}
