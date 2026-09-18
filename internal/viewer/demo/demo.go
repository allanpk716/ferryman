// Package demo 生成演示模式的确定性合成账本（--demo）：五族系覆盖心跳全部结局
// 与摆渡全链路。字段逐键对齐 accounts.py schema；数值与 policy 黄金数一致。
package demo

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"ferryman/internal/viewer/ledger"
	"ferryman/internal/viewer/policy"
)

// GLM 演示口径（与 config.example.toml [prices.glm] v2026-09-17、实测 TTL 600s 一致）
const (
	PIn, PCache, POut = 6.9, 1.7, 24
	Per               = 10000.0
	TTLS              = 600.0
	PriceVer          = "v2026-09-17"
	Provider          = "glm"
	BeatModel         = "glm-4.7"
)

// 剧本常量：摆渡走本地小模型（provider=local）；ts_iso 布局对齐 accounts.py 的
// strftime("%Y-%m-%dT%H:%M:%S%z")（本地时区）。
const (
	agent        = "cc"
	project      = "C:/WorkSpace/agent/Ferryman"
	handoffProv  = "local"
	handoffModel = "qwen3.8-27b-sglang-general"
	closeReason  = "subagents_done"
	tsLayout     = "2006-01-02T15:04:05-0700"
)

// Entries 生成全部合成行。base 通常取"今天 09:00 本地"（main 负责），本函数纯确定性：
// 同 base 同输出，内部绝不取时钟。
func Entries(base time.Time) []ledger.Entry {
	raw := rows(base)
	out := make([]ledger.Entry, 0, len(raw))
	for _, r := range raw {
		// map→Entry 走一次 JSON 往返：剧本只写一份（map 形态），typed 视图与落盘视图
		// 同源不漂移；encoding/json 的浮点短表示保证往返逐位相等，成本黄金数不受损。
		line, err := json.Marshal(r)
		if err != nil {
			panic("demo: 合成行含不可序列化值: " + err.Error()) // 行值全为标量，不可达
		}
		var e ledger.Entry
		if err := json.Unmarshal(line, &e); err != nil {
			panic("demo: 合成行与 Entry schema 不符: " + err.Error()) // 白名单逐键对齐，不可达
		}
		out = append(out, e)
	}
	return out
}

// Write 把合成行序列化为 JSONL 写入 dir 下单个文件（文件名 demo-<YYYYMM>.jsonl，
// YYYYMM 按 base 本地时间），返回文件路径。每行 json.Marshal + "\n"，
// 与 accounts.py 的落盘格式一致（单行单条、UTF-8、换行结尾）。
func Write(dir string, base time.Time) (string, error) {
	path := filepath.Join(dir, "demo-"+base.Format("200601")+".jsonl")
	var b strings.Builder
	for _, r := range rows(base) {
		line, err := json.Marshal(r)
		if err != nil {
			return "", err // 行值全为标量，不可达；仍按惯例上抛
		}
		b.Write(line)
		b.WriteByte('\n')
	}
	return path, os.WriteFile(path, []byte(b.String()), 0o644)
}

