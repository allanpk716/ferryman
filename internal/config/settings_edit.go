// settings_edit.go — 票01（设置视图基础票）：config.toml 的通用节级/条目级
// 写原语（SetSectionTOML / Set·RemoveProviderEntry / Set·RemovePriceEntry），
// 供 daemon 设置写路径复用（spec「写面」的 config 层地基）。
//
// 语义（与 dock_edit.go / dock_setactive.go / dock_migrate.go 同纪律）：
//   - 文本手术只动目标跨度：节＝表头行到下一任意表头行（[ferry.same_model]
//     等子表头不算 [ferry] 的内容）；条目＝[表.名] 表头到下一个不属于该条目
//     子树的表头前（[[prices.k.versions]] 等子表随条目整树增删）。跨度外字节
//     ——其余节、顶层注释、节前注释——逐字保留；
//   - 缺节/缺条目＝EOF 追加（与前文空行分隔）；CRLF 文件行尾跟随主流；
//   - 写前全文校验：新全文经 Default+applyTOML+Validate（与 config.Load 完全
//     同路径，relaxMinGap=false——daemon 写回必须保守护可启），跨节不变量
//     （summarize<block、阈值差≥120、ferry.chain 引用等）全量在列；校验失败
//     ＝拒写、原文件字节不动；
//   - rename 前自校验：新全文可解码、目标子树解码值==渲染预期、其余节解码
//     等价；不符即拒写；
//   - 原子写：同目录临时文件＋rename（MoveFileEx 语义），权限沿用原文件
//     （缺省 0600），写后复读与预期逐字节核对；
//   - 引用守卫（RemoveProviderEntry）：被 [ferry].chain / [ferry].provider
//     引用的条目拒删，错误点名引用方（与 config.go 加载期 chain 校验双层）。
package config

import (
	"bytes"
	"fmt"
	"os"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
)

// ProviderEntry [providers.<名>] 条目（internal/ferry providerBlock 的同形
// 镜像——config 是底层包，不反向 import 消费方；字段序即渲染序）。
type ProviderEntry struct {
	BaseURL string
	Model   string
	APIKey  string
	Window  int
	// Protocol 空 = openai（渲染前归一、恒显式落盘——renderUpstreamEntry 的
	// dialect 同纪律：缺省还是有意不应有歧义）。
	Protocol string
	// ExtraBody 透传字典（值域：标量/数组/嵌套内联表）。
	ExtraBody map[string]any
}

// PriceVersionEntry 单价版本（internal/prices PriceVersion 同形）。
type PriceVersionEntry struct {
	EffectiveFrom string // "YYYY-MM-DD"
	PIn           float64
	PCache        *float64 // nil = 无缓存价（不落盘）
	POut          float64
}

// PriceEntry [prices.<key>] 条目（internal/prices PriceBook 同形）。
type PriceEntry struct {
	Unit     string
	Per      int // ≤0 渲染时归一 10000（与 prices 解析缺省互逆）
	Versions []PriceVersionEntry
}

// SetSectionTOML 把顶层节 name（支持点分如 ferry.same_model）整写为 body
// （不含表头行的 TOML 内容行；空 body＝只留表头）。节存在＝内容行整段替换，
// 不存在＝EOF 追加。校验不过/自校验不过/写失败＝报错且原文件字节不动。
// 等价 API 形取 path 显式参数（SetActiveUpstream 同款），CLI/daemon 共用。
func SetSectionTOML(path, name, body string) error {
	segs, err := splitDottedKey(name)
	if err != nil {
		return fmt.Errorf("config: 节名 %q 非法: %w", name, err)
	}
	return settingsEditSubtree(path, segs, false, false, func(nl string) (string, error) {
		return "[" + joinHeaderKey(segs) + "]" + nl + normalizeBlockText(body, nl), nil
	})
}

// SetProviderEntry 原子写入/替换一条 [providers.<名>] 条目（整条覆盖，含
// extra_body）。非法产物/校验不过＝报错且原文件不动。
func SetProviderEntry(path, name string, e ProviderEntry) error {
	if name == "" {
		return fmt.Errorf("config: 条目名为空（原文件未动）")
	}
	return settingsEditSubtree(path, []string{"providers", name}, true, false, func(nl string) (string, error) {
		return renderProviderEntry(name, e, nl)
	})
}

