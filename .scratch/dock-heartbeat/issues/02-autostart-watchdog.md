# 02 · 常驻保障：Run 键自启＋看门计划任务＋doctor 状态

## What to build

1. **注册表自启**（internal/installer/autostart.go）：HKCU `Software\Microsoft\Windows\CurrentVersion\Run` 写 `Ferryman`＝daemon 无窗口后台启动命令；提供 Install/Uninstall/Status 三操作，幂等（重复安装无副作用，卸载后键不存在）。启动形态沿用 daemon 现有无窗口方式（参考钩子自举的拉起路径，不新造窗口）。
2. **看门**（internal/installer/watchdog.go＋cmd/ferryman 子命令 `ferryman watchdog`）：`ferryman watchdog` 单次执行——HTTP GET `http://127.0.0.1:<FERRYMAN_PORT 或默认 7311>/health`，**短超时（2s）**：
   - 无监听（connection refused）→ 拉起 daemon（与 Run 键同一命令），本次结束；
   - 有响应（任何 HTTP 状态，含 5xx）→ 正常退出；
   - 端口被占但非 daemon/超时无响应 → **只记日志退出，不双拉**（单实例约束；不杀进程）。
   计划任务：`schtasks /Create /TN FerrymanWatchdog /SC MINUTE /MO 5`（TR 指向同一 exe watchdog 子命令，无窗口）；Install/Uninstall/Status 同自启。
3. **doctor**（internal/installer/doctor.go 扩展）：新增两项检查——Run 键状态（存在/缺失/值不符）、看门任务状态（存在/缺失/下次运行时间），纳入既有 doctor 输出格式。

## 验收标准

- [ ] Run 键 Install→Status=installed→再 Install 幂等（值不重复写）；Uninstall→Status=missing。
- [ ] watchdog 判定三分支单测（httptest mock /health：正常响应退出 0；refused→触发拉起调用——拉起动作用接口注入 fake 断言被调用；有监听但超时/非 daemon→退出且**不**调用拉起）。
- [ ] schtasks 命令构造单测（参数逐字断言，含 /MO 5 与无窗口标志）；真实 schtasks 操作不在单测中执行。
- [ ] doctor 两项检查有单测（注册表/计划任务探测走可注入接口，fake 返回三种状态）。
- [ ] `go test ./...` 全绿。
- [ ] 真实安装/卸载/看门冒烟清单写入代码注释顶部（供 runbook 票引用；不在本票真实执行 schtasks/注册表写入——单测全部走 fake/构造层）。

## Blocked by

无，可立即开始。

## 涉及路径

- internal/installer/（autostart.go、watchdog.go、doctor.go、install.go 如需子命令注册、对应 _test.go）
- cmd/ferryman/（watchdog 子命令入口）

## 副作用声明

- 独占验证命令：`go test ./internal/installer/... ./cmd/...`；本票**不得**在测试中真实写注册表/建计划任务（全部 fake/命令构造断言）。

decision_refs: D5
review_blocks: 无（F5 语义已在 spec 钉死：HTTP 探活/无监听才拉起/占用不双拉）
