# Ferryman 闸门钩子（CC：UserPromptSubmit＝打字闸门；PostToolUse+AskUserQuestion＝作答闸门 #36/ADR-0028）
# —— 任何故障一律放行（DESIGN §4 fail-open）
# 可配环境变量：FERRYMAN_DISABLE=1 短路；FERRYMAN_PORT（默认 15700）；
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
    $port = if ($env:FERRYMAN_PORT) { $env:FERRYMAN_PORT } else { 15700 }
    $tokenFile = if ($env:FERRYMAN_TOKEN_FILE) { $env:FERRYMAN_TOKEN_FILE }
                 else { "$env:USERPROFILE\ferryman\daemon.token" }
    $raw = [Console]::In.ReadToEnd()
    $j = $raw | ConvertFrom-Json
    $token = (Get-Content $tokenFile -Raw -ErrorAction Stop).Trim()
    if ($j.hook_event_name -eq 'PostToolUse') {
        # ---- 作答闸门（#36/ADR-0028）：AskUserQuestion 作答后、答案发给模型前询问闸门。
        # matcher 已限 AskUserQuestion，此处再验一次（手工装错防误拦）；其余工具零输出放行。
        # 被拦 → continue:false + stopReason（拦截文案）；放行且 daemon 带警告
        # additional_context（observe 档警告/待交接标记警告，US16/US17）→ 经
        # PostToolUse 的 additionalContext 通道带给模型（CC 包成系统提醒，
        # 同打字闸门 warn 机制）；纯放行/任何故障零输出（fail-open）。
        if ($j.tool_name -ne 'AskUserQuestion') { exit 0 }
        # 答案取自钩子输入的 tool_response.answers（问题→所选标签映射；
        # multiSelect 的数组值并入一行），格式化为“问题→所选答案”、多问以；相连。
        $answers = @()
        if ($j.tool_response -and $j.tool_response.answers) {
            foreach ($p in $j.tool_response.answers.PSObject.Properties) {
                $v = $p.Value
                if ($v -is [System.Array]) { $v = ($v -join '、') }
                $answers += ('{0}→{1}' -f $p.Name, $v)
            }
        }
        $body = @{
            agent           = 'cc'
            session_id      = [string]$j.session_id
            transcript_path = [string]$j.transcript_path
            cwd             = [string]$j.cwd
            prompt          = ($answers -join '；')
            entry           = 'answer'
        } | ConvertTo-Json -Compress
        # PS5.1 对字符串 body 默认按 ANSI(GBK) 编码——中文答案会打崩 daemon 的 UTF-8 解码，
        # 必须显式转 UTF-8 字节发送（同打字分支）。
        $resp = Invoke-RestMethod -Uri "http://127.0.0.1:$port/gate" -Method Post `
            -Headers @{ Authorization = "Bearer $token" } `
            -ContentType 'application/json' `
            -Body ([System.Text.Encoding]::UTF8.GetBytes($body)) -TimeoutSec 2
        if ($resp.decision -eq 'block') {
            $out = @{ continue = $false; stopReason = $resp.reason } | ConvertTo-Json -Compress
            [Console]::Out.Write($out)
            exit 0
        }
        # 放行带警告（US16/US17）：additional_context 经 additionalContext 通道回写
        #（CC 包成系统提醒带给模型；纯放行仍零输出）。
        if ($resp.additional_context) {
            $out = @{
                hookSpecificOutput = @{
                    hookEventName    = 'PostToolUse'
                    additionalContext = $resp.additional_context
                }
            } | ConvertTo-Json -Compress -Depth 5
            [Console]::Out.Write($out)
        }
        exit 0
    }
    # ---- 打字闸门（UserPromptSubmit，原逻辑逐字不变）----
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
        # 转发 daemon 的 block 决策给 CC：suppressOriginalPrompt 按官方文档置于 hookSpecificOutput 内（顶层保留兼容旧契约）
        $out = @{ decision = 'block'; reason = $resp.reason; suppressOriginalPrompt = $true; hookSpecificOutput = @{ hookEventName = 'UserPromptSubmit'; suppressOriginalPrompt = $true } } |
            ConvertTo-Json -Compress -Depth 5
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
