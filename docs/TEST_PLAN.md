# Ferryman 测试计划

> 版本：2026-09-16 · 对应 docs/DESIGN.md v3
> 分级：**P0** = 切 enforce / 日常使用前必须通过；**P1** = 定型期前；**P2** = 观察与打磨
> 三类：**[自动]** Claude 可独立执行（pytest/脚本，可重复）；**[半自动]** Claude 搭好环境与脚本、用户执行 1-2 个动作；**[人工]** 需要用户真实使用与主观判断

## 0. 已具备的测试资产（勿重复建设）

| 资产 | 覆盖 | 状态 |
|---|---|---|
| `.e2e/` 冒烟（gitignored） | 闸门状态机全分支（5/6/7/降级/!!/401/restore/stats）+ 真实 27B 摆渡 9.8s 闭环 | ✅ 已通过（一次性） |
| `ferryman eval` 三层标准 | local 候选：③ 续接 15/15、防注入、SLA 校准 | ✅ 已通过 |
| `reports/e0a|e0b` | TTL 拐点与 Codex 数据 | ✅ 已出数 |
| pyproject dev 依赖已含 pytest | — | 待写用例 |

**本计划要做的是**：把一次性冒烟固化为可回归的 pytest；补上从未被测过的路径（钩子×真实 CC、CC Switch 存活、Codex 信任流、通知、背压、降级）。

---

## 1. [自动] 单元测试（pytest，`tests/` 目录，全离线，秒级）

| # | 级 | 对象 | 用例要点 | 验收 |
|---|---|---|---|---|
| T01 | P0 | `config.validate` | summarize≥block 拒启；差<120s 拒启；非法 gate_mode；poll≤0；relax_min_gap 仅放宽差值、相等仍拒；`FERRYMAN_CONFIG` 环境变量路径生效 | 每条非法配置抛 ValueError 且消息含原因 |
| T02 | P0 | `extract.token_estimate` | 纯 CJK=1/字；纯 ASCII≈chars/3.5；混合；空串 | 误差 ±1 |
| T03 | P0 | `transcripts` / `codex_transcripts` | 坏 JSON 行/缺 timestamp/缺 usage 跳过不抛；`Z` 后缀与无时区均归 UTC；ai-title 提最后一个；codex input 含 cached 语义校验（cached>input 计数） | 全部静默跳过，返回条目时间单调 |
| T04 | P0 | `extract.extract` | 骨架：files 按次数降序、命令去重+160 截断；正文：tool_result 整块丢弃、只取 text 块、单条 4000 截断；peak_ctx 取 usage 三项之和最大值 | 构造 fixture jsonl 逐字段断言 |
| T05 | P0 | `ledger` | 同 transcript_path 换 session_id → lineage 继承闲置史；正/反斜杠+大小写路径互通（`get_by_path`）；mtime< daemon_start → observed_active=False（lookback=0） | 3 组断言 |
| T06 | P0 | `store` | valid_handoff：agent+cwd 双键（错配不认）、covers_until 60s 容差、stale/pending 不算、24h 窗口外不算、多份取 covers_until 最大；pending_prompt 超 500token 截断；mark_injected 去重；写后无 `.tmp` 残留（原子性） | 逐条断言 |
| T07 | P0 | `server.FerryDaemon.gate`（mock ledger/store/enqueue） | 全分支：强续/!!bypass→no-ledger→mode off/observe→非窗口 allow→分支5(有效交接 block+存待续+三步指引文案)→分支6 连续 3 次 block 后第 4 次降级→分支7 警告一次+置 pending+入队；pending 新周期清除（用户回来又走 summarize 时长）；pending 24h TTL | 状态机表驱动用例 ≥12 条，每分支至少 1 正 1 反 |
| T08 | P1 | `eval.verify_handoff` | 后缀匹配、父目录合法、`...` 截断前缀匹配、host 形态（127.0.0.1）跳过、真幻觉必捕 | 5 用例 |
| T09 | P1 | `install`（tmp_path 假 settings.json） | 追加不覆盖既有（模拟 Orca 条目）；幂等（跑两次只有一份）；备份文件生成 | 3 断言 |

## 2. [自动] 集成测试（pytest，临时目录 + 秒级阈值，mock 或本地 provider）

