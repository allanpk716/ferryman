param([int]$YSweep = 69)
$ErrorActionPreference = "Stop"
Add-Type @"
using System;
using System.Runtime.InteropServices;
using System.Text;
public class GX {
  [StructLayout(LayoutKind.Sequential)] public struct RECT { public int L,T,R,B; }
  [DllImport("user32.dll")] public static extern bool SetProcessDpiAwarenessContext(IntPtr v);
  [DllImport("user32.dll")] public static extern bool EnumWindows(EnumWindowsProc cb, IntPtr lp);
  public delegate bool EnumWindowsProc(IntPtr h, IntPtr lp);
  [DllImport("user32.dll")] public static extern uint GetWindowThreadProcessId(IntPtr h, out uint pid);
  [DllImport("user32.dll", CharSet=CharSet.Unicode)] public static extern int GetWindowText(IntPtr h, StringBuilder sb, int n);
  [DllImport("user32.dll", CharSet=CharSet.Unicode)] public static extern int GetClassName(IntPtr h, StringBuilder sb, int n);
  [DllImport("user32.dll")] public static extern bool GetWindowRect(IntPtr h, out RECT r);
  [DllImport("user32.dll")] public static extern bool IsWindowVisible(IntPtr h);
  [DllImport("user32.dll")] public static extern bool IsIconic(IntPtr h);
  [DllImport("user32.dll")] public static extern bool SetForegroundWindow(IntPtr h);
  [DllImport("user32.dll")] public static extern bool SetCursorPos(int x, int y);
  [DllImport("user32.dll")] public static extern void mouse_event(uint flags, int dx, int dy, uint data, UIntPtr extra);
}
"@
[GX]::SetProcessDpiAwarenessContext([IntPtr]::new(-4)) | Out-Null
$proc = Get-Process -Name ferryman-widget -ErrorAction Stop | Select-Object -First 1
$tpid = $proc.Id
"pid=$tpid"
$script:found = [IntPtr]::Zero
$cb = {
  param($h, $lp)
  $p = [uint32]0
  [GX]::GetWindowThreadProcessId($h, [ref]$p) | Out-Null
  if ($p -eq $tpid -and [GX]::IsWindowVisible($h)) {
    $sb = New-Object System.Text.StringBuilder 256
    [GX]::GetWindowText($h, $sb, 256) | Out-Null
    $cn = New-Object System.Text.StringBuilder 256
    [GX]::GetClassName($h, $cn, 256) | Out-Null
    if ($sb.ToString().Length -gt 0) {
      $r = New-Object GX+RECT
      [GX]::GetWindowRect($h, [ref]$r) | Out-Null
      if ($sb.ToString().StartsWith("Ferryman")) { $script:found = $h }
    }
  }
  return $true
}
[GX]::EnumWindows($cb, [IntPtr]::Zero) | Out-Null
$wh = $script:found
$r = New-Object GX+RECT
[GX]::GetWindowRect($wh, [ref]$r) | Out-Null
$cx = $r.L + [int](($r.R - $r.L) / 2)
[GX]::SetForegroundWindow($wh) | Out-Null
Start-Sleep -Milliseconds 400
[GX]::SetCursorPos($cx, $r.T + $YSweep) | Out-Null
Start-Sleep -Milliseconds 120
[GX]::mouse_event(2, 0, 0, 0, [UIntPtr]::Zero)
[GX]::mouse_event(4, 0, 0, 0, [UIntPtr]::Zero)
"clicked gear at ($cx, $($r.T + $YSweep))"
for ($i = 0; $i -lt 15; $i++) {
  Start-Sleep -Milliseconds 200
  $script:st = $null
  $cb2 = {
    param($h, $lp)
    $p = [uint32]0
    [GX]::GetWindowThreadProcessId($h, [ref]$p) | Out-Null
    if ($p -eq $tpid) {
      $sb = New-Object System.Text.StringBuilder 256
      [GX]::GetWindowText($h, $sb, 256) | Out-Null
      $t = $sb.ToString()
      $cn2 = New-Object System.Text.StringBuilder 256
      [GX]::GetClassName($h, $cn2, 256) | Out-Null
      if ($cn2.ToString() -eq "Tauri Window" -and $t.Length -gt 0 -and -not $t.StartsWith("Ferryman")) {
        $script:st = $h
      }
    }
    return $true
  }
  [GX]::EnumWindows($cb2, [IntPtr]::Zero) | Out-Null
  if ($script:st) {
    $sr = New-Object GX+RECT
    [GX]::GetWindowRect($script:st, [ref]$sr) | Out-Null
    $t = $i * 200
    Write-Host ("t={0}ms settings hwnd=0x{1:X} visible={2} MINIMIZED={3} rect=({4},{5}) {6}x{7}" -f $t, $script:st.ToInt64(), [GX]::IsWindowVisible($script:st), [GX]::IsIconic($script:st), $sr.L, $sr.T, ($sr.R-$sr.L), ($sr.B-$sr.T))
    break
  }
}
if (-not $script:st) { Write-Host "no new window in 3s" }
