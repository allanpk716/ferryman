// Package report 移植 ferryman report：族系账单 + 成效账 v1 + 策略对比 + 复算附录
// （规格 ferryman/report.py 1:1）。
//
// ADR-0002：节省在 report 层由版本化公式现算，原始流水不改写。
// block 侧价格在 report 时经 --provider 指定（daemon 不知道被拦会话的 provider，
// v1 已知简化，复算附录披露）；handoff 侧用流水钉死的 price_ver。
// 公式铁律：策略数值只调 internal/policy（全仓公式单源）——本包出现第二份
// 公式即缺陷。
package report

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"ferryman/internal/accounts"
	"ferryman/internal/config"
	"ferryman/internal/mathx"
	"ferryman/internal/policy"
	"ferryman/internal/prices"
)

// 公式版本章与公式内常数（report.py:20-22）。
const (
	// SavingsFormula 成效账公式版本。
	SavingsFormula = "v1"
	// StrategyFormula 策略对比公式版本（与 SavingsFormula 同源盖章，v1.1 待改）。
	StrategyFormula = "v1"
	// CompactRatio 公式内常数（policy.strategy_costs 默认值同源）。
	CompactRatio = 0.25
)

// StrategyCaveats 终审#4（最小修复，v1.1 延后）：策略对比已知口径缺陷——
// 文本报表与 --json 同源披露，best 列在 v1.1 落地前不得作为心跳授权依据
// （report.py:26-32 两段逐字）。
var StrategyCaveats = [...]string{
	"⚠ 口径披露（公式 " + StrategyFormula + "）：none 列在 ≤TTL 时为差值口径（0）" +
		"而 >TTL 时为绝对重付；beat 列未计入其隐式保温的 S×P_cache 读；" +
		"expire_compact 未计压缩调用自身成本；⌈d/τ⌉ 在非整除时高估一跳。",
	"以上使 best 列偏向 expire_compact——v1.1（统一差值基准+floor 跳数）落地前，" +
		"best 不得作为心跳授权依据。",
}

// bookFor Python _book_for 1:1：指定 key 命中则取之；否则仅一本时取唯一本；
// 再否则 nil（不可算，不造数）。
func bookFor(books map[string]prices.PriceBook, key string) *prices.PriceBook {
	if len(books) == 0 {
		return nil
	}
	if key != "" {
		if b, ok := books[key]; ok {
			return &b
		}
	}
	if len(books) == 1 {
		for _, b := range books {
			return &b
		}
	}
	return nil
}

// HandoffCost 按流水钉死的 price_ver 折算；无价格/无版本 → nil（不可算，不造数）。
func HandoffCost(e map[string]any, books map[string]prices.PriceBook) *float64 {
	tag := strOr(e, "price_ver")
	if tag == "" {
		return nil
	}
	at := strings.Index(tag, "@") // Python tag.partition("@")：首个 "@"
	if at < 0 {
		return nil
	}
	key, ver := tag[:at], tag[at+1:]
	book, ok := books[key]
	if !ok {
		return nil
	}
	var pv *prices.PriceVersion
	for i := range book.Versions {
		if book.Versions[i].EffectiveFrom == ver {
			pv = &book.Versions[i]
			break
		}
	}
	if pv == nil {
		return nil
	}
	c := numOr(e, "prompt_tokens")/float64(book.Per)*pv.PIn +
		numOr(e, "completion_tokens")/float64(book.Per)*pv.POut
	return &c
}

// lineRow 族系行累加器（Python row() dict 的定式形）；toMap 键集 = Python 逐字。
type lineRow struct {
	lineageID                           string
	project                             string
	blocks, bypass, injects, handoffs   int
	windows                             int
	handoffCost, gross, injectCost, net float64
}

func (r *lineRow) toMap() map[string]any {
	return map[string]any{
		"lineage_id": r.lineageID, "project": r.project,
		"blocks": r.blocks, "bypass": r.bypass, "injects": r.injects,
		"handoffs": r.handoffs, "windows": r.windows,
		"handoff_cost": r.handoffCost, "gross": r.gross,
		"inject_cost": r.injectCost, "net": r.net,
	}
}