// rows 剧本本体：五族系逐行铺排，末尾按 ts 稳定排序（同 ts 保生成序，即族系 a→e）。
// 行是"白名单精确"的 map：键集合恰为 accounts.py 的 _COMMON ∪ _KIND_FIELDS[kind]——
// 多一个字段真实账本会拒收，演示数据不能撒谎，故不能用 ledger.Entry 直接序列化
// （扁平结构带全部 kind 的字段）。
func rows(base time.Time) []map[string]any {
	rs := make([]map[string]any, 0, 67)
	secs := func(t time.Time) float64 { return float64(t.UnixMilli()) / 1000 }

	// ---- lin-a「保温兑现」：beat 全中 → 续跑吃满活缓存 → 摆渡 → 回归拦截 → 接棒。
	// 完整故事线，LastTS 全场最大，列表页第一行。 ----
	{
		lin := "lin-a"
		off := 0
		u := func(sess string, ts time.Time, title string, in, cacheRead, creation, out int64) {
			off++
			rs = append(rs, mkUsage(ts, off, sess, lin, title, in, cacheRead, creation, out))
		}
		prefix := func(i int) int64 { return 20000 + 130000*int64(i)/11 } // 12 条爬坡铺到 150k
		for i, m := range [12]int{0, 3, 6, 9, 12, 15, 18, 21, 24, 27, 30, 36} {
			ts := base.Add(time.Duration(m) * time.Minute)
			switch {
			case i == 0: // 冷启动：无缓存可读，全靠 input+creation 垫起首个前缀
				u("sess-a1", ts, "", 1500, 0, 18500, 1200)
			case i == 11: // T0：派活请求（数值按剧本定死，前缀恰满 150k 进窗口/心跳段）
				u("sess-a1", ts, "Ferryman 开发·派子代理核账本", 800, 140000, 9200, 1200)
			default: // cache_read≈上一请求前缀，creation 补差到递增前缀
				in := int64(500 + 100*(i%5))
				u("sess-a1", ts, "", in, prefix(i-1), prefix(i)-prefix(i-1)-in, int64(900+150*(i%7)))
			}
		}
		t0 := base.Add(36 * time.Minute)
		close18 := t0.Add(18 * time.Minute)
		rs = append(rs, mkWindow(close18, "sess-a1", lin, secs(t0), secs(close18), 150000))
		rs = append(rs, mkBeat(t0.Add(480*time.Second), "sess-a1", lin, 150000, "hit"))
		rs = append(rs, mkBeat(t0.Add(960*time.Second), "sess-a1", lin, 150000, "hit"))
		u("sess-a1", t0.Add(1082*time.Second), "", 600, 150000, 0, 1800) // 自动续跑：命中活缓存，全绿柱
		u("sess-a1", t0.Add(1160*time.Second), "", 700, 150000, 0, 1500)
		u("sess-a1", t0.Add(1240*time.Second), "", 500, 150000, 0, 1700)
		lastWrite := t0.Add(22 * time.Minute) // 用户最后写入
		u("sess-a1", lastWrite, "", 800, 150000, 0, 1300)
		rs = append(rs, mkHandoff(lastWrite.Add(25*time.Minute), "sess-a1", lin, 48000, 2100, 41.2))
		ret := lastWrite.Add(36 * time.Minute) // 用户回来提交 → 闲置 36min 触发拦截
		rs = append(rs, mkBlock(ret, "sess-a1", lin, 150000, 2160))
		// 30s 后新 session 同 lineage：族系跨 resume 是设计意图
		rs = append(rs, mkInject(ret.Add(30*time.Second), "sess-a2", lin, 3800, "20260918-demo-1"))
		u("sess-a2", ret.Add(50*time.Second), "", 4200, 0, 0, 1500) // 冷启动全红：input 含注入正文
		u("sess-a2", ret.Add(200*time.Second), "", 800, 4200, 1000, 1800)
		u("sess-a2", ret.Add(420*time.Second), "Ferryman 开发·接棒：对账收尾", 700, 6000, 1300, 2200)
	}

	// ---- lin-b「保温到上限，放任过期」：跳全中，续跑却全款重付（对照组）。 ----
	{
		lin, sess := "lin-b", "sess-b1"
		off := 0
		u := func(ts time.Time, title string, in, cacheRead, creation, out int64) {
			off++
			rs = append(rs, mkUsage(ts, off, sess, lin, title, in, cacheRead, creation, out))
		}
		prefix := func(i int) int64 { return 20000 + 100000*int64(i)/7 } // 8 条爬坡铺到 120k
		for i, m := range [8]int{0, 3, 6, 9, 12, 15, 18, 21} {
			ts := base.Add(time.Duration(m) * time.Minute)
			if i == 0 {
				u(ts, "", 1500, 0, 18500, 1200)
				continue
			}
			in := int64(500 + 100*(i%5))
			u(ts, "", in, prefix(i-1), prefix(i)-prefix(i-1)-in, int64(900+150*(i%7)))
		}
		t0 := base.Add(21 * time.Minute)
		close40 := t0.Add(40 * time.Minute)
		rs = append(rs, mkWindow(close40, sess, lin, secs(t0), secs(close40), 120000))
		rs = append(rs, mkBeat(t0.Add(480*time.Second), sess, lin, 120000, "hit"))
		rs = append(rs, mkBeat(t0.Add(960*time.Second), sess, lin, 120000, "hit"))
		// 放任过期：40min 窗远超 TTL，续跑时缓存凉透，input 全款重付——全红柱
		u(close40.Add(2*time.Second), "", 120000, 0, 0, 2000)
		u(t0.Add(2500*time.Second), "", 600, 122000, 400, 1400)
		u(t0.Add(2600*time.Second), "长任务·子代理跑了 40 分钟", 500, 123000, 700, 1600)
	}

	// ---- lin-c「首跳 miss，停跳告警」：miss 绝不重试，一次全价后止损。 ----
	{
		lin, sess := "lin-c", "sess-c1"
		off := 0
		u := func(ts time.Time, title string, in, cacheRead, creation, out int64) {
			off++
			rs = append(rs, mkUsage(ts, off, sess, lin, title, in, cacheRead, creation, out))
		}
		prefix := func(i int) int64 { return 15000 + 15000*int64(i) } // 6 条等步铺到 90k
		for i, m := range [6]int{0, 3, 6, 9, 12, 15} {
			ts := base.Add(time.Duration(m) * time.Minute)
			if i == 0 {
				u(ts, "", 1500, 0, 13500, 1200)
				continue
			}
			in := int64(500 + 100*(i%5))
			u(ts, "", in, prefix(i-1), prefix(i)-prefix(i-1)-in, int64(900+150*(i%7)))
		}
		t0 := base.Add(15 * time.Minute)
		close15 := t0.Add(15 * time.Minute)
		rs = append(rs, mkWindow(close15, sess, lin, secs(t0), secs(close15), 90000))
		rs = append(rs, mkBeat(t0.Add(480*time.Second), sess, lin, 90000, "miss")) // MISS：之后无跳
		u(close15.Add(2*time.Second), "", 90000, 0, 0, 1900)                      // 缓存已凉，全款重付
		u(t0.Add(1000*time.Second), "心跳 miss 演示·立即停跳", 600, 91900, 500, 1300)
	}

	// ---- lin-d「短窗口零跳」：窗比 τ（8min）还短，安静期一分不花。 ----
	{
		lin, sess := "lin-d", "sess-d1"
		off := 0
		u := func(ts time.Time, title string, in, cacheRead, creation, out int64) {
			off++
			rs = append(rs, mkUsage(ts, off, sess, lin, title, in, cacheRead, creation, out))
		}
		prefix := func(i int) int64 { return 10000 + 12500*int64(i) } // 5 条等步铺到 60k
		for i, m := range [5]int{0, 3, 6, 9, 12} {
			ts := base.Add(time.Duration(m) * time.Minute)
			if i == 0 {
				u(ts, "", 1500, 0, 8500, 1200)
				continue
			}
			in := int64(500 + 100*(i%5))
			u(ts, "", in, prefix(i-1), prefix(i)-prefix(i-1)-in, int64(900+150*(i%7)))
		}
		t0 := base.Add(12 * time.Minute)
		close6 := t0.Add(6 * time.Minute)
		rs = append(rs, mkWindow(close6, sess, lin, secs(t0), secs(close6), 60000)) // 无 beat：6min<τ
		u(close6.Add(2*time.Second), "短等待·安静期内子代理返回", 500, 60000, 0, 1600)          // 活缓存绿柱
	}

	// ---- lin-e「无子代理·纯摆渡线」：闲置 25min 触发摆渡，用户未归故无 block。 ----
	{
		lin, sess := "lin-e", "sess-e1"
		off := 0
		u := func(ts time.Time, title string, in, cacheRead, creation, out int64) {
			off++
			rs = append(rs, mkUsage(ts, off, sess, lin, title, in, cacheRead, creation, out))
		}
		prefix := func(i int) int64 { return 8000 + 37000*int64(i)/9 } // 10 条铺到 45k
		for i, m := range [10]int{0, 5, 10, 15, 20, 25, 30, 35, 40, 50} {
			ts := base.Add(time.Duration(m) * time.Minute)
			switch {
			case i == 0:
				u(ts, "", 1200, 0, 6800, 1100)
			case i == 9: // 最后一条 usage，之后用户离开
				u(ts, "普通会话·用户离开触发摆渡", 600, prefix(8), 45000-prefix(8)-600, 1300)
			default:
				in := int64(500 + 100*(i%4))
				u(ts, "", in, prefix(i-1), prefix(i)-prefix(i-1)-in, int64(800+200*(i%6)))
			}
		}
		rs = append(rs, mkHandoff(base.Add(50*time.Minute).Add(25*time.Minute), sess, lin, 18000, 1400, 28.4))
	}

	// 全局按 ts 稳定排序：文件序=时间序（对齐真实账本 append-only 的读感），
	// 同 ts 保生成序（族系 a→e），确定性不依赖 map 遍历。
	sort.SliceStable(rs, func(i, j int) bool {
		return rs[i]["ts"].(float64) < rs[j]["ts"].(float64)
	})
	return rs
}

