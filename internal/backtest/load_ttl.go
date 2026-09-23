// load_ttl.go — 管子一（2026-09-23 启用役）：beat 遥测 TTL 观测收割——账本
// window 行（等待窗）与 qwatch_open/qwatch_close 行（问询窗）给出开窗区间，
// beat 行（outcome=hit）给出存活证据，两相配对得每窗 TTL 观测（分钟），供
// 同模型扫参作缺省 TTL 输入（--ttl-min 显式传入优先；两者皆空回落种子隐含
// 中位，票06 既有路径）。
//
// 口径与 daemon GET /beats 的"TTL 观测值"注释一致（query_beats.go）：hit 跳
// 证明缓存存活至少（该跳时刻−开窗）秒，每窗取最大深度；miss/error/observe
// 不反推 TTL——不造数。窗内无 hit → 该窗不产观测，计数照登。
//
// 归属：观测挂在"该窗最深 hit 跳"的 provider（记账时的价格本键）上；调用方
// 按上游定位的价格本键（BookFor(upstream).Key）取桶。beat 行缺 provider 落
// "" 桶，如实保留不猜。各桶内观测升序（确定性输出）。
package backtest

import (
	"fmt"
	"path/filepath"
	"sort"
)

// TTLObsSet 收割产物：观测集 + 如实计数（报告与 CLI 溯源用）。
type TTLObsSet struct {
	ByProvider map[string][]float64 // 价格本键 → TTL 观测（分钟，升序）
	Windows    int                  // 参与配对的开窗区间数（含无 hit 的窗）
	HitWindows int                  // 产出观测的窗数（有 ≥1 hit 跳）
	Beats      int                  // beat 行总数（含 miss/error/observe）
	HitBeats   int                  // outcome=hit 的 beat 行数
}

// winInterval 一个开窗区间（等待窗或问询窗；closed=+Inf 表示未闭合）。
type winInterval struct {
	opened float64
	closed float64
}

// winBest 每窗最强 hit 证据（最大深度及其 provider；并列取字典序小者保确定）。
type winBest struct {
	depth    float64 // 秒
	provider string
}

// HarvestTTLObs 装载账本流水并收割每窗 TTL 观测。DataDir 为账本根（读
// <DataDir>/accounts/*.jsonl，与 LoadIdle 同源惯例：文件名序、坏行跳过）。
func HarvestTTLObs(dataDir string) (*TTLObsSet, error) {
	rows, err := readLedger(filepath.Join(dataDir, "accounts"))
	if err != nil {
		return nil, fmt.Errorf("TTL 观测收割读账本失败: %w", err)
	}

	// 区间收集：window 行自带 opened/closed；qwatch_open/close 行按 ts 成对
	// （同会话交替出现，未闭合的以 +Inf 收尾——在途窗的 hit 照样算证据）。
	const inf = 1e18
	type key struct{ agent, sid string }
	intervals := map[key][]winInterval{}
	pendingQW := map[key]float64{} // 已开未闭的问询窗 openedTS
	for _, r := range rows {
		k := key{r.Agent, r.SessionID}
		switch r.Kind {
		case "window":
			if r.OpenedTS > 0 {
				closed := r.ClosedTS
				if closed <= 0 {
					closed = inf
				}
				intervals[k] = append(intervals[k], winInterval{r.OpenedTS, closed})
			}
		case "qwatch_open":
			if _, open := pendingQW[k]; !open {
				pendingQW[k] = r.TS
			}
		case "qwatch_close":
			if opened, open := pendingQW[k]; open {
				delete(pendingQW, k)
				intervals[k] = append(intervals[k], winInterval{opened, r.TS})
			}
		}
	}
	for k, opened := range pendingQW {
		intervals[k] = append(intervals[k], winInterval{opened, inf})
	}
	for k := range intervals {
		ivs := intervals[k]
		sort.Slice(ivs, func(i, j int) bool { return ivs[i].opened < ivs[j].opened })
	}

	set := &TTLObsSet{ByProvider: map[string][]float64{}}
	for _, ivs := range intervals {
		set.Windows += len(ivs)
	}

	// 单趟配对：每条 hit 跳归入包含它的最晚开窗（区间理应不重叠——两窗互斥
	// 先开者赢；防御取最晚开窗，宁并窗不漏窗），窗内保最深证据。
	best := map[key]map[float64]*winBest{}
	pair := func(r ledgerRow) (key, float64, bool) {
		k := key{r.Agent, r.SessionID}
		ivs := intervals[k]
		idx := -1
		for i := range ivs {
			if ivs[i].opened <= r.TS && r.TS <= ivs[i].closed {
				if idx < 0 || ivs[i].opened > ivs[idx].opened {
					idx = i
				}
			}
		}
		if idx < 0 { // 无区间可配（开窗行缺失/坏行跳过）——如实丢弃不猜
			return k, 0, false
		}
		return k, ivs[idx].opened, true
	}
	for _, r := range rows {
		if r.Kind != "beat" {
			continue
		}
		set.Beats++
		if r.Outcome != "hit" {
			continue
		}
		set.HitBeats++
		k, opened, ok := pair(r)
		if !ok {
			continue
		}
		depth := r.TS - opened
		if depth <= 0 {
			continue
		}
		if best[k] == nil {
			best[k] = map[float64]*winBest{}
		}
		cur := best[k][opened]
		if cur == nil {
			set.HitWindows++
			best[k][opened] = &winBest{depth, r.Provider}
			continue
		}
		if depth > cur.depth || (depth == cur.depth && r.Provider < cur.provider) {
			cur.depth, cur.provider = depth, r.Provider
		}
	}
	for _, byOpened := range best {
		for _, b := range byOpened {
			set.ByProvider[b.provider] = append(set.ByProvider[b.provider], b.depth/60)
		}
	}
	for p := range set.ByProvider {
		sort.Float64s(set.ByProvider[p])
	}
	return set, nil
}

// ObsFor 价格本键取观测（确定性拷贝；nil 集合返回 nil）。
func (s *TTLObsSet) ObsFor(bookKey string) []float64 {
	if s == nil {
		return nil
	}
	return append([]float64(nil), s.ByProvider[bookKey]...)
}
