# Ferryman 子代理生命周期钩子（Codex SubagentStart/SubagentStop）—— fire-and-forget fail-open
# 官方文档：subagent 钩子的 session_id = 父会话 id → 与 CC 同款纯计数（嵌套各计一次）。
# 注意：codex exec 不派发钩子（上游 bug openai/codex#26452），仅 TUI 路径生效。
# 可配环境变量：FERRYMAN_DISABLE=1 短路；FERRYMAN_PORT（默认 7311）；
#               FERRYMAN_TOKEN_FILE（默认 ~/ferryman/daemon.token）
if ($env:FERRYMAN_DISABLE -eq '1') { exit 0 }
# 自举：拉起不等就绪（fire-and-forget）
. "$PSScriptRoot\ferryman-ensure.ps1"; Ensure-Ferryman -WaitMs 0
$ErrorActionPreference = 'Stop'
try {
    try {
        [Console]::InputEncoding = [System.Text.Encoding]::UTF8
        [Console]::OutputEncoding = [System.Text.Encoding]::UTF8
    } catch {}
    $port = if ($env:FERRYMAN_PORT) { $env:FERRYMAN_PORT } else { 7311 }
    $tokenFile = if ($env:FERRYMAN_TOKEN_FILE) { $env:FERRYMAN_TOKEN_FILE }
                 else { "$env:USERPROFILE\ferryman\daemon.token" }
    $raw = [Console]::In.ReadToEnd()
    # 抓包：env 直传（手测）或标记文件 ON 存在（Codex 净化钩子 env）
    $dbg = $env:FERRYMAN_HOOK_DEBUG
    if (-not $dbg -and (Test-Path (Join-Path $env:USERPROFILE 'ferryman\hook-debug\ON'))) {
        $dbg = Join-Path $env:USERPROFILE 'ferryman\hook-debug\ferryman-subagent-codex.jsonl'
    }
    if ($dbg) {
        try { Add-Content -Path $dbg -Value $raw -Encoding utf8 } catch {}
    }
    $j = $raw | ConvertFrom-Json
    $evt = if ($j.hook_event_name -eq 'SubagentStart') { 'start' }
           elseif ($j.hook_event_name -eq 'SubagentStop') { 'stop' }
           else { exit 0 }                        # 未知事件：与本钩子无关，静默退出
    $body = @{
        agent      = 'codex'
        session_id = [string]$j.session_id
        event      = $evt
    } | ConvertTo-Json -Compress
    Invoke-RestMethod -Uri "http://127.0.0.1:$port/subagent" -Method Post `
        -Headers @{ Authorization = "Bearer $((Get-Content $tokenFile -Raw -ErrorAction Stop).Trim())" } `
        -ContentType 'application/json' `
        -Body ([System.Text.Encoding]::UTF8.GetBytes($body)) -TimeoutSec 2 | Out-Null
    exit 0
}
catch {
    if ($env:FERRYMAN_HOOK_DEBUG) {
        try { $_ | Out-File $env:FERRYMAN_HOOK_DEBUG -Encoding utf8 -Append } catch {}
    }
    exit 0   # 任何故障静默（fire-and-forget）
}
