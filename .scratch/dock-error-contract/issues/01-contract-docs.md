# 票 01 · 契约成文：CONTEXT.md 两条术语 + ADR-0017

## What to build
把已评审定稿的「渡口错误契约」成文：CONTEXT.md 词汇表新增两条术语（响应保真、渡口自产错误），并新写 ADR-0017 记录契约决策。ADR 要点：为什么响应侧零改写（CC 重试认标准形状，翻译反而制造 CC 不认识的形状——cc-switch v3.20.4 源码级对照）；为什么自产错误必须伪装成 CC 已认识的标准形状且按流状态分流（首字节未写出→HTTP 502/504＋Anthropic 错误体＋Retry-After；流已建立→先合成 SSE `event: error` 再断连）；三条声明式残余边界（上游流中段自断＝不抢救＋truncated 记账；非流式排水到期 200 已写＝断连＋尽力记账；关停来源只覆盖自愿退出）；对「整流 no-op」旧决定的维持说明（不移植换家/熔断）。术语条目遵守 CONTEXT.md 既有格式（定义＋_Avoid_ 行），「改写五件」条目补一句出向纪律的呼应（响应保真管辖出向）。

## 验收标准
- [ ] CONTEXT.md 新增「响应保真（response fidelity）」条目：定义＋_Avoid_（含「错误翻译」）
- [ ] CONTEXT.md 新增「渡口自产错误（dock-originated error）」条目：定义（含两种投递形态分流）＋_Avoid_
- [ ] CONTEXT.md「改写五件」条目补出向呼应一句，不改动原义
- [ ] docs/adr/0017-*.md 落盘：Status/Context/Decision/Consequences 结构（对齐仓内既有 ADR 格式），含三条残余边界与整流 no-op 维持说明
- [ ] 全部术语用法与 CONTEXT.md 既有词汇一致（渡口/上游/排水等不新造同义词）

## Blocked by
无，可立即开始

## 涉及路径
- CONTEXT.md
- docs/adr/0017-dock-error-contract.md（新建）

## 副作用声明
无独占验证命令；只跑文档无测试。不修改本票路径外任何文件。

## decision_refs: D2, D3, D6
## review_blocks: 无
