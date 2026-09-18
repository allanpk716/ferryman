# 账本成本结构分解（cost structure of the ledger）

对 `~/ferryman/accounts/*.jsonl` 的 usage 流水做成本结构分解：各分量（cache_read / input / creation / output）占多少、单请求前缀分布、全款重付事件数、族系集中度。用途：

1. **压缩赛道评估**（2026-09-18：82% 花费在 cache_read——见 `docs/research/20260918_billion-context持续压缩调研.md`）；
2. 压缩试点（bili / /compact 节律）前后对比——同脚本跑两次，看 cache_read 份额变化；
3. 闲置机制（心跳/闸门/摆渡）的账单占比跟踪（当前 4%）。

## 用法

```bash
python experiments/cost-structure/ledger_cost_structure.py            # 默认 ~/ferryman/accounts
python experiments/cost-structure/ledger_cost_structure.py --since 2026-09-01
python experiments/cost-structure/ledger_cost_structure.py --prices 6.9 1.7 24 10000
```

`--prices P_in P_cache P_out per`：换价格表口径（默认 GLM v2026-09-17：6.9/1.7/24 每 10000 token）。只读，不写任何文件。

## 基准读数（2026-09-18，2026-07-30~09-18，66,160 次请求）

| 分量 | 份额 |
|---|---|
| cache_read（重复读历史） | **82%** |
| input+creation（新内容） | 12% |
| output | 6% |
| 全款重付事件（input≥50k 且 cache_read<10k） | 723 次 ≈ 4% |

单请求 cache_read：均值 219k / p50 194k / p90 412k / p99 588k / max 1110k。
