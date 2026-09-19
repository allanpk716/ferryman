# 07 · 智谱余额只读查询

## What to build

1. **余额查询**（internal/dock/balance.go）：`GET https://open.bigmodel.cn/api/user/balance`（bigmodel 余额端点；用 `[dock].api_key` 出 `Authorization: Bearer`）。读现有 report/面板代码后确认确切路径——若仓库内已有 bigmodel 端点常量复用之；无则按 bigmodel 开放平台文档用 `/api/user/balance` 并把 URL 做成配置可覆写（默认值内置）。超时 5s；错误返回错误不 panic；**响应只取数值字段**（余额/过期时间类），不落任何其他内容。
2. **展示**（internal/report 或 viewer 现有输出）：report 命令（或 /stats 面板，选现有最小接入点）新增一行"GLM 余额：<值>"；查询失败显示"余额查询失败：<类别>"（不含真钥）。查询按需触发（report 运行时一次），不做后台轮询。

## 验收标准

- [ ] mock 上游单测：2xx JSON→解析出余额并展示行；401/超时→错误类别行，无真钥泄漏（断言输出与日志不含 api_key）。
- [ ] URL 可配置覆写（默认内置）；无 [dock].api_key→显示"未配置"，不发请求。
- [ ] `go test ./...` 全绿。

## Blocked by

01（[dock] 配置基座）。

## 涉及路径

- internal/dock/balance.go、balance_test.go（新文件）
- internal/report/（或 viewer 最小接入点＋测试）

## 副作用声明

- 默认只跑：`go test ./internal/dock/ ./internal/report/...`。

decision_refs: D6
review_blocks: 无
