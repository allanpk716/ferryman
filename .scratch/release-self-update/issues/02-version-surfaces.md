# 票 02 · 版本可见四件:doctor / MCP doctor / /stats / 面板页脚

**What to build**:
把版本号暴露到四个已有查询面:`ferryman doctor` 结论清单加一行版本;agent 面 MCP doctor 工具响应加 `version` 字段;daemon `/stats` JSON 加 `version`;web 面板页脚一行显示版本(前端从既有 API 取)。

**验收标准**:
- [ ] doctor 输出含版本行(dev 构建显 dev)
- [ ] MCP doctor 响应 JSON 含 `version` 字段;`internal/mcp` 全部既有测试(含禁词/只读红线)零回归、零动词面变化
- [ ] `/stats` 响应含 `version`;既有 /stats 消费方(看门探活、面板)不受影响
- [ ] 面板页脚显示版本号;前端遵守既有纪律(账本数据只经 createElement/textContent 进 DOM)
- [ ] `go test ./internal/installer/ ./internal/mcp/ ./internal/daemon/ ./cmd/ferryman/` 绿

**Blocked by**: 01(版本变量)
**涉及路径**: internal/installer/doctor.go, internal/installer/doctor_test.go, internal/mcp/, internal/daemon/, cmd/ferryman/web/
**副作用声明**: 无(单包测试)
**decision_refs**: D5, D7
**review_blocks**: 无
