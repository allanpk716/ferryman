# 06 · 改写接线＋双改写守卫＋dock 科目＋形态漂移告警

## What to build

把票 05 的改写器接进渡口，并交付三件配套：

1. **rewrite_enabled 分支**（internal/dock/server.go）：`[dock].rewrite_enabled=false`（默认）＝纯透传（票 01 路径）；`true`＝对 `/v1/messages` POST 请求体过改写器（票 05 API），改写后的 body 发上游；非 messages 路径一律透传（count_tokens 若存在且含 model 字段→同映射，body 其余不动）。
2. **出站头卫生**（internal/dock/server.go 或 headers.go）：rewrite 模式下——host 换上游；追踪/CDN 头删（x-forwarded-*、x-real-ip、cdn-*、traceparent 类）；**删入站 Authorization 与 x-api-key，出站 Authorization: Bearer <[dock].api_key>**（真钥只从 config 读，永不入日志/账本/错误信息）；**anthropic-beta 重建**：保留客户端全部 beta 标记＋确保含 `claude-code-20250219`（有则不加，无则追加）；anthropic-version 透传；UA 保留。透传模式＝头零处理（票 01 保真语义不变）。
3. **双改写守卫**（internal/dock/guard.go＋doctor）：`rewrite_enabled=true` 时把 upstream host 归一化（localhost/::1/127.0.0.0/8 全归一为 loopback）后判定是否指向已知本地中转端口（15721/15722/15723）→ 是则**拒绝进入改写模式**（退回纯透传）＋告警日志一行；doctor 增该项检查。
4. **dock 科目**（internal/accounts＋server.go 接线）：新科目 `dock`——透传与改写**两种模式都记**（透传时改写前后模型名同值，F13）；每请求一行纯元数据：ts、session、改写前后模型名、四列 token（上游响应 usage，SSE 解析取 message_delta；透传响应不解析的场合记可得列＋状态码）、延迟、状态码。隐私不变量：永不落消息内容。
5. **形态漂移告警**（internal/dock/drift.go）：内存登记见过的 anthropic-beta 标记全集与请求顶层参数键全集（daemon 生命周期内）；新值出现→日志告警＋`NotifyAlert`（pushover，标题走票 08 之前的现有文案即可，不依赖 08）。

## 验收标准

- [ ] rewrite=true happy path：mock 上游收到改写后 body（模型已映射/图片已降级）＋卫生后头（Bearer 真钥、beta 含 claude-code-20250219、无 x-api-key、无追踪头）。
- [ ] rewrite=false：票 01 的透传保真测试零回归（体逐字节/头集合断言仍过）。
- [ ] 守卫：rewrite=true＋upstream 为 127.0.0.1:15721 / localhost:15721 / [::1]:15722 三形态→全部拒绝改写退回透传＋告警；upstream 为 api.bigmodel.cn→放行。
- [ ] dock 科目：透传与改写各一例——行字段齐全（透传行前后模型名同值）；无消息内容字段。
- [ ] 漂移：首次见 beta=X 无告警；第二次出现新 beta=Y→告警一次（不重复）；新顶层键同型。
- [ ] `go test ./...` 全绿。

## Blocked by

01（server.go 基座）、05（改写器 API）。

## 涉及路径

- internal/dock/（server.go 编辑、guard.go、drift.go、headers 相关、对应 _test.go）
- internal/accounts/（dock 科目枚举＋行形状＋测试）

## 副作用声明

- 独占验证命令：`go test ./internal/dock/... ./internal/accounts/...`。

decision_refs: D4、D6、D10
review_blocks: 无（F10/S3/F13 语义已在 spec 钉死）
