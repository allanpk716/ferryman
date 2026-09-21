# 票 03 · 余额行随上游(去硬编码)

## What to build

面板与 `/stats` 的余额行按当前 active 上游条目的 balance_url 查询:条目配了 balance_url 就显示对应供应商余额,没配就不显示该行且不报错——替换现状的硬编码智谱端点。智谱预置条目自带 bigmodel 余额端点,行为与现状一致;kimi/deepseek 预置不配,自然无余额行。

## 验收标准

- [ ] active=智谱(预置含 balance_url)→ 余额行显示,取数端点为该条目配置值
- [ ] active=kimi/deepseek(未配 balance_url)→ 无余额行、无报错、无对智谱端点的请求
- [ ] 给任一条目配置 balance_url 后即显示
- [ ] 旧的全局硬编码智谱余额端点不再被引用(DefaultDockBalanceURL 移除或仅作为智谱预置的生成来源)
- [ ] `go test ./internal/daemon/... ./internal/viewer/...` 全绿

## Blocked by

01

## 涉及路径

- internal/daemon/
- internal/viewer/

## 副作用声明

- 独占验证命令:`go test ./internal/daemon/... ./internal/viewer/...`
- 余额查询不得在测试中真实外呼(用 httptest 假端点)

decision_refs: D11
review_blocks: 无