// RemoveProviderEntry 原子删除一条 [providers.<名>] 条目。被 [ferry].chain /
// [ferry].provider 引用＝拒删（删了会让摆渡链悬空）；未知条目＝报错不动。
func RemoveProviderEntry(path, name string) error {
	if name == "" {
		return fmt.Errorf("config: 条目名为空（原文件未动）")
	}
	if err := guardProviderRefs(path, name); err != nil {
		return err
	}
	return settingsEditSubtree(path, []string{"providers", name}, true, true, nil)
}

// SetPriceEntry 原子写入/替换一条 [prices.<key>]（含 versions 子表整组覆盖）。
func SetPriceEntry(path, key string, e PriceEntry) error {
	if key == "" {
		return fmt.Errorf("config: 价表键为空（原文件未动）")
	}
	return settingsEditSubtree(path, []string{"prices", key}, true, false, func(nl string) (string, error) {
		return renderPriceEntry(key, e, nl)
	})
}

// RemovePriceEntry 原子删除一条 [prices.<key>]（versions 子表随之整树摘除）。
// 价格表无跨节引用，不设引用守卫；未知键＝报错不动。
func RemovePriceEntry(path, key string) error {
	if key == "" {
		return fmt.Errorf("config: 价表键为空（原文件未动）")
	}
	return settingsEditSubtree(path, []string{"prices", key}, true, true, nil)
}

// guardProviderRefs 删除守卫：目标条目被 [ferry].chain 或 [ferry].provider
// 引用即拒删并点名引用方（config.go 加载期只校验 chain，此处补 provider 单键
// 一层，双层同源）。
func guardProviderRefs(path, name string) error {
	p := resolveConfigPath(path)
	raw, err := os.ReadFile(p)
	if err != nil {
		return err
	}
	var data map[string]any
	if _, err := toml.Decode(string(raw), &data); err != nil {
		return fmt.Errorf("config: 原文件解析失败（不动）: %w", err)
	}
	f, ok := data["ferry"].(map[string]any)
	if !ok {
		return nil
	}
	if s, _ := f["provider"].(string); s == name {
		return fmt.Errorf("config: 条目 %q 被 [ferry].provider 引用，拒删（先改摆渡指向再删；原文件未动）", name)
	}
	if arr, ok := f["chain"].([]any); ok {
		for _, v := range arr {
			if s, _ := v.(string); s == name {
				return fmt.Errorf("config: 条目 %q 被 [ferry].chain 引用，拒删（先从链上摘除再删；原文件未动）", name)
			}
		}
	}
	return nil
}

// settingsEditSubtree 增删一体引擎：keySegs＝目标子树的段键名（providers.<名>
// 等）；consumeSubtree＝条目语义（吞并子表头）还是节语义（到下一任意表头）；
// remove＝删除（build 须为 nil）；build＝新块渲染器（入参为文件主流行尾）。
// 全拒或全成，无部分写。
func settingsEditSubtree(path string, keySegs []string, consumeSubtree, remove bool, build func(nl string) (string, error)) error {
	p := resolveConfigPath(path)
	raw, err := os.ReadFile(p)
	if err != nil {
		return err
	}
	var oldData map[string]any
	if _, err := toml.Decode(string(raw), &oldData); err != nil {
		return fmt.Errorf("config: 原文件解析失败（不动）: %w", err)
	}
	nl := detectNL(raw)
	var block string
	if build != nil {
		if block, err = build(nl); err != nil {
			return err
		}
	}
	newRaw, err := spliceSubtree(raw, keySegs, consumeSubtree, remove, block, nl)
	if err != nil {
		return err
	}
	var want any
	if build != nil {
		var sm map[string]any
		if _, err := toml.Decode(block, &sm); err != nil {
			return fmt.Errorf("config: 渲染块非合法 TOML（不落盘）: %w", err)
		}
		want = getSubtree(sm, keySegs)
	}
	if err := verifySubtreeEdit(oldData, newRaw, keySegs, want); err != nil {
		return err
	}
	if err := validateFullLoad(newRaw); err != nil {
		return fmt.Errorf("config: 写回前置校验失败（新配置过不了 Load/Validate，原文件未动）: %w", err)
	}
	return settingsAtomicWrite(p, newRaw)
}

