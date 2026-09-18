# 票 23 · 切换工具与核对表（不执行切换）

**What to build**：切换的生产执行留待用户晨间拍板（夜链不动生产）；本票交付四件工具：① `internal/cutover`：数据备份命令（~/ferryman 全量 → 带时间戳目录，含 accounts/handoffs/index.json/config.toml/token）；② rollback 工件生成器（rollback-to-python.cmd：**幂等**——worktree 已存在则复用或清理重建；支持 `--drill` 参数走隔离演练：临时 worktree 路径+临时启动器副本，**不碰真实 start-daemon.cmd**——附录#4/#5）；③ 切换核对表文档（docs/go-cutover-runbook.md：前置条件/步骤/24h 观察清单/**回退触发条件=摆渡产物损坏、账本字段错乱、三类已装钩子异常、守护或面板崩溃不可热修**[gate 误拦/漏拦不在列——附录#9/#10]、.venv 清理延后至观察期结束）；④ 沙箱冒烟脚本（enforce + observe 三链路：独立端口+独立数据目录，直打 API 触发——附录#11）。

参照：rev1 Task 26/27；spec §Further Notes；评审附录 #4/#5/#6/#9/#10/#11。

**验收标准**：
- [ ] 备份命令对本机 ~/ferryman 演练一次成功（副本完整、原数据不动）
- [ ] rollback 生成器 --drill 在临时环境演练通过且真实启动配置零改动（diff 验证）；重复执行幂等
- [ ] runbook 文档含完整触发条件与观察清单；沙箱冒烟脚本 enforce block 契约断言跑通
- [ ] 全部产物有 Go 测试或脚本级验证

**Blocked by**：22
