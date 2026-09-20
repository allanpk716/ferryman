# 票 06 · install-mcp＋doctor 注册检查＋验收收口

## What to build

安装面与全链收口（用户视角：一条幂等命令把 agent 面注册进 CC 用户级配置，冲突时安全；doctor 能查出注册不在位；六工具全链 E2E 与全部类别级验收齐备）：

1. **`ferryman install-mcp`**（cmd/ferryman/main.go 接线＋internal/installer 新文件）：
   - 注册范围固定 CC 用户级 MCP 配置（`claude mcp add --scope user` 等效：写入用户级 MCP 配置结构的 ferryman 条目，command 指向当前 ferryman 可执行文件、args=["mcp"]）。
   - **冲突语义（F6 seam 裁定，必须逐条实现）**：①能识别为 Ferryman 自建且形态兼容的旧条目 → 幂等覆盖（重复执行结果一致）；②外部或不兼容条目 → 默认拒绝并说明原因，仅显式 `--force` 才覆盖；③回显仅限非敏感摘要与白名单字段（command/args 形状），env/header/token 类值一律掩码或不输出；--force 覆盖前输出被替换条目的脱敏摘要。
2. **doctor 增检查项**："MCP 注册在位"——查用户级 MCP 配置（与 install-mcp 同 scope），结构化结果并入票 05 的出口。
3. **/stats 契约回归**：既有 /stats 字段测试保持绿（或字段名集合快照对比不变）。
4. **验收收口**：六工具全链 E2E（临时 daemon 经 FERRYMAN_CONFIG、临时端口、断言非生产端口）＋反向断言总集（响应无消息内容样本/无凭据字段/无 token 串；MCP 工具面无写操作工具）。

## 验收标准

- [ ] install-mcp 首次执行写入用户级 ferryman 条目；重复执行幂等（两次执行后配置一致）
- [ ] 外部同名条目默认拒绝且配置原样不动（F6 断言一）；--force 才覆盖
- [ ] 含假凭据（env/header 塞假 token）的旧条目在任何回显路径不出现明文凭据（F6 断言二）
- [ ] doctor"MCP 注册在位"：装/卸两态各断言一次（未装时＝失败项并说明）
- [ ] /stats 既有字段测试保持绿（契约回归）
- [ ] 六工具全链 E2E 绿；反向断言总集绿
- [ ] go test ./... -count=1 全仓绿（除既有 SKIP）；gofmt 干净；go vet 干净

## Blocked by
票 05

## 涉及路径
- internal/installer/mcpinstall.go（新建）
- internal/installer/mcpinstall_test.go（新建）
- internal/installer/doctor.go（仅追加注册检查项）
- cmd/ferryman/main.go（仅"install-mcp"子命令接线）
- internal/mcp/（收口 E2E测试文件，如 e2e_closeout_test.go）
- tests/（如既有 E2E 汇聚点需要挂收口用例）

## 副作用声明
- 独占验证命令：go test ./... -count=1（全仓；临时端口/临时 HOME 夹具，绝不触生产端口与真实用户目录）

## decision_refs: D8、D11、D13；票 05 的结构化出口
## review_blocks: F6（冲突语义四件与两断言——spec 已烘焙裁定，实施即解除）
