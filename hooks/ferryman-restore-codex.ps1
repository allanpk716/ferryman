# Ferryman 归还钩子（Codex SessionStart，T23 准备）—— 仅 clear/startup 注入，任何故障静默
# Codex hooks.json 无 matcher 字段（Orca 全套注册即证）→ source 过滤在脚本内做。
# 可配环境变量：FERRYMAN_DISABLE=1 短路；FERRYMAN_PORT（默认 7311）；
#               FERRYMAN_TOKEN_FILE（默认 ~/ferryman/daemon.token）；FERRYMAN_HOOK_DEBUG=<文件>
if ($env:FERRYMAN_DISABLE -eq '1') { exit 0 }
# 自举：daemon 不在则拉起并等就绪（注入最怕缺席，等 2.5s）
. "$PSScriptRoot\ferryman-ensure.ps1"; Ensure-Ferryman -WaitMs 2500
$ErrorActionPreference = 'Stop'
try {
    try {
        [Console]::InputEncoding = [System.Text.Encoding]::UTF8
        [Console]::OutputEncoding = [System.Text.Encoding]::UTF8
    } catch {}
    $raw = [Console]::In.ReadToEnd()
    if ($env:FERRYMAN_HOOK_DEBUG) {
        try { Add-Content -Path $env:FERRYMAN_HOOK_DEBUG -Value $raw -Encoding utf8 } catch {}
    }
    $j = $raw | ConvertFrom-Json
    if ($j.source -and ($j.source -ne 'clear') -and ($j.source -ne 'startup')) { exit 0 }
    $port = if ($env:FERRYMAN_PORT) { $env:FERRYMAN_PORT } else { 7311 }
    $tokenFile = if ($env:FERRYMAN_TOKEN_FILE) { $env:FERRYMAN_TOKEN_FILE }
                 else { "$env:USERPROFILEerryman\daemon.token" }
    $token = (Get-Content $tokenFile -Raw -ErrorAction Stop).Trim()
    $cwd = [uri]::EscapeDataString([string]$j.cwd)
    $sid = [uri]::EscapeDataString([string]$j.session_id)
    $resp = Invoke-RestMethod -Uri "http://127.0.0.1:$port/restore?agent=codex&cwd=$cwd&session_id=$sid" `
        -Method Get -Headers @{ Authorization = "Bearer $token" } -TimeoutSec 2
    if ($resp.context) { [Console]::Out.Write([string]$resp.context) }
    exit 0
}
catch {
    exit 0
}
