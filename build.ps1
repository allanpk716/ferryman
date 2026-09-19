# build.ps1 — Ferryman 合并 exe 构建（票22 / spec C9）。
#
# console 子系统：`go build` 不带 -H windowsgui——doctor/report/install/serve
# 横幅在终端全部可见；无窗口性交给启动方：
#   - 点火脚本 start-daemon.cmd 由 ferryman-ensure.ps1 以 Start-Process
#     -WindowStyle Hidden 拉起（生产已验证机制）；
#   - 桌面/开始菜单快捷方式 WindowStyle = minimized（ferryman --install-shortcuts）。
# --release 产出 -ldflags "-s -w" 变体（去符号表，体积更小），同样无 windowsgui。
param(
    [switch]$Release
)

$ErrorActionPreference = 'Stop'
Set-Location -Path $PSScriptRoot

$out = Join-Path $PSScriptRoot 'ferryman.exe'
if ($Release) {
    go build -ldflags "-s -w" -o $out ./cmd/ferryman
} else {
    go build -o $out ./cmd/ferryman
}
if ($LASTEXITCODE -ne 0) {
    exit $LASTEXITCODE
}
Write-Output "built: $out (console subsystem, no -H windowsgui)"
