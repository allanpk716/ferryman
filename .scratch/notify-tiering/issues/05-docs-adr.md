# 票05 · 文档:CONTEXT.md 词条修订 + ADR-0026

## What to build
把通知分级的决策与术语落进项目文档:CONTEXT.md「摆渡路由」词条的降级告警句改为状态变化制、新增「通知分级」词条;新增 ADR-0026 记录行为修订依据;ADR 索引更新。

## 验收标准
- [ ] CONTEXT.md「摆渡路由」词条:「每次滑落推一条 Pushover/Toast 告警」句改为顺位级状态变化制描述(下移推/同级不推/恢复静默重置),骨架进程一次语义句保留
- [ ] CONTEXT.md 新增「通知分级」词条:off/toast/both 三值、九事件缺省保留组、未配置回落缺省、notify.events 配置组(内联表键,禁子表头);词条风格与全文一致(含 _Avoid_ 行)
- [ ] docs/adr/0026-notify-tiering.md:问题(噪音实锤 8 天 120~160 条、两大户滑落+拦截占八成)、决策(事件三值开关/缺省表/状态变化制/等值省略协议)、T25 block 行为修订依据(ADR-0023 对话区反馈后手机推送冗余)、取舍(重启重推、显式配值=缺省被省略随缺省演进)、取代关系(修订 ADR-0024 摆渡路由词条的告警句;不取代 T25 通道实现)
- [ ] docs/adr/README.md 索引补 0026 行
- [ ] 术语与 CONTEXT.md 词典一致;不引入新黑话

## Blocked by
无,可立即开始(决策材料齐:decisions D1~D8、FINDINGS、账本数据)

## 涉及路径
- CONTEXT.md
- docs/adr/0026-notify-tiering.md
- docs/adr/README.md

## 副作用声明
无;纯文档,默认不跑测试

## decision_refs
D2、D3、D7、D8
review_blocks: 无
