param([string]$HwndHex = "")
$ErrorActionPreference = "Stop"
Add-Type @"
using System;
using System.Runtime.InteropServices;
public class RB {
  [StructLayout(LayoutKind.Sequential)] public struct RECT { public int L,T,R,B; }
  [DllImport("user32.dll")] public static extern bool SetProcessDpiAwarenessContext(IntPtr v);
  [DllImport("user32.dll")] public static extern bool GetWindowRect(IntPtr h, out RECT r);
  [DllImport("user32.dll", SetLastError=true)] public static extern bool SetWindowPos(IntPtr h, IntPtr after, int x, int y, int cx, int cy, uint flags);
  [DllImport("user32.dll")] public static extern bool PrintWindow(IntPtr h, IntPtr hdc, uint flags);
}
"@
[RB]::SetProcessDpiAwarenessContext([IntPtr]::new(-4)) | Out-Null
$h = [IntPtr]([Convert]::ToInt64($HwndHex, 16))
$r = New-Object RB+RECT
[RB]::GetWindowRect($h, [ref]$r) | Out-Null
$w = $r.R - $r.L; $hg = $r.B - $r.T
"before: ${w}x${hg} at ($($r.L),$($r.T))"
$SWP = 0x0004 -bor 0x0010   # NOZORDER | NOACTIVATE
[RB]::SetWindowPos($h, [IntPtr]::Zero, $r.L, $r.T, $w + 2, $hg, $SWP) | Out-Null
Start-Sleep -Milliseconds 300
[RB]::SetWindowPos($h, [IntPtr]::Zero, $r.L, $r.T, $w, $hg, $SWP) | Out-Null
Start-Sleep -Milliseconds 800
$r2 = New-Object RB+RECT
[RB]::GetWindowRect($h, [ref]$r2) | Out-Null
"after: $($r2.R-$r2.L)x$($r2.B-$r2.T)"
Add-Type -AssemblyName System.Drawing
$bmp = New-Object System.Drawing.Bitmap($w, $hg)
$g = [System.Drawing.Graphics]::FromImage($bmp)
$hdc = $g.GetHdc()
$ok = [RB]::PrintWindow($h, $hdc, 2)
$g.ReleaseHdc($hdc); $g.Dispose()
$bmp.Save("shot-v024-settings-after-nudge.png", [System.Drawing.Imaging.ImageFormat]::Png); $bmp.Dispose()
"PrintWindow=$ok"
