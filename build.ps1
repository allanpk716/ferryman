# build.ps1 — Ferryman 合并 exe 构建（票22 / spec C9）。
#
# console 子系统：`go build` 不带 -H windowsgui——doctor/report/install/serve
# 横幅在终端全部可见；无窗口性交给启动方：
#   - 点火脚本 start-daemon.cmd 由 ferryman-ensure.ps1 以 Start-Process
#     -WindowStyle Hidden 拉起（生产已验证机制）；
#   - 桌面/开始菜单快捷方式 WindowStyle = minimized（ferryman --install-shortcuts）。
# --release 产出 -ldflags "-s -w -X main.version=<git describe --tags --always>"
# 变体（去符号表，体积更小 + 编译期注入版本号，`ferryman version` 可见），
# 同样无 windowsgui。git 不可用/非仓库才回落 main.version 缺省值 dev。
param(
    [switch]$Release,
    # -Out 产物路径（缺省 = 仓库根 ferryman.exe，行为不变；验证用可指到临时目录）
    [string]$Out = ''
)

$ErrorActionPreference = 'Stop'
Set-Location -Path $PSScriptRoot

# outPath 产物路径（勿与参数 $Out 同名——PS 变量名不分大小写，会互相覆盖）
$outPath = Join-Path $PSScriptRoot 'ferryman.exe'
if ($Out -ne '') {
    $outPath = $Out
}
if ($Release) {
    # 版本注入：tag 在 = vX.Y.Z[-N-gHASH]；--always 兜底短 hash；仅 git 失败才 dev
    $eap = $ErrorActionPreference
    $ErrorActionPreference = 'Continue'   # git 失败的 stderr 不当作终止错误
    $ver = (git describe --tags --always 2>$null | Select-Object -First 1)
    $ErrorActionPreference = $eap
    if ($LASTEXITCODE -ne 0 -or [string]::IsNullOrWhiteSpace($ver)) { $ver = 'dev' }
    $ver = $ver.Trim()
    go build -ldflags "-s -w -X main.version=$ver" -o $outPath ./cmd/ferryman
} else {
    go build -o $outPath ./cmd/ferryman
}
if ($LASTEXITCODE -ne 0) {
    exit $LASTEXITCODE
}
Write-Output "built: $outPath (console subsystem, no -H windowsgui)"
