# Ferryman 归还钩子（CC SessionStart，matcher=clear|startup 由 settings.json 限定）
# 仅 clear/startup 注入（resume/compact 不注入，DESIGN §5）；任何故障静默退出
$ErrorActionPreference = 'Stop'
try {
    $raw = [Console]::In.ReadToEnd()
    $j = $raw | ConvertFrom-Json
    if ($j.source -and ($j.source -ne 'clear') -and ($j.source -ne 'startup')) { exit 0 }
    $token = (Get-Content "$env:USERPROFILE\ferryman\daemon.token" -Raw -ErrorAction Stop).Trim()
    $cwd = [uri]::EscapeDataString([string]$j.cwd)
    $sid = [uri]::EscapeDataString([string]$j.session_id)
    $resp = Invoke-RestMethod -Uri "http://127.0.0.1:7311/restore?agent=cc&cwd=$cwd&session_id=$sid" `
        -Method Get -Headers @{ Authorization = "Bearer $token" } -TimeoutSec 2
    if ($resp.context) { [Console]::Out.Write([string]$resp.context) }
    exit 0
}
catch {
    exit 0
}
