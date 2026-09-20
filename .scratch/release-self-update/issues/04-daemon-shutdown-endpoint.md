# 票 04 · daemon 内部停机端点 /shutdown

**What to build**:
daemon HTTP 面新增 `POST /shutdown`:loopback-only、`daemon.token` 鉴权(沿用既有 token 机制)、触发优雅停机(与 os.Interrupt 同一 ctx 取消路径,面板与守护一起收)。该端点属守护内部管理面,**不进入 agent 面 MCP 动词面**(internal/mcp 零改动)。

**验收标准**:
- [ ] 正确 token + loopback → 守护优雅退出(监听口释放)
- [ ] 错 token / 无 token / GET 方法 → 401/405,守护不受影响
- [ ] 端点拒绝非 loopback 来源(测试伪造 RemoteAddr)
- [ ] `internal/mcp` 禁词/只读红线测试零回归(动词面无新增)
- [ ] `go test ./internal/daemon/` 绿

**Blocked by**: 无,可立即开始
**涉及路径**: internal/daemon/(serve/router 及测试)
**副作用声明**: 独占验证命令 `go test ./internal/daemon/ -count=1`(该包既有慢测试,别票避开)
**decision_refs**: D7
**review_blocks**: 无