// totalsMap 总计行键集 = Python（无 lineage_id/project 两键）。
func totalsMap(r *lineRow) map[string]any {
	return map[string]any{
		"blocks": r.blocks, "bypass": r.bypass, "injects": r.injects,
		"handoffs": r.handoffs, "windows": r.windows,
		"handoff_cost": r.handoffCost, "gross": r.gross,
		"inject_cost": r.injectCost, "net": r.net,
	}
}

// SavingsV1 成效账 v1：毛节省（block 侧 S×(P_in−P_cache)/per，econ_book 按行
// 时刻取版本；p_cache 缺 → 跳过该行不计）− 注入成本 − handoff 成本（按行钉死
// price_ver 折算；无价 → unpriced 汇总）。行按 net 降序（稳定序 = 插入序）。
func SavingsV1(entries []map[string]any, books map[string]prices.PriceBook,
	econBook *prices.PriceBook) map[string]any {
	var order []string // Python dict 插入序（浮点求和顺序须与 Python 一致）
	lines := map[string]*lineRow{}
	unpriced := map[string]bool{}
	row := func(lid string) *lineRow { // Python lines.setdefault
		if r, ok := lines[lid]; ok {
			return r
		}
		r := &lineRow{lineageID: lid}
		lines[lid] = r
		order = append(order, lid)
		return r
	}
	for _, e := range entries {
		r := row(lineageKeyOf(e))
		if r.project == "" { // Python r["project"] or e.get("project", "")
			r.project = strOr(e, "project")
		}
		switch strOr(e, "kind") {
		case "block":
			r.blocks++
			if econBook != nil {
				if pv := econBook.At(numOr(e, "ts")); pv != nil && pv.PCache != nil {
					r.gross += numOr(e, "prefix_tokens") / float64(econBook.Per) *
						(pv.PIn - *pv.PCache)
				}
			}
		case "bypass":
			r.bypass++
		case "inject":
			r.injects++
			if econBook != nil {
				if pv := econBook.At(numOr(e, "ts")); pv != nil {
					r.injectCost += numOr(e, "tokens") / float64(econBook.Per) * pv.PIn
				}
			}
		case "handoff":
			r.handoffs++
			if c := HandoffCost(e, books); c == nil {
				p := "?"
				if v, ok := e["provider"].(string); ok {
					p = v
				}
				unpriced[p] = true
			} else {
				r.handoffCost += *c
			}
		case "window":
			r.windows++
		}
	}
	for _, lid := range order { // 净节省最后一步：毛 − 注入 − handoff
		r := lines[lid]
		r.net = mathx.Round(r.gross-r.injectCost-r.handoffCost, 4)
	}
	tot := &lineRow{}
	lineages := make([]map[string]any, 0, len(order))
	for _, lid := range order {
		r := lines[lid]
		lineages = append(lineages, r.toMap())
		tot.blocks += r.blocks
		tot.bypass += r.bypass
		tot.injects += r.injects
		tot.handoffs += r.handoffs
		tot.windows += r.windows
		tot.handoffCost += r.handoffCost
		tot.gross += r.gross
		tot.injectCost += r.injectCost
	}
	tot.net = mathx.Round(tot.gross-tot.injectCost-tot.handoffCost, 4)
	ups := make([]string, 0, len(unpriced))
	for k := range unpriced {
		ups = append(ups, k)
	}
	sort.Strings(ups)
	sort.SliceStable(lineages, func(i, j int) bool { // key=-net 降序
		return lineages[i]["net"].(float64) > lineages[j]["net"].(float64)
	})
	return map[string]any{
		"formula":  SavingsFormula,
		"lineages": lineages,
		"totals":   totalsMap(tot),
		"unpriced": ups,
	}
}

