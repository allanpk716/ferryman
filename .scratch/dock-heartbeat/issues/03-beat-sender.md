# 03 · HttpBeatSender 真身：快照重放发送器＋serve 注入

## What to build

1. **HttpBeatSender**（internal/beat/httpsender.go）实现既有 `beat.Sender` 接口：
   - 构造参数：渡口入站地址（默认 `http://127.0.0.1:15722`）＋快照访问句柄（票 01 的 `DockSnapshot` 只读接口）。
   - `Send(plan)`：按 `plan.SessionID` 取**主快照**（最大体）；无快照→`BeatResult{Sent:true, OK:false, Err:"snapshot_missing"}`（不 panic、不阻塞）。
   - 重放：快照 body 原样、**唯一改写 `max_tokens`→1**（JSON 顶层键，重编码仅此一处；其余字节保持——实现方式：解析→改键→重编，或字节级替换，测试钉死 messages/system/tools 等内容不变）。
   - 头部：携带快照头集（content-type/anthropic-version/anthropic-beta/user-agent/accept）；auth 类按占位令牌字面量重建（`Authorization: Bearer PROXY_MANAGED`、`x-api-key: PROXY_MANAGED`）；accept-encoding 固定为可解析值（identity 或 gzip＋解析前解压，见下）。
   - **错误语义（F1，唯一语义）**：任何传输层错误（连接失败/超时/429/5xx）→ **不重试**，直接返回 `BeatResult{Sent:true, OK:false, Err:<类别>}`；只有 2xx 且 SSE 完整读到 usage 才算 OK。秒级总超时（如 10s），超时即上述错误路径。
   - **SSE 解析**：逐事件读 `message_start`/`message_delta`；**usage 以 `message_delta` 的为准（覆盖 start 的全 0——Q14 实测）**；取 input_tokens/cache_read_input_tokens/output_tokens 填 BeatResult。若响应带 content-encoding: gzip，发送侧请求头固定不带压缩（identity）即可规避；同时解析器在遇到 gzip 字节时按错误处理（防御）。
   - miss（2xx 但 cache_read 占比低）照常返回 usage，分类交给既有 `beat.Classify`——sender 不判 HIT/MISS。
2. **beat.go 注释更正**（internal/beat/beat.go:62-73 Sender 接口注释）：删除"指数退避重试 1 次"旧表述，改为"传输错误跳过不重试（ADR-0006/0007），按 ERROR 语义返回"——只改注释，接口签名与既有类型零变动。
3. **serve 注入**（internal/daemon/serve.go）：构造 HttpBeatSender（渡口开＝配了 [dock] 才构造真身；渡口关＝保持 nil，现有"enforce 但无 sender→observe 演练＋告警一次"路径不变）注入 Watcher.BeatSender——问询守望泳道即刻受益。

## 验收标准

- [ ] mock 上游（httptest，SSE 流式分块写）单测：max_tokens 被改 1 且 messages/system/tools 逐字段不变；message_delta usage 覆盖 message_start 全 0；正常路径返回 OK＋三列 token。
- [ ] 错误路径单测：连接拒绝/超时/429/500 各一例——**单次请求即返回**（断言 mock 只收到 1 次请求＝不重试）、OK=false、Err 类别正确。
- [ ] snapshot_missing 单例（无该会话快照）。
- [ ] 头部重建断言：mock 收到的头＝快照头集＋占位令牌字面量，**无**其他新增头（夹具以 Q14 捕获件头集为参照——S1：断言快照头集 ⊇ 实验重放集减 auth）。
- [ ] serve 注入测试：无 [dock] 时 BeatSender 仍为 nil 且既有 qwatch 测试全部零回归；有 [dock] 时注入非 nil。
- [ ] `go test ./...` 全绿。

## Blocked by

01（快照库＋DockSnapshot 句柄＋渡口入站口）。

## 涉及路径

- internal/beat/（httpsender.go、httpsender_test.go、beat.go 注释行）
- internal/daemon/serve.go（注入点）

## 副作用声明

- 独占验证命令：`go test ./internal/beat/... ./internal/daemon/...`。

decision_refs: D2、D3
review_blocks: 无（F1 语义已在 spec 钉死；S1/S4 采纳项在本票测试夹具落实）
