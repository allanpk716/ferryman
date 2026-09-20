# 票 03 · update 查询与下载核(--check 路径)

**What to build**:
新建 `internal/update` 包的只读部分:目标版本解析(默认 `releases/latest` 稳定版;`--prerelease` 走列表接口纳入预发布;显式 `vX.Y.Z` 直取该 release,支持降级)、资产下载(固定名 `ferryman_windows_amd64.exe` 走 `releases/latest/download/` 或对应 release 的 download 直链;HTTP 客户端走 `ProxyFromEnvironment`)、SHA256 校验(对照同 release 的 `.sha256` 资产)。repo slug 硬编码。CLI:`ferryman update --check [vX.Y.Z] [--prerelease]` 只报告不动手(已是最新/发现新版/当前 dev 无法比较时的如实提示)。

**验收标准**:
- [ ] httptest 伪 GitHub:latest 排除 prerelease;`--prerelease` 能见 rc;显式版本(含更低版本)能解析
- [ ] 下载经 `HTTPS_PROXY` 环境变量生效(测试用本地代理桩或 httptest 证明走了代理 URL)
- [ ] SHA256 不符返回明确错误且不留半文件
- [ ] `--check` 输出人话(已是最新 vX.Y.Z / 发现 vX.Y.Z,当前 vX.Y.Z)
- [ ] `go test ./internal/update/ ./cmd/ferryman/` 绿;不触碰 `internal/mcp`

**Blocked by**: 01
**涉及路径**: internal/update/(新包), cmd/ferryman/main.go(子命令注册), cmd/ferryman/main_test.go
**副作用声明**: 无(httptest 回环,零外呼)
**decision_refs**: D6, D7, D8, D11
**review_blocks**: 无
