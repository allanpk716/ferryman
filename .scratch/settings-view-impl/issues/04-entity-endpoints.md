# 票04 · 实体集合端点 + 引用完整性 + 密钥合并

## What to build
daemon 新增三组实体端点：`PUT /settings/dock/upstreams/{name}`（新增或整体替换，字段含 base_url/api_key/model_map 六键/dialect/codex/pi/balance_url/text_only）、`PUT|DELETE /settings/providers/{name}`、`PUT|DELETE /settings/prices/{key}`。全部经票03锁+写前快照调用点+审计。**密钥合并语义（F3）**：请求中密钥字段省略或等于当前掩码占位值（与 GET 返回的 masked 精确相等）→保留磁盘现值；仅显式非空新值覆盖；空串=保留现值（清钥=删除整条目）；掩码串永不落盘。**引用完整性（F5）**：DELETE providers/{name} 时若被 [ferry].chain 或 [ferry].provider 引用→409 拒删且错误信息点名引用方；DELETE dock/upstreams/{name} 时若被 [dock].active 或 [ferry.same_model].upstreams 引用→409 同规。

## 验收标准
- [ ] PUT 上游省略 api_key → 磁盘真钥逐字节保留（测试前后读文件断言）
- [ ] PUT 回传与 masked 精确相等的字符串 → 同样保留真钥
- [ ] PUT 提供新钥 → 覆盖且 GET 只回新钥尾四位
- [ ] DELETE 被 chain/provider 引用的 providers 条目 → 409+错误含引用方名
- [ ] DELETE 被 active/白名单引用的 dock 上游 → 409+错误含引用方名
- [ ] 新增上游条目（校验过）落盘且其余节不动
- [ ] `go test ./internal/daemon/ -run 'TestSettingsEntities'` 全绿（日志 04-test.log）

## Blocked by
票02、票03

## 涉及路径
- internal/daemon/settings_entities.go（新）
- internal/daemon/settings_entities_test.go（新）
- internal/daemon/httpapi.go（仅分派行）

## 副作用声明
- 独占验证：`go test ./internal/daemon/ -run 'TestSettingsEntities'`

## decision_refs
D4（F3 已解除语义）、D8、F5 已解除语义、F8 裁定（空串=保留）

## review_blocks
无
