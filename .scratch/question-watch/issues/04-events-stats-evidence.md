# 票 04 · 事件、证据形态、/stats 与一键停

## What to build

 用户看得见的一层：命中/开窗/每跳/关窗全为台账事件并在时间线查看器（viewer）可显；`/stats` 增问询守望计数器（命中数/跳数/三道计数/累计花费）；复核证据形态字段（session_id＋命中时间＋unit_count＋breakdown＋转录绝对路径）随命中事件落账；一键停＝把 `mode` 置 off 的配置开关路径（不落消息内容）。

## 验收标准

- [ ] 命中/开窗/跳（含 observe）/关窗四类事件落台账事件流，viewer 时间线可显（兼容既有事件通道，零 viewer 大改）
- [ ] 命中事件含证据形态五字段；断言事件内容不含消息正文（隐私回归）
- [ ] `/stats` 返回问询守望计数器（命中/跳/HIT/MISS/ERROR/累计花费/当前 mode）
- [ ] 一键停：置 `mode=off` 后在飞计划取消、后续不再开窗（集成级验证）
- [ ] 记账金额与既有费用账本科目对齐（不另起炉灶）
- [ ] 全量 pytest 绿

## Blocked by

票 03（事件源是调度器与窗口）。

## 夜链补注（票 03 评审转来，验收必做）

- [ ] **viewer schema 同步（Important）**：beat 账目 `hit: bool` 已改三态 `outcome: hit|miss|error|observe`——Go 端 `viewer/internal/ledger/ledger.go` 的 `Hit bool json:"hit"` 字段与 `viewer/web/app.js` 的命中渲染同步改：beat 行按 outcome 三态显示（observe 显示"演练"，不计命中/失败），`viewer/internal/ledger/testdata/` 夹具补 outcome 形态样例行；`go test ./...`（viewer 目录内）绿
- [ ] **M5**：config validate 加 `beat_interval_s > 0`（≤0 拒绝）＋单测
- [ ] **M1**：`beat.py` BeatSender 协议注释补一句时限要求（真实 sender 超时+重试须压秒级，避免阻塞守望循环）
- [ ] **M3**：`_fire_one_beat` 锁内 `Path.stat()` 处补注释"有意为之：两道验原子性所需，勿顺手移出锁外"

> 补注四项已在 e0b06dc 全部落地并经票 04 双轴评审确认 ADDRESSED（2026-09-18 夜链）。

