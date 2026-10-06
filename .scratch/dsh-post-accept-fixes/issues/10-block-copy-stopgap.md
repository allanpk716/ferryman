# 票10 · 拦截文案止血(F2):logger 带原话+两行指路

> D11 两步走的第一步;在票08(F1 选择框)落地前,把手机端"零反馈"缓解为"日志面板有字"。daemon 零改动。

## What to build
dsh 消息被闸门拦下时,插件写入宿主日志面板的那行 warn 从"只有拦截理由"升级为:拦截理由+被拦原话(截断)+两行明确指路(手打「强续 <原话>」留在原会话 / 新建会话自动拿回交接+原话)。

## 验收标准
- [ ] block 时 logger.warn 文案含:拦截原因(既有 reason 保留)+被拦原话(超长按既有截断纪律)+两行指路(「强续 重发你的内容可留在本会话」「新建会话(同目录)开场自动收到交接+原话」措辞可润,两件事必须都在)
- [ ] 原话来源=拦截现场 payload.messages(与票08 spike② 同主径,不新增 daemon 依赖)
- [ ] daemon 侧零改动;CC/codex 零波及
- [ ] 插件测试:断言 block 路径的 warn 文案含原话与两行指路;基线 79 不破

## Blocked by
无,可立即开始。

## 涉及路径
plugin/ferryman-dsh/src/events.ts
plugin/ferryman-dsh/test/events.test.ts

## 副作用声明
插件测试命令(plugin/ferryman-dsh/ 内既有跑法)。

## decision_refs: D11(F2)、D5(Toast 否决不变——本票是文案不是弹窗)
## review_blocks: 无
