# 票 04 · ferryman mcp：MCP server 核心＋五件 HTTP 工具

## What to build

MCP 面（用户视角：CC 会话把 ferryman 注册为 MCP server 后，能发现并调用六个只读工具）：

1. **`ferryman mcp` 子命令**（cmd/ferryman/main.go 接线）：stdio JSON-RPC 2.0 MCP server——initialize、tools/list、tools/call 三方法可被标准 MCP 客户端发现与调用。实现载体（官方/社区 Go SDK 或最小手写）由实施者定，**不新增第三方依赖时优先最小手写**；若引依赖须在回报中说明理由。
2. **internal/mcp 新包**：工具注册表＋五件工具（sessions、session_detail、gate_check、cost_report、heartbeat_status）——各自转发 daemon 对应端点（/sessions、/session、/gate_check、/report、/beats），请求参数透传（含 limit/过滤/scope/key），响应 JSON 原样透传。
3. **鉴权**：daemon token 经既有配置解析取得（internal/config Load 优先级：显式参数 > FERRYMAN_CONFIG > 默认路径），只活进程内存；任何响应/错误/日志不回显 token。
4. **错误面**：daemon 不可达/超时 → 工具返回明确错误（MCP isError 语义），文案含"任意钩子触发或 ferryman serve 会拉起"；**不缓存（每次都真连）、不伪造、不顺势自举 daemon**。
5. **工具元数据**：名称/参数英文；描述中文并直接引用 CONTEXT.md 词条原文（凉会话、拦截阈值、有效交接、台账、成效账、等待窗、问询窗、心跳），不造第二套翻译。
6. **红线**：工具面不存在任何写操作工具；响应不含消息内容/凭据/token；合成数据永不吐出。

## 验收标准

- [ ] stdio 三方法可被标准客户端驱动（测试用子进程或管道驱动 ferryman mcp，完成 initialize→tools/list→tools/call 全链）
- [ ] tools/list 恰好六件工具中的五件（doctor 在票 05）且无任何写操作工具（反向断言）
- [ ] 五工具对临时 daemon（FERRYMAN_CONFIG 指向临时 config、临时端口）各一发成功，参数（limit/过滤/scope）透传正确
- [ ] daemon 不可达：五工具均明确报错；连续两次调用均报错（不缓存）；全程无新 daemon 进程/监听产生（不自举）
- [ ] 任何响应/错误文本中不出现 token 串（反向断言）
- [ ] 测试显式断言所用端口非生产端口（15722/15724）
- [ ] go test ./internal/mcp/ ./cmd/ferryman/ -count=1 全绿；gofmt 干净

## Blocked by
票 01、票 02、票 03

## 涉及路径
- internal/mcp/（新建包：server、工具注册表、五工具、HTTP 客户端）
- internal/mcp/*_test.go（新建）
- cmd/ferryman/main.go（仅"mcp"子命令分派接线）
- cmd/ferryman/main_test.go（若需）

## 副作用声明
- 独占验证命令：go test ./internal/mcp/ ./cmd/ferryman/ -count=1（临时端口，绝不触生产端口）
- 构建 ferryman.exe 仅限测试夹具需要时在临时目录进行，不覆盖仓库根 exe

## decision_refs: D3、D5、D6、D8、D10、D12
## review_blocks: 无
