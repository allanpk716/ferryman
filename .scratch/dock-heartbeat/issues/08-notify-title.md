# 08 · pushover 通知文案：项目名＋会话标题降级链

## What to build

1. **标题构造器**（internal/notify）：`BuildTitle(project, sessionTitle, firstQuestion string) string` → `Ferryman｜<项目名>：<会话标题>`；降级链：会话标题空→用首问截断（如 24 rune，省略号结尾）；首问也空→仅 `Ferryman｜<项目名>`。中文安全截断（rune 级，不切半个字）。
2. **调用方接线**：找到现有 pushover 通知调用点（摆渡完成/拦截/告警等 NotifyAlert 调用处），把裸 session id 或旧标题换成 BuildTitle 输出；session id 移到**正文尾部小字**（如 `\n(sid=xxx)`）。会话标题来源：台账已有标题字段（ai-title 提取已在台账链路，读 ledger.SessionState 现有字段名）；首问来源若台账没有现成字段，从最近会话 jsonl 提取的成本过高则降级链允许"仅项目名"路径覆盖该场合——以现有数据可得性为准，不新建 jsonl 解析。
3. **管道加宽**：SendPushover 的 title 长度约束核对（Pushover 上限 250 字符，标题构造器输出封顶 100 rune）；notify_test 钉死三种降级形态的逐字输出。

## 验收标准

- [ ] BuildTitle 三形态逐字断言：有标题/无标题有首问/两者皆无；截断不切半个汉字（构造 30 汉字标题案例）。
- [ ] 至少一个真实调用点接线测试：通知标题不再含裸 session id；正文尾部含 sid 小字。
- [ ] 既有 notify 测试零回归（除文案断言按新形态更新）。
- [ ] `go test ./internal/notify/... ./internal/daemon/...` 全绿。

## Blocked by

无，可立即开始。

## 涉及路径

- internal/notify/notify.go、notify_test.go（BuildTitle＋管道）
- internal/daemon/ 中 NotifyAlert 调用点所在文件（gate/告警路径；**只动通知文案构造，不动判定逻辑**）

## 副作用声明

- 默认只跑：`go test ./internal/notify/...`＋接线文件所在包测试。

decision_refs: D8
review_blocks: 无
