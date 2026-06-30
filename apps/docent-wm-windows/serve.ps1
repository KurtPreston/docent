# docent-wm-windows: local REST window manager for docent.
# Endpoints: GET /health, GET /windows, POST /open, POST /focus
# Port from env DOCENT_WM_PORT or -Port (default 39788).

param(
    [int]$Port = $(if ($env:DOCENT_WM_PORT) { [int]$env:DOCENT_WM_PORT } else { 39788 }),
    [string]$CorsOrigin = '*'
)

$ErrorActionPreference = 'Stop'
$prefix = "http://127.0.0.1:$Port/"

# Dot-source docent window helpers when running from monorepo migration path.
$docentSrc = Join-Path $PSScriptRoot '../../docent-port'
if (Test-Path (Join-Path $docentSrc 'Window.ps1')) {
    . (Join-Path $docentSrc 'Window.ps1')
}

$listener = [System.Net.HttpListener]::new()
$listener.Prefixes.Add($prefix)
$listener.Start()
Write-Host "docent-wm-windows serving on $prefix"

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
            # Stub: return empty until Window.ps1 port is wired.
            $wins = @()
            if (Get-Command Get-DocentCursorWindows -ErrorAction SilentlyContinue) {
                foreach ($w in (Get-DocentCursorWindows -Config @{ processName = 'Cursor' })) {
                    $wins += @{ id = $w.Title; title = $w.Title; app = 'Cursor' }
                }
            }
            Send-Json $ctx @{ windows = $wins }
            continue
        }
        if ($req.HttpMethod -eq 'POST' -and $path -eq '/focus') {
            $body = Read-Body $req
            $name = if ($body.name) { [string]$body.name } elseif ($body.id) { [string]$body.id } else { $null }
            if (-not $name) { Send-Json $ctx @{ ok = $false; error = 'name or id required' } 400; continue }
            Send-Json $ctx @{ ok = $true; action = 'focused'; name = $name }
            continue
        }
        if ($req.HttpMethod -eq 'POST' -and $path -eq '/open') {
            $body = Read-Body $req
            if (-not $body.host -or -not $body.path) { Send-Json $ctx @{ ok = $false; error = 'host and path required' } 400; continue }
            Send-Json $ctx @{ ok = $true; action = 'opened'; name = $body.name }
            continue
        }
        Send-Json $ctx @{ ok = $false; error = 'not found' } 404
    }
}
finally {
    $listener.Stop()
    $listener.Close()
}
