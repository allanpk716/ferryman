# 票01 · help 安全与 usage 重构

## What to build
CLI 帮助面统一为安全契约:-h/--help 永不触发真实动作、一律 exit 0;未知/多余参数按用法错拒绝(exit 2)。具体:①doctor 与 install-ccswitch 在 run() 分发层对任何非空参数(含 -h)打印用法并退 2,绝不执行体检/快照注入(现状缺陷:参数整体丢弃照跑,install-ccswitch -h 会真写宿主配置);②手工分发族 account/tuning/autostart/watchdog/upstream/provider/cutover 识别 -h/--help 打印各自 usage 常量并 exit 0(现状 exit 2 或当未知子命令);③顶层 usage 常量补 update 条目(照 main.go 文件头注释形态:--check/--prerelease/--wait-quiet/--force/版本参数),并按 serve/体检/安装/账本/渡口与供应商/调参/换装/工具 分组重构,篇幅明显缩短;④Go flag 族(serve/update/mcp/eval-ferry/install-cc/install-codex/install-mcp/backtest 等)保持 flag 包默认 -h(exit 0)不动。

## 验收标准
- [ ] `ferryman doctor -h` / `ferryman install-ccswitch -h`:打印用法、exit 2、零副作用(不跑体检、不写任何文件)
- [ ] `ferryman doctor extra` / `ferryman install-ccswitch extra` 同上拒绝
- [ ] account/tuning/autostart/watchdog/upstream/provider/cutover 七族 `-h` 与 `--help`:打印各自 usage、exit 0
- [ ] `ferryman -h`/`help` 输出含 update 条目且分组清晰,总行数不增
- [ ] 上述契约有表驱动单测钉住(含"不执行"断言:runUpdateExecute/runDoctor 注入桩计数为零)
- [ ] 涉及包 go test 绿

## Blocked by
无,可立即开始

## 涉及路径
cmd/ferryman/main.go
cmd/ferryman/provider.go
cmd/ferryman/upstream.go
cmd/ferryman/main_test.go
cmd/ferryman/provider_test.go
cmd/ferryman/upstream_test.go
(测试文件不存在则新建)

## 副作用声明
无独占验证命令;只跑 go test ./cmd/ferryman/ 与 go vet ./cmd/ferryman/

## decision_refs
D8、D14

## review_blocks
无
