# 票 02 · L0 静态检查器包 internal/dshverify

## What to build

新包 `internal/dshverify/`：对给定 DSH 根（生产根由调用方显式传入，**本包不读环境变量猜根**）执行 L0 静态检查并返回结构化结果（按 profile 分层）：

1. 三 profile（desktop/web/headless，可配列表）各查：`profiles/<p>/ferryman-dsh` junction 存在且指向可解析；`profiles/<p>/node_modules/ferryman-dsh` 存在；`profiles/<p>/cordis.patch.yml` 含 `id: ferryman-dsh` 的 insert 行。
2. 插件 manifest 形状：package.json 含 `dsh` 键（manifestVersion/bundle.patch/client.platform）、`exports["."]` 与 `exports["./package.json"]`、main 入口文件存在（历史坑：exports 缺 "." 致失活，commit 5b3b476）。
3. 生产配置面：`~/.dsh` 根的 cordis.patch.yml insert 行在；provider/模型路由仍指向渡口（检查方式：读 DSH 配置文件中的渡口端点标记，具体形状参考 internal/provider/writer.go 的 dsh 分支写入什么就验什么——写入器是真相源）。
4. DSH 安装树版本可读（exe 文件版本或版本文件，返回版本串）。

每项检查返回 {name, profile?, ok, detail}；不判灯色（灯色归票 04）。

## 验收标准
- [ ] 临时目录夹具：完整布局→全 ok；删 insert 行/坏 exports/断 junction→对应项 fail 且 detail 指明位置
- [ ] junction 夹具用 `mklink /J`（非 Windows 跳过该夹具）
- [ ] provider 指向渡口的判据与 internal/provider/writer.go 写入形状一一对应（读代码对齐，测试里造同形状）
- [ ] `go test ./internal/dshverify/` 绿

## Blocked by
无，可立即开始

## 涉及路径
- internal/dshverify/l0.go（新）
- internal/dshverify/l0_test.go（新）
- internal/dshverify/fixtures/（新，测试夹具）

## 副作用声明
仅单测；mklink 夹具在临时目录内创建/清理；不跑全仓测试

decision_refs: D2、D6、D8
review_blocks: 无
