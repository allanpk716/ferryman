# 票 22 · 合并 exe（cmd/ferryman 吸收 cmd/viewer）

**What to build**：`cmd/ferryman/main.go`：子命令解析（无参/serve=守护 7311+面板 15900+托盘；doctor/install-cc/install-ccswitch/install-codex/account report；--demo/--port/--no-tray/--no-browser/--install-shortcuts 面板族 flags）；**cmd/viewer 全部能力与测试迁入**（评审附录#3：不得删除 viewer 测试——main_test.go 改写到 cmd/ferryman）；资产 cmd/ferryman/web、icon.ico 随包；托盘=开面板/退出（退出=停守护）；快捷方式 WindowStyle=minimized 钉 15900。`build.ps1`：**console 子系统**构建（无 -H windowsgui；--release 出 -ldflags "-s -w" 变体）。删除 cmd/viewer/。

参照：rev1 Task 25；spec §Solution 1/2、§Implementation「CLI/构建」。

**验收标准**：
- [ ] go test ./... 全绿（含迁移后的 viewer 测试）
- [ ] `go run ./cmd/ferryman serve` 终端可见中文横幅（C9 验证点）；双端口监听；--no-tray 前台可 Ctrl+C
- [ ] `go run ./cmd/ferryman --demo --no-tray --no-browser` 面板可用
- [ ] `go run ./cmd/ferryman doctor` 终端输出体检结果
- [ ] build.ps1 产出 console exe；快捷方式安装指 ferryman.exe

**Blocked by**：17, 19, 20
