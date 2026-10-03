# 票14 · internal/ 全量 7311 过期注释清理(票07 移交)

> 来源:票07 泳道报备——票07 验收项"grep internal/ 无 7311 残留"在票07 路径授权下不可达(internal/ 另有 21 个文件含 7311);本票承接清理。

## What to build
把 internal/ 里"把生产控制口描述成 7311"的过期注释改正为 15700(**仅注释/文档,不改任何代码行为、不改测试夹具端口值**)。票07 泳道已逐处盘点,分两类:

**要改(过期描述,约 13 处)**:
- internal/installer/autostart.go:9
- internal/installer/watchdog.go:24-25
- internal/installer/doctor.go:995
- internal/viewer/server/config_tuning.go:5
- internal/daemon/dock_retry.go:6
- internal/daemon/serve.go:226、229
- internal/daemon/test_hooks_test.go:8-9
- internal/daemon/query_widget_test.go:379
- internal/update/standin_test.go:4、194
- internal/update/supervisor.go:442
- internal/cutover/smoke.go:6
- internal/cutover/smoke_test.go:2
- internal/mcp/mcp_test.go:10、45
- internal/dock/SMOKE.md(文档)

**不改(刻意保留的历史注记/夹具端口,允许清单)**:
- internal/installer/watchdog.go:56(历史"旧数字"注记)
- internal/update/supervisor.go:30(端口迁移史注记"2026-09-29 由 7311 改 15700"——保留)
- internal/installer/doctor_test.go:1173、1280(历史钉子)
- 各测试文件里的 7311 若是**夹具端口号**(测试自起监听用的值)而非"生产口描述"→不改
- docs/DESIGN.md §3:24 的 notify.py 历史文件名:读上下文,若描述的是**现行运行时**则改正,若叙述历史则保留并加"(历史)"注记

判定不确定的命中:逐处读上下文,凡"描述生产控制口是什么"的改成 15700,凡"记着当年是 7311"的保留。

## 验收标准
- [ ] 上述"要改"清单逐处改正;`grep -rn 7311 internal/` 剩余命中全部属于允许清单(历史钉子/迁移史注记/夹具端口),并在回报中列出剩余命中及归类
- [ ] 纯注释/文档改动:git diff 无代码行变更
- [ ] go build ./... 过;go test ./internal/installer/ ./internal/update/ ./internal/cutover/ ./internal/mcp/ 绿

## Blocked by
无,可立即开始

## 涉及路径
internal/installer/autostart.go
internal/installer/watchdog.go
internal/installer/doctor.go
internal/viewer/server/config_tuning.go
internal/daemon/dock_retry.go
internal/daemon/serve.go
internal/daemon/test_hooks_test.go
internal/daemon/query_widget_test.go
internal/update/standin_test.go
internal/update/supervisor.go
internal/cutover/smoke.go
internal/cutover/smoke_test.go
internal/mcp/mcp_test.go
internal/dock/SMOKE.md
docs/DESIGN.md(仅 §3:24 一处上下文判定)

## 副作用声明
go build ./... 与上列包的 go test(允许)

## decision_refs
D10

## review_blocks
无
