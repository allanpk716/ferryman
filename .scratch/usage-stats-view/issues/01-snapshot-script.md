# 票01 · 快照脚本:真实账本 → stats.data.js

## What to build
端到端行为:运行脚本后,`widget/ui/mock/stats/stats.data.js` 生成,内容为 mock 页直接可加载的真实数据快照——全时段 KPI(五卡含可算性标志)、逐日序列(Tokens/请求/成本/节省额四档所需)、明细抽样(字段最小化)、不可算态与负值日标记。口径严格遵守 spec「聚合数据契约」与「成本口径」节。

## 验收标准
- [ ] `python widget/ui/mock/stats/gen_snapshot.py` 可运行(账本与 config 路径用缺省 C:/Users/allan716/ferryman/...,支持 --accounts/--config 覆盖),零网络、只读账本
- [ ] 产出 stats.data.js(JSONP 形如 `window.STATS_DATA = {...};` 或纯 JSON+加载器约定,票02 消费)
- [ ] KPI:requests=全账本 usage 行数;四列 token 合计;命中率=cache_read/(cache_read+input);成本/节省额可算性判定与 daemon 同法(config [prices.*] books + FerryProvider 解析 econ 表;econ=local 无表→computable=false+note 原文)
- [ ] 对账锚点(内置断言,运行时校验,不符即非零退出):2026-09 五类事件计数 block/bypass/inject/handoff/window=10/2/50/778/760;2026-10=67/15/63/442/216
- [ ] 逐日序列覆盖 2026-07-30(数据起点)至今天;每日含四列 token 合计、请求数、五类事件计数;净节省<0 的日子有标记位
- [ ] handoff 金额按行 price_ver(12 行可价;十月合计 33.46 积分计入对应日);不可价入 unpriced 计数
- [ ] 明细抽样:最近 N=200 条 usage 行,字段=ts_iso/project/session_id(短截断)/model/四列 token/cost(可算时);**无 title 字段**
- [ ] 文件头注释注明生成时间与源账本路径

## Blocked by
无,可立即开始

## 涉及路径
- widget/ui/mock/stats/gen_snapshot.py(新增)
- widget/ui/mock/stats/stats.data.js(生成产物,一并提交)

## 副作用声明
无(纯本地只读+产物写入选定路径;不跑测试套件)

decision_refs: D2, D3, D6, D9
review_blocks: 无(F1/F2/F5 已解除,本票即其解除条件的落地)
