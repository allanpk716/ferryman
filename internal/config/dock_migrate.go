// dock_migrate.go — 票01：旧 [dock] 单值配置的首启迁移＋三条未激活预置。
//
// 语义（票面钉死）：
//   - 触发：[dock] 节存在且无 [dock.upstreams] 表；已存在新表（手写并存/已
//     迁移）即跳过（F9）；无 [dock] 节静默跳过（F11 opt-in 不破坏）。
//   - 迁移产物：名为 cc-switch 的回退条目（继承旧单值全部有效值——含解析层
//     已物化的默认 upstream/balance 端点，保证升级当天行为零变化）＋智谱/
//     kimi/deepseek 三条未激活预置（api_key 留空占位）；active 指向 cc-switch。
//   - 写回：文本手术只替换 [dock] 节跨度（其余节逐字保留——本文件绝不从
//     结构体重排全文件，用户注释与未知节不可丢）；原子写（同目录临时文件＋
//     rename）。rename 前先解码校验新文件（dock 表与预期 DeepEqual、其余节
//     与原文件 DeepEqual），任何不符即拒迁——原文件字节不动。
//   - 失败契约：如实报错＋不破坏原配置；调用方（守护）按旧单值行为继续跑
//     （ActiveUpstream 的旧单值兜底包装），宁可退回旧行为，不丢配置。
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
)

// dockPresetUpstreams 三条未激活预置（票01；D13：拼写按 2026-09-21 官方文档
// 查证，终值用户验收）。api_key 留空占位——填 key 即可用；kimi 不用
// kimi-for-coding-highspeed（其"强制 thinking"属性未核实，F5）。
var dockPresetUpstreams = []struct {
	name string
	up   DockUpstream
}{
	{"智谱", DockUpstream{
		BaseURL:    "https://open.bigmodel.cn/api/anthropic",
		ModelMap:   map[string]string{"default": "glm-5.3", "opus": "glm-5.3", "sonnet": "glm-5.3", "haiku": "glm-5.3-flash"},
		BalanceURL: "https://open.bigmodel.cn/api/user/balance",
	}},
	{"kimi", DockUpstream{
		BaseURL:  "https://api.kimi.com/coding/",
		ModelMap: map[string]string{"default": "kimi-for-coding", "sonnet": "kimi-for-coding", "opus": "k3", "haiku": "k3-256k"},
	}},
	{"deepseek", DockUpstream{
		BaseURL:  "https://api.deepseek.com/anthropic",
		ModelMap: map[string]string{"default": "deepseek-flash", "sonnet": "deepseek-flash", "haiku": "deepseek-flash", "opus": "deepseek-v4-pro"},
	}},
}

// dockLegacyKnownKeys 迁移可安全处置的 [dock] 键集。出现集合外键＝保守拒迁：
// 文本手术会整节替换，未知键无处安放，宁可不迁也不丢配置。
var dockLegacyKnownKeys = map[string]bool{
	"upstream_base_url": true, "api_key": true, "rewrite_enabled": true,
	"model_map": true, "text_only": true, "balance_url": true,
	"listen": true, "active": true, "upstreams": true,
}

