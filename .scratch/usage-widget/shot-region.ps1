# Region screenshot for widget visual verification.
# Params are in the same coordinate space as GetWindowRect output.
# Usage: powershell -NoProfile -File shot-region.ps1 -X 4600 -Y 500 -W 450 -H 900 -Out shot.png
param([int]$X, [int]$Y, [int]$W, [int]$H, [string]$Out)

Add-Type @"
using System;
using System.Runtime.InteropServices;
public class DPIA {
  [DllImport("user32.dll")] public static extern bool SetProcessDPIAware();
}
"@
[DPIA]::SetProcessDPIAware() | Out-Null
Add-Type -AssemblyName System.Drawing

$bmp = New-Object System.Drawing.Bitmap($W, $H)
$g = [System.Drawing.Graphics]::FromImage($bmp)
$g.CopyFromScreen($X, $Y, 0, 0, $bmp.Size)
$g.Dispose()
$bmp.Save($Out, [System.Drawing.Imaging.ImageFormat]::Png)
$bmp.Dispose()
"wrote $Out (${W}x${H} at $X,$Y)"
