# 票01 · config 节级写原语（基础票）

## What to build
internal/config 新增通用节级写函数（如 `SetSectionTOML(name string, body string) error` 或等价 API）：对 config.toml 的指定顶层节做文本手术重写——只动目标节的内容行，节外内容（其他节、顶层注释、节前注释尽量保真）原样保留；写前对新全文跑一遍完整 Load 校验（复用既有 Validate/Load 全部规则，含跨节不变量），校验失败拒绝写；临时文件+rename 原子写、权限 0600、写后复读自校验（与 dock_edit.go 既有纪律同源）。同时为 providers 与 prices 两个实体表提供等价的「按条目名写入/删除」文本手术函数（dock.upstreams 已有 AddDockUpstream/RemoveDockUpstream 可参考/复用其模式）。TDD：先写失败测试。

## 验收标准
- [ ] 新函数对任一顶层节整写后：目标节=新值，其余节与节外注释逐字节不变（测试断言）
- [ ] 写入结果能通过 config.Load 全量校验；构造非法改动（如 summarize≥block）被拒绝且原文件不动
- [ ] 原子性：模拟 rename 前失败不留半文件
- [ ] providers/prices 条目增删：目标条目动、同节其他条目与注释保真
- [ ] `go test ./internal/config/ -run 'Settings|Section' ` 全绿（输出落 .scratch/settings-view-impl/logs/01-test.log）

## Blocked by
无，可立即开始

## 涉及路径
- internal/config/settings_edit.go（新）
- internal/config/settings_edit_test.go（新）

## 副作用声明
- 独占验证：`go test ./internal/config/`（输出重定向日志文件）
- 不改既有 dock_edit.go/dock_setactive.go 行为

## decision_refs
D8（节级整写）、ADR-0022

## review_blocks
无
