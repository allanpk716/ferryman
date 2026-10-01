// ccswitch_import.go — 票06：`provider import-ccswitch` 的 cc-switch 库读取
// 与供应商条目映射（D1 范围：只收 claude/codex 两类）。
//
// 数据源事实（出处 = cc-switch 3.20.4 源码直读，.scratch/cc-switch-ref）：
//   - ~/.cc-switch/cc-switch.db（sqlite），providers 表
//     (id, app_type, name, settings_config, …, PRIMARY KEY(id, app_type))；
//   - claude 类 settings_config = ~/.claude/settings.json 同构 JSON：env 里
//     ANTHROPIC_BASE_URL / ANTHROPIC_AUTH_TOKEN（个别条目 ANTHROPIC_API_KEY）/
//     ANTHROPIC_MODEL 与 ANTHROPIC_DEFAULT_{OPUS,SONNET,HAIKU}_MODEL、
//     ANTHROPIC_SMALL_FAST_MODEL（模型映射的原始载体）；
//   - codex 类 settings_config = {"auth":{"OPENAI_API_KEY":…},"config":
//     "<config.toml 文本>"}（密钥在 auth、端点在 config 文本）。
//
// 映射纪律：
//   - claude → dialect=anthropic；codex → 按端点线协议推（InferCodexDialect：
//     anthropic 端点 → 需翻译；wire_api="responses" 或 /v1 根 → 原生
//     openai_responses；推不出 → 跳过并报因，绝不臆测）；
//   - 非本地端点缺 default 模型位 → 跳过（写进表会让守护拒启——与
//     validateDockUpstreams 同判据）；
//   - 只读打开（DSN mode=ro），库路径全参数化——本包绝不内置读真 ~/.cc-switch
//     （真路径解析归 CLI 装配层；测试注临时假库）；
//   - 密钥只进出映射值（落本机 config 是 CLI 层的事），跳过条目不带密钥值
//     （T39：密钥不进报告文本）。
package provider

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"

	"ferryman/internal/config"
)

// CCSwitchRow 库一行的投影（映射所需最小列集）。
type CCSwitchRow struct {
	Name           string
	AppType        string
	SettingsConfig string
}

// CCSwitchCandidate 一行的映射结论：Skip=false 时 Up 可入表；Skip=true 时
// Reason 说明（人读，不含密钥值）。
type CCSwitchCandidate struct {
	Name    string
	AppType string
	Up      config.DockUpstream
	Skip    bool
	Reason  string
}

