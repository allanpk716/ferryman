# 票 03 · 渡口 responses 翻译车道(核心)

## What to build
按票 01 的对照表(docs/20260930_渡口翻译车道对照表.md)在渡口实现 responses 车道:

1. **路径分流**:渡口 HTTP 入口按路径分车道——/responses 及对照表第 4 节列的全部变体→responses 车道;/v1/messages 既有车道零变化。
2. **请求向翻译**(responses→Anthropic):按对照表第 1 节逐字段;model 按当前活跃供应商模型位逐请求改写(对照表第 5 节),未知模型名透传+记告警事件;GLM 目标注入 thinking:disabled(ADR-0015);未知字段透传不丢弃。
3. **响应向翻译**(Anthropic SSE→responses SSE):按对照表第 2 节(含 tool_use 流、用量、错误事件)。
4. **鉴权**:入站占位凭证剥除、出站真实密钥注入(与既有车道同机制、同 T39 纪律:真钥永不入日志/账本/错误)。
5. **原生透传分支**:dialect=openai_responses 的活跃供应商——同车道但不译,仅鉴权注入+model 改写。
6. **不支持分支(F11)**:活跃供应商 codex="unsupported" 时 codex 请求到站**即刻返回带原因的显式错误**(HTTP 错误+清晰文案),不挂起、不静默超时。
7. **记账仅事件+账本四列**:codex 车道不接快照/摆渡钩子(D7);账本行带 lane 标注。
8. 表驱动单测:测试用例从对照表逐行生成(请求向/响应向/流式事件序/错误形态);实现与表冲突=实现错。

## 验收标准
- [ ] 路径分流:responses 变体全进新车道,/v1/messages 行为既有测试全绿零改动
- [ ] 请求向/响应向翻译按对照表逐项有表驱动断言(含 tool_use 流与错误事件)
- [ ] model 改写按活跃供应商映射;未知名透传+告警事件有断言
- [ ] GLM 目标 thinking:disabled 注入有断言
- [ ] 原生透传分支(不译只注鉴权+改 model)有断言
- [ ] 不支持分支返回带原因显式错误(非挂起/超时)有断言
- [ ] 记账=事件+四列,无快照/摆渡调用
- [ ] go test ./internal/dock/... 全绿;go vet 净;真钥零出现(输出/日志/错误)

## Blocked by
票 01(对照表=规格), 票 02(schema+逐请求 seam+热切换端点)

## 涉及路径
- internal/dock/(server.go 分流接线;新车道文件 translate_*.go + tests)
- 既有 /v1/messages 路径上的文件(rewrite.go/snapshot.go/guard.go/headers.go 等)只读不改

## 副作用声明
- 独占验证命令:go test ./internal/dock/...(含流式用例,预算充足;禁联网)
- 不碰生产进程/配置

decision_refs: D2, D7, D8, D11
review_blocks: F2, F4, F6, F11
