# 票 04 · 盲评生成工具（CLI 子命令）

## What to build

新增 CLI 子命令（如 `ferryman eval-ferry --provider <名> --n <样本数> --out <目录>`）：取最近 N 份交接的确定性骨架素材，逐样本经指定供应商生成模型叙事，产物连同骨架落盘到输出目录（一样本一文件对：骨架/叙事并排，文件名含时间与会话短 ID），供人工盲评对照。供应商走票 02 的供应商表（含协议适配）。盲评的人工评分环节不在本票。

## 验收标准

- [ ] 给定假上游（httptest）与样本目录，命令产出一样本一文件对，内容含骨架与叙事两段
- [ ] 样本选取=最近 N 份交接（按时间倒序），不足 N 时如实取全部并在输出注明
- [ ] 无可用样本/供应商缺名时报错清晰（不静默空跑）
- [ ] 输出文件名含时间戳与会话短 ID，可追溯
- [ ] 全仓 go test 相关包全绿 + go vet 净

## Blocked by

02

## 涉及路径

- internal/ferry/eval.go（新建）
- internal/ferry/eval_test.go（新建）
- cmd/ferryman/（子命令接线；以实际命令注册文件为准）

## 副作用声明

只跑 `go test ./internal/ferry/... ./cmd/...` 与 `go vet`；无网络（测试用 httptest 假上游与临时目录样本）。

## decision_refs

D5

## review_blocks

无
