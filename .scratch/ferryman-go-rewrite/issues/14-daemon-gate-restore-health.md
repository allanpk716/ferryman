# 票 14 · daemon 闸门状态机 / 归还 / 健康

**What to build**：`internal/daemon`：gate.go（七分支顺序逐字：强续/!! bypass→闭未停车窗、no-ledger 放行、mode off、machine-waiting 三道豁免[文案逐字]、observe 只警告[will_block=False 文案]、enforce 分支 5/6/7[block reason 全文逐字、连续 3 次降级]、pending 三清除、_warn_ctx 2000 截断、_cache_info 纯提醒）+ restore.go（多候选只列前 5、单候选 INJECT 层[标记缺失截 1800]、待续 prompt 拼接、ctx 截 6000、inject 记账 lineage 按源会话）+ Health（/stats 字段名逐字、qwatch 节、miss_signals 现算、health_alert 启动宽限）。

参照：rev1 Task 17；Python server.py:135-500。

**验收标准**：
- [ ] tests/test_gate.py 全部 43 例 → gate_test.go 1:1 移植且绿（clock 注入+直构 Daemon）
- [ ] 全部中文文案与 Python 版 diff 为空（block reason/警告/信息条逐字）
- [ ] restore 相关用例（散在 test_gate.py）→ restore_test.go 且绿

**追加验收（票 12 占位回填）**：test_notify.py 的 test_gate_block_fires_notification_async（gate block → 异步 notify_block 链路）在本票转绿（Go 占位见 internal/notify/notify_test.go 的 t.Skip）。

**Blocked by**：13

**追加验收（票 04 占位回填）**：test_accounts.py 的 3 个闸门/归还 e2e 占位在本票转绿——test_block_books_entry / test_bypass_books_entry / test_restore_books_inject（Go 占位见 internal/accounts/accounts_test.go 的 t.Skip，含 Python 断言要点）。
