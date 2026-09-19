# 09 · 验证套件：tap.go 上游捕获器

## What to build

`experiments/router-fidelity/tap.go`（package main，与 `experiments/capture/forwarder.go` 同型变体）：

- 监听 `127.0.0.1:15723`（flag 可改），转发到上游（flag `-upstream`，默认 `https://open.bigmodel.cn/api/anthropic`）。
- 每请求捕获：headers＋body 落本地 JSON（`-out` 目录，默认 `~/ferryman/router-fidelity/`）；**与 forwarder 的差别——不脱敏、原样捕获**（供逐字段差分；文件权限 0o600；README 写明"真钥会流经落盘文件：目录不进 git、验完即删"）。
- 与 forwarder 同款：SSE FlushInterval -1、body 读全回填、`/__tap/ping` 自检端点、捕获含 seq/ts/method/path/query/headers/body（body 存解析后 JSON 便于 diff.py）。
- 额外：**响应侧摘要捕获**（状态码、SSE usage 四列——供 L2 行为对照初筛），同样只落元数据＋usage，不落响应正文。

## 验收标准

- [ ] httptest 上游单测（tap_test.go）：请求过 tap→上游收到原样 body＋原样 headers（含 auth 原样透传）；捕获文件含全部头与解析后 body；SSE 响应逐块到达客户端不被缓冲（FlushInterval 生效——用带 flush 断言的流式测试或沿用 forwarder 的测试形态）。
- [ ] 响应摘要：usage 四列从 message_delta 提取入捕获（Q14 雷：message_start 全 0）。
- [ ] `-out` 目录 0o700、文件 0o600（Windows 下 best-effort，测试断言 os.FileMode 传参）。
- [ ] `go test ./experiments/router-fidelity/` 全绿（包内只有 tap，不影响主仓测试面）。

## Blocked by

无，可立即开始。

## 涉及路径

- experiments/router-fidelity/tap.go、tap_test.go（新文件）

## 副作用声明

- 默认只跑：`go test ./experiments/router-fidelity/`。测试全部走 httptest 本地回环，**绝不真实外呼**。

decision_refs: D7
review_blocks: 无
