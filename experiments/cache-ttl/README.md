# 缓存 TTL 通用测试套件（cache-ttl）

对任意 LLM 服务商/模型实测**上下文缓存的闲置有效期**，不依赖官方文档——服务器每次响应
都会自报"这次输入有多少 token 从缓存读"（各家字段名不同，本套件已做别名兼容），
用"干等不同时长后摸一次"把掉零点夹出来。

## 回答四个问题

| # | 问题 | 测法 | 决定什么 |
|---|---|---|---|
| Q1 | 闲置 TTL：前缀放着不管能活多久 | 阶梯：每档一条独立前缀，写入→干等→只探一次 | 长等待后首发的失效代价 |
| Q2 | 命中是否续命（刷新计时） | 单前缀按 TTL~80% 间隔连摸 N 次 | 心跳保温是否可行 |
| Q3 | 有无部分命中 | 看每次 ratio 分布 | 前缀是整存整取还是分段 |
| Q4 | 跨会话前缀共享 | 不同会话用相同系统提示头 | 缓存按内容还是按会话组织 |

## 双轨：先选轨

| 轨 | 脚本 | 适用 | 判据 |
|---|---|---|---|
| **agent 轨** | `probe_pi.py` | 有"仅限指定工具"门禁的订阅套餐（Coding Plan 类） | 裸调 API 被 401/1113 拒，或要求测真实 agent 链路 |
| **CC 轨** | `probe_cc.py` | Claude Code 真实链路（前缀大、带 cache_control 断点） | agent 轨结论需在 CC 生产链路校准；CC 挂 Anthropic 官方则免测（官方已知：5 分钟、命中刷新） |
| **API 轨** | `probe_api.py` | 标准 API 计费、OpenAI 兼容端点 | 有可用 key，无工具门禁 |

pi 会把各家 usage 归一成 `{input, cacheRead, cacheWrite}`，agent 轨因此天然多服务商；
CC 轨读 `message.usage.cache_read_input_tokens`；API 轨已兼容 `prompt_tokens_details.cached_tokens`（OpenAI/Kimi/Qwen）、
`prompt_cache_hit_tokens`（DeepSeek）、`cache_read_input_tokens`（Anthropic 风格）三种字段。三轨结果字段统一，report 混读。

## 五步流程（任何服务商都按这个来）

```bash
# 0) 冒烟：单臂 12 秒 + 小前缀。关键不是结果，是确认 usage 里有缓存信号
#    （cacheRead/cached_tokens > 0）。若恒为 0 → 字段没映射或该服务商无缓存，先换轨排查
python probe_pi.py arm --gap 0.2 --chars 2000 [--provider X --model Y]
# 或
python probe_api.py arm --gap 0.2 --chars 2000 --base-url ... --model ... --api-key ...

# 1) 预检（API 轨必做）：两发小请求后去控制台费用明细，确认走的预期计费路径

# 2) 粗阶梯：1,3,5,8,12,20,30,45,60,90,120 分钟 ×3 重复（总墙钟 ≈ 最大间隔）
python probe_pi.py ladder            # / probe_api.py ladder --base-url ... --model ... --api-key ...

# 3) 边界收敛：在衰减区（部分命中的档位之间）加密档位再来一轮
python probe_pi.py ladder --gaps 10,14,16,18 --reps 3

# 4) 续命测试：interval ≈ 衰减区下限 × 0.8，连摸 6 次
python probe_pi.py refresh --interval 9 --count 6

# 汇总（两轨字段兼容，可混读）
python probe_api.py report --results "results/*.jsonl"
```

中断随时续跑（state 落盘，按实际写入时间计到期）。换天再跑一轮粗阶梯交叉验证。

## 设计铁律（每条都是防污染，别省）

1. **每臂独立前缀**：探测本身是一次命中、可能续命。复用同一段资料测出的是"能被摸活多久"，
   不是"放着能活多久"。
2. **一写一探**：每臂生命周期 = 写入 → 干等 → 只摸一次 → 报废。
3. **记 actual_gap 而非名义档位**：写入阶段耗时会推迟首个探测。
4. **×3 起步、跨天复测**：驱逐通常是 LRU + 负载敏感，结论是"间隔-命中率曲线"不是单点。
5. **阈值 0.85 + 看形态**：ratio 是 1.00/0.00 二值还是连续，本身就是结论（部分命中语义）。

