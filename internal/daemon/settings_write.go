package daemon

// settings_write.go — 设置视图票03：daemon 设置写核心——包级单写者锁 +
// PUT /settings/{section} 节级整写 + 审计行（spec「写面」「单写者互斥」「审计」
// 三条）。
//
//   - 单写者互斥：settingsWriteMu 包级锁，后续一切设置写路径（本票节级 PUT、
//     票04 实体增删、票05 快照/还原、票06 switch 换绑）必须经它串行，并发
//     请求阻塞排队；锁只罩写路径——GET /settings 读面不取锁，PUT 持锁期间
//     GET 照常回话。
//   - PUT /settings/{section}：body=该节完整 JSON 对象 → 渲染为 TOML 节内容
//     文本（键序字典序=确定性；数值整数形优先，与既有 config.toml 风格一致）
//     → 复用票01 config.SetSectionTOML（写前 Load 全量校验+原子写+写后自校验，
//     校验失败=原文件字节不动）→ 写后审计。白名单=spec 写面九节；dock/
//     providers/prices 实体集合走票04 实体端点，不经节级整写。
//   - 写前快照：snapshotBeforeWrite 已接真实现（票05——调用序定死在锁内、
//     合并/渲染/落盘之前；目录/滚动/还原见 settings_snapshot.go）。
//   - F3 密钥合并（spec 全局规则）：body 中凡毒名单密钥字段——省略、等于
//     读面掩码占位串、空串、读面对象形 {masked,has_key} → 落盘保留盘上现值；
//     仅显式非空新值覆盖（清钥走删除整条目）。合并发生在 JSON 渲染 TOML
//     之前，节级整写因此不会误清真钥；非密钥字段不参与合并（省略=清除，
//     整写语义不变）。
//   - 审计行：<data_dir>/settings-audit.log 追加单行 JSON（ts/entry=settings-ui/
//     section/outcome=saved|rejected/before/after[/error]），密钥值以 <masked>
//     替代（毒名单与读面 setIsSecretKey 同源，本包零新增口径）；永不进账本
//     （与 ledger 零交集，纯文件追加）。校验失败的写同样落行（attempt=after
//     记试图值 + rejected/error）。
//   - 响应 {saved, needs_restart}，needs_restart 恒 true：节级写零热应用——
//     改的是 config.toml，重启才生效（调参热缝走 tuning apply 另路，不经本
//     端点；读面 effects 的操作级判定只描述热缝操作，不适用于节级 PUT）。
//     [server].port 改动附 new_port（UI 明示「重启后自动改连新端口」）。

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"ferryman/internal/clock"
	"ferryman/internal/config"

	"github.com/BurntSushi/toml"
)

// settingsWriteMu 设置单写者锁（spec「单写者互斥」）：daemon 全部设置写路径
// 共用的唯一串行点——写路径整体（快照→渲染→校验落盘→审计）在锁内完成，
// 并发写排队不交错。F7（restart 编排入锁）待票07 拍板后同锁接续。
var settingsWriteMu sync.Mutex

// settingsWritableSections 节级 PUT 白名单（spec 写面九节 + 票04④ ferry）。
// ferry 走自键层变体（settingsOwnKeySections 分派，same_model 子表不经节级
// 写）；providers/prices/dock 实体集合走票04 实体端点，节级整写不碰。
var settingsWritableSections = map[string]bool{
	"gate": true, "thresholds": true, "watch": true, "notify": true,
	"heartbeat": true, "question_watch": true, "wait_window": true,
	"tuning": true, "server": true, "ferry": true,
}

// settingsOwnKeySections 含子表的节（票04④）：节级 PUT 自动走 config.
// SetSectionOwnKeys 自键层变体——只替换该节自身键层（span=节头到第一个子表
// 头），[ferry.same_model] 等子表逐字节保留；纯块节仍走 SetSectionTOML 整写
// （整写原语对带子表节会因子表残留在整树自校验中保守拒——变体即为此）。
var settingsOwnKeySections = map[string]bool{
	"ferry": true,
}

// settingsRejectOwnKeyTables 自键层写只收标量/数组键：body 里带对象值键
// （如 same_model 子表形）明确拒写——否则内联表渲染后与既有子表头撞键，
// 报错文案难懂；子表不经节级端点（spec 写面：ferry 自键=provider/chain）。
func settingsRejectOwnKeyTables(section string, body map[string]any) error {
	for k, v := range body {
		if _, isTable := v.(map[string]any); isTable {
			return fmt.Errorf("节 %s 的键 %q 为对象：自键层写只收标量/数组（子表不经本端点）", section, k)
		}
	}
	return nil
}

