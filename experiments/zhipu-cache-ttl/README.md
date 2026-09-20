# 智谱 Coding Plan 缓存 TTL 实验（GLM 战役数据目录）

> **本目录为 2026-09-17 GLM-5.3 战役的原始数据与首发脚本。**
> 通用三轨套件已沉淀至 [`../cache-ttl/`](../cache-ttl/)（测任意服务商/模型请用那边）；
> 最终结论见 `docs/20260917_1630_GLM缓存TTL实测与心跳保温可行性_实验报告.md`。

补齐 `docs/20260916_0853_Agent会话闲置缓存失效与自动交接_技术调研.md` 里的事实空白：
智谱官方文档对缓存时效只有一句"缓存有合理的时效性，过期后会重新计算"，**没有给出任何数字**。
本实验用探针把它实测出来，为编排决策（心跳保温 vs 长 wait 前 compact）提供依据。

## 测什么

| 问题 | 方法 | 结论用途 |
|---|---|---|
| **Q-A 闲置 TTL**：前缀写入后闲置多久仍命中？ | 阶梯实验：每档间隔一条独立前缀，写入 → 干等 → 只探一次 | 判断 subagent 长任务后主控首发的失效代价 |
| **Q-B 刷新语义**：命中是否重置计时？ | 单前缀每 interval 分钟连探 N 次 | 判断"心跳保温"是否成立 |

判定信号：响应 `usage.prompt_tokens_details.cached_tokens`（满值=命中，0=失效）；
探测延迟 << 写入延迟是独立的第二旁证（miss 要全量重算 prefill）。

## 用法（Track A：直打编码端点）

```bash
cd experiments/zhipu-cache-ttl

# 0) 预检：两发小请求，然后人工去 开放平台->费用明细 确认抵扣的是【编码套餐】而非余额
python cache_ttl_probe.py preflight --api-key $ZHIPU_API_KEY

# 1) TTL 阶梯：默认 1/3/5/8/12/20/30/45/60/90/120 分钟 × 3 重复，总墙钟≈2 小时
python cache_ttl_probe.py ladder --api-key $ZHIPU_API_KEY

# 2) 汇总（命中率按档位 + 延迟对比）
python cache_ttl_probe.py report --results results/

# 3) Q-B：假设阶梯测出 TTL≈10min，用 8 分钟间隔连探 6 次（共 48min）
python cache_ttl_probe.py refresh --api-key $ZHIPU_API_KEY --interval 8 --count 6
```

- 阶梯**每档每重复用一段全新前缀**（这是实验有效性的关键：复用同一前缀会被探测续命，测出的是刷新语义而非闲置 TTL）。
- 中断可续跑：重跑同一条 `ladder` 命令，已写前缀复用、按实际写入时间计到期。
- 预算：每臂 2 发 × ~16k token 输入，33 臂全 miss 约 1M token（粗估千级积分以内），对 5 小时窗口无压力。

### 门禁与计费验证

Coding Plan 声明"仅限官方支持的指定工具与产品环境"，端点可能按 User-Agent 门禁。
脚本默认 UA 伪装 Cherry Studio（官方文档点名的受支持工具），可用 `--user-agent` 换。
`preflight` 若报 1113/401/403，先核对：Base URL 是否 `https://open.bigmodel.cn/api/coding/paas/v4`、
key 是否有效；仍被拒说明门禁比 UA 更严，需改走 Track B。**每次正式跑之前都要确认费用明细扣的是套餐**，
否则测的是标准 API 缓存，结论不迁移。

## Track B：Claude Code 真实链路复核（手动）

生产路径走 `/api/anthropic` 端点，缓存行为可能与 paas/v4 不同，结论必须在这条链路复核：

```bash
# 1) 起会话灌入 1~2 万 token 固定资料（从 Track A 的前缀生成器拷一段即可）
claude -p --verbose "以下是资料：<粘贴长文本>。请只回复 READY。"
# 记下 session id（--verbose 输出，或看 ~/.claude/projects/<cwd>/ 最新 .jsonl 文件名）

# 2) 等待 X 分钟
sleep 1800

# 3) resume 追加一轮
claude -p --resume <session-id> "资料的第一行是什么？只回复那一行。"

# 4) 打开该 session 的 .jsonl，看最后一条 assistant 消息的 message.usage：
#    cache_read_input_tokens ≈ 前缀规模 -> 缓存活着；≈0 -> 已失效
```

对 5/15/30/60 分钟各测一次，与 Track A 曲线对照。注意 cc 的 system prompt（含工具定义）
也占缓存前缀，`cache_read_input_tokens` 反映的是整条链路的命中率，属预期。

## 判读 → 决策映射

| 实测结果 | 编排策略 |
|---|---|
| TTL ≥ subagent 典型时长（≥30min） | 主控闲置基本不失效，只需管 compact 时机 |
| TTL 很短（≤10min 级） | 放弃保温；长等待前先 compact/handoff 缩小前缀，把一次性重算代价压到最低 |
| TTL 短 + Q-B 证明命中刷新计时 | 仅"等待略超 TTL"场景值得心跳保温（150k 前缀每 5min 一次 ≈ 25 积分，闲置 >20min 就不如直接过期） |

## 注意事项

- 缓存驱逐大概率是 LRU + 负载敏感，会"提前死"：结论报的是**间隔-命中率曲线**，不是二值阈值；
  正式结论建议换一天再跑一轮 `ladder` 交叉验证。
- 实验窗口内尽量不要用同一账号跑其他编码任务（无关驱逐竞争 + 额度窗口干扰）。
- 服务端策略可变：结论标注日期，隔版本（模型/端点升级）复测。
- `results/` 与 `state_ladder.json` 含前缀全文，勿提交仓库（已在 .gitignore 候选中，见下）。
