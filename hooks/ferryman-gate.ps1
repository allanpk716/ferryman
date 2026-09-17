# Ferryman 闸门钩子（CC UserPromptSubmit）—— 任何故障一律放行（DESIGN §4 fail-open）
# 可配环境变量：FERRYMAN_DISABLE=1 短路；FERRYMAN_PORT（默认 7311）；
#               FERRYMAN_TOKEN_FILE（默认 ~/ferryman/daemon.token）
if ($env:FERRYMAN_DISABLE -eq '1') { exit 0 }
# 自举：daemon 不在则拉起（gate 只等 400ms——新会话本就无需拦截，POST 失败即放行）
. "$PSScriptRoot\ferryman-ensure.ps1"; Ensure-Ferryman -WaitMs 400
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
    $token = (Get-Content $tokenFile -Raw -ErrorAction Stop).Trim()
    $body = @{
        agent           = 'cc'
        session_id      = [string]$j.session_id
        transcript_path = [string]$j.transcript_path
        cwd             = [string]$j.cwd
        prompt          = [string]$j.prompt
    } | ConvertTo-Json -Compress
    # PS5.1 对字符串 body 默认按 ANSI(GBK) 编码——中文 prompt 会打崩 daemon 的 UTF-8 解码。
    # 必须显式转 UTF-8 字节发送。
    $resp = Invoke-RestMethod -Uri "http://127.0.0.1:$port/gate" -Method Post `
        -Headers @{ Authorization = "Bearer $token" } `
        -ContentType 'application/json' `
        -Body ([System.Text.Encoding]::UTF8.GetBytes($body)) -TimeoutSec 2
    if ($resp.decision -eq 'block') {
        # 原样转发 daemon 的 block 决策给 CC（decision/reason/suppressOriginalPrompt）
        $out = @{ decision = 'block'; reason = $resp.reason; suppressOriginalPrompt = $true } |
            ConvertTo-Json -Compress
        [Console]::Out.Write($out)
        exit 0
    }
    if ($resp.additional_context) {
        $out = @{
            hookSpecificOutput = @{
                hookEventName    = 'UserPromptSubmit'
                additionalContext = $resp.additional_context
            }
        } | ConvertTo-Json -Compress -Depth 5
        [Console]::Out.Write($out)
    }
    exit 0
}
catch {
    # 排障：设 FERRYMAN_HOOK_DEBUG=<文件路径> 时把异常写入该文件（否则完全静默）
    if ($env:FERRYMAN_HOOK_DEBUG) {
        try { $_ | Out-File $env:FERRYMAN_HOOK_DEBUG -Encoding utf8 -Append } catch {}
    }
    exit 0   # 连接拒绝/超时/任何非 200（含 401）→ 放行
}
