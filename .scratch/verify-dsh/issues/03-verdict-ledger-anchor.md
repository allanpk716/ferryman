# 票 03 · 判定流水与契约锚存储包 internal/dshledger

## What to build

新包 `internal/dshledger/`（daemon 数据目录下的持久层，与台账同区、不入 config.toml）：

1. **判定流水**（append-only）：每行 = DSH 版本＋插件版本＋daemon 版本三元组、判定（green/yellow/red）、日期时间、契约锚哈希、可选 installer 路径。提供 Append / List / RecentKnownGood（最近 green 三元组指针）。
2. **契约锚存储**：锚 = 四类契约面（发现面/事件面/鸭子面/浏览器半面）的形状签名快照（JSON），绑 DSH 版本；全绿写入、滚动保留最近 10 份；提供 Write / Latest / DiffFaces（旧锚 vs 新采集形状，返回漂移面清单——仅用于诊断报告，不影响执行范围）。
3. daemon 版本字段由调用方传入（票 04 取生产 daemon 自报版本，本包不猜）。

文件格式从简（JSONL 流水 + 锚目录），并发写由单一 daemon/CLI 进程串行调用保证。

## 验收标准
- [ ] Append/List/RecentKnownGood 往返正确；无 green 时 RecentKnownGood 返回空
- [ ] 锚滚动：写入第 11 份时最旧被淘汰（N=10 可配）
- [ ] DiffFaces：构造漂移（改一个 manifest 字段）返回对应面；无漂移返回空
- [ ] `go test ./internal/dshledger/` 绿

## Blocked by
无，可立即开始

## 涉及路径
- internal/dshledger/ledger.go（新）
- internal/dshledger/anchor.go（新）
- internal/dshledger/ledger_test.go（新）

## 副作用声明
仅单测（临时目录）

decision_refs: D5（全绿自动滚动＋历史；diff 仅诊断）、D7（三元组＋可选 installer；不声称范围）、D11
review_blocks: 无
