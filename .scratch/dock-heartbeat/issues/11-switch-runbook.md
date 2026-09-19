# 11 · 渡口切换 runbook＋看门停用流程

## What to build

`docs/20260919_渡口切换_runbook.md`（新文件；操作手册，与 Go cutover runbook 同水位）：

1. **切换三步**（每步含前置检查、操作、验证、回退）：
   - 第一步·拍基线：tap 架 15723、cc-switch 上游临时指 tap、跑场景矩阵、固化 golden、**恢复 cc-switch 上游**（DB 改回）；验证＝golden 落盘＋决策门 1 过。
   - 第二步·透传版上线：config 加 `[dock]`、重启 daemon、doctor 查看门/Run 键、CC 的 base_url 指向 15722（**人工**）；验证＝dock 科目有流水＋CC 正常对话；回退＝base_url 指回 15721（一行）＋删 [dock] 节。
   - 第三步·改写版（须 L1-L3 全过）：`rewrite_enabled=true`＋upstream 指 GLM 直连＋api_key 入本机 config.toml（T39：永不入库）；验证＝差分 DIFF=0＋L2 cacheRead 一致＋L3 清单过；回退＝rewrite_enabled=false（退回透传）或整个指回 15721。
2. **看门停用流程**（F5）：停用看门任务→停服→维护/换 exe→恢复看门→doctor 复核；注明"停进程≠停服务"。
3. **已知边界记录**（F12/S2）：compaction 后最大体快照可能陈旧→首跳 miss→停窗告警＝设计内行为，处置＝复测 TTL/无视。
4. **心跳启用手册**：[wait_window]/[question_watch] 三态语义、observe 演练先行、enforce 前置（渡口开）、账本查看（beat 科目/泳道标记/无效保温）。
5. **四条决策门清单**（从验证方案文档转录）＋回退演练要求（切换日必做）。

内容事实以已落地的票 01-07 实际命令/配置键为准（派单包会附前票产出的配置键与子命令清单）。

## 验收标准

- [ ] 三步各有前置/操作/验证/回退四段，命令逐字可复制（ASCII 命令行＋中文说明分层，避免 GBK 陷阱）。
- [ ] 看门停用流程完整；心跳手册含三态与 enforce 前置。
- [ ] 文中所有配置键/命令与票 01-07 实现一致（主会话终局评审核对）。
- [ ] 纯文档票：无代码改动、无测试要求。

## Blocked by

02（看门命令）、03/04（心跳配置与语义）、06（改写配置键与守卫行为）。

## 涉及路径

- docs/20260919_渡口切换_runbook.md（新文件）

## 副作用声明

- 无独占验证命令（纯文档）。

decision_refs: D5、D10
review_blocks: 无
