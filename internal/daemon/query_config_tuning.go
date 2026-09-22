package daemon

// query_config_tuning.go — 票08:GET /config_tuning 只读端点(注册于
// queryapi.go 分派表;D12:全配置总览+密钥打码+调参三列,按上游分列)。
//
// 响应两块:
//   - config:config.toml 全配置总览(拍平成段;密钥行一律打码——口径"保留
//     前 3 后 4,短值全掩",页面上即掩码呈现)。从盘上重读解析(路径解析与
//     config.Load 同源 ResolveConfigPath):面板看的是手编配置本身;运行时
//     语义(档位活值/生效值)另见 tuning 块。文件缺席/解析失败如实标注,
//     不编造、不拖垮调参块。
//   - upstreams:调参三列,按 [ferry.same_model].upstreams 白名单逐上游一行:
//     配置值(CeilingFor 手编)/ 生效值(sameModelEffective 现算,票05 出口;
//     拒算如实带 err kind)/ 建议值(该上游最新一条建议+状态徽章文案;
//     无建议 suggestion=null、suggestion_text="无建议")。
//
// 脱敏红线(queryapi.go 顶部块全文适用,此处加严执行):响应永不包含密钥
// 明文——凡命中毒名单的键,值替换为掩码后输出。纯只读,无任何状态写入。
//
// 拍平语义与密钥毒名单同 internal/viewer/server/config.go(既有 /api/config
// 的先例);两包不互依,故就地实现——改动须两处同步(文件头互见)。

import (
	"fmt"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"

	"ferryman/internal/config"
	"ferryman/internal/prices"
	"ferryman/internal/tuning"

	"github.com/BurntSushi/toml"
)

// ctRow / ctSection 拍平行/段(键、值-已脱敏、是否脱敏)。
type ctRow struct {
	Key    string `json:"key"`
	Value  string `json:"value"`
	Redact bool   `json:"redact"`
}

type ctSection struct {
	Name string  `json:"name"`
	Rows []ctRow `json:"rows"`
}

// 密钥键名毒名单(viewer/server/config.go 同款,两处同步):broad 抓复合词,
// tail 抓"末段就是 key/token/auth"的键——末段锚定避免误伤数量词
// (min_ctx_tokens 是阈值不是密钥)。
var (
	ctSecretBroad = regexp.MustCompile(`(?i)(api[_-]?key|apikey|access[_-]?key|secret|passw(or)?d|credential)`)
	ctSecretTail  = regexp.MustCompile(`(?i)(^|[._-])(key|token|auth)$`)
)

// ctMaskSecret 掩码口径(票08 钉死,端点与页面一致):保留前 3 后 4,中间固定
// 4 星(如 sk-****3456); rune 数 ≤9 或空值全掩/留空——短值可由后 4 位反推
// 的比例过高,一律全掩。
func ctMaskSecret(v string) string {
	if v == "" {
		return ""
	}
	r := []rune(v)
	if len(r) <= 9 {
		return "••••••"
	}
	return string(r[:3]) + "****" + string(r[len(r)-4:])
}

// ctIsSecretKey 键名是否命中毒名单。
func ctIsSecretKey(k string) bool {
	return ctSecretBroad.MatchString(k) || ctSecretTail.MatchString(k)
}

// ---- GET /config_tuning ----

// handleConfigTuning 入口:mode+三列+脱敏全配置,一次回话。
func handleConfigTuning(d *Daemon, w http.ResponseWriter, r *http.Request) {
	cfgPath := config.ResolveConfigPath("")
	writeJSON(w, http.StatusOK, map[string]any{
		"mode":      d.Cfg.Tuning.Mode,
		"upstreams": d.configTuningRows(cfgPath),
		"config":    d.sanitizedConfigSections(cfgPath),
	})
}

// configTuningRows 调参三列(按白名单序逐上游一行)。流水重放失败按空状态
// 处理(读坏行在 Replay 内已跳过;此处防御读 IO 错误——面板只读,绝不因
// 状态目录故障回 5xx)。
func (d *Daemon) configTuningRows(cfgPath string) []map[string]any {
	ts := tuning.NewStore(d.Cfg.DataDir())
	st, err := ts.Replay()
	if err != nil || st == nil {
		st = &tuning.State{}
	}
	latest := map[string]*tuning.Suggestion{}
	for _, sug := range st.Suggestions {
		if cur, ok := latest[sug.Upstream]; !ok ||
			sug.CreatedAt > cur.CreatedAt ||
			(sug.CreatedAt == cur.CreatedAt && sug.ID > cur.ID) {
			latest[sug.Upstream] = sug
		}
	}
	books := prices.LoadPrices(cfgPath) // [prices.*],扫参 CLI 同源同路径解析
	rows := make([]map[string]any, 0, len(d.Cfg.SameModel.Upstreams))
	for _, up := range d.Cfg.SameModel.Upstreams {
		row := map[string]any{
			"upstream":       up,
			"configured_min": d.Cfg.SameModel.CeilingFor(up),
		}
		if res, err := sameModelEffective(d.Cfg, ts, books, up); err == nil {
			row["effective_ok"] = true
			row["effective_min"] = res.ThresholdMin
			row["effective_detail"] = res
		} else {
			row["effective_ok"] = false
			row["effective_min"] = nil
			row["effective_err"] = ctErrKind(err)
			row["effective_err_text"] = err.Error()
		}
		if sug := latest[up]; sug != nil {
			status := st.Status[sug.ID]
			if status == "" {
				status = tuning.StPending
			}
			row["suggestion"] = map[string]any{
				"id":           sug.ID,
				"suggest_min":  sug.SuggestMin,
				"current_min":  sug.CurrentMin,
				"has_current":  sug.HasCurrent,
				"best_min":     sug.BestMin,
				"net_savings":  sug.NetSavings,
				"ferry_events": sug.FerryEvents,
				"min_events":   sug.MinEvents,
				"created_at":   sug.CreatedAt,
				"report_path":  sug.ReportPath,
				"status":       status,
				"status_text":  ctStatusText(status),
			}
		} else {
			row["suggestion"] = nil
			row["suggestion_text"] = "无建议"
		}
		rows = append(rows, row)
	}
	return rows
}

