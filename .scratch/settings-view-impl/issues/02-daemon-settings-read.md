# 票02 · daemon 读面 GET /settings

## What to build
daemon 新增 `GET /settings` 端点（挂 15700 既有 httpapi 分派，Bearer 鉴权同既有端点）：返回全量配置 JSON，供设置视图渲染。密钥类字段（api_key/pushover_token 等凡命中既有毒名单正则者）一律 `{masked:"••••"+尾四位, has_key:bool}`，永不回明文；响应顶层附生效元数据 `effects`：按**操作级**声明（provider_switch 即时生效；tuning 即时生效；其余全部 needs_restart=true；新增上游条目后对其首次 switch 亦需重启——以独立字段标注）。UI 数字对账以此为唯一事实源。

## 验收标准
- [ ] 响应含全部 config 节与三类实体集合（dock.upstreams/providers/prices）
- [ ] 任何密钥字段在响应中不存在明文（测试：全 JSON 序列化后 grep 不到真钥、只出现 masked 形态）
- [ ] effects 按 spec 操作级语义返回
- [ ] 未经 Bearer 请求被拒（401/403，随既有端点行为）
- [ ] `go test ./internal/daemon/ -run 'TestSettingsRead'` 全绿（日志 .scratch/settings-view-impl/logs/02-test.log）

## Blocked by
无，可立即开始

## 涉及路径
- internal/daemon/settings_read.go（新）
- internal/daemon/settings_read_test.go（新）
- internal/daemon/httpapi.go（仅加分派行）

## 副作用声明
- 独占验证：`go test ./internal/daemon/ -run 'TestSettingsRead'`

## decision_refs
D4（能存不能看）、D7（操作级生效）、ADR-0022、F6 已解除语义

## review_blocks
无
