# 票 05 · 文档：ADR 推翻"不保活"红线＋词汇表＋DESIGN 改写

## What to build

 把设计决定落到治理文档：ADR（推翻 DESIGN.md §9 "不保活" 红线——红线原意针对无条件全局 keepalive，本功能条件化/会话级/短窗；记录 D6 闲置钟取舍与 D2 紧判据实验依据）；CONTEXT.md 补词条（心跳 beat、问询潮、等答复窗口、机器等人泳道——遵守既有词条格式与 Avoid 惯例）；DESIGN.md §9 红线改写为"不做无条件全局保活；条件化会话级保活见 ADR"；README 的功能清单一句话增补。术语与 spec/共识稿一致。

## 验收标准

- [ ] `docs/adr/NNNN-conditioned-session-keepalive.md` 落地（编号顺延既有 ADR），含背景/决策/后果三段
- [ ] CONTEXT.md 新词条与既有格式一致（含 _Avoid_ 行），且不与"闲置/摆渡/闸门"既有词条冲突（闲置词条按需补"等答复窗口不改闲置定义"一句）
- [ ] DESIGN.md §9 改写后不再与实现矛盾；v3 变更记录追加一条
- [ ] README 功能清单提及问询守望（默认关）
- [ ] 全量 pytest 绿（文档票，跑一遍确认无意外破损）

## Blocked by

票 03（词条描述的运行时行为已定型）。
