# 票 02 · 报表 --json 合法性回归钉

## What to build
历史缺陷形态:account report --json 输出曾含非法 `\` 转义(Windows 路径),严格 JSON 解析器直接炸。现行实现(json.Encoder)实测不复发——本票给它在测试上钉死:强化既有 `TestRunJSONOutput`,构造一条 project 含反斜杠路径的行,捕获 stdout 断言全量输出可严格解析且含 `savings` 键。纯测试加固,不改生产码。

## 验收标准
- [ ] 强化后的测试先于实现基线存在且直接绿(本票无生产码改动,TDD 红步不适用——验收即"钉住",注释写明历史缺陷形态与出处构建)
- [ ] `go test ./internal/report/` 整包绿
- [ ] 测试内夹具行的 project 值真含反斜杠(如 `C:\Users\x\proj` 原生字面量)

## Blocked by
无,可立即开始

## 涉及路径
- `internal/report/report_test.go`(替换 TestRunJSONOutput;复用既有 captureStdout/testEnv/stubRun)

## 副作用声明
`go test ./internal/report/`(票内独占验证命令)

decision_refs: D6(降级事实:现行双路径实测合法,缺陷出自 v0.1.3-2-g5af93ea 存疑构建)
review_blocks: 无
