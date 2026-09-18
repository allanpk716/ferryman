# 04 · CLI 子命令 + 报告生成

## What to build
`ferryman e0c` 子命令(注册进 __main__,沿用 e0/e0b 惯例):扫描 ~/.claude/projects 全部会话,渲染中文报告到 `reports/e0c-cc-subagent-tokens.md`,结构:
- Q1 逐会话明细账(主会话行 + 各子代理行:self/subtree、类型、深度、模型、终态)
- Q2 占比与分布(占总账比例、按类型/深度/模型;**固定回传耦合注记**:结果回传以 input/cache_read 再计入主会话,占比结构性偏低,只作方向参考)
- Q3 对账(读对账手记 jsonl:{ts, agentId, agentType, self_reported_tokens, source},按 agentId 配对,残差逐个列出;样本不足如实报告)
- Q4 长尾清单(中断/空/在跑/不成对/无 id/组内冲突/扫描窗口外,分布与占比)
- 定型建议(附判据,不附决定)
对账手记文件路径可由参数传入;无手记文件时 Q3 节写"无样本"。

## 验收标准
- [ ] fixture 会话树端到端跑通,报告生成且关键数字与单测一致
- [ ] 四节结构齐全,回传耦合注记固定出现
- [ ] 无对账手记时 Q3 明示"无样本"不崩
- [ ] 真实 ~/.claude 冒烟跑通(允许小样本,--limit 类参数或环境变量控制)
- [ ] 端到端测试绿

## Blocked by
03