// ReadCCSwitchDB 只读读出 providers 表（app_type 再 name 字典序——导入报告与
// 写入顺序确定性）。库不存在/表不存在如实报错。
func ReadCCSwitchDB(dbPath string) ([]CCSwitchRow, error) {
	dsn := "file:" + filepath.ToSlash(dbPath) + "?mode=ro"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("provider: 打不开 cc-switch 数据库（%s）: %w", dbPath, err)
	}
	defer db.Close()
	rows, err := db.Query(`SELECT name, app_type, settings_config FROM providers ORDER BY app_type, name`)
	if err != nil {
		return nil, fmt.Errorf("provider: 读 cc-switch providers 表失败（%s；确认是 cc-switch 的 "+
			"cc-switch.db，或用 --db 指定路径）: %w", dbPath, err)
	}
	defer rows.Close()
	var out []CCSwitchRow
	for rows.Next() {
		var r CCSwitchRow
		if err := rows.Scan(&r.Name, &r.AppType, &r.SettingsConfig); err != nil {
			return nil, fmt.Errorf("provider: cc-switch 行读取失败: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// MapCCSwitchRow 单行映射（纯函数，无 IO）：可映射 → 候选条目；不可映射 →
// Skip + 原因。范围外 app_type / 非 JSON / 缺关键字段都跳过不硬造。
func MapCCSwitchRow(row CCSwitchRow) CCSwitchCandidate {
	c := CCSwitchCandidate{Name: row.Name, AppType: row.AppType}
	if strings.TrimSpace(row.Name) == "" {
		c.Skip, c.Reason = true, "条目名为空——跳过"
		return c
	}
	switch row.AppType {
	case "claude":
		return mapClaudeRow(c, row.SettingsConfig)
	case "codex":
		return mapCodexRow(c, row.SettingsConfig)
	default:
		c.Skip, c.Reason = true, fmt.Sprintf("范围外跳过（app_type=%s，只收 claude/codex 两类）", row.AppType)
		return c
	}
}

// ccswitchPayload settings_config 的共用解包形（claude 用 env；codex 用
// auth+config；一结构两用，非 JSON 统一在此拦）。
type ccswitchPayload struct {
	Env    map[string]any `json:"env"`
	Auth   map[string]any `json:"auth"`
	Config string         `json:"config"`
}

func mapClaudeRow(c CCSwitchCandidate, settingsConfig string) CCSwitchCandidate {
	var p ccswitchPayload
	if err := json.Unmarshal([]byte(settingsConfig), &p); err != nil {
		c.Skip, c.Reason = true, fmt.Sprintf("settings_config 非 JSON（跳过不硬解）: %v", err)
		return c
	}
	baseURL := strAny(p.Env, "ANTHROPIC_BASE_URL")
	if strings.TrimSpace(baseURL) == "" {
		c.Skip, c.Reason = true, "缺 ANTHROPIC_BASE_URL（env）——端点不明，跳过"
		return c
	}
	key := strAny(p.Env, "ANTHROPIC_AUTH_TOKEN")
	if key == "" {
		key = strAny(p.Env, "ANTHROPIC_API_KEY")
	}
	mm := map[string]string{}
	if v := strAny(p.Env, "ANTHROPIC_MODEL"); v != "" {
		mm["default"] = v
	}
	if v := strAny(p.Env, "ANTHROPIC_DEFAULT_OPUS_MODEL"); v != "" {
		mm["opus"] = v
	}
	if v := strAny(p.Env, "ANTHROPIC_DEFAULT_SONNET_MODEL"); v != "" {
		mm["sonnet"] = v
	}
	haiku := strAny(p.Env, "ANTHROPIC_DEFAULT_HAIKU_MODEL")
	if haiku == "" {
		haiku = strAny(p.Env, "ANTHROPIC_SMALL_FAST_MODEL")
	}
	if haiku != "" {
		mm["haiku"] = haiku
	}
	if mm["default"] == "" && !config.IsLocalRelayAddr(baseURL) {
		c.Skip, c.Reason = true, "缺 ANTHROPIC_MODEL（无 default 模型位；非本地端点必须配 default，"+
			"写进表会让守护拒启）——手编 config 补 model_map 后再切"
		return c
	}
	c.Up = config.DockUpstream{BaseURL: baseURL, APIKey: key, ModelMap: mm,
		Dialect: config.DialectAnthropic}
	return c
}

func mapCodexRow(c CCSwitchCandidate, settingsConfig string) CCSwitchCandidate {
	var p ccswitchPayload
	if err := json.Unmarshal([]byte(settingsConfig), &p); err != nil {
		c.Skip, c.Reason = true, fmt.Sprintf("settings_config 非 JSON（跳过不硬解）: %v", err)
		return c
	}
	dialect, baseURL, model, ok := InferCodexDialect(p.Config)
	if !ok {
		c.Skip, c.Reason = true, fmt.Sprintf("无法从端点推线协议（base_url=%s）——需 anthropic 端点、"+
			" wire_api=\"responses\" 或 /v1 根三者其一，跳过", baseURL)
		return c
	}
	key := strAny(p.Auth, "OPENAI_API_KEY")
	mm := map[string]string{}
	if model != "" {
		mm["default"] = model // codex 主模型兼 default：同一供应商的真实模型名，不另造
		mm["codex"] = model
	}
	if mm["default"] == "" && !config.IsLocalRelayAddr(baseURL) {
		c.Skip, c.Reason = true, "codex config 缺 model 键（无 default 模型位；非本地端点必须配 "+
			"default，写进表会让守护拒启）——手编 config 补 model_map 后再切"
		return c
	}
	c.Up = config.DockUpstream{BaseURL: baseURL, APIKey: key, ModelMap: mm, Dialect: dialect}
	return c
}

// InferCodexDialect 从 codex config.toml 文本推线协议方言（票06）：
//   - 端点含 "anthropic" 段 → anthropic（CC /v1/messages 同款线协议，需翻译）；
//   - 否则 wire_api = "responses" → openai_responses；
//   - 否则端点路径以 /v1 结尾（容忍尾斜杠）→ openai_responses（OpenAI 惯例根）；
//   - 推不出 → ok=false（baseURL/model 照带回供报因）。
//
// 返回 (dialect, baseURL, model, ok)；解析失败（非 TOML/缺 model_provider/缺
// active 表/缺 base_url）一律 ok=false。
func InferCodexDialect(configTOML string) (dialect, baseURL, model string, ok bool) {
	var doc map[string]any
	if err := toml.Unmarshal([]byte(configTOML), &doc); err != nil {
		return "", "", "", false
	}
	active, _ := doc["model_provider"].(string)
	if strings.TrimSpace(active) == "" {
		return "", "", "", false
	}
	mps, _ := doc["model_providers"].(map[string]any)
	tbl, _ := mps[active].(map[string]any)
	if tbl == nil {
		return "", "", "", false
	}
	bu, _ := tbl["base_url"].(string)
	if strings.TrimSpace(bu) == "" {
		return "", "", "", false
	}
	model, _ = doc["model"].(string)
	wire, _ := tbl["wire_api"].(string)
	lowered := strings.ToLower(bu)
	if strings.Contains(lowered, "anthropic") {
		return config.DialectAnthropic, bu, model, true
	}
	if wire == "responses" {
		return config.DialectOpenAIResponses, bu, model, true
	}
	if strings.HasSuffix(strings.TrimRight(lowered, "/"), "/v1") {
		return config.DialectOpenAIResponses, bu, model, true
	}
	return "", bu, model, false
}

// ImportCCSwitch 读库＋逐行映射（只读；库路径参数化）。行序 = ReadCCSwitchDB
// 的确定性序；逐行结论（含跳过原因）全量返回，取舍（冲突/批量写入）归 CLI 层。
func ImportCCSwitch(dbPath string) ([]CCSwitchCandidate, error) {
	rows, err := ReadCCSwitchDB(dbPath)
	if err != nil {
		return nil, err
	}
	out := make([]CCSwitchCandidate, 0, len(rows))
	for _, r := range rows {
		out = append(out, MapCCSwitchRow(r))
	}
	sort.SliceStable(out, func(i, j int) bool { // SQL 已排序，此处防驱动序漂移的兜底
		if out[i].AppType != out[j].AppType {
			return out[i].AppType < out[j].AppType
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// strAny map[string]any 的字符串取值（缺键/非串 = 空）。
func strAny(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	s, _ := m[key].(string)
	return s
}
