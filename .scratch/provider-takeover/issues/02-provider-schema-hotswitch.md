# 票 02 · 供应商表扩展+热切换(即时生效不断流的地基)

## What to build
扩既有渡口上游表(internal/config 的 DockUpstream 族)并在守护与渡口之间打通**热切换**:

1. **schema 扩列**:每条目新增 `dialect`("anthropic" 缺省=既有行为|"openai_responses" 原生 responses 线协议);codex 可用性推导(anthropic→需翻译;openai_responses→原生透传)+显式 `codex = "unsupported"` 否决位;模型位映射扩 codex 主模型键(既有 default/opus/sonnet/haiku 档位键语义不变)。解析优先级与旧配置兼容规则沿用本包既有纪律(新字段缺省=旧行为零变化)。
2. **逐请求活跃供应商 seam**:渡口当前在构造时固化上游地址;改造为对每个新请求读内存态活跃条目的解析 seam(接口化,测试可注桩)。**在途请求/SSE 流持有既有上游连接自然跑完**——seam 只影响新请求(F4 边界语义)。
3. **守护管理口热切换端点**:新增管理端点(挂 15700 管理面,与既有 /shutdown、/stats 同族):接收供应商名→校验存在与合法→内存态原子换绑+落盘持久化(原子写,复用既有 SetActiveUpstream 机制);渡口不重启、端口无空窗。switch 到 codex="unsupported" 条目的拒绝逻辑在 CLI 层(票 06),端点只做存在性/合法性校验。
4. 单测:schema 解析/校验(新旧形态)、热切换原子性(切换时在途假请求仍走旧上游、下一请求走新)、持久化往返。

## 验收标准
- [ ] dialect/codex 可用性/codex 模型位解析与校验齐,旧配置(无新字段)行为零变化有测试锁死
- [ ] 逐请求 seam:并发下切换,在途请求旧上游跑完、新请求即刻新上游(测试断言)
- [ ] 管理端点:换绑+持久化原子,非法名拒绝;守护不重启
- [ ] go test ./internal/config/... ./internal/daemon/... ./internal/dock/... 全绿;go vet 净

## Blocked by
无,可立即开始

## 涉及路径
- internal/config/dock_upstream.go(+既有 _test 同步扩)
- internal/config/config.go(仅当需要挂载新字段时)
- internal/daemon/(管理端点新文件+装配处最小接线)
- internal/dock/server.go(活跃上游 seam 的最小改造;**勿动 /v1/messages 既有行为**)

## 副作用声明
- 独占验证命令:go test ./internal/config/... ./internal/daemon/... ./internal/dock/...(daemon 套件约 40-60s)
- 不写真机配置;不碰生产进程

decision_refs: D2, D5, D11
review_blocks: F4
