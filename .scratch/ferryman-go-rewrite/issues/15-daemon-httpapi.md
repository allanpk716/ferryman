# 票 15 · daemon httpapi（5 端点 HTTP 面）

**What to build**：`internal/daemon/httpapi.go`：EnsureToken（daemon.token 64hex、0600 尽力）、AlreadyRunning（GET /stats+Bearer 2s）、DaemonLike 接口、ListenAndServe（POST /gate、/subagent、/qwatch_stop；GET /stats、/restore；Bearer 不符 401 `{"error":"unauthorized"}`；未知 404；JSON 坏 400 `bad request: …`；/restore query 缺省 agent=cc；Content-Type `application/json; charset=utf-8`；访问日志静默；只绑 127.0.0.1；绑定排他=Windows 双绑必败）。

参照：rev1 Task 18；Python server.py:676-781。

**验收标准**：
- [ ] tests/test_singleton.py → singleton_test.go 全部用例 1:1 移植且绿（真监听临时端口、401/404/400/200 全路径）
- [ ] 同端口二次 Listen 失败（排他验证）
- [ ] handler 读光 body 再回话（401/404 路径不留未读数据）

**Blocked by**：14
