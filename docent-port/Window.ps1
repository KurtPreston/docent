Set-StrictMode -Version Latest

# Windows window manager: enumerate live Cursor windows, launch a remote Cursor
# window and reliably find its HWND, plus focus/match helpers keyed on the
# worktree folder name.
#
# Ported from docent-powershell/src/Private/Window.ps1 (+ ConvertFrom-DocentCursorTitle
# from Feeds.ps1) so docent-wm-windows can serve /windows, /focus, and /open
# without the full legacy module. Depends on Native.ps1 + Desktop.ps1 +
# Logging.ps1 being dot-sourced first.

# Locate Cursor.exe: explicit config wins, then the standard per-user install.
function Resolve-DocentCursorExe {
    [CmdletBinding()]
    param([PSCustomObject]$Config)

    if ($Config -and $Config.cursorExe) {
        if (Test-Path -LiteralPath $Config.cursorExe) { return $Config.cursorExe }
        throw "Configured cursorExe not found: $($Config.cursorExe)"
    }

    $candidates = @(
        (Join-Path $env:LOCALAPPDATA 'Programs/cursor/Cursor.exe'),
        (Join-Path $env:ProgramFiles 'Cursor/Cursor.exe')
    )
    foreach ($c in $candidates) {
        if ($c -and (Test-Path -LiteralPath $c)) { return $c }
    }
    $cmd = Get-Command cursor -ErrorAction SilentlyContinue
    if ($cmd) { return $cmd.Source }

    throw "Could not locate Cursor.exe. Set 'cursorExe' in config."
}

# The basename of a (POSIX or Windows) path, used to match window titles.
function Get-DocentLeafName {
    param([Parameter(Mandatory)][string]$Path)
    return (($Path -replace '\\', '/').TrimEnd('/') -split '/')[-1]
}

# Parse a Cursor window title into its workspace leaf and optional SSH host.
# Remote Cursor windows render as either
#   "<leaf> [SSH: <host>] - Cursor"            (no file open yet)
#   "<file> - <leaf> [SSH: <host>] - Cursor"   (a file is open)
# and local windows as "<file> - <leaf> - Cursor" / "<leaf> - Cursor".
function ConvertFrom-DocentCursorTitle {
    [CmdletBinding()]
    param([AllowNull()][AllowEmptyString()][string]$Title)

    $result = @{ Leaf = $null; Host = $null }
    if ([string]::IsNullOrWhiteSpace($Title)) { return $result }

    $m = [regex]::Match($Title, '\[SSH:\s*(?<host>[^\]]+)\]')
    if ($m.Success) {
        $result.Host = $m.Groups['host'].Value.Trim()
        $pre = $Title.Substring(0, $m.Index).TrimEnd()
        $segs = $pre -split '\s+-\s+'
        $result.Leaf = $segs[-1].Trim()
        return $result
    }

    $core = $Title
    if ($core.EndsWith(' - Cursor')) { $core = $core.Substring(0, $core.Length - ' - Cursor'.Length) }
    $segs = $core -split '\s+-\s+'
    $leaf = $segs[-1].Trim()
    if ($leaf -and $leaf -ne 'Cursor') { $result.Leaf = $leaf }
    return $result
}

# Cursor windows currently visible (filtered by process name).
function Get-DocentCursorWindows {
    [CmdletBinding()]
    param([PSCustomObject]$Config)

    $procName = if ($Config -and $Config.processName) { $Config.processName } else { 'Cursor' }
    $procIds = @(Get-Process -Name $procName -ErrorAction SilentlyContinue | Select-Object -ExpandProperty Id)
    if ($procIds.Count -eq 0) { return @() }

    Get-DocentAllWindows | Where-Object { $procIds -contains $_.Pid }
}

