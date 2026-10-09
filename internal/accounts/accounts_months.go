// accounts_months.go — 票06（dsh-post-accept-fixes）账本回放的点名读取辅助：
// 按月键读指定月份文件，不做全目录扫描——boot 回放的成本上界＝最近两个月
//（spec 钉死「最近两个账本月文件」，跨月边界＝上一月文件也要读得到）。
//
// 与 Read 的关系：复用同一 readFile/handleLine 管线（坏行跳过＋stderr 告警、
// 六维过滤、ReadBytes 无行上限），差异只在文件集合由调用方点名——月键不存在
// （月份无流量/越界）即静默跳过，与 Read 对缺失文件的同纪律。
//
// ReadWindow（widget 聚合票，ADR-0027 改判）：按需解析路径的月份裁剪——
// 时间窗查询只读窗内月文件，不再全目录 91MB 起步。

package accounts

import (
	"path/filepath"
	"time"

	"ferryman/internal/clock"
)

// ReadMonths 点名月读取：months 为 YYYYMM 月键（调用方按「当前月、上一月」
// 依序传），依传入顺序读 <dir>/<month>.jsonl 并做与 Read 同规格的六维过滤；
// 缺文件/读失败静默跳过。空月键跳过（防御）。不持锁（Read 同：按需现解析，
// 读侧无共享态）。
func (a *Accounts) ReadMonths(o ReadOpts, months ...string) []map[string]any {
	out := []map[string]any{}
	for _, m := range months {
		if m == "" {
			continue
		}
		name := m + ".jsonl"
		a.readFile(filepath.Join(a.dir, name), name, o, &out)
	}
	return out
}

// ReadWindow 时间窗读取：Since 给定 → MonthsBetween 点名窗内月文件（ReadMonths）；
// Since 缺省（0=全时段）→ 回落全目录 Read。按需解析（无常驻缓存）下的常规
// 查询口——窗跨几个月就读几份文件，不再每次全量。
func (a *Accounts) ReadWindow(o ReadOpts) []map[string]any {
	if o.Since == 0 {
		return a.Read(o)
	}
	return a.ReadMonths(o, MonthsBetween(o.Since, o.Until)...)
}

// MonthsBetween 时间窗（本地时区）涉及的月键，自 since 月至 min(until, now)
// 月含两端；until 缺省（0）＝至今。窗倒挂（since 在 until 后）＝单月（since
// 月）。Since 缺省的窗不是本函数的辖区（ReadWindow 直接回落全目录）。
func MonthsBetween(since, until float64) []string {
	hi := time.Unix(int64(clock.Now()), 0).In(time.Local)
	if until > 0 {
		if u := time.Unix(int64(until), 0).In(time.Local); u.Before(hi) {
			hi = u
		}
	}
	lo := time.Unix(int64(since), 0).In(time.Local)
	if lo.After(hi) {
		lo = hi
	}
	var out []string
	for m := time.Date(lo.Year(), lo.Month(), 1, 0, 0, 0, 0, time.Local); !m.After(hi); m = m.AddDate(0, 1, 0) {
		out = append(out, m.Format("200601"))
	}
	return out
}
