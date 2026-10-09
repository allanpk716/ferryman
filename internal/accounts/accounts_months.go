// accounts_months.go — 票06（dsh-post-accept-fixes）账本回放的点名读取辅助：
// 按月键读指定月份文件，不做全目录扫描——boot 回放的成本上界＝最近两个月
//（spec 钉死「最近两个账本月文件」，跨月边界＝上一月文件也要读得到）。
//
// 与 Read 的关系：复用同一 readFile/handleLine 管线（坏行跳过＋stderr 告警、
// 六维过滤、ReadBytes 无行上限），差异只在文件集合由调用方点名——月键不存在
// （月份无流量/越界）即静默跳过，与 Read 对缺失文件的同纪律。

package accounts

import "path/filepath"

// ReadMonths 点名月读取：months 为 YYYYMM 月键（调用方按「当前月、上一月」
// 依序传），依传入顺序读 <dir>/<month>.jsonl 并做与 Read 同规格的六维过滤；
// 缺文件/读失败静默跳过。空月键跳过（防御）。不持 mu（Read 同：写侧锁在
// Record 的 mu，读侧走 cmu 下的常驻缓存——ADR-0027，与 Read 共享同一缓存）。
func (a *Accounts) ReadMonths(o ReadOpts, months ...string) []map[string]any {
	out := []map[string]any{}
	a.cmu.Lock()
	defer a.cmu.Unlock()
	a.initCacheLocked()
	for _, m := range months {
		if m == "" {
			continue
		}
		name := m + ".jsonl"
		if fs := a.refresh(filepath.Join(a.dir, name), name); fs != nil {
			filterEntries(fs.entries, o, &out)
		}
	}
	return out
}
