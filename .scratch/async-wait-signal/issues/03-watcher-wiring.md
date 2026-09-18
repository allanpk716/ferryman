# 票 03 · 守望接线：摆渡推迟与恢复闭窗喂入

## What to build
守望线程接入停车状态机：构造器增可选 ferry_daemon 引用（默认 None，既有调用零改动）；摆渡判闲在"子代理计数在飞/悬空 tool_use"两道之后加第三道"停车未过期 → 推迟，不置 handed_off"（修 20260918 12:20/14:15 误摆渡案）；用量采集每轮把新 usage 行的最大 ts 喂给 note_usage（agent 参数用会话自身的 agent，不硬编码）。serve 启动路径与集成测试 Harness 均完成接线。
设计参照：rev1 Task 3 + 附录 #10。

## 验收标准
- [ ] 单测：停车窗期间 _maybe_enqueue 不入队；窗闭（恢复行喂入）后同一会话恢复入队
- [ ] 接线后既有集成用例全过（接线不破坏现有端到端路径）
- [ ] python -m pytest tests/ -q 全量绿后才 commit

## Blocked by
票 02
