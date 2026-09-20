# 心跳前缀源定为捕获重放：daemon 透传＋内存快照，废弃 jsonl 重构

Q14 段二零花费部分（2026-09-19，对照 `~/ferryman/captures/` 捕获与探针会话 jsonl）查明：从 jsonl 重构 wire 请求需硬编码约 6 簇与 CC 版本耦合的拼装规则——tools 键名改写（schema→input_schema）、system 13 段→3 段重组＋`x-anthropic-billing-header` 计费前缀＋cliPrefix 拼接＋缓存断点放置、`<system-reminder>` 包装模板、role=system 消息的四源拼接（hook additionalContext＋skill 清单＋token 提醒＋日期）、beta 十件套/UA 版本常量、metadata 装配——CC 一升级即静默断链（saw_async 文案判据同型教训）。同时实测两独立会话的请求体除 session_id 外逐字段全同，证明 body 无隐藏的每请求动态内容，捕获重放在数据上可行。

决定：心跳前缀源定为**捕获重放**（备胎转正）——daemon 内新增透传端口（CC→daemon→cc-switch，15721 上游），只在**内存**中保留每会话最后一份真实请求体（不落盘，隐私不变量不破：台账与账本仍永不落消息内容），心跳对其原样重放、仅改 max_tokens；CC 路由经用户级 base_url 改一行指向 daemon。（2026-09-19 补记：同日进一步定案自建渡口——上游从 cc-switch 升级为 GLM 直连＋自持改写五件，beat 改从渡口入站口重放；cc-switch 退守 Codex 轨。见 ADR-0011。）当日 Q14 段三实跳通过（3/3 臂首跳 ratio=0.998、三连跳 HIT、真值 HIT/HIT 真保温，报告 `docs/20260919_1810_Q14段三捕获重放实跳_实验报告.md`），真发送门槛就此放行（仍 opt-in、默认关，ADR-0004 红线不动）。工程约束：透传必须 SSE 立即冲刷（缓冲即重试风暴，实测教训）；transport 错误退避跳过、miss 绝不重试（ADR-0006 不变式）。

## Considered Options

- **jsonl 重构（原设计 BeatBuilder 路线）**：全部复杂度用于复刻 CC 内部序列化，版本耦合最重、无自愈手段，miss 熔断只能兜底不能预防——否决。
- **改 cc-switch 落盘请求体日志**：动第三方程序、落盘全量报文与隐私不变量冲突——否决。
- **常驻旁路抓包（网线层）**：Windows 旁路抓包复杂且脆，不如透传端口直接——否决。
