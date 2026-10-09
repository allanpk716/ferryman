// accounts_aggregate.go — 聚合索引（widget 聚合票，ADR-0027 改判）。
//
// 动机：统计卡顿战役首版做的是常驻解析缓存（全量明细驻留 daemon 内存，
// 实测常驻堆 287MB）。用户裁定：省内存是开发纪律，不看机器余量——机器
// 节奏的读者（悬浮窗 30s 轮询）只需十来个合计数，为其驻留 19.5 万条明细
// 不值；人工面（统计页/report）按需解析＋进度交互即可。故撤常驻明细，
// 换成本文件的 KB 级聚合索引：
//
//   - 常驻内容＝usage 四列（模型×本地自然日）+ handoff 计数，滚动留最新
//     两个月（悬浮窗月/周窗的最大跨度）；30 模型 × 31 日 ≈ 每月 40KB。
//   - Record 落盘成功即折叠（重新解析刚 marshal 出的行——与整建路径共用
//     同一解析形状，杜绝两条取数路径分叉）。
//   - RebuildAggregates 流式整建：逐行解析→折叠→丢弃，峰值内存只多一行；
//     serve 启动期后台调用一次（与旧 Prewarm 同位）。
//   - 盘面戳核对：daemon 自身折叠随写推进戳，生产稳态零重建；外部改写
//     （测试直写/T39 式重写/文件增删）由快照前的核对侦测并整建——正确性
//     永远以盘上字节为准。
//
// 铁律不动：账本仍是 append-only JSONL 唯一事实源（ADR-0002）；聚合只是
// 可随时重建的读侧派生物，明细查询一律走 Read/ReadMonths/ReadWindow 现解析。
//
// 锁序（包内唯一顺序）：mu（Record 写盘）→ aggmu（折叠/换入）。Rebuild
// 持 mu 读文件（挡住 Record 并发追加）后 aggmu 提交，与 Record 同序，无
// 反向嵌套。

package accounts

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// aggKeepMonths 聚合滚动保留月数：当月＋上月（悬浮窗周窗跨月时的最大回看）。
const aggKeepMonths = 2

// aggDay 单日聚合格。
type aggDay struct {
	usage    map[string][4]float64 // lower(model) → [input, cache_read, cache_creation, output]
	handoffs int
}

// aggStamp 盘面戳（折叠已及的字节位）。
type aggStamp struct {
	size int64
	mod  time.Time
}

// initAggLocked 懒初始化（零值 Accounts 直接构造的测试形态兜底）。持 aggmu。
func (a *Accounts) initAggLocked() {
	if a.agg == nil {
		a.agg = map[string]map[int]*aggDay{}
	}
	if a.aggStamps == nil {
		a.aggStamps = map[string]aggStamp{}
	}
}

// foldRecorded 记账后折叠（Record 持 mu 调用，内部取 aggmu）。重新解析刚
// 落盘的行而非复用内存 entry：折叠与整建两条路径共用同一解析形状（JSON
// 数值一律 float64），杜绝取数分叉。best-effort：解析/stat 失败只跳过
// （戳推不动 → 下轮快照前核对侦测重建，保守侧正确）。
func (a *Accounts) foldRecorded(fname string, line []byte) {
	var e map[string]any
	if err := json.Unmarshal(line, &e); err != nil || e == nil {
		return // 不可达防御：行刚由 marshalLine 生成
	}
	fi, err := os.Stat(filepath.Join(a.dir, fname))
	if err != nil {
		return
	}
	a.aggmu.Lock()
	defer a.aggmu.Unlock()
	a.initAggLocked()
	aggFoldEntry(a.agg, e)
	aggTrimMonths(a.agg, aggKeepMonths)
	a.aggStamps[fname] = aggStamp{size: fi.Size(), mod: fi.ModTime()}
}

// RebuildAggregates 全量流式重建（serve 启动期后台调用）：持 mu 逐文件
// 逐行解析→折叠→丢弃——峰值内存只多一行，明细永不驻留；完成后 aggmu 内
// 一次性换入并裁剪月份。盘面戳取读前 stat；mu 挡住 Record 期间无自身追加，
// 外部改写若插在 stat 与读之间，戳落后于实盘 → 下轮核对再建（保守侧）。
func (a *Accounts) RebuildAggregates() {
	a.mu.Lock()
	defer a.mu.Unlock()
	entries, err := os.ReadDir(a.dir)
	if err != nil {
		return
	}
	agg := map[string]map[int]*aggDay{}
	stamps := map[string]aggStamp{}
	for _, de := range entries {
		if de.IsDir() || !strings.HasSuffix(de.Name(), ".jsonl") {
			continue
		}
		path := filepath.Join(a.dir, de.Name())
		fi, err := os.Stat(path)
		if err != nil {
			continue
		}
		foldFileInto(agg, path, de.Name())
		stamps[de.Name()] = aggStamp{size: fi.Size(), mod: fi.ModTime()}
	}
	aggTrimMonths(agg, aggKeepMonths)
	a.aggmu.Lock()
	a.agg, a.aggStamps = agg, stamps
	a.aggmu.Unlock()
}