## 各服务商配方

| 服务商 | 轨 | 命令要点 |
|---|---|---|
| 智谱 Coding Plan（GLM） | agent | `python probe_pi.py ladder`（本机默认经 ccswitch 代理；直连配方见下） |
| 智谱编码端点直连 | API | `--base-url https://open.bigmodel.cn/api/coding/paas/v4 --model glm-5.3 --user-agent "CherryStudio/1.5.0" --thinking-off`（有 UA 门禁风险，先预检计费） |
| DeepSeek | API | `--base-url https://api.deepseek.com --model deepseek-chat`（cached 字段为 prompt_cache_hit_tokens，已兼容） |
| Kimi / 月之暗面 | API | `--base-url https://api.moonshot.cn/v1 --model kimi-k2-...` |
| 通义 Qwen | API | `--base-url https://dashscope.aliyuncs.com/compatible-mode/v1 --model qwen-...` |
| OpenRouter | API | `--base-url https://openrouter.ai/api/v1 --model <vendor/model>` |
| 任意 pi 已配 provider | agent | `--provider <name> --model <id>`；不传则用 pi 自己的默认配置（套件因此可整体拷走） |

## 判读 → 决策映射（通用）

| 实测 | 多 agent 编排策略 |
|---|---|
| TTL ≥ 子任务典型时长 | 主控闲置基本不失效，只管 compact 时机 |
| TTL 短 + 命中续命 | "等待略超 TTL"场景可心跳保温；长闲置仍不如过期后一次性重算 |
| TTL 短 + 命中不续命 | 放弃保温；长等待前 compact/handoff 缩小前缀 |
| 二值命中 | 无部分命中可利用，前缀必须整段稳定（别在头部插时间戳/随机数） |

## 坑清单

- **门禁/UA**：订阅类套餐可能按工具特征拦裸调（agent 轨就是为绕开它）。
- **信号先行**：第 0 步冒烟若 cacheRead 恒 0，一切白搭——先解决字段/轨，再谈阶梯。
- **最小可缓存长度**：OpenAI ≥1024、Anthropic ≥1024/2048 token，前缀默认 ~17k token 已安全，别调小。
- **思考型模型**：输出会烧预算，agent 轨用 `--thinking off`（脚本内置），API 轨加 `--thinking-off`。
- **负载驱逐**：高峰期曲线会抖；精收敛轮建议串行（`--reps 1` 多跑几轮）而非并行多臂。
- **策略漂移**：服务端随时可改，报告必须标注日期+模型版本+端点；大版本后复测。
- **隐私**：`results/`、`state_*.json`、`pi-sessions/` 含前缀全文与会话记录，勿提交仓库。

## 最终报告模板

每次战役（一个服务商×模型）收尾时写一份，放项目 docs/（Ferryman 约定带日期前缀）：

```
# <服务商> <模型> 缓存 TTL 实测报告（YYYY-MM-DD）
- 测量环境：轨 / 端点 / 模型版本 / 前缀规模 / 臂数
- 间隔-命中率曲线表（含 actual_gap 范围）
- 形态结论：二值 or 部分命中；硬 TTL or 概率衰减区
- 边界：必活上界 / 必死下界 / 概率区
- 续命结论：命中是否刷新计时
- 跨会话/杂项观察
- 编排决策建议 + 复测触发条件（模型升级/策略变更）
```

## 已有战果

- **GLM-5.3（智谱 Coding Plan，2026-09-17）**：pi 轨数据在 `../zhipu-cache-ttl/results/`、CC 轨在 `results/`；
  报告：Ferryman `docs/20260917_1630_GLM缓存TTL实测与心跳保温可行性_实验报告.md`
  （结论：**≤10 分钟确定性命中（17/17）、11 分钟起即有风险（11min 2/4，12min 3/7）、≥30 分钟确定性失效，与客户端无关**；
  命中刷新计时，心跳每 ~8min 一次可行）。
