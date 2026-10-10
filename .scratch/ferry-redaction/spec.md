# Spec · 摆渡外发脱敏（ferry-redaction）

> 状态：**已立项、未开发**（2026-10-10 用户令："补上脱敏，但是先记下来，晚点再开发"）。
> 本文是立项记录＋方案草案；开发前按惯例过一轮 grill 定案，再改写为正式 spec。
> 术语遵守根目录 CONTEXT.md。

## Problem Statement

DESIGN §6.4 承诺 **L0.5 脱敏**："正则脱敏 key/密码/token/私钥/.env 值；脱敏异常/超时 → 该会话仅本地路由（无本地则骨架-only），**绝不外发未脱敏原文**"，并要求外发审计日志（目标/会话/字节数，不记内容）。

Go 版代码中这两项**都没有实现**（2026-10-10 检索 `internal/ferry`、`internal/extract`：无任何 redact/scrub/sanitize/脱敏 逻辑）。

L0 提取器（`internal/extract/extract.go`）送进模型材料的内容：

- user / assistant 的 text 正文（单条 ≤4000 码点，`ItemCharCap`）；
- 骨架：Bash 命令（单条 ≤160 码点，`CmdCharCap`）、涉及文件路径、标题、末段定格。

已被丢弃、不外发的内容：thinking；含 tool_result 的整条 user 记录（含 CC 写入的 `toolUseResult.originalFile`）；非 user/assistant 类型记录（attachment 等）。

→ **出现在用户消息、命令行、助手正文里的密钥，会随交接材料原样发给摆渡供应商。**

暴露面：

- 生产链 `local → zhipu-flash → kimi-k3`：本地千问在线时交接不出本机，只有本地挂掉时外发。
- NUC10（2026-10-10 起部署原生 CC）链为 `zhipu-flash → kimi-k3`（本地千问训练中、临时撤出），**每份交接都外发**。
- dsh 轨材料（渡口主快照，`worker.doDsh` → `ferry.DshFerrySession`）同样未脱敏，须一并覆盖。

## Solution（用户视角）

交接材料离开本机前一律先脱敏：密钥类字符串换成占位符（标类型＋尾 4 位，便于辨认），云端模型拿到的永远是去敏材料。脱敏本身出错的会话不外发，只走本地模型或纯记录版交接。外发留审计记录，可查每份交接发去了哪家、多少字节、命中几处。

## 草案决策（开发前定案）

1. **落点**：L0 之后、任何 `Chat` / `AnthropicChat` 调用之前的单一缝（链执行器入口），覆盖正文、骨架、末段定格、L2 分块与 reduce 材料；dsh 材料走同一缝。待定：落盘的交接 MD（骨架部分）是否也脱敏——纵深防御，但会影响"续接时照抄命令"。
2. **规则集**（至少）：
   - 已知前缀：`sk-`（含 `sk-ant-`、`sk-kimi-`）、GitHub `ghp_` / `gho_` / `github_pat_`、AWS `AKIA…`、Slack `xox*`、Google `AIza…`；
   - 智谱 key 形态 `[0-9a-f]{32}\.[A-Za-z0-9]{16}`；
   - `Authorization: Bearer …`、`x-api-key: …`；
   - 赋值右值：`api_key = "…"`、`API_KEY=…`、`password=`、`token=`、`secret=`（.env / TOML / JSON 形）；
   - PEM 私钥块（`-----BEGIN … PRIVATE KEY-----` 至 END，跨行）；
   - URL 内嵌凭据 `scheme://user:pass@host`；
   - **本机真钥精确匹配兜底**：config.toml 中 `[providers.*]` 与 `[dock.upstreams.*]` 的 `api_key` 原文逐一替换——对已知密钥零漏。
3. **占位符**：`[REDACTED:<类型>:…<尾4>]`（尾 4 位与 CLI 打码口径一致），让模型知道"此处有个密钥"而不泄密。
4. **失败语义**：脱敏 panic/超时 → 该会话跳过全部云端顺位（只试 local；local 不在链即骨架），沿用 DESIGN §6.4 原文；账本 handoff 行 err 记稳定字面量（如 `redact_failed`）。
5. **外发审计**：每次云端尝试一行（供应商/会话/字节数/命中计数），不记内容——DESIGN §6.4 同条承诺，一并实装。
6. **敏感 cwd local-only**（DESIGN §6.4 "敏感 cwd 前缀清单可配 local-only"）：同属未实装承诺，是否同票处理待定。

## 验收（草案）

- 单测：每类规则正反例，含 CJK 上下文、跨行 PEM；误伤控制——git commit hash、UUID、普通 hex、会话 ID 不被误杀。
- 契约钉子：fake 供应商捕获链执行器每个云端顺位的出站请求体，断言不含 config 中任一真钥原文。
- 回归：复用 2026-10-07 盲评方法（同料双评），脱敏前后各一轮，叙事分数不劣化。

## Out of Scope

- 会话 jsonl 本身不改（Ferryman 永不触碰会话）。
- 渡口转发的 CC 实时流量不脱敏（那是用户与自家上游的正常对话）。

## Further Notes

- 发现经过：2026-10-10 NUC10 部署原生 CC 时，会话转录里出现了用户填进 config.toml 的三把 key（edited_text_file 附件＋Edit 工具结果的 toolUseResult）。核查确认这两类记录被 L0 丢弃、未进材料——本次未泄露，但缺口真实存在。
- 开发前的人工规避：别在对话正文或命令行里贴密钥；账本 handoff 行的 provider 列可查哪些交接走了云端。
- 关联：DESIGN §6.4（L0.5 脱敏与外发审计）；`docs/20261002_剩余风险与哨兵.md`（风险登记行）。