// StrategyTable 策略对比表：四策略行 + best 列 + 跳过原因（文案逐字）。
// 公式只调 internal/policy.StrategyCosts（票19：第二份公式副本就此收口）。
func StrategyTable(windowEntries []map[string]any, books map[string]prices.PriceBook,
	ttlS float64, econKey string) map[string]any {
	book := bookFor(books, econKey)
	skipped := []string{}
	if ttlS <= 0 {
		skipped = append(skipped, "ttl 未配置（[heartbeat] ttl_s）")
	}
	if book == nil {
		skipped = append(skipped, "无可用品价格表（--provider / [prices.*]）")
	} else if len(book.Versions) == 0 {
		skipped = append(skipped, fmt.Sprintf("%s 无价格版本", book.Key))
		book = nil
	} else if book.Versions[len(book.Versions)-1].PCache == nil {
		skipped = append(skipped, fmt.Sprintf("%s 无 p_cache：节省额不可算（Q16）", book.Key))
		book = nil
	}
	rows := []map[string]any{}
	if book != nil && ttlS > 0 {
		for _, e := range windowEntries {
			pv := book.At(numOr(e, "ts")) // Python book.at(ts) or book.versions[-1]
			if pv == nil {
				pv = &book.Versions[len(book.Versions)-1]
			}
			sc, err := policy.StrategyCosts(*book, *pv, ttlS, numOr(e, "dur_s"),
				int(numOr(e, "prefix_tokens")), policy.DefaultBeatOutTokens, CompactRatio)
			if err != nil {
				var ncp policy.NoCachePriceError
				if errors.As(err, &ncp) { // 旧行版本缺 p_cache：报告后整表停算
					skipped = append(skipped, fmt.Sprintf("%s 无 p_cache", book.Key))
					break
				}
				continue // ttl 已校验 >0，理论不可达；防御性跳行
			}
			// Python 的 sc dict 异构（含 dur_s/prefix_tokens/best）；Go 侧
			// 数值面留在 map[string]float64，行映射在异构 map 里拼装。
			rows = append(rows, map[string]any{
				"none": sc["none"], "beat": sc["beat"], "expire": sc["expire"],
				"expire_compact": sc["expire_compact"],
				"dur_s":          numOr(e, "dur_s"),
				"prefix_tokens":  int(numOr(e, "prefix_tokens")),
				"best":           bestKey(sc),
			})
		}
	}
	return map[string]any{"rows": rows, "skipped": skipped,
		"formula": StrategyFormula, "compact_ratio": CompactRatio}
}

// bestKey Python min(四键, key=值) 的 Go 形：并列取先序（none,beat,expire,expire_compact）。
func bestKey(sc map[string]float64) string {
	best := "none"
	for _, k := range []string{"beat", "expire", "expire_compact"} {
		if sc[k] < sc[best] {
			best = k
		}
	}
	return best
}

// lineageKeyOf Python e.get("lineage_id") or e.get("session_id", "?")：
// lineage_id 空 → session_id；session_id 键缺失才落 "?"（present 空串保持空串）。
func lineageKeyOf(e map[string]any) string {
	if lid := strOr(e, "lineage_id"); lid != "" {
		return lid
	}
	if sid, ok := e["session_id"]; ok {
		s, _ := sid.(string)
		return s
	}
	return "?"
}

// parseDate Python report._parse_date：YYYY-MM-DD → 本地时区时间戳。
// endOfDay=True 取当日末（+86399.99s）——终审#3：--until 需含当日全天
// （月账主形态），--since 保持午夜。s 空 → (0, nil) 不过滤（Python None）；
// 格式坏 → error（Python strptime 抛 ValueError 炸出，Go 报错退出）。
func parseDate(s string, endOfDay bool) (float64, error) {
	if s == "" {
		return 0, nil
	}
	t, err := time.ParseInLocation("2006-01-02", s, time.Local)
	if err != nil {
		return 0, err
	}
	ts := float64(t.Unix())
	if endOfDay {
		ts += 86399.99
	}
	return ts, nil
}

