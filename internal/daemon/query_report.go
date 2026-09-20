package daemon

// query_report.go — 票03：GET /report 实现（注册于 queryapi.go 的
// queryEndpoints 分派表）。
//
// 公式单源红线：成效账只调 report.SavingsV1（internal/report 导出面，规格
// 「复用 internal/report 公式单源——禁止出现第二份公式实现」）；本文件不出现
// 任何节省/策略公式。经济价格表的选取与可算性标注是 report.bookFor /
// report.StrategyTable 既有口径的消费面搬运（watcher.defaultWaitPolicy 同款），
// 属输入装配，不是公式副本。
//
// 单列口径（CONTEXT「成效账」：bypass 绕过与无效保温单列）：bypass 在
// SavingsV1 totals 里本就是独立计数（对照组，不计净额）；无效保温（wait_close
// 行 useless_warm=true）SavingsV1 不消费该科目、在此单列汇总——两列都不混入
// 成效账净额。
//
// 四列 token（CONTEXT「四列口径」）从 usage 科目行纯加总；token 级金额不硬造
// ——价格表无缓存写价（PriceVersion 只有 p_in/p_cache/p_out），金额面一律由
// 成效账与 wait_close 实收承担。
//
// 红线（queryapi.go 顶部块全文适用）：无消息内容、无凭据字段、纯只读；账本
// Read 是盘上只读遍历，锁外进行（query_sessions.go 同纪律）。

import (
	"fmt"
	"net/http"
	"time"

	"ferryman/internal/accounts"
	"ferryman/internal/mathx"
	"ferryman/internal/prices"
	"ferryman/internal/report"
)

// queryReportPrices 价格表加载缝（report.loadPrices 同款接缝；测试注入用，
// 绝不缓存——与 report 每次现读同水位）。
var queryReportPrices = func() map[string]prices.PriceBook { return prices.LoadPrices("") }

// handleReport GET /report?scope=<project|session|month>&key=...：按 scope 过滤
// 账本流水后回四列 token 汇总＋成效账（report.SavingsV1 单源）＋可算性标注＋
// 无效保温单列。scope/key 非法 → 400 JSON。
func handleReport(d *Daemon, w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	scope := qsOr(q, "scope", "")
	key := qsOr(q, "key", "")
	opts := accounts.ReadOpts{}
	switch scope {
	case "project":
		if key == "" {
			badRequest(w, fmt.Errorf("scope=project 需要 key（项目路径）"))
			return
		}
		opts.Project = key
	case "session":
		if key == "" {
			badRequest(w, fmt.Errorf("scope=session 需要 key（session_id）"))
			return
		}
		opts.Session = key
	case "month":
		if key == "" {
			badRequest(w, fmt.Errorf("scope=month 需要 key（YYYY-MM）"))
			return
		}
		mStart, err := time.ParseInLocation("2006-01", key, time.Local)
		if err != nil {
			badRequest(w, fmt.Errorf("scope=month 的 key 必须为 YYYY-MM，得到: %q", key))
			return
		}
		// 本地时区整月半开区间（账本按月滚动用本地时区）；Read 的 Until 为
		// 含端比较，收进下月首秒前 1ms 保证下月行不漏进本月。
		opts.Since = float64(mStart.Unix())
		opts.Until = float64(mStart.AddDate(0, 1, 0).Unix()) - 0.001
	default:
		badRequest(w, fmt.Errorf("scope 必须为 project|session|month，得到: %q", scope))
		return
	}

	var entries []map[string]any
	if d.Accounts != nil {
		entries = d.Accounts.Read(opts) // 锁外只读遍历（不持任何锁做盘 I/O）
	}

	// 经济价格表选取：report.bookFor 同口径——provider 命中取之，否则仅一本
	// 取唯一本，再否则 nil（不可算，不造数）。
	books := queryReportPrices()
	econKey := d.Cfg.FerryProvider
	var econBook *prices.PriceBook
	if econKey != "" {
		if b, ok := books[econKey]; ok {
			econBook = &b
		}
	}
	if econBook == nil && len(books) == 1 {
		for _, b := range books {
			econBook = &b
		}
	}
	savings := report.SavingsV1(entries, books, econBook) // 公式单源
	computable, note := savingsComputability(econBook)

	// 四列 token（usage 科目）与无效保温（wait_close 且 useless_warm=true）
	// 一次遍历各自汇总。
	var in, cr, cc, outN, reqs, uwCount int
	var uwCost float64
	for _, e := range entries {
		k, _ := e["kind"].(string)
		switch k {
		case "usage":
			in += int(acctNum(e, "input_tokens"))
			cr += int(acctNum(e, "cache_read_tokens"))
			cc += int(acctNum(e, "cache_creation_tokens"))
			outN += int(acctNum(e, "output_tokens"))
			reqs++
		case "wait_close":
			if b, _ := e["useless_warm"].(bool); b {
				uwCount++
				uwCost += acctNum(e, "cost_actual")
			}
		}
	}

	var econProvider any // 空 → JSON null（report.Run 同形）
	if econKey != "" {
		econProvider = econKey
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"scope": scope,
		"key":   key,
		"tokens": map[string]any{
			"input_tokens":          in,
			"cache_read_tokens":     cr,
			"cache_creation_tokens": cc,
			"output_tokens":         outN,
			"requests":              reqs,
		},
		"savings":            savings, // report.SavingsV1 原样（含 bypass 单列 totals）
		"savings_computable": computable,
		"savings_note":       note, // 不可算时如实标注；可算为空串
		"useless_warm": map[string]any{
			"count":       uwCount,
			"cost_actual": mathx.Round(uwCost, 6),
		},
		"econ_provider": econProvider,
	})
}

// savingsComputability 可算性标注（report.StrategyTable 的 skip 口径同源）：
// 无价格表 / 无版本 / 末版缺 p_cache → 不可算＋如实文案；此时 SavingsV1 的
// 毛节省逐行跳过恒为 0——不硬算。
func savingsComputability(econBook *prices.PriceBook) (bool, string) {
	switch {
	case econBook == nil:
		return false, "节省额不可算：无可用品价格表（[prices.*]）"
	case len(econBook.Versions) == 0:
		return false, fmt.Sprintf("节省额不可算：%s 无价格版本", econBook.Key)
	case econBook.Versions[len(econBook.Versions)-1].PCache == nil:
		return false, fmt.Sprintf("节省额不可算：%s 无 p_cache（不硬算）", econBook.Key)
	}
	return true, ""
}
