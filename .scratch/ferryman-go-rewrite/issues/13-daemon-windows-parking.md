# 票 13 · daemon 窗口与停车（等待窗口状态机）

**What to build**：`internal/daemon`：daemon.go（Daemon 骨架+常量 DEGRADE_AFTER_BLOCKS=3/PENDING_TTL/HEALTH_GRACE=600/PARK_EXPIRE=3600/ACK_GRACE=90/QWATCH_MISS_SCAN；GateStats；PendingTable）+ windows.go（waitWindow 表、Subagent 事件→开窗/重锚/锁存/闭窗四因 subagents_done|prompt|main_resumed|expired、WindowWait 懒过期豁免、ParkingOpen 无副作用探测、NoteUsage 闭窗道、QWatchStop 一键停、Acct 记账封装）。**双锁同序范式样板**（windowsMu 外→ledgerMu 内，双资源临界区两把全拿；parkingOpenLocked 等无锁内方法）。Python server.py:102-593 语义+注释逐字搬运。

参照：rev1 Task 16；spec §Implementation「并发模型」「闸门状态机」。

**验收标准**：
- [ ] tests/test_subagent.py → subagent_test.go、tests/test_merge_t51_parking_mutex.py → merge_t51_parking_mutex_test.go，全部用例 1:1 移植且绿
- [ ] 并发用例（goroutine+WaitGroup 复刻 Python 线程用例）通过；有 gcc 加 `-race`
- [ ] Subagent 非法 event 经 error 通道返回 400 语义
- [ ] 锁序注释在 windows.go 顶部（含"锁内只有内存操作"纪律说明）

**Blocked by**：09, 10, 11, 04, 05

**追加验收（票 04 占位回填）**：test_accounts.py 的 4 个窗口 e2e 占位在本票转绿——test_window_books_on_subagent_cycle / test_window_closes_on_prompt / test_window_closes_on_bypass_prompt / test_window_reanchors_after_leak_gap（Go 占位见 internal/accounts/accounts_test.go 的 t.Skip，含 Python 断言要点）。
