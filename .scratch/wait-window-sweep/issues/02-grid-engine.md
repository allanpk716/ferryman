# 票02 · 网格生成 + 模拟引擎 + 评分

## What to build

把装载好的窗集合在**参数网格 × TTL 场景轴**上重放评分（端到端行为：输入窗数据集+价格表+config ttl_s，输出每档参数组的净节省/跳数/成本/熔断与推荐）。

功能点：
- **主网格（可行域，唯一产生行动建议）**：ttl_s 档位 = config 当前值 × 乘数 {0.5, 0.6, 0.7, 0.8, 0.9, 1.0, 1.1, 1.2, 1.4, 1.6, 1.8, 2.0}（12 档全集）；每档经 `policy.Compute(ttl_s′, prices)` 生成 (τ, 首跳时机, 等待上限)
- **对照列按 TTL 场景档重算**：每个 TTL 档 TTL′ 的对照基线 = `policy.Compute(TTL′, prices)`，差距表同档内对比
- **诊断网格（非可部署）**：τ/cap 乘数同表枚举、首跳锚 {开窗即跳, τ/2, τ}、cap 锚 {×0.5, ×1.0, ×2.0, ∞}；结果单列标注"诊断用"
- **逐窗模拟**：TTL 场景三档（config TTL / −1/3 / −1/2）；跳序列按时点推进；熔断照生产 `internal/beat.Breaker` 语义（连续 miss≥2 停跳）；**复用公式单源**——成本/节省调用 `internal/policy`/`internal/report` 既有函数，不得出现第二份实现
- **评分**：净节省 = Σ(避免重付 − 心跳花费)（成效账版本化反事实公式）；无效保温单列
- **tie-break**：一切排序键并列时按参数向量字典序取最小（τ → 首跳 → cap 升序）
- **确定性**：同输入两次运行结果逐字节一致

## 验收标准

- [ ] 主网格档位表恰为 12 乘数档，每档参数组来自 policy.Compute（断言接线，非本地重算）
- [ ] 对照列在每个 TTL 档重算（τ=safety×TTL′ 同步变化，断言）
- [ ] 诊断网格 τ/cap 乘数与主网格同表全枚举；首跳/cap 锚点齐全；结果带"非可部署"标记
- [ ] 熔断：TTL 场景档短于间隔的夹具窗，断言模拟停跳（miss≥2）
- [ ] 金样本：已知 dur/prefix 的合成窗，净节省与成本等于手算闭式值（测试内注释手算过程）
- [ ] tie-break：构造净节省并列的夹具，断言取 (τ,首跳,cap) 字典序最小者
- [ ] 同输入两次运行输出逐字节一致
- [ ] 全仓 grep 无第二份成本/节省公式实现（引用计数：policy/report 既有函数）
- [ ] `go test ./internal/backtest/ -count=1` 全绿；`go vet` 净
- [ ] StrategyTable 回归：`go test ./internal/report/ -count=1` 既有测试保持绿（未改动公式单源）

## Blocked by

01

## 涉及路径

- internal/backtest/grid.go（新建：主网格/诊断网格/对照列）
- internal/backtest/engine.go（新建：逐窗模拟/熔断/评分/tie-break）
- internal/backtest/engine_test.go（新建）

## 副作用声明

跑 `go test ./internal/backtest/ ./internal/report/` 与 `go vet`；无端口无联网。

## decision_refs

D2（净节省+无效保温单列）、D3（TTL 单点+三档场景轴）、D6（三参范围）、D8（熔断复用 Breaker）、D11（摆渡/闸门背景重放——引擎不模拟摆渡）、D12（差距表+校准建议出口）

## review_blocks

F1（网格构造 seam 的票级复核点：主/诊断双网格分工、可行域行动出口闭合、tie-break 字典序取最小、诊断网格全集枚举）
