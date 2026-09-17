# Ferryman 子代理生命周期钩子（CC SubagentStart/SubagentStop，T32）—— fire-and-forget，任何故障静默
# 上报 daemon /subagent 维护每会话子代理计数（摆渡推迟判定）。
# 可配环境变量：FERRYMAN_DISABLE=1 短路；FERRYMAN_PORT（默认 7311）；
#               FERRYMAN_TOKEN_FILE（默认 ~/ferryman/daemon.token）
if ($env:FERRYMAN_DISABLE -eq '1') { exit 0 }
# 自举：拉起不等就绪（fire-and-forget，下次事件自然上报）
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
    $j = $raw | ConvertFrom-Json
    # SubagentStart/SubagentStop（探针实测 payload 含 session_id/agent_id/agent_type）
    $evt = if ($j.hook_event_name -eq 'SubagentStart') { 'start' }
           elseif ($j.hook_event_name -eq 'SubagentStop') { 'stop' }
           else { exit 0 }                        # 未知事件：与本钩子无关，静默退出
    $token = (Get-Content $tokenFile -Raw -ErrorAction Stop).Trim()
    $body = @{
        agent      = 'cc'
        session_id = [string]$j.session_id
        event      = $evt
        agent_id   = [string]$j.agent_id
    } | ConvertTo-Json -Compress
    # PS5.1 字符串 body 默认 ANSI(GBK)——显式转 UTF-8 字节（与 gate 钩子同坑）
    Invoke-RestMethod -Uri "http://127.0.0.1:$port/subagent" -Method Post `
        -Headers @{ Authorization = "Bearer $token" } `
        -ContentType 'application/json' `
        -Body ([System.Text.Encoding]::UTF8.GetBytes($body)) -TimeoutSec 2 | Out-Null
    exit 0
}
catch {
    if ($env:FERRYMAN_HOOK_DEBUG) {
        try { $_ | Out-File $env:FERRYMAN_HOOK_DEBUG -Encoding utf8 -Append } catch {}
    }
    exit 0   # 连接拒绝/超时/任何非 200（含 401）→ 静默（fail-open，钩子侧不重试）
}
