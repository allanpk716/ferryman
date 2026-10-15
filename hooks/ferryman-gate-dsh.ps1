# Ferryman 闸门钩子（dsh CC 钩子桥 UserPromptSubmit，票01 终局修复1 变体）—— 任何故障一律放行（DESIGN §4 fail-open）。与 ferryman-gate.ps1 逐字同源，仅改 agent='dsh' 与 /dsh/gate：桥 base() 恒传空 transcript_path（index.ts:331-333），cc 键必 miss 台账＝永 no-ledger 放行，dsh 键才命中
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
        # 被拦 → continue:false + stopReason（拦截文案）；放行/任何故障零输出（fail-open）。
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
            agent           = 'dsh'
            session_id      = [string]$j.session_id
            transcript_path = [string]$j.transcript_path
            cwd             = [string]$j.cwd
            prompt          = ($answers -join '；')
            entry           = 'answer'
        } | ConvertTo-Json -Compress
        # PS5.1 对字符串 body 默认按 ANSI(GBK) 编码——中文答案会打崩 daemon 的 UTF-8 解码，
        # 必须显式转 UTF-8 字节发送（同打字分支）。
        $resp = Invoke-RestMethod -Uri "http://127.0.0.1:$port/dsh/gate" -Method Post `
            -Headers @{ Authorization = "Bearer $token" } `
            -ContentType 'application/json' `
            -Body ([System.Text.Encoding]::UTF8.GetBytes($body)) -TimeoutSec 2
        if ($resp.decision -eq 'block') {
            $out = @{ continue = $false; stopReason = $resp.reason } | ConvertTo-Json -Compress
            [Console]::Out.Write($out)
        }
        exit 0
    }
    # ---- 打字闸门（UserPromptSubmit，原逻辑逐字不变）----
    $body = @{
        agent           = 'dsh'
        session_id      = [string]$j.session_id
        transcript_path = [string]$j.transcript_path
        cwd             = [string]$j.cwd
        prompt          = [string]$j.prompt
    } | ConvertTo-Json -Compress
    # PS5.1 对字符串 body 默认按 ANSI(GBK) 编码——中文 prompt 会打崩 daemon 的 UTF-8 解码。
    # 必须显式转 UTF-8 字节发送。
    $resp = Invoke-RestMethod -Uri "http://127.0.0.1:$port/dsh/gate" -Method Post `
        -Headers @{ Authorization = "Bearer $token" } `
        -ContentType 'application/json' `
        -Body ([System.Text.Encoding]::UTF8.GetBytes($body)) -TimeoutSec 2
    if ($resp.decision -eq 'block') {
        # 原样转发 daemon 的 block 决策给 dsh 桥（decision/reason/suppressOriginalPrompt；桥折 deny 拒本轮提交）
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
