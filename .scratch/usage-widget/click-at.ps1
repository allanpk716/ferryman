param([int]$X = 1753, [int]$Y = 815)
$ErrorActionPreference = "Stop"
Add-Type @"
using System;
using System.Runtime.InteropServices;
public class RK {
  [DllImport("user32.dll")] public static extern bool SetProcessDpiAwarenessContext(IntPtr v);
  [DllImport("user32.dll")] public static extern bool SetCursorPos(int x, int y);
  [DllImport("user32.dll")] public static extern void mouse_event(uint flags, int dx, int dy, uint data, UIntPtr extra);
}
"@
[RK]::SetProcessDpiAwarenessContext([IntPtr]::new(-4)) | Out-Null
[RK]::SetCursorPos($X, $Y) | Out-Null
Start-Sleep -Milliseconds 150
[RK]::mouse_event(2, 0, 0, 0, [UIntPtr]::Zero)
[RK]::mouse_event(4, 0, 0, 0, [UIntPtr]::Zero)
"clicked at ($X, $Y)"
