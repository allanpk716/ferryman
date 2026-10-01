param([string]$HwndHex = "", [string]$Out = "shot-settings-live.png")
$ErrorActionPreference = "Stop"
Add-Type @"
using System;
using System.Runtime.InteropServices;
public class SW2 {
  [DllImport("user32.dll")] public static extern bool SetProcessDpiAwarenessContext(IntPtr v);
  [DllImport("user32.dll")] public static extern bool ShowWindow(IntPtr h, int cmd);
  [DllImport("user32.dll")] public static extern bool SetForegroundWindow(IntPtr h);
  [DllImport("user32.dll")] public static extern bool IsWindowVisible(IntPtr h);
  [DllImport("user32.dll")] public static extern bool PrintWindow(IntPtr h, IntPtr hdc, uint flags);
  [DllImport("user32.dll")] public static extern bool GetWindowRect(IntPtr h, out RECT r);
  [StructLayout(LayoutKind.Sequential)] public struct RECT { public int L,T,R,B; }
}
"@
[SW2]::SetProcessDpiAwarenessContext([IntPtr]::new(-4)) | Out-Null
$h = [IntPtr]([Convert]::ToInt64($HwndHex, 16))
[SW2]::SetForegroundWindow($h) | Out-Null
Start-Sleep -Milliseconds 300
"already-visible check: " + [SW2]::IsWindowVisible($h)
Add-Type -AssemblyName System.Drawing
$r = New-Object SW2+RECT
[SW2]::GetWindowRect($h, [ref]$r) | Out-Null
$w = $r.R - $r.L; $hg = $r.B - $r.T
"rect: ($($r.L),$($r.T)) ${w}x${hg}"
$bmp = New-Object System.Drawing.Bitmap($w, $hg)
$g = [System.Drawing.Graphics]::FromImage($bmp)
$hdc = $g.GetHdc()
$ok = [SW2]::PrintWindow($h, $hdc, 2)
$g.ReleaseHdc($hdc); $g.Dispose()
$bmp.Save($Out, [System.Drawing.Imaging.ImageFormat]::Png); $bmp.Dispose()
"PrintWindow=$ok -> $Out"
