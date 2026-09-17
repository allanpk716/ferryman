# Ferryman 守护进程自举（被各钩子 dot-source，勿单独执行）
# 思路：不做开机自启——任意 agent 的任意钩子触发时探测 :7311，
# 不在则隐藏窗口拉起 ~/ferryman/start-daemon.cmd（install-cc 生成的点火脚本）。
# 常态开销 = 一次 250ms 上限的 TCP 探测；任何故障静默（fail-open 一贯原则）。
function Ensure-Ferryman {
    param([int]$WaitMs = 2500)
    $port = if ($env:FERRYMAN_PORT) { [int]$env:FERRYMAN_PORT } else { 7311 }
    $c = New-Object System.Net.Sockets.TcpClient
    try {
        # Wait(250) 对"连接被拒"（faulted task）也返回 True，必须再查 Connected
        $up = $c.ConnectAsync('127.0.0.1', $port).Wait(250) -and $c.Connected
    } catch { $up = $false } finally { $c.Dispose() }
    if ($up) { return }
    $launcher = Join-Path $env:USERPROFILE 'ferryman\start-daemon.cmd'
    if (-not (Test-Path $launcher)) { return }
    try {
        Start-Process -FilePath $env:ComSpec -ArgumentList '/c', "`"$launcher`"" `
            -WindowStyle Hidden | Out-Null
    } catch { return }
    if ($WaitMs -le 0) { return }
    $deadline = [DateTime]::UtcNow.AddMilliseconds($WaitMs)
    while ([DateTime]::UtcNow -lt $deadline) {
        Start-Sleep -Milliseconds 150
        $c2 = New-Object System.Net.Sockets.TcpClient
        try {
            if ($c2.ConnectAsync('127.0.0.1', $port).Wait(100) -and $c2.Connected) { return }
        } catch {} finally { $c2.Dispose() }
    }
}
