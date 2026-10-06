# 票07 · doctor 版本上报修正

## What to build
Ferryman 的 doctor 体检项里,版本号报的是 MCP 进程自己的旧 exe 版本,误导排障;改为报 daemon(守护)的真实版本。

## 验收标准
- [ ] doctor 面 version 字段来自 daemon 端点/守护版本(经既有 daemon 通信面读取),不再取 MCP 进程自身 exe
- [ ] daemon 不可达时如实标注(daemon 不可达/版本未知),不伪造
- [ ] 其余 doctor 检查项零改动;单测覆盖:可达→报 daemon 版本;不可达→标注未知

## Blocked by
无,可立即开始。

## 涉及路径
internal/mcp/tools.go
internal/mcp/server.go
internal/installer/doctor.go
(以实际 version 上报点为准,限这三个文件内)

## 副作用声明
只跑 internal/mcp、internal/installer 包单测。

## decision_refs: D9(A5②)
## review_blocks: 无