| # | 级 | 场景 | 方法 | 验收 |
|---|---|---|---|---|
| T10 | P0 | 全链路（固化 `.e2e`） | 合成会话 → 起 daemon（子进程/线程）→ 等摆渡 → /gate 分支5 → /restore 注入含待续 prompt → /stats 计数；provider 用 mock（回放固定交接）保证离线可重复 | 全链路 ≤30s 跑完，CI 可回归 |
| T11 | P0 | skeleton 降级 | provider 指向死端口 | 摆渡失败→骨架交接落盘（status=skeleton）→gate 仍走分支5（不变量不破） |
| T12 | P0 | 摆渡墙钟超时 | mock provider 睡 500s | 480s 到点强杀→降级骨架→worker 不死、下一任务正常 |
| T13 | P1 | 队列背压 | maxsize=1 + 连续 3 任务 | 满则"延迟"日志、进程不崩、下轮轮询重试成功 |
| T14 | P1 | 懒富化单次性 | monkeypatch 计数 `extract` 调用 | 同一 last_write 版本只读盘 1 次（防轮询反复大开文件） |
| T15 | P1 | 鉴权与健康 | 错 token 401；正 token 200；构造"1h 内有写入但 gate 零调用" → /stats health_alert=true；无写入时 false | 4 断言 |
| T16 | P0 | **钩子脚本 × 测试 daemon**（PowerShell 真跑） | `powershell -File hooks/ferryman-gate.ps1` 喂 mock stdin：block 响应 → stdout 为合法 CC JSON（`additionalContext` 必须嵌 `hookSpecificOutput`）；allow+警告 → 嵌套结构正确；**daemon 停机 → exit 0 无输出（fail-open）**；`FERRYMAN_DISABLE=1` → exit 0 且不产生任何网络调用 | 每条断言退出码+stdout JSON 结构 |
| T17 | P0 | restore 钩子 | source=resume/compact → 无输出退出；clear → 输出注入上下文；daemon 死 → exit 0 | 3 断言 |
| T18 | P1 | 20MB 级大会话 | 用真实大会话副本（本地，不入库）跑 L0+分块 | L0 完成 <2min、材料缩比 ≥2×、不 OOM |
| T31 | P1 | 悬空 tool_use 判定 | 尾部窗口集合差（tool_use id − tool_result id）；单测 7 例 + 守望级"悬空不入队/对照入队" | 悬空→推迟（不置 handed_off），matched/text-only/空文件→静止；窗口切割漏判可容忍（covers_until 兜底） |
| T32 | P1 | 子代理生命周期钩子 | 探针（2026-09-16 实测 CC 2.1.273：前台/后台/嵌套两层均触发）+ 计数单测 + 守望"计数>0 不入队" + /subagent 端点 + ps1 真跑 + install 注册断言 + 端到端（真实钩子→daemon events_total） | 计数>0→推迟；嵌套各计一次；重复 stop 钳 0；1h 泄漏防护；`subagents/` 转录不登记；daemon 重启→T31 兜底 |

## 3. [半自动] 需要你执行 1-2 个动作（我负责搭环境、写校验脚本、读结果）

| # | 级 | 场景 | 你要做的 | 我做的 | 通过标准 |
|---|---|---|---|---|---|
| T19 | **P0** | **真实 CC 拦截体验** | 我 `install-cc` 后：① 开真会话干点活 ② 闲置超阈值（配置可调短验证）③ 回来发一句"继续" | 监控 daemon 日志与 index，比对 transcript 无新增行（拦截零成本，DESIGN §9-3） | 被拦、文案可读、`/cost` 无变化、transcript 零新增 |
| T20 | P0 | 被拦 prompt 抹除体验（坑#4） | 被拦后 TUI 上翻，看原句能否找回 | — | 找回方式写进 block 文案（或确认找不到） |
| T21 | **P0** | **CC Switch 存活（§3 地雷）** | 在 CC Switch 里切一次供应商 | 前后跑检测脚本比对 settings.json 钩子在位 | 钩子存活=通过；被抹 → 立即做模板同步 |
| T22 | P0 | /clear 归还（真实） | 被 T19 拦后手敲 /clear 开新会话 | 检查新会话 transcript 首轮是否含注入上下文+待续 prompt | 新会话第一轮就知道"刚才干到哪" |
| T23 | P1 | Codex /hooks 信任流 | 我写好 Codex 钩子配置后，你在 Codex TUI 里 `/hooks` 完成信任 | 比对被拦 turn 前后 rollout 文件（应零新增） | 被拦 turn 零 token、不写 rollout |
| T24 | P1 | E1 横评定路由 | 把 DeepSeek/GLM key 填进 `~/ferryman/config.toml`（照 config.example.toml） | 跑 `ferryman eval --provider deepseek/glm` 出横评表 | 三候选三层标准齐全 → 拍板默认路由 |
| T25 | P1 | claude-notify 接入验证 | 手机收 Pushover / 桌面看 Toast | ~~实现接入~~✅（2026-09-16 夜：`notify.py` 双通道 + 单测/集成 84 绿 + Toast 真机验证 OK + daemon 已启用 `[notify] enabled`）→ 白天补：真实 block 触发后手机收 Pushover | 两通道至少一路收到且文案带交接路径 |

## 4. [人工] 长周期观察（无法替代真实使用）

| # | 级 | 内容 | 观察点 |
|---|---|---|---|
| T26 | P0（一周） | **observe 模式真实使用一周** | 误伤率（活跃会话被警告的频次）、警告文案是否烦人、健康告警是否误报/漏报 |
| T27 | P1 | 经济学实测 | 被拦后 /clear 续接 vs 「强续」放行续聊，各 3-5 次：额度消耗感知对比（订阅制只能看体感与速度） |
| T28 | P1 | 并行/多 worktree 归还 | 同目录多会话同时被拦 → /clear 后多候选清单体验；A/B 交接是否会错配（清单应可见可纠正） |
| T29 | P2 | 4090x2 gateway 加固回归 | 加 key/TLS 后 `providers.local` 连通性与 E1 结果不回归 |
| T30 | P2 | 极端路径 | 30 天后归档清理正确；daemon 连跑 7 天内存平稳（无 fd/句柄泄漏）；机器睡眠唤醒后闲置判定仍正确 |

---

## 5. 执行顺序与出口准则

```
第一批（自动·半天）  T01-T09  单元            → 出口：全绿，状态机 12+ 表驱动用例
第二批（自动·半天）  T10-T18  集成+钩子        → 出口：全绿；钩子 fail-open 实证
第三批（半自动·一次） T19-T22 真实 CC 四连      → 出口：拦截零成本 + 归还闭环 + CC Switch 存活
第四批（半自动·按需） T23-T25 Codex/横评/通知   → 出口：路由定案 + Codex 灰度开闸
持续（人工·一周）    T26-T30                   → 出口：observe 误伤率可接受 → 切 enforce
```

**enforce 切换的硬门槛**：T01-T22 全绿 + T26 一周观察无异状。

## 6. 已知不在本计划内（延期项）

开机自启与断电恢复（T30 部分覆盖）、Pi/dsh 适配器、LiteLLM 代理兜底——见 DESIGN §8。
