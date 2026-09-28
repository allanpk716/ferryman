# 票 03 · daemon 侧：排水接线 + 关停来源落日志

## What to build
1. **排水接线**：daemon 关停路径（serve.go 的 `<-ctx.Done()` 段）把 `dockSrv.Close()` 换成 `dockSrv.Shutdown(排水ctx)`——排水上限取自配置（票 02 的键），到期由 Server 内部收尾（票 02 已实现），daemon 不重复造收尾逻辑。
2. **关停来源落日志**：三个自愿退出源在取消主 context 前各记一行日志（含来源名＋时间）：`/shutdown` 端点处理分支、托盘退出分支、Ctrl+C/中断信号分支。找到各取消源的实际代码位置（serve.go 上游的 ctx 构造与各触发点，含托盘模块）逐一加日志；外部强杀/崩溃不在覆盖内（声明式残余，日志措辞不承诺全覆盖）。

## 验收标准
- [ ] daemon 关停先 Shutdown（带上限）再 Close，超时兜底不悬挂（上限到必走完）
- [ ] 三个自愿退出源各触发一次 → 日志各有一行来源（可区分）
- [ ] 渡口未启用（cfg.Dock == nil）时关停路径不空引用、行为不变
- [ ] `go test ./internal/daemon/...` 全绿（含既有测试零回归）

## Blocked by
票 02（Server.Shutdown API 与配置键）

## 涉及路径
- internal/daemon/（serve.go＋托盘/信号相关文件＋测试）
- cmd/ferryman/main.go（**协调者实施中裁定并入**：托盘退出的真实 cancel 点在此（quit 点击→systray.Quit→stop()），不并入则只能排除法兜底——有误归因风险，违背第 5 件初衷；改动=onQuit 回调挂 NoteShutdownSource 预记缝）

## 副作用声明
测试跑 `go test ./internal/daemon/...`（票内独占）。

## decision_refs: D9, D13
## review_blocks: 无
