# 票 01 · 检测器：紧判据提问潮判定

## What to build

 transcripts 尾部 → 提问潮判定的端到端能力：给定转录文件路径，程序能回答"末条 assistant 消息是否提问潮、问题单元数多少、判据构成如何、末尾悬空 tool_use 是否仅由 AskUserQuestion 构成"。纯函数、零 IO 副作用之外的新依赖，不碰守望/台账主路径。判据按 spec 决策 1（紧档：标记行/问号行/编号行须含问号或疑问词；先剥代码块）；隐私不变量：返回值只有计数与布尔，永不携带消息内容。

## 验收标准

- [ ] `detect(path) -> Verdict{is_surge, unit_count, breakdown, askuserquestion_dangling}` 纯函数落地
- [ ] 已知正例 fixture（真实提问潮消息：`❓ **Qn**` 块形态，units≥5）判定 is_surge=True
- [ ] 反例 fixture（状态汇报/票台账/步骤清单：编号行无问号无疑问词）判定 False——round-0 实验 153 条误报的形态缩影
- [ ] 代码块内的问号/编号不计数（剥块）
- [ ] 一次一问消息（1 单元）不触发
- [ ] 悬空 tool_use 集合 ⊆ {AskUserQuestion} 时 askuserquestion_dangling=True 且不影响 is_surge；含其他工具则如实报告
- [ ] 单测覆盖以上全部 + 坏行/空文件安全跳过
- [ ] 全量 pytest 绿（rebase 后基线 204 用例）

## Blocked by

无，可立即开始。
