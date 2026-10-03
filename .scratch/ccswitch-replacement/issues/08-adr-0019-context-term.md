# 票08 · ADR-0019 落盘 + CONTEXT.md 补"agent 配置面"术语

## What to build
①docs/adr/0019-takeover-ccswitch-go-translation.md(新建):决策=接管 cc-switch,翻译层并入 Go 渡口,不转 Rust。内容含:背景(cc-switch 双形态=本地代理+配置切换器,Rust 约四万五千行;Ferryman 已有 codex 翻译车道/外科写入器/doctor/热切换)、决策(全 Go;pi 走 anthropic-messages 复用 CC 车道;provider 族为统一配置面)、备选对比(独立 Rust 翻译服务=多进程多运维,回到两层代理老问题;daemon 整体转 Rust=全量重写毁掉 soak/灰度/换装演练全部验证结果)、后果(cc-switch 卸载级替换;非 anthropic 方言进远期池)、**不设重开触发条件——翻案须立新 ADR 推翻本篇**;②CONTEXT.md 新增"agent 配置面"术语小节:与既有"渡口上游/摆渡供应商/服务商接管"词条并列——定义为"provider 族命令对 CC/codex/pi 三类 agent 宿主配置的统一管理面(apply 四份目标/switch 热切换/doctor 生效链体检)",不与渡口上游表混淆;③在票07 建好的 docs/adr/README.md 索引里补 0019 条目。

## 验收标准
- [ ] ADR-0019 含背景/决策/备选对比/后果/无重开触发条件五要素,术语合 CONTEXT.md 词汇表
- [ ] CONTEXT.md 新小节不与既有词条冲突(渡口上游/摆渡供应商/服务商接管)
- [ ] adr/README.md 索引含 0019
- [ ] 纯文档

## Blocked by
票07(索引先建,0019 补入)

## 涉及路径
docs/adr/0019-takeover-ccswitch-go-translation.md(新建)
docs/adr/README.md(补一行)
CONTEXT.md

## 副作用声明
无验证命令;纯文档

## decision_refs
D1、D2、D4;ADR-0019

## review_blocks
无