// snapshotBeforeWrite 写前快照（票05 已接线）：真实现 takeSnapshot("auto")
// ——<data_dir>/backups/config/ 整文件字节快照、滚动 20 份
// （settings_snapshot.go）。reason 细节（"settings-ui:<节>"）入口与节随审计
// 行溯源，快照文件名类恒 auto。var 形=测试缝（appendLineBestEffort 同惯例）。
// 调用点已在 settingsWriteMu 临界区内，实现不取锁（重入即死锁）。
var snapshotBeforeWrite = func(_ string) error {
	_, err := takeSnapshot("auto")
	return err
}

// ---- PUT /settings/{section} ----

// handleSettingsPut 设置写面分派入口（httpapi.go doPost 的 PUT 早退落点）。
// 守门序随既有 POST 面：未知节/实体路径 404 在 auth 前（POST 面 server.py
// 同序搬运），Bearer 之后才解 body；替身（非 *Daemon）404（handleSettingsRead
// 同语义）。坏 JSON 400 且不落审计——未成写尝试。
func handleSettingsPut(dl DaemonLike, token string, w http.ResponseWriter, r *http.Request, bodyRaw []byte) {
	sec := strings.TrimPrefix(r.URL.Path, "/settings/")
	if sec == "" || strings.Contains(sec, "/") || !settingsWritableSections[sec] {
		notFound(w)
		return
	}
	if !isAuthed(r, token) {
		unauthorized(w)
		return
	}
	d, ok := dl.(*Daemon)
	if !ok {
		notFound(w)
		return
	}
	body, err := decodeJSONObject(bodyRaw)
	if err != nil {
		badRequest(w, err)
		return
	}
	resp, err := d.settingsPutSection(sec, body)
	if err != nil {
		badRequest(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// settingsPutSection 节级整写（锁内全序：快照桩→密钥合并→渲染→
// SetSectionTOML→审计）。失败路径同样落审计行（outcome=rejected+error）后
// 以 400 回话；config 字节不动由票01 原语保证（拒写=原样）。
func (d *Daemon) settingsPutSection(section string, body map[string]any) (map[string]any, error) {
	settingsWriteMu.Lock()
	defer settingsWriteMu.Unlock()

	cfgPath := config.ResolveConfigPath("")
	before := settingsDiskSection(cfgPath, section) // 审计 before/合并基线=盘上现值（写面改的是盘；内存换挡在重启）

	// 写前快照（票05 真实现；调用序定死：合并/渲染/落盘之前）。
	if err := snapshotBeforeWrite("settings-ui:" + section); err != nil {
		auditSettingsWrite(d, section, "rejected", before, body, err)
		return nil, fmt.Errorf("写前快照失败: %w", err)
	}

	// 票04④：含子表的节只收标量/数组自键（子表不经节级端点），在快照后、
	// 合并前明确拒（对齐 merge 失败的审计面）。
	if settingsOwnKeySections[section] {
		if err := settingsRejectOwnKeyTables(section, body); err != nil {
			auditSettingsWrite(d, section, "rejected", before, body, err)
			return nil, err
		}
	}

	// F3 密钥合并：省略/掩码占位/空串 → 保留盘上现值；仅显式非空新值覆盖。
	merged, err := settingsMergeSecrets(before, body)
	if err != nil {
		auditSettingsWrite(d, section, "rejected", before, body, err)
		return nil, err
	}

	tomlBody, err := settingsSectionTOMLBody(merged)
	if err != nil {
		auditSettingsWrite(d, section, "rejected", before, merged, err)
		return nil, err
	}
	// 票04④ 分派：含子表的节走自键层变体（子表逐字节保留），纯块节整写。
	if settingsOwnKeySections[section] {
		err = config.SetSectionOwnKeys(cfgPath, section, tomlBody)
	} else {
		err = config.SetSectionTOML(cfgPath, section, tomlBody)
	}
	if err != nil {
		auditSettingsWrite(d, section, "rejected", before, merged, err)
		return nil, err
	}
	auditSettingsWrite(d, section, "saved", before, merged, nil) // after=合并后落盘面（密钥字段两形同掩 <masked>）

	resp := map[string]any{
		"saved": true,
		// 节级写零热应用：改的是 config.toml，重启才生效。调参热缝走 tuning
		// apply 另路，不经本端点——不复用读面 effects 的操作级判定（返工②）。
		"needs_restart": true,
	}
	if section == "server" { // 改端口：响应附 new_port（spec「改端口」条）
		if np, changed := settingsNewPort(before, merged); changed {
			resp["new_port"] = np
		}
	}
	return resp, nil
}

// settingsMergeSecrets F3 密钥合并（spec 全局规则）：body 中凡毒名单密钥
// 字段——省略、等于读面掩码占位串（setMaskSecret(盘值)，"••••"+尾四位）、
// 空串、读面对象形 {masked,…} → 保留盘上现值；仅显式非空新值覆盖；怪类型
// 拒写（防数字/数组之类渲染进 config）。盘上省略该键时空串照落（语义=未
// 设置）。非密钥字段不参与合并（节级整写：省略=清除）。返回合并后的节对象
// （纯拷贝，不动入参）。
func settingsMergeSecrets(before, body map[string]any) (map[string]any, error) {
	out := make(map[string]any, len(body)+2)
	for k, v := range body {
		out[k] = v
	}
	// 盘上密钥字段被省略 → 注入现值（防整写误清）。
	for k, dv := range before {
		if !setIsSecretKey(k) {
			continue
		}
		if _, present := out[k]; !present {
			out[k] = dv
		}
	}
	// 在场密钥字段：占位/空串/对象形 → 盘值；显式非空 → 覆盖；怪类型 → 拒。
	for k, v := range out {
		if !setIsSecretKey(k) {
			continue
		}
		dstr, diskHas := before[k].(string)
		switch x := v.(type) {
		case string:
			if x == "" {
				if diskHas {
					out[k] = dstr
				} else {
					out[k] = "" // 盘上本无此钥：空串照落（=未设置）
				}
			} else if diskHas {
				if m, _ := setMaskSecret(dstr); x == m { // 读面掩码占位串
					out[k] = dstr
				}
			}
		case map[string]any:
			if _, isPlaceholder := x["masked"]; isPlaceholder { // 读面对象形占位
				if diskHas {
					out[k] = dstr
				} else {
					out[k] = ""
				}
			} else {
				return nil, fmt.Errorf("密钥字段 %q 值非法（应为字符串或掩码占位）", k)
			}
		default:
			return nil, fmt.Errorf("密钥字段 %q 值非法（应为字符串）", k)
		}
	}
	return out, nil
}

// ---- 审计 ----

// auditSettingsWrite 审计行落盘：单行 JSON 追加 <data_dir>/settings-audit.log，
// 永不进账本。密钥值以 <masked> 替代（毒名单 setIsSecretKey 与读面同源；审计
// 口径连尾四位都不给——审计行比读面更不泄露）。尽力而为：写已原子落盘，
// 追加失败不回滚也不拖垮回话（gate.log 同纪律），只损失该行。
func auditSettingsWrite(d *Daemon, section, outcome string, before, after map[string]any, cause error) {
	if d.Cfg == nil {
		return
	}
	line := map[string]any{
		"ts":      clock.Now(), // 冻结钟可测（freezeClock 同款）
		"entry":   "settings-ui",
		"section": section,
		"outcome": outcome, // saved | rejected
		"before":  settingsMaskForAudit(before),
		"after":   settingsMaskForAudit(after),
	}
	if cause != nil {
		line["error"] = cause.Error()
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false) // <masked> 直出（writeJSON 同款非转义口径）
	if err := enc.Encode(line); err != nil {
		return // map[string]any 不可达，护底线
	}
	auditPath := filepath.Join(d.Cfg.DataDir(), "settings-audit.log")
	if err := os.MkdirAll(filepath.Dir(auditPath), 0o755); err != nil {
		return // 目录建不出＝无处可记（写本身已成，不回滚；EnsureToken 同 MkdirAll 先例）
	}
	_ = appendLineBestEffort(auditPath, strings.TrimSuffix(buf.String(), "\n")+"\n")
}

// settingsMaskForAudit 审计脱敏（纯拷贝，不动入参）：凡毒名单键下的字符串叶
// 一律替换 "<masked>"；表/数组下钻；非串标量不动（密钥只可能是串）。
func settingsMaskForAudit(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		switch t := v.(type) {
		case map[string]any:
			out[k] = settingsMaskForAudit(t)
		case []any:
			masked := make([]any, len(t))
			for i, e := range t {
				if sub, ok := e.(map[string]any); ok {
					masked[i] = settingsMaskForAudit(sub)
				} else {
					masked[i] = e
				}
			}
			out[k] = masked
		case string:
			if setIsSecretKey(k) {
				out[k] = "<masked>"
			} else {
				out[k] = t
			}
		default:
			out[k] = v
		}
	}
	return out
}

// settingsDiskSection 盘上某节当前值（审计 before 的事实源）。文件缺席/坏
// TOML → nil（审计如实记 null；写路径随后的 SetSectionTOML 会以同样的读
// 失败拒写，不会落半形）。
func settingsDiskSection(cfgPath, section string) map[string]any {
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		return nil
	}
	var data map[string]any
	if _, err := toml.Decode(string(raw), &data); err != nil {
		return nil
	}
	sec, _ := data[section].(map[string]any)
	return sec
}

// ---- JSON 节对象 → TOML 节内容 ----

// settingsSectionTOMLBody JSON 节对象 → TOML 节内容文本（不含表头行，尾带
// 行尾；键序字典序=渲染确定性，票01 renderInlineTable 同纪律）。数值走
// 最短往返 'g'：整数值自然落整数形（1500 不落 1500.0，与既有文件风格一致，
// config applyTOML 数值兼容）；嵌套对象落内联表（model_map 同风格）。null
// 与未知类型=错误（拒写，不落半形）。
func settingsSectionTOMLBody(body map[string]any) (string, error) {
	keys := make([]string, 0, len(body))
	for k := range body {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		v, err := settingsTOMLValue(body[k])
		if err != nil {
			return "", fmt.Errorf("节键 %q: %w", k, err)
		}
		b.WriteString(settingsTOMLKey(k) + " = " + v + "\n")
	}
	return b.String(), nil
}

// settingsTOMLValue TOML 值渲染（JSON 解码值域）。
func settingsTOMLValue(v any) (string, error) {
	switch x := v.(type) {
	case string:
		return settingsTOMLString(x), nil
	case bool:
		if x {
			return "true", nil
		}
		return "false", nil
	case float64:
		return strconv.FormatFloat(x, 'g', -1, 64), nil
	case int: // HTTP 面恒 float64（JSON 解码）；int 形兜直构调用方（渲染与解析数值兼容）
		return strconv.Itoa(x), nil
	case int64:
		return strconv.FormatInt(x, 10), nil
	case []any:
		items := make([]string, 0, len(x))
		for _, it := range x {
			s, err := settingsTOMLValue(it)
			if err != nil {
				return "", err
			}
			items = append(items, s)
		}
		return "[" + strings.Join(items, ", ") + "]", nil
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			s, err := settingsTOMLValue(x[k])
			if err != nil {
				return "", err
			}
			parts = append(parts, settingsTOMLKey(k)+" = "+s)
		}
		return "{ " + strings.Join(parts, ", ") + " }", nil
	case nil:
		return "", errors.New("null 不可落 TOML（省略键而非传 null）")
	default:
		return "", fmt.Errorf("不支持的值类型 %T", v)
	}
}

