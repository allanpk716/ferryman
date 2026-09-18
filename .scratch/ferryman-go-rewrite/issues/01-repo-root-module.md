# 票 01 · 仓库收形：根 module 化，viewer 并入

**What to build**：仓库收形成根部单一 Go module——viewer 目录整体并入：内部包迁 `internal/viewer/{server,demo,ledger,policy}`（policy 暂留占位，票 03 销毁）、入口迁 `cmd/viewer/`（含 main_test.go）、web 前端与 icon.ico 资产**随 cmd 包放置**（go:embed 只能引用包目录子树）。根 go.mod：module 名 `ferryman`，依赖照抄（BurntSushi/toml、getlantern/systray）。既有 Go 测试全部保持绿；viewer 能力（demo/托盘/面板/快捷方式）零回归。

参照：spec §Implementation Decisions「仓库形态」；rev1 计划 Task 1。

**验收标准**：
- [ ] go.mod 在仓库根，module ferryman，go mod tidy 通过
- [ ] viewer/internal/* → internal/viewer/*；viewer/main.go(+main_test.go) → cmd/viewer/；viewer/web、viewer/icon.ico → cmd/viewer/ 旁，embed 指令相对本包生效
- [ ] viewer/ 目录删除（ferryman-timeline.exe 构建产物一并清除，不入库）
- [ ] `go build ./...` 与 `go test ./...` 全绿
- [ ] `go run ./cmd/viewer --demo --no-tray --no-browser` 能起（合成账本面板可访问）

**Blocked by**：无，可立即开始
