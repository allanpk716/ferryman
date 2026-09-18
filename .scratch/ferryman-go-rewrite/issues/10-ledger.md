# 票 10 · ledger 台账移植

**What to build**：`internal/ledger`（ledger.py 1:1）：SessionState 全字段（qwatch 窗口四件：OpenedTS *float64/BeatsFired/Plan []float64/Snapshot *QSnap；EnrichedWrite 初值 -1）；Touch 语义逐字（lineage 同 path 换 sid 继承闲置史四字段、size=0 不覆盖、mtime>last_write 才推进+observed_active+**清 qwatch 窗**）；子代理计数（start/stop、stop 下限 0、1h 泄漏防护）；`Mu()` 暴露 + `SubagentActiveLocked` 无锁内方法（daemon 临界区用）。**共享可变引用语义：读写均须持锁**（注释明写）。

参照：rev1 Task 12；spec §Implementation「并发模型」。

**验收标准**：
- [ ] tests/test_ledger.py 全部用例 1:1 移植且绿（lineage/泄漏/qwatch 清窗/Touch 覆盖分支）
- [ ] 公共方法自带锁；无锁内方法仅供持锁临界区
- [ ] 并发读写测试（goroutine+WaitGroup）不 race（有 gcc 时 -race 验证）

**Blocked by**：02