// settingsTOMLKey 键名渲染：裸键（[A-Za-z0-9_-]+）直书，非裸键转基本串。
func settingsTOMLKey(k string) string {
	if k == "" {
		return `""`
	}
	for _, c := range k {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' ||
			c >= '0' && c <= '9' || c == '_' || c == '-') {
			return settingsTOMLString(k)
		}
	}
	return k
}

// settingsTOMLString TOML 基本串：转义反斜杠/引号/控制符（\uXXXX）。
func settingsTOMLString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 {
				fmt.Fprintf(&b, `\u%04X`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}

// ---- 生效语义与端口 ----

// settingsNewPort [server].port 改动探测：after 显式给 port 且与盘上 before
// 值不同 → (新端口, true)。缺键/非数/同值＝不附 new_port。
func settingsNewPort(before, after map[string]any) (int, bool) {
	np, ok := settingsAsFloat(after["port"])
	if !ok {
		return 0, false
	}
	if op, ok := settingsAsFloat(before["port"]); ok && np == op {
		return 0, false
	}
	return int(np), true
}

// settingsAsFloat 数值归一（TOML 解码 int64 / JSON 解码 float64 两形）。
func settingsAsFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case int64:
		return float64(x), true
	case int:
		return float64(x), true
	}
	return 0, false
}