// commonRow 填 accounts.py 的 _COMMON 八键；ts 按其 round(ts,3) 语义截到毫秒。
func commonRow(kind string, ts time.Time, session, lineage string) map[string]any {
	return map[string]any{
		"v": 1, "kind": kind,
		"ts":     float64(ts.UnixMilli()) / 1000,
		"ts_iso": ts.Format(tsLayout),
		"agent":  agent, "session_id": session, "lineage_id": lineage, "project": project,
	}
}

func mkUsage(ts time.Time, off int, session, lineage, title string, in, cacheRead, creation, out int64) map[string]any {
	r := commonRow("usage", ts, session, lineage)
	r["model"] = BeatModel // 演示全程 GLM 口径：主会话与心跳同一模型、同一价目表
	r["title"] = title
	r["input_tokens"] = in
	r["cache_read_tokens"] = cacheRead
	r["cache_creation_tokens"] = creation
	r["output_tokens"] = out
	r["offset"] = off
	return r
}

// mkWindow 窗口行 ts 取关窗时刻（close_reason 只有关窗时才知道）。
func mkWindow(ts time.Time, session, lineage string, opened, closed float64, prefix int64) map[string]any {
	r := commonRow("window", ts, session, lineage)
	r["opened_ts"] = opened
	r["closed_ts"] = closed
	r["dur_s"] = closed - opened
	r["prefix_tokens"] = prefix
	r["close_reason"] = closeReason
	return r
}

