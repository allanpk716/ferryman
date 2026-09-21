# 票01 · 类型契约 + 数据装载

## What to build

`ferryman backtest` 的数据地基（端到端行为：给定一个账本目录与过滤 profile，产出内存中的窗数据集与全套计数，供引擎/报告/CLI 三层消费）。本票同时定义**贯穿全特性的契约类型**（窗、数据集、装载计数、扫参结果、场景结果、网格点），票02/票03 只依赖本票类型不互相依赖。

功能点：
- 从 `<data_dir>/accounts/*.jsonl` 读全部 `kind=window` 行（纯元数据：opened_ts/closed_ts/dur_s/prefix_tokens/close_reason/agent/session_id/project）
- project 为空时经同 session_id 的 usage 行连接还原，**确定性优先级**：主会话行（subagent=""）> 子代理行；同级多个不同 project 取时间最新一条；平局按字典序**取最小**
- 无可连接行 → "unknown" 桶：照登、计数单列；受 `--exclude` 显式排除约束，**不被 `--projects` 正集包含**
- `--projects` / `--exclude` glob 过滤（默认全量）；输出过滤前后窗计数
- 按时间对半切留出集（前半选参 / 后半只验证；两半窗数差 ≤1，切分边界确定）
- provider 缺 `P_cache` 的窗入"不可算"桶照登并计数
- 计数结构含：总窗数、过滤后窗数、未还原数、多值匹配 session 数、unknown 桶数、不可算桶数、close_reason 分布（过滤前后两口径）、装载时点戳

## 验收标准

- [ ] 临时账本夹具（自构 window/usage JSONL）装载出窗集合与计数，数字与夹具设计值一致
- [ ] project 还原确定性三组夹具各断言：主行优先 / ts 最新优先 / 平局字典序取最小
- [ ] no-match 窗进 unknown 桶且不被 --projects 正集包含、被 --exclude 命中时剔除
- [ ] --projects/--exclude glob 生效，过滤前后计数正确
- [ ] 时间对半切：两半窗数差 ≤1；同输入两次装载切分结果一致
- [ ] P_cache 缺省 provider 的窗入"不可算"桶并计数
- [ ] `go test ./internal/backtest/ -count=1` 全绿；`go vet ./internal/backtest/` 净
- [ ] 无任何本机路径/项目名硬编码（数据目录经参数/config 传入）

## Blocked by

无，可立即开始

## 涉及路径

- internal/backtest/types.go（新建：契约类型）
- internal/backtest/load.go（新建：装载/还原/过滤/切分/计数）
- internal/backtest/load_test.go（新建：夹具与断言）

## 副作用声明

只跑 `go test ./internal/backtest/` 与 `go vet ./internal/backtest/`；无端口、无联网、不触碰 ~/ferryman 生产账本（测试全部用临时目录夹具）。

## decision_refs

D4（数据源①记录窗）、D16（通用性/过滤参数化）、D17（留出集）、D18（project 还原）、D19（P_cache 不可算桶）

## review_blocks

F4（round1 算术回归的票级复核点：报告与 JSON 的全部窗计数必须来自实跑现算并输出时点戳——规格文本数字仅时点快照，不得硬编码进代码或测试期望值）
