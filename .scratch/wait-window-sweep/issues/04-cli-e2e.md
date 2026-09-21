# 票04 · CLI 接线端到端 + 本机首跑

## What to build

`ferryman backtest` 子命令上线（端到端行为：用户/agent 敲一条命令，读自己的账本与价格表，得到 docs 实验报告与结构化结果）。

功能点：
- 子命令注册进 cmd/ferryman（serve/doctor/update/... 同款装配风格）
- flags：`--projects` / `--exclude`（glob，可多次，默认全量）、`--json`（stdout 结构化结果+退出码）、`--out <path>`（报告落点，缺省 docs/ 实验报告惯例文件名）
- config 解析复用既有优先级（显式参数 > `FERRYMAN_CONFIG` > 默认路径，同 daemon）；`data_dir` 定位账本；价格表装载
- 装配 01→02→03 全链；零窗数据集时如实报告（非零退出码或明确空态标注，不崩溃）
- **本机首跑（第一遍，心跳全链开闸决策依据）**：对真实 ~/ferryman 账本 dry 跑一次（只读，不写任何账本科目），产出 `docs/20260921_等待窗扫参_实验报告.md` 作为票产物

## 验收标准

- [ ] 临时 data_dir 夹具端到端：`ferryman backtest --json` 退出码 0，markdown 报告落 --out 指定路径，计数正确
- [ ] `--projects`/`--exclude` 生效（夹具含 ft-lab 样式路径时剔除）
- [ ] FERRYMAN_CONFIG 优先级与 daemon 一致（夹具断言）
- [ ] 价格表缺 P_cache 的 provider → "不可算"桶如实标注
- [ ] 零窗数据集：明确空态输出，不 panic
- [ ] 本机真实账本首跑成功：报告落 `docs/20260921_等待窗扫参_实验报告.md`，含时点戳与实跑计数；账本零写入（跑前后 `git status` 于 ~/ferryman 无关，本仓账本文件 mtime/内容不变——用只读打开断言）
- [ ] `go test ./cmd/ferryman/ -count=1` 全绿 + `go vet ./cmd/ferryman/` 净 + 全仓 `go build ./...` 过
- [ ] 无本机路径/项目名硬编码（通用性：默认全量，profile 只经 flags）

## Blocked by

02, 03

## 涉及路径

- cmd/ferryman/main.go（子命令注册，最小 diff）
- cmd/ferryman/backtest.go（新建：装配与 flags）
- cmd/ferryman/backtest_test.go（新建：E2E 夹具）
- docs/20260921_等待窗扫参_实验报告.md（新建：本机首跑运行产物）

## 副作用声明

跑 `go test ./cmd/ferryman/`、`go vet`、`go build ./...`；本机首跑**只读** ~/ferryman 账本与 config（不写账本、不触碰 daemon 端口、不起进程副作用）。

## decision_refs

D7（CLI 形态）、D13（跑两遍的第一遍）、D14（排期）、D15（--json/agent 可用）、D16（通用性）

## review_blocks

无