// mkBeat 成本一律 policy.Derive 现算，绝不手抄：与反跑端点同源同公式，价目表改了
// 演示图自动跟随。HIT 跳付保温价（预测=实际）；MISS 跳预测仍是单跳价、实际付过期
// 全价款（miss 本身就是一次全价重付，之后停跳绝不重试）。
func mkBeat(ts time.Time, session, lineage string, prefix int64, outcome string) map[string]any {
	r := commonRow("beat", ts, session, lineage)
	res := beatCost(prefix)
	cacheRead, actual := int64(0), res.Expire
	if outcome == "hit" {
		cacheRead, actual = prefix, res.PerBeat
	}
	r["provider"] = Provider
	r["model"] = BeatModel
	r["price_ver"] = PriceVer
	r["prefix_tokens"] = prefix
	r["cache_read"] = cacheRead
	r["outcome"] = outcome // T51 票04：hit 布尔改三态 hit|miss|error（+observe）
	r["cost_pred"] = res.PerBeat
	r["cost_actual"] = actual
	return r
}

func mkHandoff(ts time.Time, session, lineage string, prompt, completion int64, wallS float64) map[string]any {
	r := commonRow("handoff", ts, session, lineage)
	r["provider"] = handoffProv
	r["model"] = handoffModel
	r["price_ver"] = PriceVer
	r["prompt_tokens"] = prompt
	r["completion_tokens"] = completion
	r["outcome"] = "fresh"
	r["wall_s"] = wallS
	return r
}

func mkBlock(ts time.Time, session, lineage string, prefix int64, idleS float64) map[string]any {
	r := commonRow("block", ts, session, lineage)
	r["prefix_tokens"] = prefix
	r["idle_s"] = idleS
	return r
}

func mkInject(ts time.Time, session, lineage string, tokens int64, handoffID string) map[string]any {
	r := commonRow("inject", ts, session, lineage)
	r["tokens"] = tokens
	r["handoff_id"] = handoffID
	return r
}

// beatCost 每个前缀档现算一次（剧本用到 150k/120k/90k 三档）。Derive 的三类拒绝
// （无缓存价/无 TTL/per_beat 非正）在本包常量口径下不可达；一旦失守宁可 panic
// 也不静默写出撒谎的成本。
func beatCost(prefix int64) policy.Result {
	r, err := policy.Derive(policy.Params{
		PIn: PIn, PCache: PCache, POut: POut, Per: Per,
		PrefixTokens: float64(prefix), TTLS: TTLS, Safety: 0.8, BeatOutTokens: 300,
	})
	if err != nil {
		panic(fmt.Sprintf("demo: policy.Derive(prefix=%d) 不可达失败: %v", prefix, err))
	}
	return r
}
