# 票 01 · W1a 两方言对照表(翻译车道的规格)

## What to build
通读本仓 .scratch/cc-switch-ref/ 下的参考实现源码(Rust,src-tauri/src/proxy/:forwarder.rs、providers/transform_codex_anthropic.rs、providers/streaming_codex_anthropic.rs、providers/codex_responses_sse.rs、providers/transform_responses.rs 及相关 converter),产出 OpenAI responses 方言↔Anthropic messages 方言的**完整对照表**文档:`docs/20260930_渡口翻译车道对照表.md`。此表是票 03(翻译车道实现)的唯一规格,评审先审表后审码。

必含五节:
1. **请求体映射**(responses→Anthropic messages):model、instructions/system、input/messages、tools 与 tool 定义形状、tool_choice、reasoning/effort、stream、store、以及参考实现处理的其余字段;未知字段的透传策略(透传不丢弃)。
2. **SSE 事件流映射**(Anthropic SSE→responses SSE):message_start/content_block_start/content_block_delta(含 tool_use 流与 thinking 流)/content_block_stop/message_delta/message_stop/usage/ping,以及错误事件;事件序与 ID 对应关系。
3. **鉴权与头**:入站占位凭证剥除、出站真实密钥注入的位置;Content-Type/长度等头处理。
4. **端点变体清单**:POST /responses、GET /responses/{id} 及参考实现识别的全部变体路径与各自处理。
5. **model 字段改写规则**(必含,F6):CC 三档位(default/opus/sonnet/haiku)与 codex 主模型按当前活跃供应商的模型位映射逐请求改写;未知模型名=透传+记告警事件,不静默替换;GLM 目标注入 thinking:disabled 的落点(ADR-0015)。

每行结论标注来源(参考实现 文件:行);参考实现没有、需要 Ferryman 自行决定的差异点,单列一节注明"参考实现无此处理,本仓决定=…"。

## 验收标准
- [ ] 对照表五节齐全,落在 docs/20260930_渡口翻译车道对照表.md
- [ ] 每行映射有参考实现出处(文件:行);无臆造条目(仓库 CLAUDE.md 规矩:中转怪癖禁止猜测)
- [ ] "model 字段改写规则"节完整(三档位+codex 主模型+未知名策略+thinking:disabled 落点)
- [ ] 端点变体清单穷尽参考实现识别的全部路径
- [ ] 差异点(参考实现未覆盖、本仓自行决定)单列且每条有理由

## Blocked by
无,可立即开始

## 涉及路径
- docs/20260930_渡口翻译车道对照表.md(新建)

## 副作用声明
无测试/构建副作用(纯文档票;验收=内容核对)

decision_refs: D2, D6, D8
review_blocks: F6
