param([string]$HwndHex = "")
Add-Type @"
using System; using System.Runtime.InteropServices;
public class CL2 { [DllImport("user32.dll")] public static extern bool PostMessage(IntPtr h, uint m, IntPtr w, IntPtr l); }
"@
$h = [IntPtr]([Convert]::ToInt64($HwndHex, 16))
[CL2]::PostMessage($h, 0x0010, [IntPtr]::Zero, [IntPtr]::Zero) | Out-Null
"WM_CLOSE sent"
