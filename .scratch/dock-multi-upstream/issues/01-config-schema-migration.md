# 票 01 · 配置上游表 + 首启迁移与预置 + 装配按 active

## What to build

升级后的守护进程首启即完成渡口供应商管理的地基:旧 `[dock]` 单值配置(upstream_base_url/api_key/model_map/text_only/balance_url)自动迁移为名为 `cc-switch` 的回退条目并**保持 active**(用户可验证:升级当天一切行为与升级前完全一致,链路仍走 127.0.0.1:15721);同次首启生成智谱/kimi/deepseek 三条**未激活**预置条目(api_key 留空占位)。此后渡口的转发目的地、出站密钥、模型改写映射、text_only、余额端点全部取自 `[dock].active` 指向的条目;rewrite 隐含开启(旧 rewrite_enabled 废弃),上游为本地地址时既有守卫仍强制透传。迁移幂等;新表与旧单值并存时以新表+active 为准。

预置值(实施时按官方文档核对拼写):
- 智谱:base_url=`https://open.bigmodel.cn/api/anthropic`,model_map{default/opus/sonnet=glm-5.3, haiku=glm-5.3-flash},balance_url=`https://open.bigmodel.cn/api/user/balance`
- kimi:base_url=`https://api.kimi.com/coding/`,model_map{default/sonnet=kimi-for-coding, opus=k3, haiku=k3-256k}(不用 highspeed)
- deepseek:base_url=`https://api.deepseek.com/anthropic`,model_map{default/sonnet/haiku=deepseek-flash, opus=deepseek-v4-pro}

## 验收标准

- [ ] 旧单值配置首启后:config.toml 含 `[dock.upstreams.cc-switch]`,active 指向它,base_url/api_key/model_map/text_only/balance_url 全部继承旧值
- [ ] 同次首启生成三条未激活预置(api_key 空、按上述预置值)
- [ ] 二次启动不重复迁移;已存在 `[dock.upstreams]`(手写新表)时跳过迁移,解析以新表+active 为准
- [ ] model_map 校验:非本地 base_url 条目必含 default,缺失报错;本地地址条目(守卫透传域)豁免
- [ ] 渡口装配点(serve 侧)不再读旧单值字段,目的地/密钥/model_map/text_only 全部来自 active 条目
- [ ] 守卫行为不回归:上游为本地地址时禁改写退透传(既有测试全绿)
- [ ] config.example.toml 更新为新 schema(含三条预置与注释)
- [ ] `go test ./internal/config/... ./internal/dock/... ./internal/daemon/...` 全绿

## Blocked by

无,可立即开始。

## 涉及路径

- internal/config/
- internal/daemon/
- internal/dock/
- config.example.toml

## 副作用声明

- 独占验证命令:`go test ./internal/config/... ./internal/dock/... ./internal/daemon/...`(daemon 测试较慢,允许)

decision_refs: D2, D8, D10, D13, D15
review_blocks: F1, F6
