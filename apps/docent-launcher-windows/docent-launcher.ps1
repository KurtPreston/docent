#requires -Version 7.0
<#
.SYNOPSIS
docent launcher (Windows) -- a Spotlight-style, always-on-top picker bound to a
global hotkey (default Ctrl+Alt+Space). Type to fuzzy-filter work items (plus
nested sessions / tickets / PRs); Enter opens/launches a work item, focuses a
session window, or opens a ticket/PR URL; Esc hides. The "Open ↗" button pops
the full dashboard out into your system browser (forwarding the -Token as a
one-time ?token= query param when set).

.DESCRIPTION
Built on WPF (PresentationFramework) + Win32 RegisterHotKey -- both ship with
Windows, so there is no extra runtime to install and no admin required.

Adapted from the legacy docent-powershell launcher for the monorepo split:
work-item rows are pulled from docentd's GET /api/workitems (which may be a REMOTE
docentd). Selecting a work item POSTs /api/workitems/{key}/open or /launch on
docentd; focusing a session POSTs to the LOCAL wsm /focus (the window manager,
from https://github.com/KurtPreston/wsm, that actually owns the windows on this
machine).

.PARAMETER SessionsUrl
Base URL of docentd (serves /api/workitems). Default http://127.0.0.1:39787. Point
this at your remote docentd when docentd runs elsewhere.

.PARAMETER WsmUrl
Base URL of the local wsm window manager (serves /focus). Default
http://127.0.0.1:39788.

.PARAMETER Token
Optional bearer token for docentd (only needed if your docentd authenticates
GET /api/workitems; the default docentd leaves it open).

.PARAMETER Hotkey
Modifier+key string, e.g. "Ctrl+Alt+Space" (default) or "Win+Space".

.PARAMETER SelfTest
Fetch + flatten /api/workitems and print the entries, then exit (no window).

