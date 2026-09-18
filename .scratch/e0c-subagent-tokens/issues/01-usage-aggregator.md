# 01 · usage 聚合器(解析层纯函数)

## What to build
给定一段 Claude Code 转录 jsonl(主会话或子代理转录通用),产出该文件的确定性账目:
- 四列加总(input / output / cache 写 / cache 读),按 message.id 去重;
- 响应数(唯一 message.id 数)与逐响应模型分布(行级 message.model);
- 长尾信号清单:组内非零 usage 不一致、无 message.id 的 assistant 行、坏/半行 JSON、全零占位组。

规则(spec 定死):assistant 行按 message.id 分组;**优先取带 stop_reason 的行**;组内非零 usage 冲突 → 整组入长尾不计入加总;无 id 行跳过计长尾;占位行(usage 全零/null)不贡献账目。

## 验收标准
- [ ] 同一 message.id 拆多行(thinking/tool_use 各一行、同一 usage)只计一次
- [ ] 占位行(0/null)不贡献;stop=None 行不干扰
- [ ] 组内非零冲突 → 长尾,不入账
- [ ] 无 id 行、坏 JSON 行 → 跳过并计长尾
- [ ] 模型分布按行级 model 分组(与 meta.model 无关)
- [ ] 空文件/无 assistant 行 → 全零账目 + 无长尾,不抛异常
- [ ] 合成 fixture 单测全绿(仓库 pytest 惯例)

## Blocked by
无,可立即开始
