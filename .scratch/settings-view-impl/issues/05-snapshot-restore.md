# 票05 · 配置快照与还原

## What to build
daemon 新增快照模块：目录 `<data_dir>/backups/config/`，文件名 `config-<YYYYMMDD-HHMMSS>-<auto|manual>.toml`，整文件字节快照（含密钥），权限仅当前用户可读（Windows 下尽力收紧 ACL，至少 0600 语义），滚动保留 20 份（超出删最旧 auto）。端点：`POST /settings/snapshots`（手动）、`GET /settings/snapshots`（**仅元数据**：id/时间/原因/字节数——绝不回文件内容，无内容下载端点）、`POST /settings/snapshots/{id}/restore`（还原前**强制先快照当前态**（原因=pre-restore）→整文件字节还原→响应 needs_restart=true）。提供包内函数 `SnapshotAuto(reason string)` 供票03/04/06 写路径调用（接上票03预留的桩）。全部经票03锁。

## 验收标准
- [ ] 写路径触发自动快照：文件存在且与写前磁盘逐字节一致
- [ ] GET 响应 JSON 序列化后不含任何密钥明文（只含元数据字段）
- [ ] restore 后 config.toml 与目标快照逐字节一致；还原前当前态已另存 pre-restore 快照
- [ ] 生成 25 份后只剩 20 份且最旧 auto 被删、manual 优先保留
- [ ] `go test ./internal/daemon/ -run 'TestSnapshot'` 全绿（日志 05-test.log）

## Blocked by
票03

## 涉及路径
- internal/daemon/settings_snapshot.go（新）
- internal/daemon/settings_snapshot_test.go（新）
- internal/daemon/httpapi.go（仅分派行）

## 副作用声明
- 独占验证：`go test ./internal/daemon/ -run 'TestSnapshot'`

## decision_refs
D6、F4 已解除语义（元数据限定）

## review_blocks
无
