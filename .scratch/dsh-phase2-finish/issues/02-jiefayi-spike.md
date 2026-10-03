# 票 02 · 接法乙代价 spike（只读评估报告）

## What to build
只读评估"把 dsh 接管路由从 pi-ai 自定义路由换成 llm-deepseek 适配器"（换取 `x-deepseek-harness-session-id` 归因头上线）的渡口侧代价，产出报告 `.scratch/dsh-phase2-finish/spike/jiefayi-report.md`：
①DeepSeek 方言清单——llm-deepseek 适配器相对标准 anthropic-messages 的私有扩展（请求头/参数/响应字段），逐条列出处（源码 file:line）；
②渡口剥除/改写工作量评估——每条方言在渡口改写面（internal/dock/）的处理方式与量级（行级估算）；
③与现 pi-ai 路的行为差异（鉴权、流式、缓存语义、错误面）；
④结论——代价量级（小时级/天级）＋建议（切/不切/条件切）。
源码依据：调研克隆（只读）＋本仓 internal/dock/ 现行实现。**不改任何业务代码、不碰路由、不写 ~/.dsh。**

## 验收标准
- [ ] 报告落盘且四节齐全（方言清单/工作量/差异/结论建议）
- [ ] 方言清单逐条带源码 file:line 出处
- [ ] 工作量评估具体到渡口改写面的模块与条目，可被晨间直接引用拍板
- [ ] 全程零业务代码改动（git status 干净可证）

## Blocked by
无，可立即开始

## 涉及路径
.scratch/dsh-phase2-finish/spike/

## 副作用声明
纯只读调查（源码克隆与本仓）；无测试/构建。

## decision_refs
D7（接法乙＝只读 spike＋人拍板）、D3（不切路由红线）

## review_blocks
无