# Decide whether a window title belongs to a given remote workspace. Matching is
# host-aware and anchored so that, e.g., leaf 'salsa-next' does NOT match the
# window for 'salsa-next-b'.
function Test-DocentWorkspaceWindow {
    [CmdletBinding()]
    param(
        [AllowEmptyString()][AllowNull()][string]$Title,
        [Parameter(Mandatory)][string]$LeafName,
        [string]$RemoteHost
    )
    if ([string]::IsNullOrWhiteSpace($Title)) { return $false }

    if ($RemoteHost) {
        if ($Title.Contains("$LeafName [SSH: $RemoteHost]")) { return $true }
    }
    if ($Title -eq "$LeafName - Cursor") { return $true }

    if (-not $RemoteHost) {
        $parsed = ConvertFrom-DocentCursorTitle -Title $Title
        if ($parsed.Leaf -eq $LeafName -and [string]::IsNullOrEmpty($parsed.Host)) { return $true }
    }
    return $false
}

# Find an existing Cursor window for a workspace (host-aware title match).
function Find-DocentCursorWindow {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)][PSCustomObject]$Config,
        [Parameter(Mandatory)][string]$LeafName,
        [string]$RemoteHost
    )
    Get-DocentCursorWindows -Config $Config |
        Where-Object { Test-DocentWorkspaceWindow -Title $_.Title -LeafName $LeafName -RemoteHost $RemoteHost } |
        Select-Object -First 1
}

# Launch a remote Cursor window for $Uri and return its HWND once it appears.
# Mitigates the known `--folder-uri vscode-remote://` hang/no-op when Cursor is
# already running: non-blocking launch (Start-Process), poll for a NEW window
# whose title contains the folder leaf, and retry with a short delay.
function Open-DocentCursorWindow {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)][PSCustomObject]$Config,
        [Parameter(Mandatory)][string]$Uri,
        [Parameter(Mandatory)][string]$LeafName,
        [string]$RemoteHost
    )

    $exe = Resolve-DocentCursorExe -Config $Config
    Write-DocentDebug "Cursor.exe: $exe"

    # Idempotency / folder-uri no-op mitigation: when the workspace is ALREADY
    # open, `--new-window --folder-uri <same folder>` is a no-op. Adopt the
    # existing window instead of waiting out a launch that never produces a HWND.
    $existing = Find-DocentCursorWindow -Config $Config -LeafName $LeafName -RemoteHost $RemoteHost
    if ($existing) {
        Write-DocentInfo "Workspace '$LeafName' already open (hwnd $($existing.Hwnd)); adopting existing window."
        return $existing.Hwnd
    }

    $retries = [int]$Config.launchRetries
    $timeout = [int]$Config.launchTimeoutSec
    $delay = [int]$Config.launchDelaySec

    for ($attempt = 1; $attempt -le ($retries + 1); $attempt++) {
        $before = @(Get-DocentCursorWindows -Config $Config | Select-Object -ExpandProperty Hwnd)
        Write-DocentInfo "Launching Cursor (attempt $attempt) for '$LeafName'."
        Write-DocentDebug "$exe --new-window --folder-uri $Uri"

        Start-Process -FilePath $exe -ArgumentList @('--new-window', '--folder-uri', $Uri) | Out-Null

        $deadline = (Get-Date).AddSeconds($timeout)
        while ((Get-Date) -lt $deadline) {
            Start-Sleep -Milliseconds 500
            $current = Get-DocentCursorWindows -Config $Config

            $match = $current | Where-Object {
                ($before -notcontains $_.Hwnd) -and
                (Test-DocentWorkspaceWindow -Title $_.Title -LeafName $LeafName -RemoteHost $RemoteHost)
            } | Select-Object -First 1
            if ($match) {
                Write-DocentInfo "Matched window: '$($match.Title)' (hwnd $($match.Hwnd))."
                return $match.Hwnd
            }
        }

        $current = Get-DocentCursorWindows -Config $Config
        $newAny = $current | Where-Object { $before -notcontains $_.Hwnd } | Select-Object -First 1
        if ($newAny) {
            Write-DocentWarn "No title match for '$LeafName'; using new window '$($newAny.Title)'."
            return $newAny.Hwnd
        }

        Write-DocentWarn "No new Cursor window after ${timeout}s (likely the folder-uri hang). Retrying in ${delay}s."
        Start-Sleep -Seconds $delay
    }

    throw "Failed to open a Cursor window for '$LeafName' after $($retries + 1) attempts."
}
