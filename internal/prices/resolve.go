// resolve.go — 价格本定位与现价版本解析（Go 侧装配缝；prices.py 无对应件，
// 与 LoadPrices 的容错偏离同属 Go 侧补件）。
//
// 口径与 report.bookFor / config.bookFor / backtest.resolveEconBook 逐字一致：
// key 精确命中 → 取之；仅一本 → 取唯一本；再否则不可定位（返回 nil，调用方
// 拒算，不猜）。新代码（同模型阈值计算器等）一律走本出口；report/config/
// backtest 的三处本地副本属历史遗留，收口另立票，不在本件混改。
package prices

// BookFor 按键定位价格本：精确命中取之；未命中且仅一本时回退该本；
// 多本无映射或空表 → nil。
func BookFor(books map[string]PriceBook, key string) *PriceBook {
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

// BookVersionAt 现价版本解析：最后一个生效日 ≤ ts 的版本；早于一切版本 →
// 回落末版（watcher/report/doctor 同口径——价格表只登记已发生的改价，
// "还没有任何版本生效"按最新已知价对待，不视为不可算）。无版本 → nil。
func BookVersionAt(b *PriceBook, ts float64) *PriceVersion {
	if b == nil || len(b.Versions) == 0 {
		return nil
	}
	if pv := b.At(ts); pv != nil {
		return pv
	}
	return &b.Versions[len(b.Versions)-1]
}
