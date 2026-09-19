# 01 · 渡口透传竖切：opt-in 监听＋内存快照＋纯转发保真

## What to build

新包 `internal/dock`＋配置节＋daemon 接线，端到端打通"配了 `[dock]` 才有的本地中转"：

1. **配置节**（internal/config）：`[dock]` 含 `upstream_base_url`（默认 `http://127.0.0.1:15721`）、`listen`（默认 `127.0.0.1:15722`）。**节缺失＝渡口完全不启动**（不绑端口、零行为变化——这是评审 F11 的裁定语义，验收硬性要求）。
2. **内存快照库**（internal/dock/snapshot.go）：仅捕获 `/v1/messages` POST（路径以 `/v1/messages/count_tokens` 结尾的排除）；按 body `metadata.session_id` 归会话；每会话存（a）主快照＝**最大请求体**＋最小必要头集（content-type、anthropic-version、anthropic-beta、user-agent、accept、accept-encoding；**auth 类头一律不入**）、（b）最后一份（仅诊断）。Pinned 不变式：提供 Pin/Unpin 接口，pinned 会话不可淘汰；LRU 只淘汰非 pinned（上限 16 个非 pinned）；Unpin 后（对应窗口收尾＋最后一跳结算）才可清理。daemon 重启＝内存全丢（不持久化，注释写明）。
3. **透传 handler**（internal/dock/server.go）：`httputil.ReverseProxy`＋Rewrite API（`experiments/capture/forwarder.go` 同款：`SetURL(target)`＋`pr.Out.Host = target.Host`＋`FlushInterval: -1`）；读体后 `io.NopCloser(bytes.NewReader(body))` 回填再进代理（body 字节保真）；转发前把请求喂给快照库；Transport 超时对齐长响应（≥50min，SSE 不断流）。
4. **daemon 接线**（internal/daemon/serve.go）：配置有 `[dock]` 节才构造并启动 dock listener（独立 goroutine，错误不拖垮主服务）；提供 `Daemon.DockSnapshot()` 只读访问句柄供后续心跳票使用。
5. 入站不鉴权（绑 127.0.0.1 已足；占位令牌照抄不校验）。

## 验收标准

- [ ] 无 `[dock]` 节：daemon 启动后**不监听 15722**，既有测试全绿零改动（零行为变化的可执行证明）。
- [ ] 有 `[dock]` 节：`httptest` 后端＋全头夹具（content-type/anthropic-version/anthropic-beta/user-agent/accept/accept-encoding/accept 压缩头全套，模拟 CC 真实发送）过透传——**请求体逐字节相等；后端收到的头集合除 Host→上游、逐跳头（Connection 等）剥离外逐项相同；零新增头**（`.xcheck/20260919-195118/exp/xf7_reverseproxy_headers/main.go` 实验同款断言）。
- [ ] 快照：大会话请求后跟小请求（同 session）→主快照仍是大体；Pin 住后塞满 16+ 非 pinned 会话→pinned 不被淘汰；Unpin 后可被 LRU 淘汰；count_tokens 路径不入快照。
- [ ] `metadata.session_id` 提取失败/缺失的请求：透传不受影响，快照跳过（记计数即可，不告警）。
- [ ] `go test ./...` 全绿（新增 internal/dock 包测试＋config 解析测试＋serve 接线测试）。

## Blocked by

无，可立即开始。

## 涉及路径

- internal/dock/（新包：snapshot.go、server.go、dock.go、对应 _test.go）
- internal/config/（[dock] 节解析＋测试）
- internal/daemon/serve.go（接线，改动面最小化）

## 副作用声明

- 独占验证命令：`go test ./internal/dock/... ./internal/config/... ./internal/daemon/...`；提交前主会话跑全仓 `go test ./...`。

decision_refs: D3、D4、D10
review_blocks: F11（本票交付其裁定语义：opt-in 监听）；F12（snapshot_missing 表述按 spec 措辞）；F2/F3/F4（快照三项钉死语义的实现）