// spliceSubtree 文本手术核心：定位 keySegs 子树跨度，替换为 block（或整段
// 摘除）；跨度外字节逐字保留。缺目标＝remove 报错 / 写入 EOF 追加。
func spliceSubtree(raw []byte, keySegs []string, consumeSubtree, remove bool, block, nl string) ([]byte, error) {
	text := string(raw)
	lines := strings.SplitAfter(text, "\n")
	start, end, found := findSubtreeSpan(lines, keySegs, consumeSubtree)
	if remove {
		if !found {
			return nil, fmt.Errorf("config: %s 不存在（原文件未动）", strings.Join(keySegs, "."))
		}
		if start > 0 && strings.TrimSpace(lines[start-1]) == "" {
			start-- // 表头前恰为空行时连空行一并摘（防空行堆积，dock_edit 同纪律）
		}
		return []byte(strings.Join(lines[:start], "") + strings.Join(lines[end:], "")), nil
	}
	blockNL := normalizeBlockText(block, nl)
	if !found { // 缺目标：EOF 追加，与前文空行分隔
		out := text
		if out != "" {
			if !strings.HasSuffix(out, "\n") {
				out += nl
			}
			if !strings.HasSuffix(out, nl+nl) {
				out += nl
			}
		}
		return []byte(out + blockNL), nil
	}
	sep := ""
	if end < len(lines) {
		sep = nl // 下一表头前保持一空行（属本节内容行，节外字节不受影响）
	}
	return []byte(strings.Join(lines[:start], "") + blockNL + sep + strings.Join(lines[end:], "")), nil
}

// findSubtreeSpan 定位 keySegs 子树跨度 [start,end)。start＝段键名恰等于
// keySegs 的表头行；end＝start 后第一个表头行——consumeSubtree=true（条目
// 语义）时跳过所有 keySegs.* 子表头（[prices.k.versions] 随条目），false
// （节语义）时到下一任意表头（[ferry.same_model] 不算 [ferry] 的内容）。
func findSubtreeSpan(lines []string, keySegs []string, consumeSubtree bool) (start, end int, found bool) {
	start = -1
	for i, ln := range lines {
		segs, ok := headerSegments(ln)
		if !ok {
			continue
		}
		if start < 0 {
			if segsEqual(segs, keySegs) {
				start = i
			}
			continue
		}
		if consumeSubtree && segsHasPrefix(segs, keySegs) {
			continue
		}
		return start, i, true
	}
	if start < 0 {
		return 0, 0, false
	}
	return start, len(lines), true
}

// verifySubtreeEdit rename 前自校验：新全文可解码；目标子树解码值==渲染预期
// （remove 时预期 nil）；其余节解码等价。任何不符即拒写，原文件字节不动。
func verifySubtreeEdit(oldData map[string]any, newRaw []byte, keySegs []string, want any) error {
	var m2 map[string]any
	if err := toml.Unmarshal(newRaw, &m2); err != nil {
		return fmt.Errorf("config: 写回产物非合法 TOML（不落盘）: %w", err)
	}
	if got := getSubtree(m2, keySegs); !reflect.DeepEqual(got, want) {
		return fmt.Errorf("config: 写回产物与预期不符（不落盘）: %s got=%v want=%v",
			strings.Join(keySegs, "."), got, want)
	}
	deleteSubtree(oldData, keySegs)
	deleteSubtree(m2, keySegs)
	if !reflect.DeepEqual(oldData, m2) {
		return fmt.Errorf("config: 写回改动了 %s 之外的节（不落盘）", strings.Join(keySegs, "."))
	}
	return nil
}

// validateFullLoad 新全文按生产 Load 同路径校验：Default+applyTOML+Validate
// （relaxMinGap=false）。等价于把新全文落盘后跑一遍 config.Load。
func validateFullLoad(raw []byte) error {
	var data map[string]any
	if _, err := toml.Decode(string(raw), &data); err != nil {
		return err
	}
	cfg := Default()
	if err := applyTOML(cfg, data); err != nil {
		return err
	}
	return Validate(cfg, false)
}

// settingsAtomicWrite 与 dock_edit.go 同纪律：权限沿用原文件（缺省 0600）、
// 同目录临时文件、rename 原子替换；失败清理 tmp 不留半文件。写后复读与
// 预期逐字节核对（票面钉死的自校验面）。
func settingsAtomicWrite(p string, newRaw []byte) error {
	mode := os.FileMode(0o600)
	if fi, err := os.Stat(p); err == nil {
		mode = fi.Mode().Perm() // 保留原文件权限（含真钥的敏感能级不放宽）
	}
	tmp := p + ".settings-tmp"
	if err := os.WriteFile(tmp, newRaw, mode); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, p); err != nil { // 原子替换（Windows MoveFileEx 语义）
		_ = os.Remove(tmp)
		return err
	}
	back, err := os.ReadFile(p) // 写后复读自校验
	if err != nil {
		return err
	}
	if !bytes.Equal(back, newRaw) {
		return fmt.Errorf("config: 写后复读与预期不符（%s 可能已损坏，请人工核查）", p)
	}
	return nil
}