// RenderText 文本报表：全部中文模板与 Python render_text 逐字对齐
// （表头/总计/口径披露/复算附录；过滤字典走 Python dict repr 形）。
func RenderText(s, st map[string]any, books map[string]prices.PriceBook,
	econKey string, filters map[string]string) string {
	unit := ""
	if eb := bookFor(books, econKey); eb != nil {
		unit = fmt.Sprintf("（单位：%s）", eb.Unit)
	}
	L := []string{"# Ferryman 账本报表", "",
		fmt.Sprintf("- 成效公式：%s · 策略公式：%s（常数 compact_ratio=%s） · 经济价格表：%s%s",
			SavingsFormula, StrategyFormula, config.PyFloatStr(CompactRatio),
			pyOrNone(econKey, "（未指定）"), unit),
		fmt.Sprintf("- 过滤：%s · 生成：%s", pyDictRepr(filters),
			time.Now().Format("2006-01-02 15:04")), "",
		"## 按族系（lineage）", "",
		"| lineage | 项目 | block | bypass | inject | handoff | window " +
			"| handoff成本 | 毛节省 | 注入成本 | 净节省 |",
		"|---|---|---:|---:|---:|---:|---:" +
			"|---:|---:|---:|---:|"}
	for _, r := range asRows(s["lineages"]) {
		L = append(L, fmt.Sprintf("| %s | %s | %d | %d | %d | %d | %d | %.2f | %.2f | %.2f | %.2f |",
			mathx.RuneTrunc(strOr(r, "lineage_id"), 24),
			mathx.RuneTrunc(strOr(r, "project"), 20),
			intOf(r["blocks"]), intOf(r["bypass"]), intOf(r["injects"]),
			intOf(r["handoffs"]), intOf(r["windows"]),
			anyNum(r["handoff_cost"]), anyNum(r["gross"]),
			anyNum(r["inject_cost"]), anyNum(r["net"])))
	}
	t := s["totals"].(map[string]any)
	L = append(L, "", fmt.Sprintf(
		"**总计**：block %d · bypass %d（对照组，不计节省） · 净节省 **%.2f**%s",
		intOf(t["blocks"]), intOf(t["bypass"]), anyNum(t["net"]), unit), "")
	if ups := asStrings(s["unpriced"]); len(ups) > 0 {
		L = append(L, fmt.Sprintf(
			"- 无价格 provider（token 已记、金额不可算）：%s", strings.Join(ups, ", ")))
	}
	if rows := asRows(st["rows"]); len(rows) > 0 {
		L = append(L, "## 等待窗口策略对比（事后，可复算）", "",
			"| dur_s | 前缀 | 不作为 | 心跳 | 放任 | 放任+compact | 最优 |",
			"|---:|---:|---:|---:|---:|---:|---|")
		for _, r := range rows {
			L = append(L, fmt.Sprintf("| %.0f | %d | %.2f | %.2f | %.2f | %.2f | %s |",
				anyNum(r["dur_s"]), intOf(r["prefix_tokens"]),
				anyNum(r["none"]), anyNum(r["beat"]), anyNum(r["expire"]),
				anyNum(r["expire_compact"]), strOr(r, "best")))
		}
		for _, c := range StrategyCaveats { // 终审#4：口径披露（与 --json 同源）
			L = append(L, "- "+c)
		}
	}
	for _, skip := range asStrings(st["skipped"]) {
		L = append(L, fmt.Sprintf("- 策略对比跳过：%s", skip))
	}
	L = append(L, "", "## 复算附录", "",
		"- 公式（v1）：净节省 = Σ block S×(P_in−P_cache)/per − Σ inject tok×P_in/per"+
			" − Σ handoff (prompt×P_in + completion×P_out)/per",
		"- handoff 侧价格取流水钉死的 price_ver（改价不重算旧账）；"+
			"block 侧取本表头经济价格表（daemon 不知被拦会话 provider，v1 简化）",
		"- 复现：ferryman account report --json（同过滤参数）",
		"")
	return strings.Join(L, "\n")
}

// Args 六维过滤 + 输出开关（Python CLI namespace 的 Go 形）。
type Args struct {
	Since, Until, Project, Session, Kind, Provider string
	JSON                                           bool
}

// accountsFor / loadPrices 可替换接缝（Python 测试 monkeypatch 的 _accounts_for /
// load_prices 等价物）。
var (
	accountsFor = func(cfg *config.Config) (*accounts.Accounts, error) {
		return accounts.New(cfg.DataDir())
	}
	loadPrices = func() map[string]prices.PriceBook { return prices.LoadPrices("") }
)

