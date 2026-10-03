# 票09 · model_map 增 pi 主模型键 + pi 可用性位(config 层)

## What to build
渡口上游表条目扩展:①model_map 增 pi 主模型键(与既有 codex 键并列;解析进 DockUpstream 结构与校验——非本地条目该键可选,值须在值域);②上游条目增 pi 可用性位:对标 CodexAvailability 的"显式否决位+按 dialect 推导"模式——`pi = "unsupported"` 显式否决(仅此一值,其余缺省按 dialect 推导:anthropic 方言=可用,openai_responses=不可用);③config.example.toml 同步文档化两键(含智谱条目示例:codex 键+pi 键);④provider list 输出行补 pi 三态可用性显示(不支持/可用),与 codex 行并列。本票只做 config 层与展示,switch/apply 行为语义在票12。

## 验收标准
- [ ] model_map 解析/校验含 pi 键;表驱动单测覆盖(合法/非法值/缺省)
- [ ] pi 可用性位解析+推导单测(unsupported/anthropic 推导可用/openai_responses 推导不可用)
- [ ] config.example.toml 两键文档化
- [ ] provider list 行显示 pi 可用性(单测钉输出形态)
- [ ] go test ./internal/config/ ./cmd/ferryman/ 绿;旧行为零变化(无新键时推导不改变现有条目语义——现有 anthropic 条目对 pi 推导为可用,但 apply/switch 未接前无行为面,仅展示)

## Blocked by
无,可立即开始

## 涉及路径
internal/config/dock_upstream.go
internal/config/dock_upstream_test.go
internal/config/config.go(如解析入口在此)
config.example.toml
cmd/ferryman/provider.go(仅 list 行)
cmd/ferryman/provider_test.go

## 副作用声明
无独占验证命令;go test ./internal/config/ ./cmd/ferryman/

## decision_refs
D16;F2(config 侧)

## review_blocks
无
