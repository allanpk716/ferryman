# 0.2.4 真机验收：SendInput 点击 widget 齿轮 → 独立设置窗应打开（invoke open_settings_window）。
# 量设置窗物理尺寸+DPI（PMv2 上下文）、PrintWindow 截图、PostMessage WM_CLOSE 关闭（设置窗允许默认关闭=销毁）。
# 齿轮位置=窗口顶部 grip 之下（full 档 padding10+grip~24+gap5+gear半高8 ≈ 顶起 46px，横居中）；
# 点击点小扫重试（42..54px），点到 conn-warn/disc 都无碍（点 disc 只开详情卡，关闭即可再试）。
# 用法: powershell -NoProfile -ExecutionPolicy Bypass -File gear-settings-e2e.ps1 [-Shot shot-settings-live.png]

param([string]$Shot = "shot-settings-live.png")
$ErrorActionPreference = 'Stop'

$src = @'
using System;
using System.Runtime.InteropServices;
using System.Text;
public class GE {
  [StructLayout(LayoutKind.Sequential)] public struct RECT { public int L,T,R,B; }
  [DllImport("user32.dll")] public static extern bool SetProcessDpiAwarenessContext(IntPtr v);
  [DllImport("user32.dll")] public static extern bool EnumWindows(EnumWindowsProc cb, IntPtr lp);
  public delegate bool EnumWindowsProc(IntPtr h, IntPtr lp);
  [DllImport("user32.dll")] public static extern uint GetWindowThreadProcessId(IntPtr h, out uint pid);
  [DllImport("user32.dll", CharSet=CharSet.Unicode)] public static extern int GetWindowText(IntPtr h, StringBuilder sb, int n);
  [DllImport("user32.dll")] public static extern bool GetWindowRect(IntPtr h, out RECT r);
  [DllImport("user32.dll")] public static extern bool IsWindowVisible(IntPtr h);
  [DllImport("user32.dll")] public static extern uint GetDpiForWindow(IntPtr h);
  [DllImport("user32.dll")] public static extern bool SetForegroundWindow(IntPtr h);
  [DllImport("user32.dll")] public static extern bool SetCursorPos(int x, int y);
  [DllImport("user32.dll")] public static extern void mouse_event(uint flags, int dx, int dy, uint data, UIntPtr extra);
  [DllImport("user32.dll")] public static extern bool PrintWindow(IntPtr h, IntPtr hdc, uint flags);
  [DllImport("user32.dll")] public static extern bool PostMessage(IntPtr h, uint msg, IntPtr w, IntPtr l);
}
'@
Add-Type -TypeDefinition $src
[GE]::SetProcessDpiAwarenessContext([IntPtr]::new(-4)) | Out-Null

$proc = Get-Process -Name ferryman-widget -ErrorAction SilentlyContinue
if (-not $proc) { $proc = Get-Process -Name FerrymanWidget -ErrorAction SilentlyContinue }
if (-not $proc) { throw "widget process not found" }
$proc = $proc | Select-Object -First 1
$targetPid = $proc.Id
"widget pid=$targetPid"

function Find-Win([string]$titleSub) {
  $script:found = [IntPtr]::Zero
  $cb = {
    param($h, $lp)
    $p = [uint32]0
    [GE]::GetWindowThreadProcessId($h, [ref]$p) | Out-Null
    if ($p -eq $targetPid -and [GE]::IsWindowVisible($h)) {
      $sb = New-Object System.Text.StringBuilder 256
      [GE]::GetWindowText($h, $sb, 256) | Out-Null
      if ($sb.ToString().Contains($titleSub)) { $script:found = $h; return $false }
    }
    return $true
  }
  [GE]::EnumWindows($cb, [IntPtr]::Zero) | Out-Null
  return $script:found
}

$wh = Find-Win '悬浮窗'
if ($wh -eq [IntPtr]::Zero) { throw "widget window not found" }
$r = New-Object GE+RECT
[GE]::GetWindowRect($wh, [ref]$r) | Out-Null
"widget rect: ($($r.L),$($r.T)) $($r.R-$r.L)x$($r.B-$r.T) dpi=$([GE]::GetDpiForWindow($wh))"

[GE]::SetForegroundWindow($wh) | Out-Null
Start-Sleep -Milliseconds 400

$cx = $r.L + [int](($r.R - $r.L) / 2)
# 齿轮位≈顶起 46 逻辑px（padding10+grip24+gap5+gear半高8）；物理 = ×实际缩放
# （H/620：DPI 144→1.5、96→1.0，RDP 会话漂移两边都对）。围绕基位小扫重试。
$scale = ($r.B - $r.T) / 620.0
$base = [int](46 * $scale)
$settings = [IntPtr]::Zero
foreach ($dy in @($base, ($base - 6), ($base + 6), ($base - 12), ($base + 12))) {
  [GE]::SetCursorPos($cx, $r.T + $dy) | Out-Null
  Start-Sleep -Milliseconds 120
  [GE]::mouse_event(2, 0, 0, 0, [UIntPtr]::Zero)   # LEFTDOWN
  [GE]::mouse_event(4, 0, 0, 0, [UIntPtr]::Zero)   # LEFTUP
  for ($i = 0; $i -lt 8; $i++) {
    Start-Sleep -Milliseconds 200
    $settings = Find-Win '显示配置'
    if ($settings -ne [IntPtr]::Zero) { break }
  }
  if ($settings -ne [IntPtr]::Zero) { "opened at dy=$dy"; break }
  "no settings window after dy=$dy, retrying"
}
if ($settings -eq [IntPtr]::Zero) { throw "settings window never appeared" }

Start-Sleep -Milliseconds 800  # 等 WebView 首帧渲染再截图
$sr = New-Object GE+RECT
[GE]::GetWindowRect($settings, [ref]$sr) | Out-Null
$dpi = [GE]::GetDpiForWindow($settings)
$sw = $sr.R - $sr.L; $sh = $sr.B - $sr.T
"settings rect: ($($sr.L),$($sr.T)) ${sw}x${sh} dpi=$dpi"
"logical = physical / $($dpi/96.0) => $([math]::Round($sw/($dpi/96.0),1)) x $([math]::Round($sh/($dpi/96.0),1))"

Add-Type -AssemblyName System.Drawing
$bmp = New-Object System.Drawing.Bitmap($sw, $sh)
$g = [System.Drawing.Graphics]::FromImage($bmp)
$hdc = $g.GetHdc()
$ok = [GE]::PrintWindow($settings, $hdc, 2)
$g.ReleaseHdc($hdc); $g.Dispose()
$bmp.Save($Shot, [System.Drawing.Imaging.ImageFormat]::Png); $bmp.Dispose()
"PrintWindow=$ok -> $Shot"

# 详情卡可能被误开（dy 扫描点到 disc）——点一下窗口空白处关掉，再关设置窗
[GE]::SetCursorPos($cx, $r.T + 200) | Out-Null
Start-Sleep -Milliseconds 100
[GE]::mouse_event(2, 0, 0, 0, [UIntPtr]::Zero); [GE]::mouse_event(4, 0, 0, 0, [UIntPtr]::Zero)
Start-Sleep -Milliseconds 200
[GE]::PostMessage($settings, 0x0010, [IntPtr]::Zero, [IntPtr]::Zero) | Out-Null  # WM_CLOSE
Start-Sleep -Milliseconds 600
$still = Find-Win '显示配置'
"after WM_CLOSE: settings still open = $($still -ne [IntPtr]::Zero)"
