# 票03 · daemon 触发判定接线（watcher 扫描入槽+并行交接）

## What to build
daemon 侧触发扫描（watcher_dsh 先例）：闲置 ≥ trigger_ratio×TTL ∧ peak_ctx ≥ min_peak_tokens ∧ 无在途请求 ∧ 无有效 compressed 标记 ∧ 无未过期在槽指令 → 写入指令槽（expires_at=now+command_ttl_ratio×TTL）；同时按既有 L1 管线异步生成交接文档（与指令独立，互不阻塞，交接失败不影响指令）。

## 验收标准
- [ ] 表驱动测试绿：五条件各自不满足时不入槽；全部满足入槽且带 expires_at；同会话重复触发覆盖旧槽
- [ ] 交接异步生成分支可测（mock/注入）；交接失败仅日志不回滚指令
- [ ] 现有 watcher/gate 测试不回归

## Blocked by
票02（指令槽与配置节先落）

## 涉及路径
- internal/daemon/watcher*.go 或新增 compact_trigger.go + _test.go
- internal/config/（若需补键，与票02协调以票02为准）

## 副作用声明
默认只跑 go test 指定包

decision_refs: D1
review_blocks: F1
