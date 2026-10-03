# 票07 · 遗留改正 + ADR 索引 + 风险哨兵表

## What to build
三组清欠:①就地改正:docs/DESIGN.md §4 技术栈段改 Go/15700(守护)/15900(面板)/15722(渡口)现状;docs/TEST_PLAN.md 头部标注"已被 go test ./... 口径取代(2026-09-19 Go 切换)"并保留原文;internal/config/config.go 与 internal/cutover/rollback.go 注释里的旧端口 7311 改 15700(仅注释,不改代码行为);②docs/adr/README.md(新建):ADR 索引——逐条编号+标题+一句话主旨,消歧 0016×2(same-model ferry / widget geometry)与 0018×2(quiet-gate / settings-window),用别名注记不改文件名;③docs/20261002_剩余风险与哨兵.md(新建):风险×状态(已闭环/一次性/仍开放)×炸了会怎样×哨兵(日志关键字/doctor 项/账本指标);必含行:换装首跳旧茬(一次性,doctor update_residues)、codex 供流切换期回抢风险(观察窗+doctor provider_codex_dock)、智谱配额归一压力(短期唯一应急=全局手动切换 provider switch;按 agent 绑定上游/凭证是远期设计决策非现状能力)、cc-switch 卸载前提(基线只用智谱 anthropic 方言;将来 codex 上非 anthropic 上游须先补方言能力)、判热钟已持久化(闭环)、渡口排水窗 180s(哨兵=update.log)。

## 验收标准
- [ ] DESIGN.md 无 Python/7311 表述;TEST_PLAN.md 有取代标注
- [ ] 7311 注释清零(grep internal/ 无残留,历史文档除外)
- [ ] adr/README.md 覆盖 0001-0019 全部条目且撞号消歧
- [ ] 风险表含上述必含行,每行有哨兵
- [ ] 纯文档+注释改动,go build ./... 不受影响

## Blocked by
无,可立即开始

## 涉及路径
docs/DESIGN.md
docs/TEST_PLAN.md
docs/adr/README.md(新建)
docs/20261002_剩余风险与哨兵.md(新建)
internal/config/config.go
internal/cutover/rollback.go

## 副作用声明
无验证命令;注释级改动后跑 go build ./... 确认

## decision_refs
D10、D11、D13;F4/F5 风险表两行

## review_blocks
无
