# 票03 · daemon 单写者锁 + 节级 PUT + 审计行

## What to build
daemon 新增设置写入核心：①包级互斥锁（sync.Mutex），后续一切设置写路径（本票 PUT、票04 实体、票05 快照/还原、票06 switch）必须经它串行，并发请求阻塞排队；②`PUT /settings/{section}`：body=该节完整对象→复用票01节级写原语落盘→每次写前调用票05快照接口（本票先以函数桩预留调用点，票05落地后自动生效——桩仅记录「应快照」不落盘）→写后落审计行；③审计行：追加写 `<data_dir>/settings-audit.log`，格式=单行 JSON（ts/entry=settings-ui/section/before/after——密钥值以 `<masked>` 替代），永不进账本。响应 `{saved, needs_restart}`；若节含 [server].port 改动，响应附 `new_port`。

## 验收标准
- [ ] 两并发 PUT 不同节：结果=两节都写入、无交错损坏（httptest 并发断言）
- [ ] PUT 期间 GET 不被阻塞（锁只罩写路径）
- [ ] 校验失败的 PUT：config 不动、审计行记 attempt+rejected
- [ ] 改 [server].port 的响应含 new_port
- [ ] 审计行密钥字段为 `<masked>`，真钥不出现
- [ ] `go test ./internal/daemon/ -run 'TestSettingsWrite'` 全绿（日志 03-test.log）

## Blocked by
票01

## 涉及路径
- internal/daemon/settings_write.go（新）
- internal/daemon/settings_write_test.go（新）
- internal/daemon/httpapi.go（仅加 PUT 分派行）

## 副作用声明
- 独占验证：`go test ./internal/daemon/ -run 'TestSettingsWrite'`

## decision_refs
D7、D8、F1 已解除语义（单写者互斥）、F2 已解除部分（审计）

## review_blocks
无
