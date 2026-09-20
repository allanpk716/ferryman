# 票 08 · CI 发布流水线 release.yml

**What to build**:
新建 `.github/workflows/release.yml`:触发 `push: tags: ['v*']`;`permissions: contents: write`;runner `windows-latest`;**每个 run 步骤显式 `shell: bash`**;步骤:checkout → setup-go(读 go.mod)→ `go vet ./...` + `go test ./...` → `go build -trimpath -ldflags "-s -w -X main.version=${GITHUB_REF_NAME}" -o ferryman_windows_amd64.exe ./cmd/ferryman` → `sha256sum` 产 `.sha256` → tag 严格 semver 校验(`vX.Y.Z` 或 `vX.Y.Z-<pre>`,不合法即失败)→ 含预发布后缀则 Release 标 `prerelease: true` → 直接 publish(无 draft),body 取 tag 注释(`git tag -l --format='%(contents)'`)。文件头注释写明:发布须用 annotated tag(`git tag -a`),lightweight tag 的 body 为空。

**验收标准**:
- [x] 票内静态断验(bash):文件含 permissions/shell: bash/windows-latest/prerelease 分支/semver 校验/${GITHUB_REF_NAME} 注入各关键字段
- [x] YAML 语法有效(python -c yaml.safe_load 或等价)
- [x] 本票不真打 tag(真实触发属验收期人工动作,票内注明)
- [x] 产物文件名固定 `ferryman_windows_amd64.exe` + `.sha256`

**Blocked by**: 01(版本注入变量)
**涉及路径**: .github/workflows/release.yml(新)
**副作用声明**: 无(纯文件;不跑 go test)
**decision_refs**: D2, D3, D11, D12
**review_blocks**: F11
