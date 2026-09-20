# 票 07 · 托盘菜单扩展:版本/检查更新/立即升级

**What to build**:
扩展既有 systray 托盘菜单为:「版本 vX.Y.Z」(disabled 展示项)→「打开面板」(既有)→「检查更新」(进程内调票03 只读 CheckLatest,结果经 internal/notify 通道或托盘气泡呈现,按 systray 能力择一)→「立即升级」(spawn detached 隐藏 `ferryman update --supervise`)→「退出」(既有)。

**验收标准**:
- [ ] 菜单项齐、顺序对、版本项 disabled 且随版本变量显示
- [ ] 「检查更新」不发写操作、结果可感知(notify 落地证据或气泡调用证据)
- [ ] 「立即升级」以 detached 隐藏方式拉起监督者,守护自身不退出不卡 UI(菜单结构用测试断言;实际升级链路归票05 测试)
- [ ] `--no-tray` 模式零影响;`go test ./cmd/ferryman/` 绿

**Blocked by**: 03, 05
**涉及路径**: cmd/ferryman/main.go, cmd/ferryman/main_test.go
**副作用声明**: 无
**decision_refs**: D5, D6
**review_blocks**: F12
