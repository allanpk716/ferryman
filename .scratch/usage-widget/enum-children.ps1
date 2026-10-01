param([string]$HwndHex = "")
$ErrorActionPreference = "Stop"
Add-Type @"
using System;
using System.Runtime.InteropServices;
using System.Text;
public class CH {
  [DllImport("user32.dll")] public static extern bool SetProcessDpiAwarenessContext(IntPtr v);
  [DllImport("user32.dll")] public static extern bool EnumChildWindows(IntPtr h, EnumChildProc cb, IntPtr lp);
  public delegate bool EnumChildProc(IntPtr h, IntPtr lp);
  [DllImport("user32.dll", CharSet=CharSet.Unicode)] public static extern int GetClassName(IntPtr h, StringBuilder sb, int n);
  [DllImport("user32.dll")] public static extern bool GetWindowRect(IntPtr h, out RECT r);
  [DllImport("user32.dll")] public static extern bool IsWindowVisible(IntPtr h);
  [StructLayout(LayoutKind.Sequential)] public struct RECT { public int L,T,R,B; }
}
"@
[CH]::SetProcessDpiAwarenessContext([IntPtr]::new(-4)) | Out-Null
$h = [IntPtr]([Convert]::ToInt64($HwndHex, 16))
$cb = {
  param($ch, $lp)
  $cn = New-Object System.Text.StringBuilder 256
  [CH]::GetClassName($ch, $cn, 256) | Out-Null
  $r = New-Object CH+RECT
  [CH]::GetWindowRect($ch, [ref]$r) | Out-Null
  Write-Host ("child 0x{0:X} class={1} vis={2} size={3}x{4}" -f $ch.ToInt64(), $cn.ToString(), [CH]::IsWindowVisible($ch), ($r.R-$r.L), ($r.B-$r.T))
  return $true
}
[CH]::EnumChildWindows($h, $cb, [IntPtr]::Zero) | Out-Null
