# 票 01 · daemon 查询面骨架＋/sessions＋/session

## What to build

daemon 新增只读查询面的骨架，并打通前两个端点（用户视角：AI agent 经 MCP 面能列出全部会话的闲置状态、能深查单会话的成本与交接覆盖）：

1. **骨架**（internal/daemon/queryapi.go）：新增端点的注册与分派表——五个只读 GET 端点（/sessions、/session、/gate_check、/report、/beats）统一注册；鉴权与 127.0.0.1 绑定复用既有机制（Bearer token，与 /stats 同源）；每端点一个独立 handler 文件，本票先为 /gate_check、/report、/beats 留 stub（501 或明确"未实现"JSON），后续票逐个替换——**stub 与实现不得都写在同一文件**，保证后续票改动路径互斥。
2. **GET /sessions**：query 参数 agent（按 Agent 过滤）、cwd（项目路径前缀过滤）、limit（默认 50）；从台账（内存态）按最后写入时间倒序返回：session_id、Agent、项目路径、最后写入距今秒、闲置判定（凉会话/未凉，按该 Agent 的拦截阈值）、上下文规模、族系。
3. **GET /session?id=**：单会话聚合——台账条目＋四列会话总账（主转录＋各子代理转录四列加总，来源 internal/accounts 既有口径）＋有效交接覆盖（covers_until、fresh/skeleton）＋窗口状态（等待窗/问询窗在飞与停车标志）。id 不存在返回 404 JSON。

红线：响应永不包含消息内容（台账标题/路径/计数/金额可以，对话原文不行）、永不包含凭据字段；全部只读，无任何状态写入；/stats 既有字段名逐字契约不动。

## 验收标准

- [ ] GET /sessions 返回字段齐全（七要素），默认 limit 50、按最后写入倒序；agent/cwd 过滤生效
- [ ] 台账为空时 /sessions 返回空数组而非错误
- [ ] GET /session?id= 聚合四列总账＝主转录＋各子代理加总（构造含子代理流水的夹具验证）；covers_until 与交接状态正确；窗口状态如实
- [ ] id 不存在 → 404 JSON；未带 Bearer → 401（与既有端点同语义）
- [ ] /gate_check、/report、/beats 三个 stub 端点注册在位且返回明确"未实现"标记（后续票替换）
- [ ] /stats 响应字段与改动前逐字一致（既有 /stats 测试保持绿）
- [ ] 响应中不含消息内容样本、不含 api_key/token 等凭据字段（反向断言）
- [ ] go test ./internal/daemon/ -count=1 全绿；gofmt 干净

## Blocked by
无，可立即开始

## 涉及路径
- internal/daemon/queryapi.go（新建：注册与分派骨架＋三个 stub）
- internal/daemon/query_sessions.go（新建：/sessions 与 /session 实现）
- internal/daemon/query_sessions_test.go（新建）
- （若分派必须在既有 httpapi.go/serve.go 挂一个注册点：仅允许单行/单块接线改动，并在回报中注明）

## 副作用声明
- 独占验证命令：go test ./internal/daemon/ -count=1（临时端口/临时目录夹具，绝不绑定 15722/15724 生产端口）

## decision_refs: D2、D5、D6
## review_blocks: 无
