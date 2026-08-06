#requires -Version 7.0
<#
.SYNOPSIS
Keeps the docent cockpit open as its own always-available app window, bound to a
global hotkey (default Ctrl+Alt+C).

.DESCRIPTION
The cockpit is meant to be a window you never close, not a browser tab you lose
among twenty others. This script gives it that shape:

- It opens the cockpit with the browser's --app flag, so it gets a chromeless
  window of its own with no tab strip, address bar, or sibling tabs to hide
  behind. A dedicated --user-data-dir keeps it out of your normal browser
  profile, so closing your last browser window never takes the cockpit with it
  and the window title stays stable enough to find again.
- The hotkey is open-or-focus, never open-again: a second press finds the
  existing window instead of stacking duplicates.
- It parks the window on a named Windows virtual desktop (default "cockpit") and
  jumps there on focus, so the cockpit gets a screen of its own instead of
  competing with editor windows. It does this through the same MScholtes
  VirtualDesktop module wsm uses, and addresses the desktop by name, so wsm sees
  and reuses the same desktop rather than a rival one. Without that module the
  script still works; it just leaves window placement to Windows.

This is the counterpart to docent-launcher.ps1: that one is a transient picker
you summon and dismiss, this one is a persistent surface you jump to.

.PARAMETER Url
Base URL of docentd. Default http://127.0.0.1:39787.

.PARAMETER Token
Optional bearer token for docentd. Passed once as ?token=, which the dashboard
caches in sessionStorage and strips from the URL.

.PARAMETER Hotkey
Modifier+key string, e.g. "Ctrl+Alt+C" (default).

.PARAMETER Desktop
Name of the virtual desktop to park the cockpit on. Default "cockpit". Pass ''
to leave placement alone.

.PARAMETER Browser
Path to a Chromium-based browser. Default: auto-detect Edge, then Chrome.

.PARAMETER Once
Open (or focus) the cockpit and exit, without registering a hotkey. Useful from
another launcher or a shortcut.

