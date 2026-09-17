# Ferryman 闸门钩子（Codex UserPromptSubmit，T23 准备）—— 任何故障一律放行（DESIGN §4 fail-open）
# stdin schema 已按官方文档校准（2026-09-17 实测定案）：session_id / transcript_path（rollout
# 路径，可为 null）/ cwd / prompt / hook_event_name / model；SessionStart 另有 source
# （startup|resume|clear|compact）。响应契约：decision:block+reason、顶层 additionalContext。
# 防御式字段映射：Codex 钩子 stdin schema 未经信任流实测，session_id / rollout_path /
# transcript_path 多字段回落（晨间 /hooks 信任后用 FERRYMAN_HOOK_DEBUG 抓真实 payload 校准）。
# 可配环境变量：FERRYMAN_DISABLE=1 短路；FERRYMAN_PORT（默认 7311）；
#               FERRYMAN_TOKEN_FILE（默认 ~/ferryman/daemon.token）；FERRYMAN_HOOK_DEBUG=<文件>
if ($env:FERRYMAN_DISABLE -eq '1') { exit 0 }
# 自举：daemon 不在则拉起（Codex 启动也能点火；只等 400ms）
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
    # 抓包：env 直传（手测）或标记文件 ON 存在（Codex 净化钩子 env，exec/TUI 里只能靠文件开关）
    $dbg = $env:FERRYMAN_HOOK_DEBUG
    if (-not $dbg -and (Test-Path (Join-Path $env:USERPROFILE 'ferryman\hook-debug\ON'))) {
        $dbg = Join-Path $env:USERPROFILE 'ferryman\hook-debug\ferryman-gate-codex.jsonl'
    }
    if ($dbg) {
        try { Add-Content -Path $dbg -Value $raw -Encoding utf8 } catch {}
    }
    $j = $raw | ConvertFrom-Json
    # 字段回落：rollout_path → transcript_path → path
    $tp = [string]$j.rollout_path
    if (-not $tp) { $tp = [string]$j.transcript_path }
    if (-not $tp) { $tp = [string]$j.path }
    # session_id 缺席时从 rollout 文件名推导（daemon 同款：rollout-<ts>-<uuid>.jsonl 取尾段）
    $sid = [string]$j.session_id
    if (-not $sid -and $tp) {
        $stem = [System.IO.Path]::GetFileNameWithoutExtension($tp)
        if ($stem -match '-') { $sid = $stem.Split('-')[-1] } else { $sid = $stem }
    }
    $token = (Get-Content $tokenFile -Raw -ErrorAction Stop).Trim()
    $body = @{
        agent           = 'codex'
        session_id      = $sid
        transcript_path = $tp
        cwd             = [string]$j.cwd
        prompt          = [string]$j.prompt
    } | ConvertTo-Json -Compress
    # PS5.1 对字符串 body 默认按 ANSI(GBK) 编码——必须显式转 UTF-8 字节发送。
    $resp = Invoke-RestMethod -Uri "http://127.0.0.1:$port/gate" -Method Post `
        -Headers @{ Authorization = "Bearer $token" } `
        -ContentType 'application/json' `
        -Body ([System.Text.Encoding]::UTF8.GetBytes($body)) -TimeoutSec 2
    if ($resp.decision -eq 'block') {
        # Codex 认同构 block JSON（DESIGN §5：reason 非空）
        $out = @{ decision = 'block'; reason = $resp.reason; suppressOriginalPrompt = $true } |
            ConvertTo-Json -Compress
        [Console]::Out.Write($out)
        exit 0
    }
    if ($resp.additional_context) {
        # Codex 契约（官方文档）：顶层 additionalContext 字段（CC 的 hookSpecificOutput
        # 包装是 Claude 专属，Codex 不认）；纯文本 stdout 亦可，但 JSON 字段最明确
        $out = @{ additionalContext = $resp.additional_context } | ConvertTo-Json -Compress
        [Console]::Out.Write($out)
    }
    exit 0
}
catch {
    if ($env:FERRYMAN_HOOK_DEBUG) {
        try { $_ | Out-File $env:FERRYMAN_HOOK_DEBUG -Encoding utf8 -Append } catch {}
    }
    exit 0   # 连接拒绝/超时/任何非 200（含 401）→ 放行
}
