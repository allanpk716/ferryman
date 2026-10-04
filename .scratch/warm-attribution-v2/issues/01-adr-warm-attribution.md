# 01 · ADR-0020 保温归因 v2 语义

## What to build

撰写 docs/adr/0020-warm-attribution-savings-v2.md，把保温归因 v2 的不可逆语义决定记档：归因双轴四象限定义（need∧hit 才计兑现节省；朴素命中判据为何被否）、结算单元=保温回合、报表时 join 而非事件时结算、v1 冻结并存、记账合同与口径全文（数据源分工／配对合同／回合切分／前缀口径／跨月归属）。格式对齐 docs/adr/ 既有篇目（看 0016/0019 的结构）。

内容来源＝规格 .scratch/warm-attribution-v2/spec.md 的「Implementation Decisions」与 Further Notes，不改语义只做行文。术语遵守根 CONTEXT.md。歧义分支成本处置标注为"待定（活动约束）"，不写成已定。

## 验收标准

- [ ] 文件存在于 docs/adr/0020-warm-attribution-savings-v2.md，结构对齐既有 ADR
- [ ] 四象限、回合、join、v1 冻结四点各有"为何"而不只是"是什么"
- [ ] 记账合同五要素齐全且与 spec 逐字一致（数据源/配对/切分/前缀/跨月）
- [ ] 歧义分支成本处置明确标注待定；冷重放现象与 TTL sanity-check 计划入 Further Notes

## Blocked by

无，可立即开始

## 涉及路径

docs/adr/0020-warm-attribution-savings-v2.md（新建）

## 副作用声明

无（纯文档；不跑测试）

decision_refs: D2, D3, D4, D8, D9
review_blocks: 无