.EXAMPLE
pwsh -File docent-cockpit.ps1
pwsh -File docent-cockpit.ps1 -Url http://desktop:39787 -Hotkey "Ctrl+Alt+D"
pwsh -File docent-cockpit.ps1 -Once
#>
[CmdletBinding()]
param(
    [string]$Url = $(if ($env:DOCENT_URL) { $env:DOCENT_URL } else { 'http://127.0.0.1:39787' }),
    [string]$Token = $env:DOCENT_TOKEN,
    [string]$Hotkey = 'Ctrl+Alt+C',
    [string]$Desktop = 'cockpit',
    [string]$Browser = '',
    [switch]$Once
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

$script:Url = $Url.TrimEnd('/')
$script:Token = $Token

# A profile dir of its own. Without it the cockpit shares the default profile,
# where it is one window among many and dies with the rest of the browser.
$script:ProfileDir = Join-Path $env:LOCALAPPDATA 'docent\cockpit-profile'

# The window is identified by its title. Chromium titles an --app window after
# the page's document.title, which the cockpit page sets to this exact string.
$script:WindowTitle = 'docent cockpit'

Add-Type -AssemblyName PresentationFramework, PresentationCore, WindowsBase

Add-Type @"
using System;
using System.Text;
using System.Runtime.InteropServices;
public static class DocentCockpitWin {
    [DllImport("user32.dll")] public static extern bool RegisterHotKey(IntPtr hWnd, int id, uint fsModifiers, uint vk);
    [DllImport("user32.dll")] public static extern bool UnregisterHotKey(IntPtr hWnd, int id);
    [DllImport("user32.dll")] public static extern bool SetForegroundWindow(IntPtr hWnd);
    [DllImport("user32.dll")] public static extern bool ShowWindow(IntPtr hWnd, int nCmdShow);
    [DllImport("user32.dll")] public static extern bool IsIconic(IntPtr hWnd);
    [DllImport("user32.dll")] public static extern bool EnumWindows(EnumWindowsProc cb, IntPtr lParam);
    [DllImport("user32.dll")] public static extern bool IsWindowVisible(IntPtr hWnd);
    [DllImport("user32.dll", CharSet = CharSet.Unicode)] public static extern int GetWindowTextW(IntPtr hWnd, StringBuilder text, int count);
    [DllImport("user32.dll", CharSet = CharSet.Unicode)] public static extern int GetClassNameW(IntPtr hWnd, StringBuilder text, int count);
    public delegate bool EnumWindowsProc(IntPtr hWnd, IntPtr lParam);

    public static IntPtr FindByTitle(string needle, string className) {
        IntPtr found = IntPtr.Zero;
        EnumWindows(delegate(IntPtr h, IntPtr p) {
            if (!IsWindowVisible(h)) return true;
            StringBuilder t = new StringBuilder(512);
            if (GetWindowTextW(h, t, t.Capacity) == 0) return true;
            if (t.ToString().IndexOf(needle, StringComparison.OrdinalIgnoreCase) < 0) return true;
            if (!string.IsNullOrEmpty(className)) {
                StringBuilder c = new StringBuilder(256);
                GetClassNameW(h, c, c.Capacity);
                // Chromium's top-level windows all use this class; matching it
                // avoids grabbing an editor or terminal that merely happens to
                // have the cockpit's name in its title.
                if (c.ToString().IndexOf(className, StringComparison.OrdinalIgnoreCase) < 0) return true;
            }
            found = h;
            return false;
        }, IntPtr.Zero);
        return found;
    }
}
"@

function Resolve-Browser {
    if ($Browser) { return $Browser }
    # ${env:ProgramFiles(x86)} needs the braces: without them PowerShell reads
    # $env:ProgramFiles and treats "(x86)" as literal text, which silently yields
    # a path that never exists. Edge in particular installs under the x86 tree
    # even on 64-bit Windows, so that entry is the one that usually matches.
    $candidates = @(
        "${env:ProgramFiles(x86)}\Microsoft\Edge\Application\msedge.exe",
        "$env:ProgramFiles\Microsoft\Edge\Application\msedge.exe",
        "$env:ProgramFiles\Google\Chrome\Application\chrome.exe",
        "${env:ProgramFiles(x86)}\Google\Chrome\Application\chrome.exe"
    )
    foreach ($c in $candidates) {
        if ($c -and (Test-Path $c)) { return $c }
    }
    foreach ($n in @('msedge', 'chrome')) {
        $cmd = Get-Command $n -ErrorAction SilentlyContinue
        if ($cmd) { return $cmd.Source }
    }
    return ''
}

function Get-CockpitUrl {
    $u = "$script:Url/"
    if ($script:Token) { $u += "?token=$([uri]::EscapeDataString($script:Token))" }
    return $u
}

function Find-CockpitWindow {
    return [DocentCockpitWin]::FindByTitle($script:WindowTitle, 'Chrome_WidgetWin')
}

# Virtual-desktop placement is best-effort: the MScholtes VirtualDesktop module
# is a hard requirement for wsm but not for this script, and the whole thing
# still does its job (a persistent app window on a hotkey) without it. Resolved
# once and cached, since the check is a module probe.
$script:VdChecked = $false
$script:VdOk = $false
function Test-VirtualDesktop {
    if ($script:VdChecked) { return $script:VdOk }
    $script:VdChecked = $true
    if (-not $Desktop) { return $false }
    if (-not (Get-Module -Name VirtualDesktop)) {
        if (-not (Get-Module -ListAvailable -Name VirtualDesktop)) {
            Write-Verbose "VirtualDesktop module not installed; skipping desktop placement. Run: Install-Module VirtualDesktop -Scope CurrentUser"
            return $false
        }
        try { Import-Module VirtualDesktop -ErrorAction Stop -DisableNameChecking }
        catch {
            Write-Warning "Could not import the VirtualDesktop module: $_"
            return $false
        }
    }
    $script:VdOk = $true
    return $true
}

# Find-or-create the named desktop. Named, not indexed, so it survives desktops
# being added and removed around it -- and so wsm, which also addresses desktops
# by name, reuses this one instead of making a second.
function Get-CockpitDesktop {
    if (-not (Test-VirtualDesktop)) { return $null }
    try {
        $count = Get-DesktopCount
        for ($i = 0; $i -lt $count; $i++) {
            $d = Get-Desktop -Index $i
            if ((Get-DesktopName -Desktop $d) -eq $Desktop) { return $d }
        }
        $d = New-Desktop
        Set-DesktopName -Desktop $d -Name $Desktop | Out-Null
        return $d
    }
    catch {
        Write-Warning "Virtual desktop '$Desktop' unavailable: $_"
        return $null
    }
}

# Focus-or-open. Switching to the hosting desktop before activating matters:
# SetForegroundWindow alone is unreliable across desktops, and the point of
# parking the cockpit is that it lives somewhere you are not currently looking.
function Show-Cockpit {
    $hwnd = Find-CockpitWindow
    if ($hwnd -ne [IntPtr]::Zero) {
        if (Test-VirtualDesktop) {
            try {
                $d = Get-DesktopFromWindow -Hwnd $hwnd
                if ($d) { Switch-Desktop -Desktop $d | Out-Null }
            }
            catch { Write-Verbose "Could not switch to the cockpit's desktop: $_" }
        }
        if ([DocentCockpitWin]::IsIconic($hwnd)) {
            [DocentCockpitWin]::ShowWindow($hwnd, 9) | Out-Null  # SW_RESTORE
        }
        [DocentCockpitWin]::SetForegroundWindow($hwnd) | Out-Null
        return
    }

    $exe = Resolve-Browser
    if (-not $exe) {
        Write-Warning "No Chromium-based browser found; pass -Browser <path to msedge.exe or chrome.exe>."
        return
    }
    New-Item -ItemType Directory -Force -Path $script:ProfileDir | Out-Null
    $launchArgs = @(
        "--app=$(Get-CockpitUrl)",
        "--user-data-dir=$script:ProfileDir",
        '--window-size=1600,1000',
        # The cockpit is a local tool; suppress the profile-level noise a fresh
        # user-data-dir would otherwise show on every first launch.
        '--no-first-run',
        '--no-default-browser-check'
    )
    Start-Process -FilePath $exe -ArgumentList $launchArgs | Out-Null

    $desk = Get-CockpitDesktop
    if (-not $desk) { return }
    # Poll for the window rather than sleeping a fixed interval: a cold browser
    # start is seconds, a warm one milliseconds, and moving it late is worse than
    # useless because the user has already seen it appear on the wrong desktop.
    $deadline = (Get-Date).AddSeconds(15)
    while ((Get-Date) -lt $deadline) {
        Start-Sleep -Milliseconds 250
        $hwnd = Find-CockpitWindow
        if ($hwnd -eq [IntPtr]::Zero) { continue }
        try { Move-Window -Desktop $desk -Hwnd $hwnd | Out-Null }
        catch { Write-Verbose "Could not move the cockpit to desktop '$Desktop': $_" }
        return
    }
    Write-Verbose "Cockpit window did not appear within 15s; leaving it wherever it lands."
}

if ($Once) {
    Show-Cockpit
    return
}

function ConvertTo-HotkeyParts {
    param([string]$Spec)
    $mods = 0; $vk = 0
    foreach ($part in ($Spec -split '\+')) {
        switch ($part.Trim().ToLowerInvariant()) {
            'ctrl' { $mods = $mods -bor 0x0002 }
            'control' { $mods = $mods -bor 0x0002 }
            'alt' { $mods = $mods -bor 0x0001 }
            'shift' { $mods = $mods -bor 0x0004 }
            'win' { $mods = $mods -bor 0x0008 }
            'space' { $vk = 0x20 }
            default {
                $k = $part.Trim().ToUpperInvariant()
                if ($k.Length -eq 1) { $vk = [int][char]$k }
            }
        }
    }
    return @{ Mods = [uint32]$mods; Vk = [uint32]$vk }
}

# A zero-sized hidden window exists only to own the hotkey registration; the
# cockpit itself is a browser window this script does not otherwise manage.
[xml]$xaml = @"
<Window xmlns="http://schemas.microsoft.com/winfx/2006/xaml/presentation"
        WindowStyle="None" ShowInTaskbar="False" Width="0" Height="0"
        Visibility="Hidden"/>
"@
$reader = New-Object System.Xml.XmlNodeReader $xaml
$hidden = [Windows.Markup.XamlReader]::Load($reader)

$helper = New-Object System.Windows.Interop.WindowInteropHelper $hidden
$hwnd = $helper.EnsureHandle()
$hk = ConvertTo-HotkeyParts -Spec $Hotkey
$hotkeyId = 0xD0D
$source = [System.Windows.Interop.HwndSource]::FromHwnd($hwnd)
$source.AddHook({
        param($h, $msg, $wParam, $lParam, [ref]$handled)
        if ($msg -eq 0x0312 -and ([int]$wParam -eq $hotkeyId)) {
            Show-Cockpit
            $handled.Value = $true
        }
        return [IntPtr]::Zero
    })

if (-not [DocentCockpitWin]::RegisterHotKey($hwnd, $hotkeyId, $hk.Mods, $hk.Vk)) {
    Write-Warning "Could not register hotkey '$Hotkey' (already in use?)."
}

# Open it now, so starting this script leaves you with a cockpit rather than
# only the promise of one.
Show-Cockpit

$where = if (Test-VirtualDesktop) { "desktop '$Desktop'" } else { 'the current desktop' }
Write-Host "docent cockpit on $where. Hotkey: $Hotkey  ($script:Url)"
Write-Host "Close this console to stop watching the hotkey; the cockpit window survives."

$app = New-Object System.Windows.Application
try { $app.Run() }
finally {
    [DocentCockpitWin]::UnregisterHotKey($hwnd, $hotkeyId) | Out-Null
}