// Run 报表主入口：--json 输出结构逐字；文本渲染全模板逐字。返回 0 成功。
func Run(args Args) int {
	// stdout 无 reconfigure 等价：Go 天然 UTF-8 输出（Python GBK 控制台兜底不需要）。
	cfg, err := config.Load("", false)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	acc, err := accountsFor(cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	books := loadPrices()
	econKey := args.Provider // Python provider or cfg.ferry_provider or None
	if econKey == "" {
		econKey = cfg.FerryProvider
	}
	filters := map[string]string{}
	for _, kv := range [...]struct{ k, v string }{
		{"since", args.Since}, {"until", args.Until}, {"project", args.Project},
		{"session", args.Session}, {"kind", args.Kind},
	} {
		if kv.v != "" {
			filters[kv.k] = kv.v
		}
	}
	since, err := parseDate(args.Since, false)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	until, err := parseDate(args.Until, true) // 终审#3：until 含当日全天
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	entries := acc.Read(accounts.ReadOpts{Since: since, Until: until,
		Project: args.Project, Session: args.Session, Kind: args.Kind})
	econBook := bookFor(books, econKey)
	s := SavingsV1(entries, books, econBook)
	var windows []map[string]any
	for _, e := range entries {
		if strOr(e, "kind") == "window" {
			windows = append(windows, e)
		}
	}
	st := StrategyTable(windows, books, cfg.Heartbeat.TTLS, econKey)
	if args.JSON {
		var econProvider any // Python None → JSON null
		if econKey != "" {
			econProvider = econKey
		}
		payload := map[string]any{"savings": s, "strategy": st,
			"econ_provider":    econProvider,
			"strategy_caveats": StrategyCaveats[:]}
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false) // json.dumps(ensure_ascii=False)
		enc.SetIndent("", "  ")  // indent=2
		if err := enc.Encode(payload); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		os.Stdout.WriteString(buf.String()) // Encode 自带尾换行 = Python print
	} else {
		fmt.Println(RenderText(s, st, books, econKey, filters))
	}
	return 0
}

// ---- map 取值助手（JSON 解析行与内存行两形态通吃） ----

func strOr(e map[string]any, k string) string {
	if v, ok := e[k].(string); ok {
		return v
	}
	return ""
}

func numOr(e map[string]any, k string) float64 {
	if v, ok := e[k].(float64); ok {
		return v
	}
	return 0
}

// anyNum float64/int/int64 通吃（内存行 int、JSON 行 float64）。
func anyNum(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	case int64:
		return float64(n)
	}
	return 0
}

func intOf(v any) int { return int(anyNum(v)) }

// asRows []map[string]any / []any（JSON 解析形）两形态通吃。
func asRows(v any) []map[string]any {
	switch rs := v.(type) {
	case []map[string]any:
		return rs
	case []any:
		out := make([]map[string]any, 0, len(rs))
		for _, r := range rs {
			if m, ok := r.(map[string]any); ok {
				out = append(out, m)
			}
		}
		return out
	}
	return nil
}

// asStrings []string / []any 两形态通吃。
func asStrings(v any) []string {
	switch xs := v.(type) {
	case []string:
		return xs
	case []any:
		out := make([]string, 0, len(xs))
		for _, x := range xs {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// pyOrNone Python `x or fallback` 的渲染形：空串取 fallback。
func pyOrNone(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

// pyDictRepr Python f-string 里 {filters} 的 dict repr（insertion 序；
// Run 只产 since/until/project/session/kind 五键）。
func pyDictRepr(filters map[string]string) string {
	var parts []string
	for _, k := range [...]string{"since", "until", "project", "session", "kind"} {
		if v, ok := filters[k]; ok {
			parts = append(parts, fmt.Sprintf("'%s': '%s'", k, v))
		}
	}
	if len(parts) == 0 {
		return "（无）"
	}
	return "{" + strings.Join(parts, ", ") + "}"
}
