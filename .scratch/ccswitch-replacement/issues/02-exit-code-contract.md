# 票02 · exit code 契约统一:数据空态与失败分离

## What to build
退出码语义统一为 0=成功、1=失败、2=用法错,并消除"数据空态混入失败"的例外:①backtest 引擎未产出(ttl_s 未配置等数据态)现状"报告照落但退 1"改为"报告照落+结构化标注(输出内明示空态原因)+exit 0";②全 CLI 退出码核对:tuning sweep 部分失败保持 1(真失败)、upstream use"写成功/验失败"保持 1 但在输出里两态分明(行为不改,核对成文);③在 usage 文末补三行退出码契约说明(为票06 CLI.md 提供底稿)。

## 验收标准
- [ ] backtest 数据空态:报告落盘、输出含空态标注、exit 0(有单测钉)
- [ ] backtest 真错误(账本读不到)仍 exit 1
- [ ] 退出码契约说明进 usage 文末
- [ ] go test ./cmd/ferryman/ 绿

## Blocked by
无,可立即开始

## 涉及路径
cmd/ferryman/backtest.go
cmd/ferryman/backtest_test.go(不存在则新建)
cmd/ferryman/main.go(仅 usage 文末三行;与票01 同文件,调度互斥自然串行)

## 副作用声明
无独占验证命令;go test ./cmd/ferryman/

## decision_refs
D8

## review_blocks
无
