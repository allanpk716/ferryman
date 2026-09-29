# Collapse-simulation test for widget geometry self-heal (ADR-0016).
# Finds the running FerrymanWidget window, force-resizes it to 60x275 physical px
# via SetWindowPos (same external force as the RDP DPI collapse), then polls until
# the watchdog restores the design size (132x620 logical = 198x930 physical @150%).
# Usage: powershell -NoProfile -ExecutionPolicy Bypass -File collapse-widget-test.ps1 [-Rounds 3]

param([int]$Rounds = 3)
$ErrorActionPreference = 'Stop'

Add-Type @"
using System;
using System.Runtime.InteropServices;
public class W {
  [StructLayout(LayoutKind.Sequential)] public struct RECT { public int Left; public int Top; public int Right; public int Bottom; }
  [DllImport("user32.dll")] public static extern bool GetWindowRect(IntPtr h, out RECT r);
  [DllImport("user32.dll", SetLastError = true)] public static extern bool SetWindowPos(IntPtr h, IntPtr after, int x, int y, int cx, int cy, uint flags);
}
"@

# Process name: live widget may run as either ferryman-widget or FerrymanWidget
$proc = Get-Process -Name ferryman-widget -ErrorAction SilentlyContinue
if (-not $proc) { $proc = Get-Process -Name FerrymanWidget -ErrorAction SilentlyContinue }
if (-not $proc) { throw "widget process not found" }
$proc = $proc | Select-Object -First 1
"process: $($proc.ProcessName) pid=$($proc.Id) path=$($proc.Path)"
$h = $proc.MainWindowHandle
if ($h -eq [IntPtr]::Zero) { throw "MainWindowHandle is 0" }

function Get-Rect([IntPtr]$h) {
  $r = New-Object W+RECT
  [W]::GetWindowRect($h, [ref]$r) | Out-Null
  [pscustomobject]@{ X = $r.Left; Y = $r.Top; W = ($r.Right - $r.Left); H = ($r.Bottom - $r.Top) }
}

# SWP_NOZORDER 0x0004 | SWP_NOACTIVATE 0x0010
$SWP = 0x0004 -bor 0x0010
$pass = 0; $fail = 0

for ($i = 1; $i -le $Rounds; $i++) {
  "── round $i ──"
  $before = Get-Rect $h
  "before:      X=$($before.X) Y=$($before.Y) W=$($before.W) H=$($before.H)"
  [W]::SetWindowPos($h, [IntPtr]::Zero, $before.X, $before.Y, 60, 275, $SWP) | Out-Null
  Start-Sleep -Milliseconds 300
  $collapsed = Get-Rect $h
  "just after:  W=$($collapsed.W) H=$($collapsed.H)"
  $healed = $null
  $t0 = Get-Date
  for ($s = 0; $s -lt 14; $s++) {
    Start-Sleep -Milliseconds 500
    $r = Get-Rect $h
    if (([math]::Abs($r.W - 198) -le 3 -and [math]::Abs($r.H - 930) -le 3) -or
        ([math]::Abs($r.W - 132) -le 3 -and [math]::Abs($r.H - 620) -le 3)) { $healed = $r; break }
  }
  if ($healed) {
    $ms = [int]((Get-Date) - $t0).TotalMilliseconds
    "healed in ${ms}ms: X=$($healed.X) Y=$($healed.Y) W=$($healed.W) H=$($healed.H)"
    $pass++
  } else {
    $r = Get-Rect $h
    "!! NOT healed within 7s: W=$($r.W) H=$($r.H)"
    $fail++
  }
}
"result: $pass pass / $fail fail"
if ($fail -gt 0) { exit 1 }
