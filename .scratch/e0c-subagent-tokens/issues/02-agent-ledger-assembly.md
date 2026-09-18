# 02 · 子代理账目装配器

## What to build
扫描会话目录 `<会话id>/subagents/`,装配每个子代理的账目行:
{agentId, agentType, 描述, spawnDepth, self 四列(经 01 聚合器), 行级模型分布, 首末时间戳, 终态推断}。

规则:
- meta 与转录配对;不成对:有转录无 meta → token 照入总账、agentType=未知、depth=未知桶(不参与 direct/total 分桶与 subtree;toolUseId 能从转录内容恢复则恢复父子);有 meta 无转录 → 计数+1、token 记 0、标"无转录"。
- 计数基准 = spawn 事件数(转录文件数 + 有 meta 无转录数);转录文件数另列。
- 终态推断:最后响应 stop=end_turn → 完成;空文件 → 空;其余 → 中断;mtime 距扫描 <5 分钟 → 在跑(非终值)。

## 验收标准
- [ ] 正常配对样本四列正确
- [ ] 缺 meta → 未知桶规则正确,toolUseId 可恢复时父子关系恢复
- [ ] 有 meta 无转录 → 计数+1、token 0、标"无转录"
- [ ] direct/total 双口径计数正确(含 depth=2 样本)
- [ ] 终态四分类(完成/中断/空/在跑)推断正确
- [ ] 合成会话目录 fixture 单测全绿

## Blocked by
01