.EXAMPLE
pwsh -File docent-launcher.ps1
pwsh -File docent-launcher.ps1 -SessionsUrl http://desktop:39787 -WsmUrl http://127.0.0.1:39788
#>
[CmdletBinding()]
param(
    [string]$SessionsUrl = $(if ($env:DOCENT_SESSIONS_URL) { $env:DOCENT_SESSIONS_URL } elseif ($env:DOCENT_URL) { $env:DOCENT_URL } else { 'http://127.0.0.1:39787' }),
    [string]$WsmUrl = $(if ($env:WSM_URL) { $env:WSM_URL } else { 'http://127.0.0.1:39788' }),
    [string]$Token = $env:DOCENT_TOKEN,
    [string]$Hotkey = 'Ctrl+Alt+Space',
    [switch]$SelfTest
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

$script:SessionsUrl = $SessionsUrl.TrimEnd('/')
$script:WsmUrl = $WsmUrl.TrimEnd('/')
$script:Token = $Token

# --- data: flatten /api/workitems into pickable entries --------------------
# Self-contained so it can run inside a background runspace (no access to the
# parent session's $script: vars or functions): everything it needs is passed in.
$script:FetchEntries = {
    param([string]$SessionsUrl, [string]$Token)
    # Tolerate older/variant /api/workitems payloads: a group missing jiraUrl, prs,
    # sessions, etc. should yield $null on access, not throw under StrictMode.
    Set-StrictMode -Off
    try {
        $headers = @{}
        if ($Token) { $headers['Authorization'] = "Bearer $Token" }
        $data = Invoke-RestMethod -Uri "$SessionsUrl/api/workitems" -Headers $headers -TimeoutSec 5
    }
    catch { return @() }

    $entries = @()
    $provider = $data.provider
    foreach ($g in @($data.groups)) {
        $ticket = if ($g.PSObject.Properties.Name -contains 'ticket') { $g.ticket } else { $null }

        # One primary row per dashboard work-item group (repo/branch, ticket, etc.).
        $wiLabel = if ($g.repo -and $g.branch) { "$($g.repo)  $($g.branch)" }
                   elseif ($g.repo) { $g.repo }
                   elseif ($ticket -and $g.summary) { "$ticket  $($g.summary)" }
                   elseif ($ticket) { $ticket }
                   elseif ($g.key) { $g.key }
                   else { 'Work item' }
        $wiSub = @()
        if ($ticket -and $g.repo -and $g.branch) { $wiSub += $ticket }
        if ($g.summary -and $g.repo -and $g.branch) { $wiSub += $g.summary }
        if ($g.status) { $wiSub += $g.status }
        if ($g.jiraStatus) { $wiSub += $g.jiraStatus }
        if ($g.openPath) { $wiSub += $g.openPath }
        $entries += [PSCustomObject]@{
            Type     = 'workitem'
            Label    = $wiLabel
            Sub      = ($wiSub -join '  ·  ')
            Name     = $null
            Host     = $null
            Url      = $null
            Key      = $g.key
            Provider = $provider
            DeepLink = $g.deepLink
            Color    = $g.color
            Sort     = if ($g.needsFollowup) { 0 } else { 1 }
            Search   = "$wiLabel $ticket $($g.summary) $($g.repo) $($g.branch) $($g.openPath) $($g.status)".ToLowerInvariant()
        }

        foreach ($s in @($g.sessions)) {
            $label = $s.name
            $sub = @()
            if ($ticket) { $sub += $ticket }
            if ($s.host) { $sub += $s.host }
            if ($s.needsFollowup) { $sub += '● follow-up' }
            elseif (-not $s.live) { $sub += 'closed' }
            $entries += [PSCustomObject]@{
                Type     = 'session'
                Label    = $label
                Sub      = ($sub -join '  ·  ')
                Name     = $s.name
                Host     = $s.host
                Url      = $null
                Key      = $null
                Provider = $null
                DeepLink = $null
                Color    = $s.color
                Sort     = if ($s.needsFollowup) { 2 } elseif ($s.live) { 3 } else { 4 }
                Search   = "$label $ticket $($s.host)".ToLowerInvariant()
            }
        }
        foreach ($pr in @($g.prs)) {
            $label = "PR #$($pr.prNumber)  $($pr.title)"
            $entries += [PSCustomObject]@{
                Type     = 'pr'
                Label    = $label
                Sub      = (@($ticket, $pr.repo, $pr.state) | Where-Object { $_ } ) -join '  ·  '
                Name     = $null
                Host     = $null
                Url      = $pr.url
                Key      = $null
                Provider = $null
                DeepLink = $null
                Color    = $g.color
                Sort     = 5
                Search   = "$label $ticket $($pr.repo)".ToLowerInvariant()
            }
        }
        if ($ticket -and @($g.sessions).Count -eq 0 -and @($g.prs).Count -eq 0 -and $g.jiraUrl) {
            $entries += [PSCustomObject]@{
                Type     = 'ticket'
                Label    = "$ticket  $($g.summary)"
                Sub      = (@($g.jiraStatus) | Where-Object { $_ }) -join ''
                Name     = $null
                Host     = $null
                Url      = $g.jiraUrl
                Key      = $null
                Provider = $null
                DeepLink = $null
                Color    = $g.color
                Sort     = 6
                Search   = "$ticket $($g.summary)".ToLowerInvariant()
            }
        }
    }
    return @($entries | Sort-Object Sort, Label)
}

# Thin synchronous wrapper used by -SelfTest (the live UI fetches async instead).
function Get-LauncherEntries {
    return @(& $script:FetchEntries $script:SessionsUrl $script:Token)
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

# Activate the chosen entry: open/launch a work item via docentd, focus a
# session via the LOCAL wsm, or open a ticket/PR URL in a browser.
function Invoke-LauncherEntry {
    param($Entry)
    if (-not $Entry) { return }
    if ($Entry.Type -eq 'session') {
        try {
            $headers = @{ 'Content-Type' = 'application/json' }
            $body = @{ name = $Entry.Name; host = $Entry.Host } | ConvertTo-Json
            Invoke-RestMethod -Uri "$script:WsmUrl/focus" -Method Post -Headers $headers `
                -Body $body -TimeoutSec 5 | Out-Null
        }
        catch { }
    }
    elseif ($Entry.Type -eq 'workitem' -and $Entry.Key) {
        $headers = @{ 'Content-Type' = 'application/json' }
        if ($script:Token) { $headers['Authorization'] = "Bearer $($script:Token)" }
        $keyEnc = [uri]::EscapeDataString($Entry.Key)
        $tryOpen = ($Entry.Provider -eq 'cursor') -or [bool]$Entry.DeepLink

        if ($tryOpen) {
            $link = $Entry.DeepLink
            try {
                $resp = Invoke-RestMethod -Uri "$script:SessionsUrl/api/workitems/$keyEnc/open" `
                    -Method Post -Headers $headers -TimeoutSec 10
                if ($resp.deepLink) { $link = $resp.deepLink }
            }
            catch {
                # Fall through to cached deepLink or /launch.
            }
            if ($link) {
                try { Start-Process $link }
                catch { Write-Warning "Could not open deep link '$link': $_" }
                return
            }
        }

        try {
            Invoke-RestMethod -Uri "$script:SessionsUrl/api/workitems/$keyEnc/launch" `
                -Method Post -Headers $headers -TimeoutSec 35 | Out-Null
        }
        catch {
            Write-Warning "docent launch failed for '$($Entry.Key)': $_"
        }
    }
    elseif ($Entry.Url) {
        Start-Process $Entry.Url
    }
}

if ($SelfTest) {
    $e = @(Get-LauncherEntries)
    Write-Host "launcher self-test: $($e.Count) entries from $script:SessionsUrl/api/workitems (focus -> $script:WsmUrl/focus)"
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
      <Grid>
        <Grid.ColumnDefinitions>
          <ColumnDefinition Width="*"/>
          <ColumnDefinition Width="Auto"/>
        </Grid.ColumnDefinitions>
        <TextBox x:Name="Search" Grid.Column="0" FontSize="20" Padding="10,8" BorderThickness="0"
                 Background="#1A1E2B" Foreground="#E6E8EF" CaretBrush="#7AA2F7"
                 FontFamily="Segoe UI"/>
        <Button x:Name="PopOut" Grid.Column="1" Margin="8,0,0,0" Focusable="False"
                Background="#1A1E2B" Foreground="#7AA2F7" BorderThickness="0" Cursor="Hand"
                Padding="12,8" ToolTip="Open the dashboard in your system browser">
          <Button.Template>
            <ControlTemplate TargetType="Button">
              <Border x:Name="PopOutBg" CornerRadius="8" Background="{TemplateBinding Background}"
                      Padding="{TemplateBinding Padding}">
                <ContentPresenter HorizontalAlignment="Center" VerticalAlignment="Center"/>
              </Border>
              <ControlTemplate.Triggers>
                <Trigger Property="IsMouseOver" Value="True">
                  <Setter TargetName="PopOutBg" Property="Background" Value="#2A3047"/>
                </Trigger>
              </ControlTemplate.Triggers>
            </ControlTemplate>
          </Button.Template>
          <TextBlock Text="Open &#x2197;" FontSize="14" FontWeight="SemiBold" FontFamily="Segoe UI"/>
        </Button>
      </Grid>
      <ListBox x:Name="Results" Margin="0,8,0,0" MaxHeight="420" BorderThickness="0"
               Background="Transparent" Foreground="#E6E8EF" FontSize="14"
               ScrollViewer.HorizontalScrollBarVisibility="Disabled">
        <ListBox.ItemTemplate>
          <DataTemplate>
            <StackPanel Orientation="Horizontal" Margin="4,5">
              <Border Width="14" Height="14" CornerRadius="4" Margin="2,0,10,0"
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
$popOut = $window.FindName('PopOut')
$script:AllEntries = @()
$script:Loading = $false
$script:FetchPS = $null
$script:FetchHandle = $null
$script:FetchTimer = $null

# A non-actionable list row (loading / empty states). Carries the full entry
# shape so Invoke-LauncherEntry stays safe under Set-StrictMode if selected.
function New-StatusEntry {
    param([string]$Label)
    [PSCustomObject]@{
        Type = $null; Label = $Label; Sub = ''; Name = $null
        Host = $null; Url = $null; Key = $null; Provider = $null
        DeepLink = $null; Color = '#3A4060'; Search = ''
    }
}

# Open the docentd dashboard (served at SessionsUrl) in the system browser. When
# a token is configured we pass it as a one-time ?token= query param; the
# dashboard's auth.js caches it in sessionStorage and strips it from the URL.
function Open-DashboardInBrowser {
    $url = "$script:SessionsUrl/"
    if ($script:Token) {
        $url += "?token=$([uri]::EscapeDataString($script:Token))"
    }
    Hide-Launcher
    try { Start-Process $url }
    catch { Write-Warning "Could not open dashboard '$script:SessionsUrl': $_" }
}

function Update-Results {
    if ($script:Loading) {
        $results.ItemsSource = @(New-StatusEntry -Label 'Loading…')
        return
    }
    $q = $search.Text.ToLowerInvariant().Trim()
    $items = @($script:AllEntries | Where-Object { Test-FuzzyMatch -Query $q -Target $_.Search })
    if ($items.Count -eq 0 -and @($script:AllEntries).Count -eq 0) {
        $results.ItemsSource = @(New-StatusEntry -Label 'No work items (is docentd running?)')
        return
    }
    $results.ItemsSource = $items
    if ($items.Count -gt 0) { $results.SelectedIndex = 0 }
}

# Poll handler for the async /api/workitems fetch. Defined at top level so it runs in
# the script scope (and can see $script: state + Update-Results), unlike a
# GetNewClosure block which would get its own private $script: scope. Runs on the
# UI thread (DispatcherTimer), so touching the ListBox here is safe.
$script:OnFetchTick = {
    if (-not $script:FetchHandle.IsCompleted) { return }
    $script:FetchTimer.Stop()
    $ps = $script:FetchPS
    try { $entries = @($ps.EndInvoke($script:FetchHandle)) }
    catch { $entries = @() }
    finally {
        if ($ps) { $ps.Dispose() }
        $script:FetchPS = $null
    }
    $script:Loading = $false
    $script:AllEntries = $entries
    Update-Results
}

# Fetch /api/workitems off the UI thread so the window paints instantly. Cancels any
# in-flight fetch first so a slow request can't clobber a newer summon.
function Start-LauncherFetch {
    if ($script:FetchTimer) { $script:FetchTimer.Stop() }
    if ($script:FetchPS) {
        try { $script:FetchPS.Stop(); $script:FetchPS.Dispose() } catch { }
        $script:FetchPS = $null
    }

    $script:FetchPS = [PowerShell]::Create()
    [void]$script:FetchPS.AddScript($script:FetchEntries).AddArgument($script:SessionsUrl).AddArgument($script:Token)
    $script:FetchHandle = $script:FetchPS.BeginInvoke()

    $script:FetchTimer = New-Object System.Windows.Threading.DispatcherTimer
    $script:FetchTimer.Interval = [TimeSpan]::FromMilliseconds(100)
    $script:FetchTimer.Add_Tick($script:OnFetchTick)
    $script:FetchTimer.Start()
}

function Show-Launcher {
    $search.Text = ''
    $script:Loading = $true
    $script:AllEntries = @()
    Update-Results
    $window.Show()
    $window.Topmost = $true
    $window.Activate() | Out-Null
    $search.Focus() | Out-Null
    Start-LauncherFetch
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
$popOut.Add_Click({ Open-DashboardInBrowser })
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

Write-Host "docent launcher running. Hotkey: $Hotkey  (sessions: $script:SessionsUrl, focus: $script:WsmUrl)"
Write-Host "Press the hotkey to summon; Esc to dismiss. Close this window to quit."

$app = New-Object System.Windows.Application
try { $app.Run() }
finally {
    [DocentHotKey]::UnregisterHotKey($hwnd, $hotkeyId) | Out-Null
}
