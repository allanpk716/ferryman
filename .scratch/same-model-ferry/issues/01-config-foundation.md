# 票01 · 配置面地基:同模型与调参配置节+钳位校验+doctor 检查

## What to build
给 Ferryman 配置加两个节并让校验与体检跟上,打通端到端:`[ferry]` 同模型节(enabled 总开关、upstreams 白名单、每上游 ceiling 覆盖)与 `[tuning]` 节(mode=manual|recommend|auto、window、min_events)。解析、默认值(同模型默认 off;tuning 默认 recommend;种子阈值 20min)、钳位校验(同模型阈值∈[10,总结阈值];不变量链 同模型≤总结≤拦截;manual 档配置值=生效值语义留好接口)。config.example.toml 加带中文注释的模板段(照现有 [prices.glm] 段写法)。doctor 四条新检查:same_model 开而白名单空或全未启用;白名单条目无实跳臂结论;tuning=auto 而价格表缺 p_cache;同模型阈值与总结阈值钳位冲突。

## 验收标准
- [ ] internal/config 解析新节,缺省值正确(同模型 off、recommend、20min)
- [ ] 钳位与不变量链校验:越界/倒挂配置被拒并给人话错误
- [ ] config.example.toml 含同模型+调参模板段(注释说明白名单预置≠启用)
- [ ] doctor 四条检查各有文案与判定,go test ./internal/config/... 全绿
- [ ] 新增配置键枚举值(UI 友好:扁平、可校验)

## Blocked by
无,可立即开始

## 涉及路径
internal/config/
config.example.toml
internal/installer/doctor.go(doctor 检查注册;协调者订正:单源装配在此,初稿误注 serve.go——泳道 2026-09-22 查实)

## 副作用声明
go test ./internal/config/...;go build ./...(类型检查)

## decision_refs
D2 D5 D6 D10 D13

## review_blocks
F4(作用域:ceiling 全局单值+按上游覆盖在本票 schema 钉死)
