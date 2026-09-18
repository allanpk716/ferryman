# 票 17 · daemon worker + serve 装配

**What to build**：`internal/daemon`：**Provider 最小占位类型定义在 daemon 包**（评审附录#2：票 18 之前 ferryFunc/NewWorker 需可编译——daemon.Provider{Name,BaseURL,Model,APIKey,Window}）；worker.go（tasks chan cap 10、select-default 满延迟、每任务 ferryFunc 调用+480s ctx 墙钟、失败/超时→骨架降级[骨架头尾文案逐字]+failed 记账[骨架保存后]、成功→fresh 保存+记账[price_ver 按流水时刻取版本、usage 失败记 0]）；serve.go（=python serve()：配置→token→Ledger/Store/Accounts→队列→Daemon→监听[绑定失败分流 already_running]→pid 文件→Watcher/Worker→横幅逐字→优雅停）。

参照：rev1 Task 20。

**验收标准**：
- [ ] tests/test_integration.py + tests/test_big_session.py → 两个 Go 测试文件全部 1:1 移植且绿（恒败替身走骨架路径）
- [ ] **成功路径装配测试**（附录#11 补强）：恒成功 ferryFunc（httptest 假 provider 形状）跑一单——fresh 交接落盘、账本 handoff 行 outcome=fresh 字段齐全、ValidHandoff 命中
- [ ] serve 横幅中文逐字；pid JSON {pid,port,started_at}

**Blocked by**：15, 16, 11