// sanitizedConfigSections 全配置总览(脱敏拍平)。found=false/解析失败如实
// 返回并带人话 note;成功时附 mtime(看配置改没改过)。
func (d *Daemon) sanitizedConfigSections(cfgPath string) map[string]any {
	out := map[string]any{"found": false, "path": cfgPath,
		"sections": []ctSection{}}
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		out["note"] = fmt.Sprintf("配置文件不可读(%v)——全配置总览缺席,调参三列照常", err)
		return out
	}
	sections, err := ctParseConfig(raw)
	if err != nil {
		out["found"] = true
		out["note"] = "TOML 解析失败:" + err.Error()
		return out
	}
	out["found"] = true
	if stt, err := os.Stat(cfgPath); err == nil {
		out["mtime"] = stt.ModTime().Format("2006-01-02 15:04:05")
	}
	out["sections"] = sections
	return out
}

// ctParseConfig 把 TOML 拍平成有序段列表(viewer/server/config.go 同语义,
// 两处同步):顶层标量进「全局」段;每张表自成一段,段名点路径;段间表名字典
// 序、段内键字典序——输出稳定可 diff。密钥行值一律掩码。
func ctParseConfig(raw []byte) ([]ctSection, error) {
	var doc map[string]any
	if err := toml.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	var sections []ctSection
	if g := ctScalarSection("全局", doc); g != nil {
		sections = append(sections, *g)
	}
	var names []string
	for k, v := range doc {
		if _, ok := v.(map[string]any); ok {
			names = append(names, k)
		}
	}
	sort.Strings(names)
	for _, n := range names {
		ctWalkTable(&sections, n, doc[n].(map[string]any))
	}
	return sections, nil
}

func ctIsTable(v any) bool { _, ok := v.(map[string]any); return ok }

// ctScalarSection 表内标量键成段;纯容器表返回 nil(不产生空段)。
func ctScalarSection(name string, tbl map[string]any) *ctSection {
	var keys []string
	for k, v := range tbl {
		if !ctIsTable(v) {
			keys = append(keys, k)
		}
	}
	if len(keys) == 0 {
		return nil
	}
	sort.Strings(keys)
	sec := &ctSection{Name: name, Rows: make([]ctRow, 0, len(keys))}
	for _, k := range keys {
		sec.Rows = append(sec.Rows, ctMkRow(k, tbl[k]))
	}
	return sec
}

// ctWalkTable 一张表:先标量段再递归子表(段名父.子,字典序)。
func ctWalkTable(sections *[]ctSection, prefix string, tbl map[string]any) {
	if s := ctScalarSection(prefix, tbl); s != nil {
		*sections = append(*sections, *s)
	}
	var subs []string
	for k, v := range tbl {
		if ctIsTable(v) {
			subs = append(subs, k)
		}
	}
	sort.Strings(subs)
	for _, k := range subs {
		ctWalkTable(sections, prefix+"."+k, tbl[k].(map[string]any))
	}
}

// ctMkRow 单键成行:命中毒名单 → 值掩码+redact 标记(保留键名与"此处有钥"
// 的事实,隐去明文)。
func ctMkRow(k string, v any) ctRow {
	s := ctStringify(v)
	if ctIsSecretKey(k) {
		return ctRow{Key: k, Value: ctMaskSecret(s), Redact: true}
	}
	return ctRow{Key: k, Value: s}
}

// ctStringify TOML 标量/数组 → 展示串(viewer stringify 同语义,两处同步)。
func ctStringify(v any) string {
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
		return strings.TrimRight(fmt.Sprintf("%f", t), "0")
	case []any:
		parts := make([]string, len(t))
		for i, e := range t {
			parts[i] = ctStringify(e)
		}
		return strings.Join(parts, ", ")
	case nil:
		return ""
	default:
		return fmt.Sprintf("%v", t)
	}
}
