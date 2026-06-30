#requires -Version 7.0
<#
.SYNOPSIS
docent launcher (Windows) -- a Spotlight-style, always-on-top picker bound to a
global hotkey (default Ctrl+Alt+Space). Type to fuzzy-filter your sessions /
tickets / PRs; Enter focuses the session window or opens the ticket/PR URL; Esc
hides.

.DESCRIPTION
Built on WPF (PresentationFramework) + Win32 RegisterHotKey -- both ship with
Windows, so there is no extra runtime to install and no admin required.

Adapted from the legacy docent-powershell launcher for the monorepo split:
session rows are pulled from docentd's GET /sessions (which may be a REMOTE
docentd), while focusing a session POSTs to the LOCAL docent-wm /focus (the
window manager that actually owns the windows on this machine).

.PARAMETER SessionsUrl
Base URL of docentd (serves /sessions). Default http://127.0.0.1:39787. Point
this at your remote docentd when docentd runs elsewhere.

.PARAMETER WmUrl
Base URL of the local docent-wm window manager (serves /focus). Default
http://127.0.0.1:39788.

.PARAMETER Token
Optional bearer token for docentd (only needed if your docentd authenticates
GET /sessions; the default docentd leaves it open).

.PARAMETER Hotkey
Modifier+key string, e.g. "Ctrl+Alt+Space" (default) or "Win+Space".

.PARAMETER SelfTest
Fetch + flatten /sessions and print the entries, then exit (no window).

