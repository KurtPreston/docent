Set-StrictMode -Version Latest

# Win32 interop for enumerating top-level windows (Windows backend only). We
# need full window enumeration (not just Process.MainWindowHandle) because
# Electron apps like Cursor host multiple windows under a single process, and
# the freshly-opened remote window may not be the "main" one.
#
# Ported from docent-powershell/src/Private/Native.ps1 so docent-wm-windows can
# enumerate/focus/close Cursor windows without the full legacy module.

function Initialize-DocentNative {
    if (([System.Management.Automation.PSTypeName]'Docent.NativeMethods').Type) { return }

    $signature = @'
using System;
using System.Collections.Generic;
using System.Runtime.InteropServices;
using System.Text;

namespace Docent {
    public class WinInfo {
        public IntPtr Hwnd;
        public uint Pid;
        public string Title;
    }

    public static class NativeMethods {
        public delegate bool EnumWindowsProc(IntPtr hWnd, IntPtr lParam);

        [DllImport("user32.dll")]
        public static extern bool EnumWindows(EnumWindowsProc lpEnumFunc, IntPtr lParam);

        [DllImport("user32.dll")]
        public static extern bool IsWindowVisible(IntPtr hWnd);

        [DllImport("user32.dll", CharSet = CharSet.Unicode)]
        public static extern int GetWindowTextLength(IntPtr hWnd);

        [DllImport("user32.dll", CharSet = CharSet.Unicode)]
        public static extern int GetWindowText(IntPtr hWnd, StringBuilder lpString, int nMaxCount);

        [DllImport("user32.dll")]
        public static extern uint GetWindowThreadProcessId(IntPtr hWnd, out uint lpdwProcessId);

        [DllImport("user32.dll")]
        [return: MarshalAs(UnmanagedType.Bool)]
        public static extern bool SetForegroundWindow(IntPtr hWnd);

        [DllImport("user32.dll")]
        public static extern bool ShowWindow(IntPtr hWnd, int nCmdShow);

        [DllImport("user32.dll", CharSet = CharSet.Auto)]
        public static extern IntPtr SendMessageTimeout(IntPtr hWnd, uint Msg, IntPtr wParam, IntPtr lParam, uint flags, uint timeout, out IntPtr result);

        [DllImport("user32.dll", CharSet = CharSet.Auto)]
        [return: MarshalAs(UnmanagedType.Bool)]
        public static extern bool PostMessage(IntPtr hWnd, uint Msg, IntPtr wParam, IntPtr lParam);

        public const uint WM_CLOSE = 0x0010;
        public const int SW_RESTORE = 9;

        public static List<WinInfo> GetWindows() {
            var list = new List<WinInfo>();
            EnumWindows((h, l) => {
                if (!IsWindowVisible(h)) { return true; }
                int len = GetWindowTextLength(h);
                if (len == 0) { return true; }
                var sb = new StringBuilder(len + 1);
                GetWindowText(h, sb, sb.Capacity);
                uint pid;
                GetWindowThreadProcessId(h, out pid);
                list.Add(new WinInfo { Hwnd = h, Pid = pid, Title = sb.ToString() });
                return true;
            }, IntPtr.Zero);
            return list;
        }
    }
}
'@

    Add-Type -TypeDefinition $signature -Language CSharp
}

# Returns visible top-level windows as PSCustomObjects: Hwnd, Pid, Title.
function Get-DocentAllWindows {
    Initialize-DocentNative
    foreach ($w in [Docent.NativeMethods]::GetWindows()) {
        [PSCustomObject]@{
            Hwnd  = $w.Hwnd
            Pid   = [int]$w.Pid
            Title = $w.Title
        }
    }
}

function Set-DocentForegroundWindow {
    param([Parameter(Mandatory)][IntPtr]$Hwnd)
    Initialize-DocentNative
    [void][Docent.NativeMethods]::ShowWindow($Hwnd, [Docent.NativeMethods]::SW_RESTORE)
    [void][Docent.NativeMethods]::SetForegroundWindow($Hwnd)
}

function Close-DocentWindowHandle {
    param([Parameter(Mandatory)][IntPtr]$Hwnd)
    Initialize-DocentNative
    [void][Docent.NativeMethods]::PostMessage($Hwnd, [Docent.NativeMethods]::WM_CLOSE, [IntPtr]::Zero, [IntPtr]::Zero)
}
