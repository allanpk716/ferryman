# 票01 · config 层:notify.events 配置组解析与缺省表

## What to build
用户在 config.toml 的 [notify] 节写 `events = { block = "toast", tuning = "off", ... }` 内联表键后,daemon 加载配置时得到九事件的显式配置;未写任何 events(或只写了部分键)时,其余事件回落内置缺省表。三值合法(off/toast/both),非法值沿既有配置错误路径拒载。config.example.toml 给出唯一 TOML 示例并注明禁用 [notify.events] 子表头。

## 验收标准
- [ ] NotifyCfg 新增 Events 映射(事件名→三值);Default() 集成九事件缺省表(block=toast,tuning=off,tray_reply=toast,chain_degrade/chain_skeleton/breaker/upgrade/hard_cut/drift=both)
- [ ] [notify] events 内联表键解析:显式键覆盖缺省、未配置键回落缺省;events 整键省略=全缺省
- [ ] 非法值(非 off/toast/both)在配置加载时报错(沿既有错误路径风格)
- [ ] [notify.events] 独立子表头不解析为 events(保持原语义:未知节忽略或报错按既有行为,不得误读)
- [ ] 与既有五键(enabled/pushover/pushover_token/pushover_user/toast)共存互不干扰
- [ ] config.example.toml:notify 节注释更新+唯一 TOML 内联表示例+「禁用子表头」注记
- [ ] 单测:解析矩阵(全缺省/部分覆盖/全覆盖/非法值/子表头不误读)落 internal/config/config_test.go

## Blocked by
无,可立即开始

## 涉及路径
- internal/config/config.go
- internal/config/config_test.go
- config.example.toml

## 副作用声明
无独占验证命令;默认 go test ./internal/config/ + go vet

## decision_refs
D2、D5
review_blocks: 无
