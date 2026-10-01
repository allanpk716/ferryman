# PrintWindow (PW_RENDERFULLCONTENT=2) capture of the running widget window.
# CopyFromScreen is blind to the DComp overlay plane after programmatic moves,
# so rendering verification must go through PrintWindow (2026-09-29 lesson).
# Usage: powershell -NoProfile -ExecutionPolicy Bypass -File shot-printwindow.ps1 -Out shot.png

param([string]$Out = "shot-widget.png")
$ErrorActionPreference = 'Stop'

Add-Type @"
using System;
using System.Runtime.InteropServices;
public class PW {
  [StructLayout(LayoutKind.Sequential)] public struct RECT { public int Left; public int Top; public int Right; public int Bottom; }
  [DllImport("user32.dll")] public static extern bool GetWindowRect(IntPtr h, out RECT r);
  [DllImport("user32.dll")] public static extern bool PrintWindow(IntPtr h, IntPtr hdc, uint flags);
  [DllImport("user32.dll")] public static extern bool SetProcessDPIAware();
}
"@
[PW]::SetProcessDPIAware() | Out-Null
Add-Type -AssemblyName System.Drawing

$proc = Get-Process -Name ferryman-widget -ErrorAction SilentlyContinue
if (-not $proc) { $proc = Get-Process -Name FerrymanWidget -ErrorAction SilentlyContinue }
if (-not $proc) { throw "widget process not found" }
$proc = $proc | Select-Object -First 1
$h = $proc.MainWindowHandle
if ($h -eq [IntPtr]::Zero) { throw "MainWindowHandle is 0" }

$r = New-Object PW+RECT
[PW]::GetWindowRect($h, [ref]$r) | Out-Null
$w = $r.Right - $r.Left; $hgt = $r.Bottom - $r.Top
"window rect: X=$($r.Left) Y=$($r.Top) W=$w H=$hgt"

$bmp = New-Object System.Drawing.Bitmap($w, $hgt)
$g = [System.Drawing.Graphics]::FromImage($bmp)
$hdc = $g.GetHdc()
$ok = [PW]::PrintWindow($h, $hdc, 2)
$g.ReleaseHdc($hdc)
$g.Dispose()
if (-not $ok) { $bmp.Dispose(); throw "PrintWindow returned false" }
$bmp.Save($Out, [System.Drawing.Imaging.ImageFormat]::Png)
$bmp.Dispose()
"wrote $Out (${w}x${hgt}, PrintWindow=$ok)"
