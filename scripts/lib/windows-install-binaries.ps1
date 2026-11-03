# Build the selected Windows binaries before configuring services.

# --- build dashboard + docentd (local only) ----------------------------------
# The Vite output (apps\docentd\web\dist) is embedded into docentd via the
# `embed` build tag, so the installed binary is self-contained (no -web needed).
if ($Mode -eq 'local' -and -not $NoBuild) {
    if (-not (Test-GoOk)) {
        Write-Error "need Go >= 1.22 on PATH to build docentd locally (install Go or pass -NoBuild / use -RemoteUrl)."
        exit 1
    }
    if (-not (Test-NodeOk)) {
        Write-Error "need Node >= 18 on PATH to build the dashboard (install Node 18+, or pass -NoBuild)."
        exit 1
    }
    Log "building dashboard -> $WebRoot\dist"
    Step "npm ci + npm run build ($WebRoot)" {
        & npm --prefix $WebRoot ci
        if ($LASTEXITCODE -ne 0) { throw "npm ci failed" }
        & npm --prefix $WebRoot run build
        if ($LASTEXITCODE -ne 0) { throw "npm run build failed" }
    }
    Log "building docentd -> $DocentdBin (dashboard embedded via -tags embed)"
    Step "go build -tags embed -o $DocentdBin ./apps/docentd" {
        New-Item -ItemType Directory -Force -Path $BinDir | Out-Null
        & go build -tags embed -o $DocentdBin (Join-Path $Root 'apps\docentd')
        if ($LASTEXITCODE -ne 0) { throw "go build failed" }
    }
}
elseif ($Mode -eq 'local') {
    Log "skipping dashboard + docentd build (-NoBuild)"
}

# --- build docent-tunnel (remote + tunnel) -----------------------------------
if ($UseTunnel -and -not $NoBuild) {
    if (-not (Test-GoOk)) {
        Write-Error "need Go >= 1.22 on PATH to build docent-tunnel for the SSH forward (install Go, or pass -NoTunnel to hit the remote URL directly)."
        exit 1
    }
    Log "building docent-tunnel -> $DocentTunnelBin"
    Step "go build -o $DocentTunnelBin ./apps/docent-tunnel" {
        New-Item -ItemType Directory -Force -Path $BinDir | Out-Null
        & go build -o $DocentTunnelBin (Join-Path $Root 'apps\docent-tunnel')
        if ($LASTEXITCODE -ne 0) { throw "go build failed" }
    }
}
elseif ($UseTunnel) {
    Log "skipping docent-tunnel build (-NoBuild)"
}

