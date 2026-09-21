# 渡口多上游直连,cc-switch 退役为回退通道——换上游活在代理内,不改写 CC 配置

Ferryman 今天的链条是 CC → 渡口(127.0.0.1:15722)→ cc-switch(15721)→ 供应商:渡口 `[dock]` 只有单值上游字段,真正的供应商切换发生在 cc-switch 那层(它改写 `~/.claude/settings.json` 的 env)。2026-09-21 grill 定稿(决议 Q1–Q16 全数通过):把切换收进渡口,实现最精简版供应商管理,cc-switch 从链条退役。

状态:已接受(2026-09-21;实施未开始,本 ADR 先行锁定架构)。

决定(2026-09-21):

- **渡口上游表**:`[dock.upstreams.<名>]` 条目(base_url / api_key / model_map〔必含 default,可选 opus/sonnet/haiku 档位键〕/ text_only 可选 / balance_url 可选)+ `[dock].active` 单选键。全体条目只收真供应商端点;上游为本地地址时守卫保留既有规则——强制透传、禁用改写。首批三条:智谱 CodingPlan(国内线 `open.bigmodel.cn/api/anthropic`,glm-5.3 系)、Kimi Coding Plan(`api.kimi.com/coding/`,kimi-for-coding/k3 系)、DeepSeek 按量(`api.deepseek.com/anthropic`,官方自带 claude-\* 自动映射)。
- **换上游 = 代理内部换目的地**:`ferryman upstream list` / `ferryman upstream use <名>` 写回 active 并自动重启守护(复用既有看门/自举拉起链路;钩子 fail-open 挡住几秒空窗;台账由 watcher 启动扫描重建)。**不学 CC Switch 改写 CC 自己的配置**——CC 指向渡口保持人工一次性静态配置。纯手动,无自动故障转移。
- **换上游即冷启动**:重启清空内存缓存快照,旧会话前缀对新上游无意义、按全量重付;交接库(covers_until)与供应商无关,不作废;心跳零改动——占位令牌出站换真钥的既有机制使心跳自动跟随新上游,无需触发熔断。
- **cc-switch 退役**:旧 `[dock]` 单值字段(upstream_base_url/api_key/model_map)首启自动迁移为名为 `cc-switch` 的条目并保持 active(指向 15721,守卫自动禁改写)。它是回退通道的具象化:回退 = `ferryman upstream use cc-switch` 一条命令,平时不碰。
- **两张表分立**:渡口上游(Anthropic Messages,带 model 映射)与摆渡供应商(`[providers]`,OpenAI chat/completions,已存在)不合并、不联动。OpenAI 协议不进渡口——四家供应商全部有 Anthropic 兼容端点,转换层无消费者;摆渡腿继续独立说 OpenAI 协议。
- **模型改写随条目走**:现有 rewrite 逻辑(剥 `[1M]` 后缀→真名透传→别名映射→default 兜底)从全局单份变为每上游一份 model_map;count_tokens 同映射。

## Considered Options

- **复刻 CC Switch 本尊(改写 `~/.claude/settings.json` 的 env)**:切换要 CC 重启才生效、引入脏状态,且 Ferryman 本就在流量路径内,绕行无理——否决。
- **cc-switch 常驻为上游表普通条目/过渡层**:让"供应商"混进一个不是供应商的中转,双中转层语义混杂——否决,退役为专用回退条目。
- **热加载换上游(不重启守护)**:切换低频,原子化 target/apiKey 的复杂度不值票价;重启反而免费兑现快照作废——否决。
- **统一供应商注册表(两 lane 共一张表)**:同一"智谱"在两 lane 用的是不同端点、不同协议、不同模型名,合并只会造半填条目——否决。
- **渡口说 OpenAI 协议/做协议转换层**:无消费者(四家均有 Anthropic 端点),且转换层远超"最精简"——否决。
- **自动故障转移**:另一量级工程,精简版砍——否决。

## 后果

直连后,原先 cc-switch 中转层修过的兼容坑需自行处理——配套规则已立(仓库根 `CLAUDE.md`):中转 API 相关问题必先读 cc-switch(farion1231/cc-switch)源码参考,禁止猜测。拦截阈值维持按 Agent 配置,不随上游变(服务商维度阈值今日并未实现,暂不引入)。余额端点从硬编码智谱改为每上游可选 balance_url,不配不显示。WebUI 换上游控件后置到 observe 周(2026-09-20 起 7 天)结束后再动;Codex 与 OpenAI 协议支持一并推迟,仅保留扩展余量。
