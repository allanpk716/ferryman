// lineage.go — 子代理族系读取（P2-1 第四块砖：族系/判活/子代理记账的数据源）。
//
// 双向引用（dsh 源码钉死）：
//   - 子侧（权威）：子会话自头 origin:"subagent" + parentSession + delegationDepth
//     （child-agent.ts childSessionMeta；archive-admission 以子头为族系判据）；
//   - 父侧（索引）：父头 subagent/catalog 事件 childId/childCreatedAt/mode/label
//     （catalog.ts establishCatalogChild——one-shot/continuable，v1 起可 unknown）。
//
// 子代理记账的 P2-1 落点：daemon 侧按子头归父入账（CC 票01 同款语义）；
// 本函数族是给 P2-5（判活信号）与报表族系视图留的现成数据源。
package dshtrans

// CatalogChildren 全量读一个会话文件，按事件序汇总 subagent/catalog 子女引用。
// 坏帧/坏行静默跳过（防御纪律）；无子女返回 nil。写入方恒整行成帧/成批
// 追加，一轮尾读即覆盖全部已提交事件（残帧内的行属写方崩溃恢复域，非读取方消费面）。
func CatalogChildren(path string, isZstd bool) []Child {
	res := TailText(path, isZstd, 0)
	if res.Text == "" {
		return nil
	}
	_, _, children := ParseChunk(res.Text, "")
	return children
}