// renderProviderEntry 渲染一条供应商条目块（首行表头；字段序=结构体序，
// 恒全量落盘保确定性；extra_body 非空才落、键序确定）。window≤0 归一 131072
// （ferry.LoadProviders 缺省同值，渲染与解析互逆）；protocol 空=归一 openai。
func renderProviderEntry(name string, e ProviderEntry, nl string) (string, error) {
	var b strings.Builder
	w := func(s string) {
		b.WriteString(s)
		b.WriteString(nl)
	}
	w("[providers." + tomlKey(name) + "]")
	w("base_url = " + tomlString(e.BaseURL))
	w("model = " + tomlString(e.Model))
	w("api_key = " + tomlString(e.APIKey))
	win := e.Window
	if win <= 0 {
		win = 131072 // ferry defaultWindow 同值（底层包不 import 消费方）
	}
	w("window = " + strconv.Itoa(win))
	proto := e.Protocol
	if proto == "" {
		proto = "openai" // ferry ProtocolOpenAI 同值（缺省归一显式落盘）
	}
	w("protocol = " + tomlString(proto))
	if len(e.ExtraBody) > 0 {
		inline, err := renderInlineTable(e.ExtraBody)
		if err != nil {
			return "", fmt.Errorf("config: providers.%s extra_body 渲染失败: %w", name, err)
		}
		w("extra_body = { " + inline + " }")
	}
	return b.String(), nil
}

// renderPriceEntry 渲染一条价目表条目块（versions 为数组表头子块；per≤0
// 归一 10000 与 prices 解析缺省互逆；p_cache 仅在位时落盘）。
func renderPriceEntry(key string, e PriceEntry, nl string) (string, error) {
	var b strings.Builder
	w := func(s string) {
		b.WriteString(s)
		b.WriteString(nl)
	}
	w("[prices." + tomlKey(key) + "]")
	w("unit = " + tomlString(e.Unit))
	per := e.Per
	if per <= 0 {
		per = 10000 // prices.LoadPrices asInt(blk["per"], 10000) 同值
	}
	w("per = " + strconv.Itoa(per))
	for _, v := range e.Versions {
		w("")
		w("[[prices." + tomlKey(key) + ".versions]]")
		w("effective_from = " + tomlString(v.EffectiveFrom))
		w("p_in = " + renderTOMLFloat(v.PIn))
		if v.PCache != nil {
			w("p_cache = " + renderTOMLFloat(*v.PCache))
		}
		w("p_out = " + renderTOMLFloat(v.POut))
	}
	return b.String(), nil
}

// renderInlineTable 内联表渲染（键序确定：字典序）。
func renderInlineTable(m map[string]any) (string, error) {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		v, err := renderTOMLValue(m[k])
		if err != nil {
			return "", err
		}
		parts = append(parts, tomlKey(k)+" = "+v)
	}
	return strings.Join(parts, ", "), nil
}

// renderTOMLValue TOML 内联值渲染（extra_body 值域：标量/数组/嵌套表）。
func renderTOMLValue(v any) (string, error) {
	switch x := v.(type) {
	case string:
		return tomlString(x), nil
	case bool:
		if x {
			return "true", nil
		}
		return "false", nil
	case int:
		return strconv.Itoa(x), nil
	case int32:
		return strconv.FormatInt(int64(x), 10), nil
	case int64:
		return strconv.FormatInt(x, 10), nil
	case float32:
		return renderTOMLFloat(float64(x)), nil
	case float64:
		return renderTOMLFloat(x), nil
	case []any:
		items := make([]string, 0, len(x))
		for _, it := range x {
			s, err := renderTOMLValue(it)
			if err != nil {
				return "", err
			}
			items = append(items, s)
		}
		return "[" + strings.Join(items, ", ") + "]", nil
	case map[string]any:
		inner, err := renderInlineTable(x)
		if err != nil {
			return "", err
		}
		return "{ " + inner + " }", nil
	default:
		return "", fmt.Errorf("不支持的值类型 %T（值域：标量/数组/嵌套表）", v)
	}
}

// renderTOMLFloat TOML 浮点渲染（最短往返；24 → "24" 合法 TOML 整数形，
// 解析侧 asFloat 数值兼容）。
func renderTOMLFloat(f float64) string {
	return strconv.FormatFloat(f, 'g', -1, 64)
}

