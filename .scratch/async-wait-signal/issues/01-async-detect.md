# 票 01 · 转录尾部异步派发判定

## What to build
新增一个纯函数，读 CC 会话转录尾部（256KB 滑窗），判定"最后一个 Task/Agent 派发是否为异步/后台启动"：input 的 run_in_background/background 为真，或其 tool_result 首个文本块以 "Async agent launched" 为前缀（严格前缀匹配，不是子串）。判不中/坏行/文件缺失一律 False。message 字段非 dict 的行安全跳过（不得抛 AttributeError）。该函数是后续停车状态机的判定底座。
设计参照：docs/superpowers/plans/2026-09-18-t48-async-wait-signal.rev1.md Task 1 + 文末评审附录 #6/#11。

## 验收标准
- [ ] 新函数通过全部新增单测：input 标志位命中 / result 前缀命中 / 同步派发 False / 旧 async 后新 sync → False（last-dispatch-wins）/ 同步 result 文本中段复读 "Async agent launched" → False（前缀匹配负例）/ 文件缺失 False / message 为字符串或列表的坏行 False（不抛）
- [ ] python -m pytest tests/ -q 全量绿后才 commit（含本票新用例）

## Blocked by
无，可立即开始