.EXAMPLE
pwsh -File docent-launcher.ps1
pwsh -File docent-launcher.ps1 -SessionsUrl http://desktop:39787 -WmUrl http://127.0.0.1:39788
#>
[CmdletBinding()]
param(
    [string]$SessionsUrl = $(if ($env:DOCENT_SESSIONS_URL) { $env:DOCENT_SESSIONS_URL } elseif ($env:DOCENT_URL) { $env:DOCENT_URL } else { 'http://127.0.0.1:39787' }),
    [string]$WmUrl = $(if ($env:DOCENT_WM_URL) { $env:DOCENT_WM_URL } else { 'http://127.0.0.1:39788' }),
    [string]$Token = $env:DOCENT_TOKEN,
    [string]$Hotkey = 'Ctrl+Alt+Space',
    [switch]$SelfTest
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

$script:SessionsUrl = $SessionsUrl.TrimEnd('/')
$script:WmUrl = $WmUrl.TrimEnd('/')
$script:Token = $Token

# --- data: flatten /sessions into pickable entries -------------------------
function Get-LauncherEntries {
    try {
        $headers = @{}
        if ($script:Token) { $headers['Authorization'] = "Bearer $script:Token" }
        $data = Invoke-RestMethod -Uri "$script:SessionsUrl/sessions" -Headers $headers -TimeoutSec 5
    }
    catch { return @() }

    $entries = @()
    foreach ($g in @($data.groups)) {
        $ticket = if ($g.PSObject.Properties.Name -contains 'ticket') { $g.ticket } else { $null }
        foreach ($s in @($g.sessions)) {
            $label = $s.name
            $sub = @()
            if ($ticket) { $sub += $ticket }
            if ($s.host) { $sub += $s.host }
            if ($s.needsFollowup) { $sub += '● follow-up' }
            elseif (-not $s.live) { $sub += 'closed' }
            $entries += [PSCustomObject]@{
                Type   = 'session'
                Label  = $label
                Sub    = ($sub -join '  ·  ')
                Name   = $s.name
                Host   = $s.host
                Url    = $null
                Color  = $s.color
                Sort   = if ($s.needsFollowup) { 0 } elseif ($s.live) { 1 } else { 2 }
                Search = "$label $ticket $($s.host)".ToLowerInvariant()
            }
        }
        foreach ($pr in @($g.prs)) {
            $label = "PR #$($pr.prNumber)  $($pr.title)"
            $entries += [PSCustomObject]@{
                Type   = 'pr'
                Label  = $label
                Sub    = (@($ticket, $pr.repo, $pr.state) | Where-Object { $_ } ) -join '  ·  '
                Name   = $null
                Host   = $null
                Url    = $pr.url
                Color  = $g.color
                Sort   = 3
                Search = "$label $ticket $($pr.repo)".ToLowerInvariant()
            }
        }
        if ($ticket -and @($g.sessions).Count -eq 0 -and @($g.prs).Count -eq 0 -and $g.jiraUrl) {
            $entries += [PSCustomObject]@{
                Type   = 'ticket'
                Label  = "$ticket  $($g.summary)"
                Sub    = (@($g.jiraStatus) | Where-Object { $_ }) -join ''
                Name   = $null
                Host   = $null
                Url    = $g.jiraUrl
                Color  = $g.color
                Sort   = 4
                Search = "$ticket $($g.summary)".ToLowerInvariant()
            }
        }
    }
    return @($entries | Sort-Object Sort, Label)
}

# Subsequence fuzzy match (chars of query appear in order in target).
function Test-FuzzyMatch {
    param([string]$Query, [string]$Target)
    if (-not $Query) { return $true }
    $qi = 0
    foreach ($ch in $Target.ToCharArray()) {
        if ($qi -lt $Query.Length -and $ch -eq $Query[$qi]) { $qi++ }
        if ($qi -ge $Query.Length) { return $true }
    }
    return ($qi -ge $Query.Length)
}

# Activate the chosen entry: focus the session via the LOCAL docent-wm, or open
# the ticket/PR URL in a browser.
function Invoke-LauncherEntry {
    param($Entry)
    if (-not $Entry) { return }
    if ($Entry.Type -eq 'session') {
        try {
            $headers = @{ 'Content-Type' = 'application/json' }
            $body = @{ name = $Entry.Name; host = $Entry.Host } | ConvertTo-Json
            Invoke-RestMethod -Uri "$script:WmUrl/focus" -Method Post -Headers $headers `
                -Body $body -TimeoutSec 5 | Out-Null
        }
        catch { }
    }
    elseif ($Entry.Url) {
        Start-Process $Entry.Url
    }
}

if ($SelfTest) {
    $e = @(Get-LauncherEntries)
    Write-Host "launcher self-test: $($e.Count) entries from $script:SessionsUrl/sessions (focus -> $script:WmUrl/focus)"
    $e | Select-Object -First 12 | ForEach-Object { "  [$($_.Type)] $($_.Label)  ($($_.Sub))" }
    $f = @($e | Where-Object { Test-FuzzyMatch -Query 'slk' -Target $_.Search })
    Write-Host "fuzzy 'slk' matches: $($f.Count)"
    return
}

# --- WPF window ------------------------------------------------------------
Add-Type -AssemblyName PresentationFramework, PresentationCore, WindowsBase, System.Windows.Forms

[xml]$xaml = @"
<Window xmlns="http://schemas.microsoft.com/winfx/2006/xaml/presentation"
        xmlns:x="http://schemas.microsoft.com/winfx/2006/xaml"
        WindowStyle="None" ResizeMode="NoResize" AllowsTransparency="True"
        Background="Transparent" ShowInTaskbar="False" Topmost="True"
        WindowStartupLocation="CenterScreen" Width="620" SizeToContent="Height"
        Visibility="Hidden">
  <Border CornerRadius="14" Background="#F20F1117" BorderBrush="#33FFFFFF" BorderThickness="1" Padding="10">
    <StackPanel>
      <TextBox x:Name="Search" FontSize="20" Padding="10,8" BorderThickness="0"
               Background="#1A1E2B" Foreground="#E6E8EF" CaretBrush="#7AA2F7"
               FontFamily="Segoe UI"/>
      <ListBox x:Name="Results" Margin="0,8,0,0" MaxHeight="420" BorderThickness="0"
               Background="Transparent" Foreground="#E6E8EF" FontSize="14"
               ScrollViewer.HorizontalScrollBarVisibility="Disabled">
        <ListBox.ItemTemplate>
          <DataTemplate>
            <StackPanel Orientation="Horizontal" Margin="4,5">
              <Border Width="10" Height="10" CornerRadius="3" Margin="2,0,10,0"
                      Background="{Binding Color}" VerticalAlignment="Center"/>
              <StackPanel>
                <TextBlock Text="{Binding Label}" FontWeight="SemiBold"/>
                <TextBlock Text="{Binding Sub}" Foreground="#9AA0B4" FontSize="11.5"/>
              </StackPanel>
            </StackPanel>
          </DataTemplate>
        </ListBox.ItemTemplate>
      </ListBox>
    </StackPanel>
  </Border>
</Window>
"@

$reader = New-Object System.Xml.XmlNodeReader $xaml
$window = [Windows.Markup.XamlReader]::Load($reader)
$search = $window.FindName('Search')
$results = $window.FindName('Results')
$script:AllEntries = @()

function Update-Results {
    $q = $search.Text.ToLowerInvariant().Trim()
    $items = @($script:AllEntries | Where-Object { Test-FuzzyMatch -Query $q -Target $_.Search })
    $results.ItemsSource = $items
    if ($items.Count -gt 0) { $results.SelectedIndex = 0 }
}

function Show-Launcher {
    $script:AllEntries = @(Get-LauncherEntries)
    $search.Text = ''
    Update-Results
    $window.Show()
    $window.Topmost = $true
    $window.Activate() | Out-Null
    $search.Focus() | Out-Null
}

function Hide-Launcher { $window.Hide() }

function Invoke-Selected {
    $sel = $results.SelectedItem
    Hide-Launcher
    if ($sel) { Invoke-LauncherEntry -Entry $sel }
}

$search.Add_TextChanged({ Update-Results })
$search.Add_PreviewKeyDown({
        param($s, $e)
        switch ($e.Key) {
            'Escape' { Hide-Launcher; $e.Handled = $true }
            'Return' { Invoke-Selected; $e.Handled = $true }
            'Down' { if ($results.SelectedIndex -lt $results.Items.Count - 1) { $results.SelectedIndex++ }; $e.Handled = $true }
            'Up' { if ($results.SelectedIndex -gt 0) { $results.SelectedIndex-- }; $e.Handled = $true }
        }
    })
$results.Add_MouseDoubleClick({ Invoke-Selected })
$window.Add_Deactivated({ Hide-Launcher })

# --- global hotkey via Win32 RegisterHotKey --------------------------------
Add-Type @"
using System;
using System.Runtime.InteropServices;
public static class DocentHotKey {
    [DllImport("user32.dll")] public static extern bool RegisterHotKey(IntPtr hWnd, int id, uint fsModifiers, uint vk);
    [DllImport("user32.dll")] public static extern bool UnregisterHotKey(IntPtr hWnd, int id);
}
"@

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

$helper = New-Object System.Windows.Interop.WindowInteropHelper $window
$hwnd = $helper.EnsureHandle()
$hk = ConvertTo-HotkeyParts -Spec $Hotkey
$hotkeyId = 0xD0C
$source = [System.Windows.Interop.HwndSource]::FromHwnd($hwnd)
$source.AddHook({
        param($hwnd, $msg, $wParam, $lParam, [ref]$handled)
        if ($msg -eq 0x0312 -and ([int]$wParam -eq $hotkeyId)) {
            if ($window.Visibility -eq 'Visible') { Hide-Launcher } else { Show-Launcher }
            $handled.Value = $true
        }
        return [IntPtr]::Zero
    })

if (-not [DocentHotKey]::RegisterHotKey($hwnd, $hotkeyId, $hk.Mods, $hk.Vk)) {
    Write-Warning "Could not register hotkey '$Hotkey' (already in use?). The launcher will still run; press the hotkey owner or restart."
}

Write-Host "docent launcher running. Hotkey: $Hotkey  (sessions: $script:SessionsUrl, focus: $script:WmUrl)"
Write-Host "Press the hotkey to summon; Esc to dismiss. Close this window to quit."

$app = New-Object System.Windows.Application
try { $app.Run() }
finally {
    [DocentHotKey]::UnregisterHotKey($hwnd, $hotkeyId) | Out-Null
}
