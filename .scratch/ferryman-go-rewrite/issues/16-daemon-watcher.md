# 票 16 · daemon watcher（守望轮询 + 开窗 + 心跳调度）

**What to build**：`internal/daemon/watcher.go`：CodexWatchDirs（主目录+额外+Orca runtime 路径逐字）；Watcher（Run ctx 循环/PollOnce 测试直调；CC 侧 `**/*.jsonl` 跳 subagents→prevOpen 快照→Touch→关窗事件对照[opened/beats 取 touch 前]→usage 采集→qwatch 开窗判定→心跳调度→入队判定；Codex 跨目录 sid 去重）；入队五道推迟逐字（未观察/未达阈值/交接覆盖/等答复窗[死线例外]/子代理/悬空[死线豁免]/停车窗[死线不豁免]）；懒富化按版本；qwatch 四条件+双版本章+**双锁同序临界区**（复用票 13 范式）；心跳调度（计划 t0+i×interval、两道验+在途占用同一临界区[锁内 os.Stat 有意为之]、网络锁外、结账三态+熔断 demote/pause）；usage 喂 NoteUsage。

参照：rev1 Task 19；Python daemon.py:35-490。

**验收标准**：
- [ ] test_qwatch_window.py(32)/test_qwatch_scheduler.py(26 调度)/test_qwatch_events.py/test_qwatch_e2e.py → 四个对应 Go 测试文件全部 1:1 移植且绿
- [ ] Python `__new__` 裸构造的旧测试 = Go 直构结构体 + nil 检查（Daemon/BeatSender/Accounts 为 nil 时旧形态行为）
- [ ] 有 gcc 加 `-race`

**Blocked by**：13, 06, 07, 08, 09, 12