// splitDottedKey 引号感知的点分段："ferry.same_model" → [ferry same_model]、
// "a.\"b.c\"" → [a b.c]（引号内点不切）。空名/空段/未闭合引号＝错误。
func splitDottedKey(s string) ([]string, error) {
	var segs []string
	var cur strings.Builder
	var quote byte
	flush := func() error {
		seg := strings.TrimSpace(cur.String())
		if seg == "" {
			return fmt.Errorf("键名含空段")
		}
		segs = append(segs, seg)
		cur.Reset()
		return nil
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if quote != 0 {
			if c == '\\' && quote == '"' && i+1 < len(s) { // 基本串转义
				cur.WriteByte(c)
				i++
				cur.WriteByte(s[i])
				continue
			}
			if c == quote {
				quote = 0
			} else {
				cur.WriteByte(c)
			}
			continue
		}
		switch c {
		case '"', '\'':
			quote = c
		case '.':
			if err := flush(); err != nil {
				return nil, err
			}
		default:
			cur.WriteByte(c)
		}
	}
	if quote != 0 {
		return nil, fmt.Errorf("键名引号未闭合")
	}
	if err := flush(); err != nil {
		return nil, err
	}
	if len(segs) == 0 {
		return nil, fmt.Errorf("键名为空")
	}
	return segs, nil
}

// headerSegments 表头行的段键名：`[a.b."c.d"]` / `[[a.b]]` → [a b c.d]。
// 非表头行/异形（未闭合引号）＝(nil,false)——歧义形态由写回自校验的解码
// 比对兜底（firstHeaderSegment 同哲学，此处取完整段链）。
func headerSegments(line string) ([]string, bool) {
	t := strings.TrimLeft(line, " \t\r")
	if !strings.HasPrefix(t, "[") {
		return nil, false
	}
	t = strings.TrimPrefix(t, "[")
	t = strings.TrimPrefix(t, "[") // [[数组表头]]
	end := strings.Index(t, "]")
	if end < 0 {
		return nil, false
	}
	segs, err := splitDottedKey(t[:end])
	if err != nil {
		return nil, false
	}
	return segs, true
}

func segsEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// segsHasPrefix a 以 b 为严格前缀（a 比 b 长）——子表归属判定。
func segsHasPrefix(a, b []string) bool {
	if len(a) <= len(b) {
		return false
	}
	for i := range b {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// joinHeaderKey 表头键渲染（逐段 tomlKey：裸键直书，非裸键加引号）。
func joinHeaderKey(segs []string) string {
	parts := make([]string, len(segs))
	for i, s := range segs {
		parts[i] = tomlKey(s)
	}
	return strings.Join(parts, ".")
}

// detectNL 原文件主流行尾（首个 LF 前有 CR 即 CRLF——rewriteUpstreamsTable 同判）。
func detectNL(raw []byte) string {
	if i := strings.Index(string(raw), "\n"); i > 0 && raw[i-1] == '\r' {
		return "\r\n"
	}
	return "\n"
}

// normalizeBlockText 块文本行尾归一（跟随 nl；尾随空白行收束为单行尾；
// 空白块→空串）。多行字符串内的行尾随主流重排——TOML 多行串本就等价处理。
func normalizeBlockText(block, nl string) string {
	core := strings.TrimRight(block, " \t\r\n")
	if core == "" {
		return ""
	}
	lines := strings.Split(core, "\n")
	for i := range lines {
		lines[i] = strings.TrimSuffix(lines[i], "\r")
	}
	return strings.Join(lines, nl) + nl
}

// getSubtree 按段键名取嵌套表子树；任一段缺失/非表＝nil。
func getSubtree(m map[string]any, segs []string) any {
	var cur any = m
	for _, s := range segs {
		t, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur, ok = t[s]
		if !ok {
			return nil
		}
	}
	return cur
}

// deleteSubtree 按段键名删除嵌套表键；父表因此变空的逐级回收（追加条目
// 隐式建出的表，删唯一子键后不应残留——否则与其余节等价比对失真）。
func deleteSubtree(m map[string]any, segs []string) {
	if len(segs) == 0 {
		return
	}
	chain := []map[string]any{m}
	for i := 0; i < len(segs)-1; i++ {
		next, ok := chain[i][segs[i]]
		if !ok {
			return
		}
		nt, ok := next.(map[string]any)
		if !ok {
			return
		}
		chain = append(chain, nt)
	}
	parent := chain[len(chain)-1]
	delete(parent, segs[len(segs)-1])
	for i := len(chain) - 1; i > 0; i-- { // 空父表回收（仍有子键即止）
		if len(chain[i]) != 0 {
			break
		}
		delete(chain[i-1], segs[i-1])
	}
}
