// Package prices 移植价格表：~/ferryman/config.toml [prices.*] → 版本化单价
// （设计 §1.1，Q1/Q16）。规格：ferryman/prices.py（1:1）。
//
// - 每条流水钉死记账时的 price_tag（"key@effective_from"），改价不重算旧账；
// - p_cache 允许缺省（nil = 无缓存经济：策略计算器拒算，report 标注不可算）。
package prices

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/BurntSushi/toml"
)

// PriceVersion 单价版本。
type PriceVersion struct {
	EffectiveFrom string   // "YYYY-MM-DD"，生效日 UTC 零点起
	PIn           float64  // 输入单价
	PCache        *float64 // nil = 未公布/无缓存价
	POut          float64  // 输出单价
}

// PriceBook 一份价目表，对应 config.toml 的 [prices.<key>] 节。
type PriceBook struct {
	Key      string         // 对应 [prices.<key>]
	Unit     string         // 自述单位（如 "智谱积分"）
	Per      int            // 每 N token 一个计价块（万=10000，M=1000000）
	Versions []PriceVersion // effective_from 升序
}

// At 取最后一个生效日 ≤ ts 的版本；早于一切版本 → nil。
// 与 Python 逐字对应：升序遍历，命中即覆盖 best。
// （偏离规格：effective_from 解析失败的版本按"未生效"跳过——Python 的 strptime
// 会抛异常炸调用方，Go 侧容忍脏配置不 panic。）
func (b *PriceBook) At(ts float64) *PriceVersion {
	var best *PriceVersion
	for i := range b.Versions { // 升序，取最后一个 effective_from ≤ ts
		v := &b.Versions[i]
		day, err := time.Parse("2006-01-02", v.EffectiveFrom) // UTC 零点
		if err != nil {
			continue
		}
		if float64(day.Unix()) <= ts {
			best = v
		}
	}
	return best
}

// PriceTag 记账钉死的价格标签："key@YYYY-MM-DD"。
func PriceTag(bookKey string, pv PriceVersion) string {
	return bookKey + "@" + pv.EffectiveFrom
}

// LoadPrices 读 [prices.*]；无文件/无节 → 空 map。path 为空 → ~/ferryman/config.toml。
// （偏离规格：TOML 解析失败按空表处理——签名不带 error，坏配置不炸常驻进程。）
func LoadPrices(path string) map[string]PriceBook {
	p := path
	if p == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return map[string]PriceBook{}
		}
		p = filepath.Join(home, "ferryman", "config.toml")
	}
	raw, err := os.ReadFile(p)
	if err != nil { // 无文件（Python 的 not p.exists()）→ 空 dict
		return map[string]PriceBook{}
	}
	var data map[string]any
	if _, err := toml.Decode(string(raw), &data); err != nil {
		return map[string]PriceBook{}
	}
	books := map[string]PriceBook{}
	for key, blkAny := range orEmpty(data["prices"]) { // (data.get("prices") or {}).items()
		blk := orEmpty(blkAny)
		var vers []PriceVersion
		for _, vbAny := range orSlice(blk["versions"]) { // blk.get("versions", [])
			vb := orEmpty(vbAny)
			// 票03评审M1：p_in/p_out 缺失或非数值的版本跳过并 stderr 告警——
			// asFloat 的 0 兜底会产出零价行，带着合法 price_tag 记账（假账）。
			if !isNumeric(vb["p_in"]) || !isNumeric(vb["p_out"]) {
				fmt.Fprintf(os.Stderr, "[prices] 版本缺必填键，跳过: %s@%s\n",
					key, asString(vb["effective_from"]))
				continue
			}
			vers = append(vers, PriceVersion{
				EffectiveFrom: asString(vb["effective_from"]),
				PIn:           asFloat(vb["p_in"]),
				PCache:        asPCache(vb["p_cache"]), // 键缺失 → nil（TOML 无 null）
				POut:          asFloat(vb["p_out"]),
			})
		}
		sort.SliceStable(vers, func(i, j int) bool { // vers.sort(key=effective_from)
			return vers[i].EffectiveFrom < vers[j].EffectiveFrom
		})
		books[key] = PriceBook{
			Key:      key,
			Unit:     asString(blk["unit"]),    // blk.get("unit", "")
			Per:      asInt(blk["per"], 10000), // blk.get("per", 10_000)
			Versions: vers,
		}
	}
	return books
}

// orEmpty 对应 Python 的 (x or {})：缺失/非表 → 空表。
func orEmpty(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

// orSlice 对应 Python 的 .get("versions", [])：缺失/非数组 → 空。
func orSlice(v any) []map[string]any {
	switch s := v.(type) {
	case []map[string]any:
		return s
	case []any:
		out := make([]map[string]any, 0, len(s))
		for _, e := range s {
			if m, ok := e.(map[string]any); ok {
				out = append(out, m)
			}
		}
		return out
	}
	return nil
}

// asString 对应 Python 的 str(...)：字符串原样；TOML 裸日期（time.Time）取 "YYYY-MM-DD"。
func asString(v any) string {
	switch s := v.(type) {
	case string:
		return s
	case time.Time:
		return s.Format("2006-01-02")
	case fmt.Stringer:
		return s.String()
	}
	return ""
}

// asFloat 对应 Python 的 float(...)：TOML 整数/浮点皆可；缺失/非数 → 0。
func asFloat(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case int64:
		return float64(n)
	case int:
		return float64(n)
	}
	return 0
}

// isNumeric 数值键判定（票03评审M1）：TOML 整数/浮点皆可；缺失/串/布尔皆非数值。
func isNumeric(v any) bool {
	switch v.(type) {
	case float64, int64, int:
		return true
	}
	return false
}

// asPCache 对应 Python 的 float(vb["p_cache"]) if vb.get("p_cache") is not None else None：
// 键缺失（TOML 无 null）或非数值 → nil；0 也是合法价，照收。
func asPCache(v any) *float64 {
	switch n := v.(type) {
	case float64:
		return &n
	case int64:
		f := float64(n)
		return &f
	case int:
		f := float64(n)
		return &f
	}
	return nil
}

// asInt 对应 Python 的 int(...)，def 走第二参。
func asInt(v any, def int) int {
	switch n := v.(type) {
	case int64:
		return int(n)
	case int:
		return n
	case float64:
		return int(n)
	}
	return def
}
