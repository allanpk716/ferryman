# 抓包实测：CC 请求精确形状 + cc-switch 兼容性两坑定性

- **日期**：2026-09-18 09:44–09:54（约 10 分钟实操）
- **方法**：`experiments/capture/forwarder.go`（Go 标准库 ReverseProxy 抓包转发器，CC→15722→cc-switch:15721→GLM）+ 项目级 settings 覆盖路由（不改全局配置）+ 一次哑工具直打探针。捕获文件在 `~/ferryman/captures/`（本地保留，永不入库——内含完整系统提示与工具表）。
- **回答了**：《20260918_Q14心跳保真度实验方案》段一（真实请求形状）✅；《20260918_billion-context持续压缩调研》问题 5（cc-switch 是否剥注入工具）✅ 与问题 6（CC 会话标识）✅。

## 三个主结论

### 1. CC 每请求都带会话头（bili 调研问题 6 → 无碰撞风险）

```
X-Claude-Code-Session-Id: ed5996f7-d98b-4c15-bccb-7bfbccdaa224   ← 单会话全程稳定 UUID
User-Agent: claude-cli/2.1.273 (external, sdk-cli)
Anthropic-Beta: claude-code-20250219,context-1m-2025-08-07,interleaved-think
```

billion-context 担心的"CC 不发会话标识→首消息哈希回退→并发碰撞"**不成立**：CC 原生发 `x-claude-code-session-id`，bili 的会话隔离对 CC 开箱即用。

### 2. cc-switch 不剥注入工具（bili 调研问题 5 → 注入能活）

哑工具探针（`claude-haiku-4-5` 别名、stream=false、单工具 `echo_test_tool`）：GLM（glm-5.3-flash）返回 `tool_use: echo_test_tool(x=42)`、`stop_reason=tool_use`。**工具定义与调用链路全程无改动通过**。上游回显 `model=glm-5.3-flash`（真名），usage 带 `cache_read_input_tokens`（GLM 报缓存读）。

### 3. CC 请求精确形状（Q14 段一交付，beat 重构的规格）

| 项 | wire 实测值 |
|---|---|
| model | `claude-opus-5`——**无 [1M] 后缀**（别名 `claude-opus-5[1M]` 只活在 settings；1M 信号走 beta 头 `context-1m-2025-08-07`） |
| max_tokens | 64000 |
| stream / thinking | true / true（对象） |
| temperature | 缺省不发 |
| 顶层键 | `messages, system, thinking, context_management, output_config, stream, model, tools, metadata, max_tokens` |
| tools | 30 个（当前插件全集） |
| system | 9222 字符（分段数组） |
| messages | 含 **role=system 条目**（SessionStart 钩子注入上下文）；内容块带 `cache_control: ephemeral` 断点 |
| metadata.user_id | device_id 十六进制（无账号 UUID） |
| 鉴权 | `Authorization: Bearer PROXY_MANAGED`（真钥只在 cc-switch DB） |

**对 beat 执行器的硬要求**（比原设计更严）：① 发 `claude-opus-5` 而非带 [1M] 的别名；② beta 头三件套照抄；③ `cache_control` 断点逐字节复刻（它是前缀内容的一部分，错位即 miss）；④ `context_management`/`output_config` 等新顶层键照抄——任何缺失都可能改变上游缓存键。捕获文件就是重构模板。

## 附带发现

- **CC 原生自动压缩的旋钮在 wire 上**：`context_management` 字段（自动 compact 触发配置）——compact 赛道（T47 预演）多了一个可观测点。
- **重试风暴的真相是转发器自己的锅**：Go ReverseProxy 不设 `FlushInterval:-1` 会缓冲 SSE，客户端判定断流按指数退避重试（实测 10 发字节级全同、间隔 1→35s 递增）。修复后一发 200、10 秒返回。**教训对 beat 设计同样适用：重试是费用放大器，transport 错误一律退避跳过、绝不重发**（设计 §3.6 不变式再+1 实证）。
- **编码地雷**：Git Bash 下 curl 发中文默认 GBK，cc-switch 按 UTF-8 解析直接 500（`invalid unicode code point`）。手工探针一律纯 ASCII 或文件载入。
- **环境覆盖层级**：settings.json 的 `env` 块优先于 shell 环境变量；项目级 `.claude/settings.json` 又优先于用户级——抓包路由用项目级覆盖即可，全局配置零改动。

## 对 bili 评估的净影响

四个坑里两个（#5 工具、#6 会话头）**当场解掉且都是好消息**。剩余：窗口配置陷阱（配置侧，已知解法=故意配小）、子代理质量风险（上游 issue #862，只能试点观察）、压缩轮 usage 不进 CC 账本（对账需并 bili 日志）。**结论：链路兼容性不再是阻碍，bili 试点的决策现在只剩经济账（T47 预演）与质量风险。**

## 工具沉淀

- `experiments/capture/forwarder.go`——可复跑抓包转发器（`go run forwarder.go`），已带 SSE 冲刷与响应状态日志。
- `~/ferryman/captures/*.json`——本次 11 份真实请求捕获（Q14 beat 重构模板，含重试风暴对照组）。
