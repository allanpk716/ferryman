# 票04 · 设置视图读写面适配 + 等值省略规范化(F1 解除票)

## What to build
工作台设置视图对 notify.events 配置组的完整读写往返:读面展示生效值、UI 九事件三值选择、写入器等值省略规范化——用户保存任何设置都不静默清除、不物化未显式配置的事件键。

## 验收标准
- [ ] settings_read.go notify 段返回 events 键(生效值=用户显式配置回落内置缺省,仅供展示)
- [ ] widget/ui/ferryman-settings.js 通知节:九事件三值选择(off/toast/both)入表单;notifyBody() 回传完整 events 对象(含用户未改项——回读现值带回,防节级整写清除);ferryman-settings.html 表单结构同步
- [ ] settings_write.go 写入路径对 events 各键做**等值省略规范化**:写盘前与内置缺省表比对,等值键不落盘——未配置键保存后仍缺省、盘上不新增;全部键都等值时 events 键整体不落盘(盘上无该键=回落缺省语义)
- [ ] 往返回归测试①:读→保存(不改 events)→盘上 events 相关字节不变(settings_write_test 惯例)
- [ ] 往返回归测试②:UI 改一事件(非缺省值)→盘上仅该键出现/变化
- [ ] 等值省略取舍例测试:显式配置值恰等于当前缺省→规范化后盘上无该键(随缺省演进)
- [ ] 密钥合并、写前快照、审计行纪律不回归(既有 settings 测试全绿)

## Blocked by
票01(缺省表常量供读面与写入器比对)

## 涉及路径
- internal/daemon/settings_read.go
- internal/daemon/settings_read_test.go
- internal/daemon/settings_write.go
- internal/daemon/settings_write_test.go
- widget/ui/ferryman-settings.js
- widget/ui/ferryman-settings.html

## 副作用声明
无独占验证命令;默认 go test ./internal/daemon/ -run Settings + go vet;widget UI 改动随主程序仓(不做 tauri 构建)

## decision_refs
D2、D5、D6;spec「设置视图读写面适配」节(等值省略协议全文)
review_blocks: F1
