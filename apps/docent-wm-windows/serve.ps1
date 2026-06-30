# docent-wm-windows: local REST window manager for docent.
#
# Endpoints (consumed by docentd's docent-wm collector + the dashboard / launcher):
#   GET  /health            -> "ok"
#   GET  /windows           -> { windows: [ { id, title, app, host } ] }
#   POST /focus  {name|id, host?}      -> { ok, action, name } | 404
#   POST /open   {host, path, name, uri?} -> { ok, action, name }
#
# Real window control is provided by the ported PowerShell helpers in
# ../../docent-port (Logging/Native/Desktop/Window). When those are missing the
# service still answers /health and returns an empty /windows list so docentd
# degrades gracefully. VirtualDesktop is optional: focus/open fall back to plain
# foregrounding when it is not installed.
#
# Port from -Port or env DOCENT_WM_PORT (default 39788).

param(
    [int]$Port = $(if ($env:DOCENT_WM_PORT) { [int]$env:DOCENT_WM_PORT } else { 39788 }),
    [string]$CorsOrigin = '*',
    [string]$CursorExe = $env:DOCENT_CURSOR_EXE,
    [string]$ProcessName = $(if ($env:DOCENT_PROCESS_NAME) { $env:DOCENT_PROCESS_NAME } else { 'Cursor' }),
    [string]$UriTemplate = $(if ($env:DOCENT_URI_TEMPLATE) { $env:DOCENT_URI_TEMPLATE } else { 'vscode-remote://ssh-remote+{host}{path}' })
)

$ErrorActionPreference = 'Stop'
$prefix = "http://127.0.0.1:$Port/"

# --- load the ported window-control helpers --------------------------------
$docentPort = Join-Path $PSScriptRoot '../../docent-port'
$script:HaveWindowHelpers = $false
foreach ($f in @('Logging.ps1', 'Native.ps1', 'Desktop.ps1', 'Window.ps1')) {
    $p = Join-Path $docentPort $f
    if (Test-Path -LiteralPath $p) { . $p }
}
if (Get-Command Get-DocentCursorWindows -ErrorAction SilentlyContinue) {
    $script:HaveWindowHelpers = $true
}
else {
    Write-Warning "docent-port window helpers not found under $docentPort; /windows will be empty and /focus,/open are no-ops."
}

# Config object the Window.ps1 helpers expect.
$script:Config = [PSCustomObject]@{
    processName      = $ProcessName
    cursorExe        = if ($CursorExe) { $CursorExe } else { $null }
    uri              = $UriTemplate
    launchTimeoutSec = 25
    launchRetries    = 2
    launchDelaySec   = 2
}

# Safe property read (helpers enable Set-StrictMode, so $obj.missing throws).
function Get-Prop {
    param($Object, [string]$Name)
    if ($null -eq $Object) { return $null }
    $prop = $Object.PSObject.Properties[$Name]
    if ($prop) { return $prop.Value }
    return $null
}

function Send-Cors {
    param($Response, [string]$Origin)
    $Response.Headers.Add('Access-Control-Allow-Origin', $Origin)
    $Response.Headers.Add('Access-Control-Allow-Methods', 'GET, POST, OPTIONS')
    $Response.Headers.Add('Access-Control-Allow-Headers', 'Content-Type, Authorization')
}

function Send-Json {
    param($Context, $Object, [int]$Status = 200)
    $json = $Object | ConvertTo-Json -Compress -Depth 6
    $bytes = [System.Text.Encoding]::UTF8.GetBytes($json)
    $resp = $Context.Response
    Send-Cors $resp $CorsOrigin
    $resp.ContentType = 'application/json'
    $resp.StatusCode = $Status
    $resp.ContentLength64 = $bytes.Length
    try { $resp.OutputStream.Write($bytes, 0, $bytes.Length) } finally { $resp.OutputStream.Close() }
}

function Read-Body($Request) {
    if (-not $Request.HasEntityBody) { return $null }
    $reader = [System.IO.StreamReader]::new($Request.InputStream, $Request.ContentEncoding)
    try { return ($reader.ReadToEnd() | ConvertFrom-Json) } finally { $reader.Dispose() }
}

# --- handlers --------------------------------------------------------------

# Live Cursor windows in the wmclient shape: { id, title, app, host }.
function Get-WindowList {
    if (-not $script:HaveWindowHelpers) { return @() }
    $out = @()
    foreach ($w in (Get-DocentCursorWindows -Config $script:Config)) {
        $parsed = ConvertFrom-DocentCursorTitle -Title $w.Title
        $entry = @{
            id    = [string]$w.Hwnd
            title = $w.Title
            app   = $script:Config.processName
        }
        if ($parsed.Host) { $entry.host = $parsed.Host }
        $out += $entry
    }
    return $out
}

