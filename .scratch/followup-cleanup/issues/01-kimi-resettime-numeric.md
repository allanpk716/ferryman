# 票 01 · Kimi 重置时刻数字串兼容

## What to build
Kimi 用量查询的 `resetTime` 字段以**数字字符串**到达(如 `"1761412800"` 秒、`"1790280000000"` 毫秒)时,解析器当前只试 ISO 布局、全败即判"无重置"——窗口静默丢倒计时。补上:字符串先剪首尾空白,ISO 布局全败后按纯数字解析,秒/毫秒自动判位(阈值 `1_000_000_000_000`,与既有数字形态同款),`n>0` 才有效;非数字/空串/≤0 仍判无重置。带空白 ISO 串因 TrimSpace 顺带被接受——这是显式申报的宽容化,提交信息必须点名。

## 验收标准
- [ ] 表驱动单测:秒级数字串、毫秒级数字串、`"abc"`、`""`、`"0"`、`"-5"`、ISO 字符串不回归
- [ ] parseKimi 全链路测试:数字串 resetTime 进周窗与 5h 窗(HasReset=true、时刻正确)
- [ ] 先写测试跑红,再实现跑绿(TDD 顺序可从产物看出)
- [ ] `go test ./internal/quota/` 整包绿(既有 GLM/Kimi/DS 用例零回归)
- [ ] 提交信息点名 TrimSpace 宽容化

## Blocked by
无,可立即开始

## 涉及路径
- `internal/quota/kimi.go`(resetTimeOf 字符串支;新增 strconv import)
- `internal/quota/quota_test.go`(追加两个测试)

## 副作用声明
`go test ./internal/quota/`(票内独占验证命令)

decision_refs: D5(错误串/纪律不变)
review_blocks: 无
