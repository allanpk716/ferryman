# 票 12 · harvest 用量采集 + notify 通知

**What to build**：`internal/harvest`（harvest.py 1:1）：ParseUsageChunk（ai-title/assistant usage 两类出行、坏行跳过、宽松取整）、HarvestState（偏移/标题/项目从账本恢复；残行留待下轮；size<offset 从头重采；同 MsgID 只记首发；偏移先推进后返回 at-most-once）。`internal/notify`（notify.py 1:1）：SendPushover（URL 包级 var 供 httptest）、SendToast（powershell WinRT，命令执行提为包级 var 供 mock）、NotifyAlert/NotifyBlock（文案逐字、enabled=false 静默、凭据回落环境变量、绝不抛出）。

参照：rev1 Task 14/15。

**验收标准**：
- [ ] tests/test_harvest.py 全部用例 1:1 移植且绿
- [ ] tests/test_notify.py 全部用例 1:1 移植且绿（httptest 假端点 + mock 命令执行）
- [ ] 通知任何故障只返回 false/记日志，不影响调用方

**Blocked by**：04, 05
