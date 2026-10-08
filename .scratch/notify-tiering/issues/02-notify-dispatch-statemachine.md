# 票02 · notify 事件分派核心 + block/摆渡链/调参接线 + 滑落状态机

## What to build
notify 包获得按事件名分派的发送能力(查 cfg.Notify.Events[事件] 得 off/toast/both 再决定通道);闸门拦截通知(block)、摆渡链滑落(chain_degrade)、骨架(chain_skeleton)、调参气泡(tuning)四类触发点接入分派;worker 的级间滑落告警从「每次滑落一条」改为「顺位级状态变化制」。

## 验收标准
- [ ] notify 新增事件分派入口(如 NotifyEvent(event, title, message, cfg)):enabled=false 全静默;事件值 off 不发、toast 仅 SendToast、both 双通道;[notify].pushover/.toast 通道开关作为通道上限与事件值与运算
- [ ] 旧 NotifyAlert 保留可用(过渡期其余调用点不破编译),语义=both 直发
- [ ] gate.go 拦截通知走 block(缺省 toast:手机不发、桌面发)
- [ ] worker alertChainDegrade 改顺位级状态机:状态=最近一次成功到达顺位(Worker 内存,冷启动 0);下移推一条且文案含新顺位与失败原因,滑至链尾(顺位=链长-1)文案体现最后一站严重度;同级失败零推送;上移恢复静默重置;状态转移纯函数化可测(不真发送)
- [ ] alertChainSkeleton sync.Once 语义不动,走 chain_skeleton
- [ ] tuning 三函数走 tuning(缺省 off=默认静默;配置显式值仍可打开)
- [ ] 相关测试更新:notify_test 发送矩阵(事件×三值×通道开关×总开关)、状态机转移表测试、tuning_test/gate_async_e2e_test/notify_wiring_test 断言更新;全部不弹真 toast 不出网(httptest+mock runToast 惯例)

## Blocked by
票01(Events 结构与缺省表)

## 涉及路径
- internal/notify/notify.go
- internal/notify/notify_test.go
- internal/notify/tuning.go
- internal/notify/tuning_test.go
- internal/notify/gate_async_e2e_test.go
- internal/daemon/worker.go
- internal/daemon/worker_test.go
- internal/daemon/gate.go
- internal/daemon/notify_wiring_test.go

## 副作用声明
无独占验证命令;默认 go test ./internal/notify/ ./internal/daemon/ -run 'Notify|Tuning|Chain|Gate' + go vet

## decision_refs
D2、D3、D6、D8
review_blocks: 无
