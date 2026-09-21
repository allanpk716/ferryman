# 票 04 · doctor 上游健康检查 + DeepSeek 边界列明

## What to build

`ferryman doctor` 新增"渡口上游"检查组:active 条目存在且可解析;非本地条目 model_map 含 default;缺 api_key 的未激活预置提示"未配置(手编 config 填 api_key)"。doctor 输出与 docs/ 各列明 DeepSeek 官方已知边界(不支持 document/search_result/redacted_thinking/mcp_tool_use/mcp_tool_result 消息类型;忽略 tool_result.is_error、tool_choice.disable_parallel_tool_use、thinking.budget_tokens——遇不支持负载会如实透传上游错误,处置=人手换上游)。docs/ 新增一篇多上游使用说明:三家端点与模型档位、key 填法、换上游与回退一条命令、DeepSeek 边界。

## 验收标准

- [ ] doctor 输出含上游健康检查组,覆盖上述三种状态(健康/缺 default/缺 key)
- [ ] doctor 输出含 DeepSeek 边界提示(激活 deepseek 条目时)
- [ ] docs/ 新增多上游说明文档,含:三家端点与档位表、key 手编填法、换上游与回退命令、DeepSeek 边界清单
- [ ] `go test ./internal/installer/...` 全绿

## Blocked by

01

## 涉及路径

- internal/installer/
- docs/

## 副作用声明

- 独占验证命令:`go test ./internal/installer/...`

decision_refs: D12, D13
review_blocks: F2(边界列明部分)