// MigrateDockFirstBoot 首启迁移＋预置（仅守护进程调用——CLI/doctor 只读解析，
// 绝不经此写回用户配置）。成功后就地以新表视图替换 cfg.Dock（其余字段仍取
// 自原文件解析，未动）。详见文件头语义。
func MigrateDockFirstBoot(path string, cfg *Config) error {
	p := resolveConfigPath(path)
	if _, err := os.Stat(p); err != nil {
		return nil // 文件不存在＝无配置可迁（Dock 保持 nil）
	}
	data := map[string]any{}
	if _, err := toml.DecodeFile(p, &data); err != nil {
		return err // Load 阶段早已失败的路径，防御性原样上抛
	}
	rawDock, ok := data["dock"]
	if !ok {
		return nil
	}
	dk, err := asTable(rawDock, "dock")
	if err != nil {
		return err
	}
	if _, hasTable := dk["upstreams"]; hasTable {
		return nil // 新表已在：跳过迁移（幂等＋F9 并存裁定）
	}
	for k := range dk {
		if !dockLegacyKnownKeys[k] {
			return fmt.Errorf("config: [dock] 含未认识键 %q，保守跳过迁移（原配置未动）", k)
		}
	}
	dcfg, err := parseDockSection(dk)
	if err != nil {
		return err
	}

	newDock := &DockCfg{Listen: dcfg.Listen, Active: "cc-switch"}
	newDock.Upstreams = map[string]DockUpstream{
		"cc-switch": {
			// 继承旧单值有效值（parseDockSection 已物化解析层默认）：
			// 上游默认 15721、余额默认端点单源——升级当天行为零变化。
			BaseURL:    dcfg.UpstreamBaseURL,
			APIKey:     dcfg.APIKey,
			ModelMap:   dcfg.ModelMap,
			TextOnly:   dcfg.TextOnly,
			BalanceURL: dcfg.BalanceURL,
		},
	}
	for _, pr := range dockPresetUpstreams {
		newDock.Upstreams[pr.name] = cloneDockUpstream(pr.up) // 深拷贝：预置是包级模板，产物不与之共享 map/slice
	}
	// 未来表先过校验（与 Validate 同单源）：迁出一半才发现不合法＝写出会让
	// 下次启动拒启的配置，绝不干。
	if probs := validateDockUpstreams(newDock); len(probs) > 0 {
		return fmt.Errorf("config: 旧 [dock] 单值迁为上游表将违反校验，不迁移（原配置未动）: %s",
			strings.Join(probs, "; "))
	}

	rawBytes, err := os.ReadFile(p)
	if err != nil {
		return err
	}
	newRaw, err := replaceDockSection(rawBytes, newDock)
	if err != nil {
		return err
	}
	if err := verifyMigration(rawBytes, newRaw, newDock); err != nil {
		return err
	}
	mode := os.FileMode(0o600)
	if fi, err := os.Stat(p); err == nil {
		mode = fi.Mode().Perm() // 保留原文件权限（含真钥的敏感能级不放宽）
	}
	tmp := p + ".migrate-tmp"
	if err := os.WriteFile(tmp, newRaw, mode); err != nil {
		return err
	}
	if err := os.Rename(tmp, p); err != nil { // 原子替换（Windows MoveFileEx 语义）
		_ = os.Remove(tmp)
		return err
	}
	cfg.Dock = newDock // 就地换新表视图
	return nil
}

// cloneDockUpstream 条目深拷贝（ModelMap/TextOnly 引用不共享）。
func cloneDockUpstream(u DockUpstream) DockUpstream {
	c := u
	if u.ModelMap != nil {
		c.ModelMap = make(map[string]string, len(u.ModelMap))
		for k, v := range u.ModelMap {
			c.ModelMap[k] = v
		}
	}
	if u.TextOnly != nil {
		c.TextOnly = append([]string(nil), u.TextOnly...)
	}
	return c
}

// resolveConfigPath Load 同款路径解析：显式参数 > FERRYMAN_CONFIG > ~/ferryman/config.toml。
func resolveConfigPath(path string) string {
	p := path
	if p == "" {
		p = os.Getenv("FERRYMAN_CONFIG")
	}
	if p == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			home = ""
		}
		p = filepath.Join(home, "ferryman", "config.toml")
	}
	return p
}

// verifyMigration rename 前的完整自校验：新全文可解码、dock 表与预期逐字段
// 一致（经生产解析器 parseDockSection 回读）、其余节与原文件逐字等价。
func verifyMigration(oldRaw, newRaw []byte, want *DockCfg) error {
	var m2 map[string]any
	if err := toml.Unmarshal(newRaw, &m2); err != nil {
		return fmt.Errorf("config: 迁移产物非合法 TOML（不落盘）: %w", err)
	}
	dock2, err := asTable(m2["dock"], "dock")
	if err != nil {
		return fmt.Errorf("config: 迁移产物缺 [dock] 节（不落盘）: %w", err)
	}
	parsed, err := parseDockSection(dock2)
	if err != nil {
		return fmt.Errorf("config: 迁移产物 dock 节解析失败（不落盘）: %w", err)
	}
	if parsed.Active != want.Active || parsed.Listen != want.Listen ||
		!reflect.DeepEqual(parsed.Upstreams, want.Upstreams) {
		return fmt.Errorf("config: 迁移产物与预期不符（不落盘）: got %+v", parsed)
	}
	var m1 map[string]any
	if err := toml.Unmarshal(oldRaw, &m1); err != nil {
		return err
	}
	delete(m1, "dock")
	delete(m2, "dock")
	if !reflect.DeepEqual(m1, m2) {
		return fmt.Errorf("config: 迁移改动了 [dock] 之外的节（不落盘）")
	}
	return nil
}