# Bring the window for a workspace name to the foreground (switching virtual
# desktops when possible). Returns $true if a window was found and focused.
function Invoke-Focus {
    param([string]$Name, [string]$RemoteHost)
    if (-not $script:HaveWindowHelpers -or [string]::IsNullOrWhiteSpace($Name)) { return $false }

    $leaf = Get-DocentLeafName -Path $Name
    $win = Find-DocentCursorWindow -Config $script:Config -LeafName $leaf -RemoteHost $RemoteHost
    if (-not $win) {
        # Retry without host (the title may not yet render its [SSH:] marker).
        $win = Find-DocentCursorWindow -Config $script:Config -LeafName $leaf
    }
    if (-not $win) { return $false }

    # Prefer a docent-created desktop named after the workspace; otherwise jump
    # to whatever desktop currently hosts the window.
    $desktop = Get-DocentDesktopByName -Name $Name
    if ($desktop) { Switch-DocentDesktop -Desktop $desktop }
    else { Switch-DocentDesktopForWindow -Hwnd $win.Hwnd }

    Set-DocentForegroundWindow -Hwnd $win.Hwnd
    return $true
}

# Open (or focus, if already open) a remote Cursor workspace.
function Invoke-Open {
    param([string]$RemoteHost, [string]$Path, [string]$Name, [string]$Uri)
    if (-not $script:HaveWindowHelpers) { throw "window helpers unavailable" }

    $leaf = if ($Name) { Get-DocentLeafName -Path $Name } else { Get-DocentLeafName -Path $Path }
    if (-not $Uri) {
        $Uri = $script:Config.uri.Replace('{host}', $RemoteHost).Replace('{path}', $Path)
    }
    $deskName = if ($Name) { $Name } else { $leaf }

    $target = Get-DocentOrNewDesktop -Name $deskName
    $hwnd = Open-DocentCursorWindow -Config $script:Config -Uri $Uri -LeafName $leaf -RemoteHost $RemoteHost
    if ($hwnd -and $hwnd -ne [IntPtr]::Zero) {
        Move-DocentWindowToDesktop -Desktop $target -Hwnd $hwnd
        if ($target) { Switch-DocentDesktop -Desktop $target }
        Set-DocentForegroundWindow -Hwnd $hwnd
    }
    return $hwnd
}

# --- listener loop ---------------------------------------------------------
$listener = [System.Net.HttpListener]::new()
$listener.Prefixes.Add($prefix)
$listener.Start()
Write-Host "docent-wm-windows serving on $prefix (helpers: $script:HaveWindowHelpers)"

try {
    while ($listener.IsListening) {
        $ctx = $listener.GetContext()
        $req = $ctx.Request
        $path = $req.Url.AbsolutePath

        if ($req.HttpMethod -eq 'OPTIONS') {
            Send-Cors $ctx.Response $CorsOrigin
            $ctx.Response.StatusCode = 204
            $ctx.Response.Close()
            continue
        }

        if ($req.HttpMethod -eq 'GET' -and $path -eq '/health') {
            $resp = $ctx.Response
            Send-Cors $resp $CorsOrigin
            $resp.StatusCode = 200
            $buf = [System.Text.Encoding]::UTF8.GetBytes('ok')
            $resp.ContentLength64 = $buf.Length
            $resp.OutputStream.Write($buf, 0, $buf.Length)
            $resp.OutputStream.Close()
            continue
        }

        if ($req.HttpMethod -eq 'GET' -and $path -eq '/windows') {
            try {
                Send-Json $ctx @{ windows = @(Get-WindowList) }
            }
            catch {
                Send-Json $ctx @{ windows = @(); error = $_.Exception.Message } 500
            }
            continue
        }

        if ($req.HttpMethod -eq 'POST' -and $path -eq '/focus') {
            $body = Read-Body $req
            $name = Get-Prop $body 'name'
            if (-not $name) { $name = Get-Prop $body 'id' }
            $remoteHost = Get-Prop $body 'host'
            if (-not $name) { Send-Json $ctx @{ ok = $false; error = 'name or id required' } 400; continue }
            try {
                if (Invoke-Focus -Name ([string]$name) -RemoteHost ([string]$remoteHost)) {
                    Send-Json $ctx @{ ok = $true; action = 'focused'; name = [string]$name }
                }
                else {
                    Send-Json $ctx @{ ok = $false; error = "no open window for $name" } 404
                }
            }
            catch {
                Send-Json $ctx @{ ok = $false; error = $_.Exception.Message } 500
            }
            continue
        }

        if ($req.HttpMethod -eq 'POST' -and $path -eq '/open') {
            $body = Read-Body $req
            $remoteHost = Get-Prop $body 'host'
            $bpath = Get-Prop $body 'path'
            $name = Get-Prop $body 'name'
            $uri = Get-Prop $body 'uri'
            if (-not $remoteHost -or -not $bpath) {
                Send-Json $ctx @{ ok = $false; error = 'host and path required' } 400; continue
            }
            try {
                $hwnd = Invoke-Open -RemoteHost ([string]$remoteHost) -Path ([string]$bpath) -Name ([string]$name) -Uri ([string]$uri)
                Send-Json $ctx @{ ok = $true; action = 'opened'; name = [string]$name; hwnd = [string]$hwnd }
            }
            catch {
                Send-Json $ctx @{ ok = $false; error = $_.Exception.Message } 500
            }
            continue
        }

        Send-Json $ctx @{ ok = $false; error = 'not found' } 404
    }
}
finally {
    $listener.Stop()
    $listener.Close()
}
