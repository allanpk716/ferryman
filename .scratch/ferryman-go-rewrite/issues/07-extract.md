# 票 07 · extract（L0 确定性骨架提取）

**What to build**：`internal/extract`（extract.py 1:1）：Facts/Item/SkeletonText（中文骨架文案逐字）/TokenEstimate（CJK 码点范围 `[\x{3000}-\x{9FFF}\x{FF31}-\x{FFEF}]` ×1 + 其余码点/3.5 + 1，**按码点计数**）/截断全按码点（Item 4000/命令 160/FreezeUser 500/FreezeAs 1500 + 尾标逐字）/末段定格三档互斥（choice>工具摘要>逐字）/AskUserQuestion 多问存第一问/files 排序 (-count,path)/命令去重保序/ai-title 回落/MaterialText/ChunkItems（每条+8 token）。行读无上限。

参照：rev1 Task 8。

**验收标准**：
- [ ] tests/test_extract.py 全部用例 1:1 移植且绿（骨架文案断言逐字照抄为 Go 原始字符串）
- [ ] 中文长文本 TokenEstimate 与 Python 实测一致（抽 3 例对照）
- [ ] RuneTrunc 用于全部截断点，无字节截断

**Blocked by**：06
