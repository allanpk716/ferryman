// config.go——只读展示守护进程配置（config.toml）：GET /api/config。
// 铁律：①只读，绝不写；②形似密钥的值一律脱敏（页面上永远看不到明文密钥）；
// ③找不到/解析失败如实报告，不编造。
package server

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

// secretBroad / secretTail 命中即脱敏（两选一）：
// broad 抓复合词（api_key/access_key/secret/password/credential…）；
// tail 抓"末段就是 key/token/auth"的键（key、provider.token、public_key）。
// 末段锚定避免误伤数量词——min_ctx_tokens 是 token 数量阈值，不是密钥。
var (
	secretBroad = regexp.MustCompile(`(?i)(api[_-]?key|apikey|access[_-]?key|secret|passw(or)?d|credential)`)
	secretTail  = regexp.MustCompile(`(?i)(^|[._-])(key|token|auth)$`)
)

// ConfigPath 由数据目录推配置路径：约定 config.toml 与 accounts/ 同级
//（即数据根下）；dataDir 本身可能是根（直接给了含 *.jsonl 的目录），
// 两处都探，父目录优先。都找不到返回空串，由 handler 如实报告。
func ConfigPath(dataDir string) string {
	cands := []string{
		filepath.Join(filepath.Dir(dataDir), "config.toml"),
		filepath.Join(dataDir, "config.toml"),
	}
	for _, c := range cands {
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			return c
		}
	}
	return ""
}

// cfgRow 一行配置：键、值（脱敏后）、是否被脱敏。
type cfgRow struct {
	Key    string `json:"key"`
	Value  string `json:"value"`
	Redact bool   `json:"redact"`
}

// cfgSection 一段配置（TOML 表 → 一段；嵌套表拍平成 a.b 段名）。
type cfgSection struct {
	Name string   `json:"name"`
	Rows []cfgRow `json:"rows"`
}

// parseConfig 把 TOML 拍平成有序段列表：顶层标量进「全局」段；每张表
//（含嵌套）自成一段，段名用点路径。段间按表名字母序、段内按键字母序——
// TOML 本身无序，输出必须稳定可 diff。
func parseConfig(raw []byte) ([]cfgSection, error) {
	var doc map[string]any
	if err := toml.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	var sections []cfgSection
	if g := scalarSection("全局", doc); g != nil {
		sections = append(sections, *g)
	}
	var names []string
	for k, v := range doc {
		if isTable(v) {
			names = append(names, k)
		}
	}
	sort.Strings(names)
	for _, n := range names {
		walkTable(&sections, n, doc[n].(map[string]any))
	}
	return sections, nil
}

func isTable(v any) bool { _, ok := v.(map[string]any); return ok }

// scalarSection 表内标量键成段；无标量键返回 nil（纯容器表不产生空段）。
func scalarSection(name string, tbl map[string]any) *cfgSection {
	var keys []string
	for k, v := range tbl {
		if !isTable(v) {
			keys = append(keys, k)
		}
	}
	if len(keys) == 0 {
		return nil
	}
	sort.Strings(keys)
	sec := &cfgSection{Name: name, Rows: make([]cfgRow, 0, len(keys))}
	for _, k := range keys {
		sec.Rows = append(sec.Rows, mkRow(k, tbl[k]))
	}
	return sec
}

// walkTable 一张表：先出自己的标量段，再按序递归子表（段名父.子）。
func walkTable(sections *[]cfgSection, prefix string, tbl map[string]any) {
	if s := scalarSection(prefix, tbl); s != nil {
		*sections = append(*sections, *s)
	}
	var subs []string
	for k, v := range tbl {
		if isTable(v) {
			subs = append(subs, k)
		}
	}
	sort.Strings(subs)
	for _, k := range subs {
		walkTable(sections, prefix+"."+k, tbl[k].(map[string]any))
	}
}

// mkRow 单键成行：命中毒名单 → 值替换为占位（长度照报，便于确认没改错行）。
func mkRow(k string, v any) cfgRow {
	if secretBroad.MatchString(k) || secretTail.MatchString(k) {
		s := fmt.Sprintf("%v", v)
		return cfgRow{Key: k, Value: fmt.Sprintf("••• 已隐藏（%d 字符）", len(s)), Redact: true}
	}
	return cfgRow{Key: k, Value: stringify(v)}
}

// stringify TOML 标量/数组 → 展示串：字符串去引号原样；bool/数值十进制；
// 数组逗号连接（本配置里数组只有小列表，如 enabled）。
func stringify(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case bool:
		if t {
			return "true"
		}
		return "false"
	case int64:
		return fmt.Sprintf("%d", t)
	case float64:
		s := fmt.Sprintf("%f", t)
		return strings.TrimRight(s, "0")
	case []any:
		parts := make([]string, len(t))
		for i, e := range t {
			parts[i] = stringify(e)
		}
		return strings.Join(parts, ", ")
	case nil:
		return ""
	default:
		return fmt.Sprintf("%v", t)
	}
}

// handleConfig GET /api/config → 守护进程配置只读快照。
// found=false / 解析失败都回 200（页面照常渲染并说明原因），只有读失败 500。
func (s *Server) handleConfig(w http.ResponseWriter, _ *http.Request) {
	path := ConfigPath(s.dataDir)
	if path == "" {
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":    false,
			"found": false,
			"note": fmt.Sprintf("未找到 config.toml（找了 %s 与 %s）——那是守护进程的配置；查看器只读展示，找不到不编造",
				filepath.Join(filepath.Dir(s.dataDir), "config.toml"), filepath.Join(s.dataDir, "config.toml")),
		})
		return
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	sections, err := parseConfig(raw)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": false, "found": true, "path": path,
			"note": "TOML 解析失败：" + err.Error(),
		})
		return
	}
	mtime := ""
	if st, err := os.Stat(path); err == nil {
		mtime = st.ModTime().Format("2006-01-02 15:04:05")
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":       true,
		"found":    true,
		"path":     path,
		"mtime":    mtime,
		"sections": sections,
		"note":     "只读展示：要改参数请编辑文件本身（守护进程重启后生效）",
	})
}
