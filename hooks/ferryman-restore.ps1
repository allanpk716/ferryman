# Ferryman 归还钩子（CC SessionStart，matcher=clear|startup 由 settings.json 限定）
# 仅 clear/startup 注入（resume/compact 不注入，DESIGN §5）；任何故障静默退出
# 可配环境变量：FERRYMAN_PORT（默认 7311）；FERRYMAN_TOKEN_FILE（默认 ~/ferryman/daemon.token）
$ErrorActionPreference = 'Stop'
try {
    try {
        [Console]::InputEncoding = [System.Text.Encoding]::UTF8
        [Console]::OutputEncoding = [System.Text.Encoding]::UTF8
    } catch {}
    # 自举：daemon 不在则拉起并等就绪（注入最怕 daemon 缺席，值得等 2.5s；
    # 钩子超时已放宽到 10s，见 install.py）
    . "$PSScriptRoot\ferryman-ensure.ps1"; Ensure-Ferryman -WaitMs 2500
    $raw = [Console]::In.ReadToEnd()
    $j = $raw | ConvertFrom-Json
    if ($j.source -and ($j.source -ne 'clear') -and ($j.source -ne 'startup')) { exit 0 }
    $port = if ($env:FERRYMAN_PORT) { $env:FERRYMAN_PORT } else { 7311 }
    $tokenFile = if ($env:FERRYMAN_TOKEN_FILE) { $env:FERRYMAN_TOKEN_FILE }
                 else { "$env:USERPROFILE\ferryman\daemon.token" }
    $token = (Get-Content $tokenFile -Raw -ErrorAction Stop).Trim()
    $cwd = [uri]::EscapeDataString([string]$j.cwd)
    $sid = [uri]::EscapeDataString([string]$j.session_id)
    $resp = Invoke-RestMethod -Uri "http://127.0.0.1:$port/restore?agent=cc&cwd=$cwd&session_id=$sid" `
        -Method Get -Headers @{ Authorization = "Bearer $token" } -TimeoutSec 2
    if ($resp.context) { [Console]::Out.Write([string]$resp.context) }
    exit 0
}
catch {
    exit 0
}
