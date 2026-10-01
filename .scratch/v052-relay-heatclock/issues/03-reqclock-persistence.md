# 票 03 · 判热时钟持久化（重启回种，修同模型摆渡重启失忆）

## What to build
判热时钟（beat.LastRequestClock）增加快照持久化：Note 推进时把 sid→ts 快照
落盘到 dataDir（reqclock.json），daemon 装配 watcher 时读快照回种。守护重启
后钟不归零——重启观察窗的判热门按真实钟值判，闲置 20-24 分钟、缓存仍活的
会话不再被整批误判冷。快照写入用临时文件+替换（防半写）；写失败静默（丢
快照=下次重启保守判冷，方向安全）；sid 键按 ts 淘汰或容量上限（防无界增长）。
不做渡口记账喂钟（D3 已替代）。

## 验收标准
- [ ] 持久化：Note 推进时快照落盘（临时文件+替换写）；写失败静默不影响 Note 主路径
- [ ] 回种：装配 watcher 时读快照；文件缺失/损坏=空钟起步（fail-safe，现状语义）
- [ ] 回种值与后续 Note 取 max（单调语义不变）；空 sid 不入不变
- [ ] 容量/淘汰：sid 键有界（按 ts 淘汰或容量上限），注释说明
- [ ] 钉子（红绿）：快照含 sid→ts，新构造读快照后 Last(sid) 返回该值（当前代码红：恒无观测）
- [ ] 钉子（红绿）：快照钟=20 分钟前 + 台账闲置 20 分钟 → maybeSameModel 判热通过（当前代码红：-1 判冷）
- [ ] `go test ./internal/beat/... ./internal/daemon/...` 绿
- [ ] 观察指标口径注记（写进代码注释或文档）："-1 占比回落"自首次快照产生后的重启起算

## Blocked by
无，可立即开始（与票01/02 路径互斥）

## 涉及路径
- internal/beat/reqclock.go
- internal/beat/reqclock_test.go
- internal/daemon/serve.go
- internal/daemon/watcher.go
- internal/daemon/watcher_test.go（或新增装配回种测试文件）

## 副作用声明
无独占验证命令；包级测试即可。测试不得写生产 ~/ferryman 数据目录（用 t.TempDir）

decision_refs: D6, D1
review_blocks: F5, F8
