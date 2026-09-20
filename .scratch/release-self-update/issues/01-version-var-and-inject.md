# 票 01 · 版本变量与注入链(prefactor)

**What to build**:
main 包持可注入版本变量(缺省 `dev`),打通编译期注入链,并加 `ferryman version` 子命令:本地开发构建输出 `dev` 并提示非 release;注入后输出实际版本(如 `v0.1.0`)。`build.ps1 -Release` 的 ldflags 增加 `-X` 注入,值取 `git describe --tags --always`(无 tag 时保持 dev)。

**验收标准**:
- [ ] 不注入时 `ferryman version` 输出 `dev`(含"非 release 构建"提示)
- [ ] 注入 `vX.Y.Z` 后子命令输出该值(测试用注入变量或 ldflags 冒烟证明)
- [ ] `build.ps1 -Release` 编出的 exe `version` 输出与 `git describe --tags --always` 一致(手工冒烟一次,结果记票)
- [ ] `go test ./cmd/ferryman/` 绿;无参/serve 等既有子命令行为零变化
- [ ] 验证用的临时 exe 一律 `-o $env:TEMP\...`,不碰仓库根 `ferryman.exe`

**Blocked by**: 无,可立即开始
**涉及路径**: cmd/ferryman/main.go, cmd/ferryman/main_test.go, build.ps1
**副作用声明**: 无(仅单包测试;不跑全仓构建)
**decision_refs**: D3, D4
**review_blocks**: 无