// replaceDockSection 文本手术：定位 [dock] 节跨度（首个 dock 表头行到下一个
// 非 dock 表头行前），整段替换为渲染出的新节；跨度外字节逐字保留。
func replaceDockSection(raw []byte, nd *DockCfg) ([]byte, error) {
	text := string(raw)
	lines := strings.SplitAfter(text, "\n")
	start, end, found := findDockSpan(lines)
	if !found {
		return nil, fmt.Errorf("config: 无法定位 [dock] 表头（内联/异形形态），保守跳过迁移")
	}
	nl := "\n"
	if i := strings.Index(text, "\n"); i > 0 && text[i-1] == '\r' {
		nl = "\r\n" // 跟随原文件主流行尾
	}
	out := strings.Join(lines[:start], "") + renderDockSection(nd, nl) + strings.Join(lines[end:], "")
	return []byte(out), nil
}

// findDockSpan 返回 dock 节跨度 [start, end)（行索引，含行尾符）。dock 节＝
// 所有首段为 dock 的表头行（含 [dock.model_map] 等子表）到下一个非 dock 表头
// 之前。
func findDockSpan(lines []string) (start, end int, found bool) {
	start = -1
	for i, ln := range lines {
		t := strings.TrimLeft(ln, " \t\r")
		if !strings.HasPrefix(t, "[") {
			continue // 普通行（含节内键值/空行/注释）
		}
		if firstHeaderSegment(t) != "dock" {
			if start >= 0 {
				return start, i, true
			}
			continue // dock 之前的其他节
		}
		if start < 0 {
			start = i
		}
	}
	if start < 0 {
		return 0, 0, false
	}
	return start, len(lines), true
}

// firstHeaderSegment 表头行首段键名：`[dock.upstreams."a.b"]` → dock（段内
// 引号剥除；只用于跨度识别，非完整 TOML 解析——歧义形态由 verifyMigration
// 的解码比对兜底）。
func firstHeaderSegment(headerLine string) string {
	body := strings.TrimPrefix(headerLine, "[")
	if i := strings.Index(body, "]"); i >= 0 {
		body = body[:i]
	}
	seg := body
	if i := strings.Index(body, "."); i >= 0 {
		seg = body[:i]
	}
	seg = strings.TrimSpace(seg)
	return strings.Trim(seg, `"'`)
}

// renderDockSection 渲染新 [dock] 节全文（行尾跟随原文件）。条目序固定：
// cc-switch（迁移条目）在前，其余按名排序——渲染确定性，幂等重写逐字节稳定。
func renderDockSection(nd *DockCfg, nl string) string {
	var b strings.Builder
	w := func(format string, args ...any) {
		b.WriteString(fmt.Sprintf(format, args...))
		b.WriteString(nl)
	}
	w("[dock]")
	w("listen = %s", tomlString(nd.Listen))
	w("active = %s", tomlString(nd.Active))

	names := make([]string, 0, len(nd.Upstreams))
	for name := range nd.Upstreams {
		if name != "cc-switch" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	if _, ok := nd.Upstreams["cc-switch"]; ok {
		names = append([]string{"cc-switch"}, names...)
	}
	for _, name := range names {
		up := nd.Upstreams[name]
		w("")
		w("[%s]", dockTableHeader("dock.upstreams", name))
		switch name {
		case "cc-switch":
			w("# 迁移自旧 [dock] 单值配置——本地中转回退通道（上游为本地地址时守卫强制透传）")
		default:
			w("# 预置（未激活）：填入 api_key 后换上游即可用")
		}
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
	}
	w("") // 与后续节保持一空行间隔
	return b.String()
}

// renderModelMapInline 内联表渲染，键序确定：default 在前、其余按字典序。
func renderModelMapInline(mm map[string]string) string {
	others := make([]string, 0, len(mm))
	for k := range mm {
		if k != "default" {
			others = append(others, k)
		}
	}
	sort.Strings(others)
	keys := append([]string{"default"}, others...)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		if v, ok := mm[k]; ok && v != "" {
			parts = append(parts, tomlKey(k)+" = "+tomlString(v))
		}
	}
	return strings.Join(parts, ", ")
}

// dockTableHeader 表头键渲染：`dock.upstreams` ＋ 名（裸键直书，非裸键加引号
// ——中文条目名须引号）。
func dockTableHeader(path, name string) string {
	return path + "." + tomlKey(name)
}

// tomlKey TOML 键：裸键字符集（A-Za-z0-9_-）直书，否则基本字符串引号。
func tomlKey(k string) string {
	if isBareTOMLKey(k) {
		return k
	}
	return strconv.Quote(k)
}

func isBareTOMLKey(k string) bool {
	if k == "" {
		return false
	}
	for _, r := range k {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			return false
		}
	}
	return true
}

// tomlString TOML 基本字符串（Go strconv.Quote 的转义集与 TOML 兼容；配置值
// 域为 URL/钥/模型名，不含 TOML 不支持的控制字符转义形）。
func tomlString(s string) string { return strconv.Quote(s) }
