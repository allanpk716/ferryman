# 探针：枚举 ferryman-widget 进程 (PID 可传参) 的可见窗口，PMv2 DPI 上下文读数
# 用法: powershell -NoProfile -File probe-settings.ps1 [-Pid 34828]
param([int]$TargetPid = 34828)

$src = @'
using System;
using System.Runtime.InteropServices;
using System.Text;
public class WinProbe {
  [DllImport("user32.dll")] public static extern bool SetProcessDpiAwarenessContext(IntPtr value);
  [DllImport("user32.dll")] public static extern bool EnumWindows(EnumWindowsProc cb, IntPtr lp);
  public delegate bool EnumWindowsProc(IntPtr h, IntPtr lp);
  [DllImport("user32.dll")] public static extern uint GetWindowThreadProcessId(IntPtr h, out uint pid);
  [DllImport("user32.dll", CharSet=CharSet.Unicode)] public static extern int GetWindowText(IntPtr h, StringBuilder sb, int n);
  [DllImport("user32.dll")] public static extern bool GetWindowRect(IntPtr h, out RECT r);
  [DllImport("user32.dll")] public static extern bool IsWindowVisible(IntPtr h);
  [DllImport("user32.dll")] public static extern uint GetDpiForWindow(IntPtr h);
  [DllImport("user32.dll")] public static extern int GetDpiAwarenessContextForProcess(IntPtr h);
  [StructLayout(LayoutKind.Sequential)] public struct RECT { public int L,T,R,B; }
}
'@
Add-Type -TypeDefinition $src
[WinProbe]::SetProcessDpiAwarenessContext([IntPtr]::new(-4)) | Out-Null

$found = New-Object System.Collections.ArrayList
$cb = {
  param($h, $lp)
  $procId = [uint32]0
  [WinProbe]::GetWindowThreadProcessId($h, [ref]$procId) | Out-Null
  if ($procId -eq $TargetPid) {
    $sb = New-Object System.Text.StringBuilder 256
    [WinProbe]::GetWindowText($h, $sb, 256) | Out-Null
    $title = $sb.ToString()
    if ($title) {
      $r = New-Object WinProbe+RECT
      [WinProbe]::GetWindowRect($h, [ref]$r) | Out-Null
      $vis = [WinProbe]::IsWindowVisible($h)
      $dpi = [WinProbe]::GetDpiForWindow($h)
      $null = $found.Add(("hwnd=0x{0:X} vis={1} dpi={2} pos=({3},{4}) size={5}x{6} title={7}" -f $h.ToInt64(), $vis, $dpi, $r.L, $r.T, ($r.R-$r.L), ($r.B-$r.T), $title))
    }
  }
  return $true
}
[WinProbe]::EnumWindows($cb, [IntPtr]::Zero) | Out-Null
$found | ForEach-Object { Write-Output $_ }
if (-not $found.Count) { Write-Output "no titled windows for pid $TargetPid" }

# 附带：枚举显示器（PMv2 视角的 DPI 与工作区）
Add-Type -AssemblyName System.Windows.Forms
[System.Windows.Forms.Screen]::AllScreens | ForEach-Object {
  Write-Output ("monitor={0} primary={1} bounds={2} workarea={3}" -f $_.DeviceName, $_.Primary, $_.Bounds, $_.WorkingArea)
}
