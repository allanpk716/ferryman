# Ferryman 闸门钩子（CC UserPromptSubmit）—— 任何故障一律放行（DESIGN §4 fail-open）
if ($env:FERRYMAN_DISABLE -eq '1') { exit 0 }
$ErrorActionPreference = 'Stop'
try {
    $raw = [Console]::In.ReadToEnd()
    $j = $raw | ConvertFrom-Json
    $token = (Get-Content "$env:USERPROFILE\ferryman\daemon.token" -Raw -ErrorAction Stop).Trim()
    $body = @{
        agent           = 'cc'
        session_id      = [string]$j.session_id
        transcript_path = [string]$j.transcript_path
        cwd             = [string]$j.cwd
        prompt          = [string]$j.prompt
    } | ConvertTo-Json -Compress
    $resp = Invoke-RestMethod -Uri 'http://127.0.0.1:7311/gate' -Method Post `
        -Headers @{ Authorization = "Bearer $token" } `
        -ContentType 'application/json' -Body $body -TimeoutSec 2
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
    exit 0   # 连接拒绝/超时/任何非 200 → 放行
}
