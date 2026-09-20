# WebUI 动效改进——实施计划索引

审计基线：commit `8cfffc1`（improve-animations 八类全扫，2026-09-20）。
约束：纯 vanilla（离线铁律）、DOM 纪律、公式单源、不渲染消息内容、克制仪表盘风。

| # | 计划 | 严重度 | 状态 | 批次 |
| --- | --- | --- | --- | --- |
| 001 | 动效 token 基建 | LOW | DONE | 批0 |
| 002 | 时间线视窗补间 | MEDIUM | DONE | 批1 |
| 003 | 反跑结果条生长 | LOW | DONE | 批1 |
| 004 | tooltip 渐显渐隐 | LOW | DONE | 批1 |
| 005 | 按压/hover/淡入反馈批 | LOW | DONE | 批2 |

2026-09-20 实施完成；独立评审（feature-dev:code-reviewer，规格=本目录五计划）：
重点问题 a–g 全 PASS，硬约束八项全过，确认缺陷 **0**。机械验证：node --check ✓
go build ./... ✓ tip.hidden 残留 0 ✓。剩：用户 --demo 实机过目（最终验收）。

执行顺序：**001 必须最先**（002/003/005 引用其 token）；002~005 相互独立可任意序。

未立项（审计否决，勿翻案）：数字滚动、柱子入场生长、列表 stagger、表格行按压、
details 折叠动画、缩放/拖拽过渡。C1（draw 全量重绘性能）挂起——先在真实大会话
实测，卡再做。

验收：`node --check cmd/ferryman/web/app.js` + `go build ./...` + `ferryman --demo --no-browser`
实机 feel check（各计划 Verification 节）+ 独立评审 pass + 用户 demo 模式过目。