// foldFileInto 单文件流式折叠（ReadBytes 无行上限；splitlines 尾段语义）。
// 坏行跳过＋stderr 告警——与 Read 的 handleLine 同纪律（stdout 机器可解析）。
func foldFileInto(agg map[string]map[int]*aggDay, path, base string) {
	fh, err := os.Open(path)
	if err != nil {
		return // 打不开：静默跳过（与 Read 同纪律）
	}
	defer fh.Close()
	r := bufio.NewReader(fh)
	for i := 1; ; i++ {
		raw, rerr := r.ReadBytes('\n')
		if len(raw) > 0 {
			if line := bytes.TrimSuffix(raw, []byte("\n")); len(bytes.TrimSpace(line)) != 0 {
				var e map[string]any
				if err := json.Unmarshal(line, &e); err != nil || e == nil {
					fmt.Fprintf(os.Stderr, "[accounts] 跳过损坏行 %s:%d\n", base, i)
				} else {
					aggFoldEntry(agg, e)
				}
			}
		}
		if rerr != nil {
			break
		}
	}
}

// aggFoldEntry 单行折叠（无锁纯函数，调用方持各自锁）：usage 四列按
// lower(model)×本地日累计、handoff 计数；其余科目不入（聚合面只供悬浮窗，
// 明细查询按需现解析）。
func aggFoldEntry(agg map[string]map[int]*aggDay, e map[string]any) {
	kind, _ := e["kind"].(string)
	if kind != "usage" && kind != "handoff" {
		return
	}
	ts, _ := e["ts"].(float64)
	t := time.Unix(int64(ts), 0).In(time.Local)
	month, day := t.Format("200601"), t.Day()
	m := agg[month]
	if m == nil {
		m = map[int]*aggDay{}
		agg[month] = m
	}
	c := m[day]
	if c == nil {
		c = &aggDay{}
		m[day] = c
	}
	if kind == "handoff" {
		c.handoffs++
		return
	}
	if c.usage == nil {
		c.usage = map[string][4]float64{}
	}
	model := strings.ToLower(strOr(e, "model"))
	v := c.usage[model]
	v[0] += numOr(e, "input_tokens")
	v[1] += numOr(e, "cache_read_tokens")
	v[2] += numOr(e, "cache_creation_tokens")
	v[3] += numOr(e, "output_tokens")
	c.usage[model] = v
}

// aggTrimMonths 月份裁剪：留最新 keep 个月（月键由 Format("200601") 生成，
// 字典序=时间序）。悬浮窗月/周窗最大跨度=当月+上月，两格足矣。
func aggTrimMonths(agg map[string]map[int]*aggDay, keep int) {
	if len(agg) <= keep {
		return
	}
	months := make([]string, 0, len(agg))
	for m := range agg {
		months = append(months, m)
	}
	sort.Strings(months)
	for _, m := range months[:len(months)-keep] {
		delete(agg, m)
	}
}

// AggUsageCell usage 聚合格（快照单元）：模型已小写（归属键），四列原文。
type AggUsageCell struct {
	Month                          string
	Day                            int
	Model                          string
	In, CacheRead, CacheWrite, Out float64
}

// AggHandoffCell handoff 计数格（快照单元）。
type AggHandoffCell struct {
	Month string
	Day   int
	Count int
}

// AggSnapshot 聚合快照（深拷贝，消费方可任意把玩）。
type AggSnapshot struct {
	Usage   []AggUsageCell
	Handoff []AggHandoffCell
}

// AggregateSnapshot 聚合快照：先核对盘面戳（不符→整建），再深拷贝返回。
// 月/日/模型三序确定（测试可比对）。
func (a *Accounts) AggregateSnapshot() AggSnapshot {
	a.ensureAggFresh()
	a.aggmu.Lock()
	defer a.aggmu.Unlock()
	snap := AggSnapshot{}
	months := make([]string, 0, len(a.agg))
	for m := range a.agg {
		months = append(months, m)
	}
	sort.Strings(months)
	for _, m := range months {
		days := make([]int, 0, len(a.agg[m]))
		for d := range a.agg[m] {
			days = append(days, d)
		}
		sort.Ints(days)
		for _, d := range days {
			c := a.agg[m][d]
			if c.handoffs > 0 {
				snap.Handoff = append(snap.Handoff, AggHandoffCell{Month: m, Day: d, Count: c.handoffs})
			}
			models := make([]string, 0, len(c.usage))
			for k := range c.usage {
				models = append(models, k)
			}
			sort.Strings(models)
			for _, k := range models {
				v := c.usage[k]
				snap.Usage = append(snap.Usage, AggUsageCell{Month: m, Day: d, Model: k,
					In: v[0], CacheRead: v[1], CacheWrite: v[2], Out: v[3]})
			}
		}
	}
	return snap
}

// ensureAggFresh 盘面戳核对：目录内任一 *.jsonl 新增/消失、size 或 mtime 与
// 折叠戳不符 → RebuildAggregates。生产稳态（daemon 唯一写者，折叠随写推
// 进戳）零触发；触发面＝测试直写/T39 式重写/换装后目录替换。
func (a *Accounts) ensureAggFresh() {
	entries, err := os.ReadDir(a.dir)
	if err != nil {
		return
	}
	cur := make(map[string]aggStamp, len(entries))
	for _, de := range entries {
		if de.IsDir() || !strings.HasSuffix(de.Name(), ".jsonl") {
			continue
		}
		fi, err := os.Stat(filepath.Join(a.dir, de.Name()))
		if err != nil {
			return // stat 失败：保守跳过核对（快照发旧值，下轮再核）
		}
		cur[de.Name()] = aggStamp{size: fi.Size(), mod: fi.ModTime()}
	}
	a.aggmu.Lock()
	same := len(cur) == len(a.aggStamps)
	if same {
		for k, v := range cur {
			if s, ok := a.aggStamps[k]; !ok || s != v {
				same = false
				break
			}
		}
	}
	a.aggmu.Unlock()
	if !same {
		a.RebuildAggregates()
	}
}
