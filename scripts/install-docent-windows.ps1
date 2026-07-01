#requires -Version 7.0
<#
.SYNOPSIS
Install the docent Windows front-end (docent-launcher-windows) and, optionally,
docentd locally. The window manager lives in the separate wsm project -- install
it from https://github.com/KurtPreston/wsm (default port 39788).

.DESCRIPTION
The Windows counterpart to install-docent-macos.sh / install-docent-linux.sh,
written natively in PowerShell so it runs in your shell without Git Bash / WSL.

The launcher is a PowerShell program (no Go build):
  - docent-launcher-windows  apps/docent-launcher-windows/docent-launcher.ps1  (WPF hotkey picker)
It runs hidden + auto-restarting via a Scheduled Task using the watchdog pattern
(at-logon trigger + a 1-minute repeating watchdog + MultipleInstances=IgnoreNew,
launched through a hidden, waiting .vbs so no console window flashes). The window
manager (wsm) has its own installer in the wsm repo.

On first run it asks whether docentd runs on THIS machine (local) or on a REMOTE
host. Local builds docentd.exe, runs docent-setup, and registers a docentd task.
Remote only verifies the remote /health is reachable and points the launcher +
dashboard at it.

Re-running is safe and idempotent: before (re)registering each task it stops the
task and kills the running program tree, so a re-run always restarts the
programs on the latest code (otherwise the old process keeps serving, since
MultipleInstances=IgnoreNew makes Start a no-op while one is running).

.PARAMETER RemoteUrl
Use a remote docentd at this base URL (skips the prompt + local build).

.PARAMETER Token
Bearer token for the remote docentd (optional).

.PARAMETER SshHost
SSH host for the docent-tunnel forward to a remote docentd (default: the host
parsed from -RemoteUrl). Remote mode uses the forward unless -NoTunnel.

.PARAMETER SshIdentity
SSH private key for the docent-tunnel forward (otherwise ssh-agent is used).

.PARAMETER NoTunnel
Don't set up the SSH forward; point the launcher directly at the remote URL.

.PARAMETER NoTasks
Skip Scheduled Task registration (build/config only).

.PARAMETER NoBuild
Skip docentd.exe build (reuse an existing binary in BinDir).

.PARAMETER NoModules
Deprecated no-op. The VirtualDesktop module is now installed by wsm's own
installer; kept for backward compatibility.

.PARAMETER Hotkey
Launcher hotkey (default: Ctrl+Alt+Space).

.PARAMETER Port
Dashboard port (default: 39787).

.PARAMETER WsmPort
Window-manager port (default: 39788).

.PARAMETER BinDir
Install docentd.exe here (default: ~\.local\bin).

.PARAMETER ConfigDir
Config root (default: ~\.config\docent).

.PARAMETER DryRun
Print actions without changing the system.

.EXAMPLE
pwsh -File scripts/install-docent-windows.ps1 -RemoteUrl http://desktop:39787

.EXAMPLE
.\scripts\install-docent-windows.ps1 -DryRun
#>
[CmdletBinding()]
param(
    [string]$RemoteUrl,
    [string]$Token,
    [string]$SshHost,
    [string]$SshIdentity,
    [switch]$NoTunnel,
    [switch]$NoTasks,
    [switch]$NoBuild,
    [switch]$NoModules,
    [string]$Hotkey = 'Ctrl+Alt+Space',
    [int]$Port = 39787,
    [int]$WsmPort = 39788,
    [string]$BinDir = (Join-Path $HOME '.local\bin'),
    [string]$ConfigDir = (Join-Path $HOME '.config\docent'),
    [switch]$DryRun
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

# Repo root = parent of this script's folder.
$Root = Split-Path -Parent $PSScriptRoot
$WebRoot = Join-Path $Root 'apps\docentd\web'
$ConfigPath = Join-Path $ConfigDir 'docentd.yaml'
$DocentdBin = Join-Path $BinDir 'docentd.exe'

# Environment fallbacks (parity with the macOS installer env vars).
if (-not $RemoteUrl -and $env:DOCENTD_URL) { $RemoteUrl = $env:DOCENTD_URL }
if (-not $Token) { $Token = if ($env:DOCENTD_TOKEN) { $env:DOCENTD_TOKEN } elseif ($env:DOCENT_TOKEN) { $env:DOCENT_TOKEN } else { '' } }
if (-not $SshHost -and $env:DOCENT_TUNNEL_HOST) { $SshHost = $env:DOCENT_TUNNEL_HOST }
if (-not $SshIdentity -and $env:DOCENT_TUNNEL_IDENTITY) { $SshIdentity = $env:DOCENT_TUNNEL_IDENTITY }
if ($env:DOCENT_BIN_DIR) { $BinDir = $env:DOCENT_BIN_DIR; $DocentdBin = Join-Path $BinDir 'docentd.exe' }
if ($env:DOCENT_CONFIG_DIR) { $ConfigDir = $env:DOCENT_CONFIG_DIR; $ConfigPath = Join-Path $ConfigDir 'docentd.yaml' }
$DocentTunnelBin = Join-Path $BinDir 'docent-tunnel.exe'

function Log { param([string]$Message) Write-Host "==> $Message" }
function Step {
    # Run a scriptblock, or just describe it under -DryRun.
    param([string]$What, [scriptblock]$Action)
    if ($DryRun) { Write-Host "[dry-run] $What"; return }
    & $Action
}

# --- locate pwsh 7 (for the task launchers) ----------------------------------
$PwshExe = (Get-Process -Id $PID).Path
if (-not $PwshExe -or -not (Test-Path -LiteralPath $PwshExe)) {
    $cmd = Get-Command pwsh -ErrorAction SilentlyContinue
    $PwshExe = if ($cmd) { $cmd.Source } else { 'C:\Program Files\PowerShell\7\pwsh.exe' }
}
Log "using pwsh: $PwshExe"

# --- Go toolchain (only for a local docentd build) ---------------------------
function Test-GoOk {
    $v = try { (& go version) 2>$null } catch { $null }
    if ($v -match 'go(\d+)\.(\d+)') {
        $maj = [int]$Matches[1]; $min = [int]$Matches[2]
        return ($maj -gt 1) -or ($maj -eq 1 -and $min -ge 22)
    }
    return $false
}

# --- resolve docentd location ------------------------------------------------
$Mode = $null
$Sessions = $null
$UseTunnel = $false

if ($RemoteUrl) {
    $Mode = 'remote'
    $RemoteUrl = $RemoteUrl.TrimEnd('/')
}
elseif ($DryRun) {
    $Mode = 'local'
}
else {
    Write-Host ""
    Write-Host "Where does docentd run?"
    Write-Host "  1) This machine (build + register docentd locally) [default]"
    Write-Host "  2) Remote host  (only install the launcher here)"
    $choice = Read-Host "Choice [1]"
    if ($choice -in '2', 'remote', 'Remote') { $Mode = 'remote' } else { $Mode = 'local' }
}

if ($Mode -eq 'remote') {
    if (-not $RemoteUrl) {
        $RemoteUrl = (Read-Host "Remote docentd base URL (e.g. http://desktop:39787)").TrimEnd('/')
        if (-not $RemoteUrl) { throw "remote docentd URL is required" }
        if (-not $Token) { $Token = Read-Host "Bearer token for $RemoteUrl (blank if none)" }
    }

    # Remote mode reaches docentd through a local SSH forward (docent-tunnel) by
    # default; -NoTunnel opts out and points the launcher at the remote URL.
    $UseTunnel = -not $NoTunnel
    if ($UseTunnel -and -not $SshHost) {
        $defaultHost = ''
        if ($RemoteUrl -match '^[a-zA-Z]+://([^:/]+)') { $defaultHost = $Matches[1] }
        if ($DryRun) { $SshHost = $defaultHost }
        else {
            $inp = Read-Host "SSH host for the dev box [$defaultHost]"
            $SshHost = if ($inp) { $inp } else { $defaultHost }
        }
        if (-not $SshHost) { throw "an SSH host is required for docent-tunnel (pass -SshHost, or -NoTunnel to skip)" }
    }

    if ($UseTunnel) {
        $Sessions = "http://127.0.0.1:$Port"
        Log "reaching remote docentd $RemoteUrl through docent-tunnel -> $SshHost (local 127.0.0.1:$Port)"
    }
    else {
        Log "verifying remote docentd at $RemoteUrl/health"
        if (-not $DryRun) {
            $headers = @{}
            if ($Token) { $headers['Authorization'] = "Bearer $Token" }
            try {
                Invoke-WebRequest -UseBasicParsing -Uri "$RemoteUrl/health" -Headers $headers -TimeoutSec 8 | Out-Null
                Write-Host "  docentd     $RemoteUrl  ok"
            }
            catch {
                Write-Host ""
                Write-Error @"
could not reach $RemoteUrl/health.
  - Is docentd running on the remote host?
  - If it binds 127.0.0.1 only, re-run with -SshHost <host> to set up docent-tunnel,
    or add a tunnel yourself (e.g. ssh -L $Port`:127.0.0.1:$Port desktop).
"@
                exit 1
            }
        }
        $Sessions = $RemoteUrl
    }
}
else {
    $Sessions = "http://127.0.0.1:$Port"
}

# --- build docentd (local only) ----------------------------------------------
if ($Mode -eq 'local' -and -not $NoBuild) {
    if (-not (Test-GoOk)) {
        Write-Error "need Go >= 1.22 on PATH to build docentd locally (install Go or pass -NoBuild / use -RemoteUrl)."
        exit 1
    }
    Log "building docentd -> $DocentdBin"
    Step "go build -o $DocentdBin ./apps/docentd" {
        New-Item -ItemType Directory -Force -Path $BinDir | Out-Null
        & go build -o $DocentdBin (Join-Path $Root 'apps\docentd')
        if ($LASTEXITCODE -ne 0) { throw "go build failed" }
    }
}
elseif ($Mode -eq 'local') {
    Log "skipping docentd build (-NoBuild)"
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

# --- config bootstrap --------------------------------------------------------
function Set-EnvLine {
    param([string]$Key, [string]$Value, [string]$File)
    $lines = if (Test-Path -LiteralPath $File) { @(Get-Content -LiteralPath $File) } else { @() }
    if ($lines -match "^$Key=") {
        $lines = $lines | ForEach-Object { if ($_ -match "^$Key=") { "$Key=$Value" } else { $_ } }
    }
    else {
        $lines += "$Key=$Value"
    }
    Set-Content -LiteralPath $File -Value $lines -Encoding UTF8
}

Log "docent config at $ConfigDir"
Step "mkdir $ConfigDir" { New-Item -ItemType Directory -Force -Path $ConfigDir | Out-Null }

if ($Mode -eq 'local') {
    if (-not (Test-Path -LiteralPath $ConfigPath)) {
        Log "writing $ConfigPath"
        Step "write daemon config $ConfigPath" {
            $yaml = @"
# docentd daemon settings (generated by install-docent-windows.ps1).
port: $Port
refreshSec: 30
wsmUrl: http://127.0.0.1:$WsmPort
configDir: $ConfigDir
# Collector directives + optional ai live in configDir/config.yaml
# Secrets (credential_refs) live in configDir/.env
"@
            Set-Content -LiteralPath $ConfigPath -Value $yaml -Encoding UTF8
        }
    }
    else {
        Log "daemon config present at $ConfigPath (leaving as-is)"
    }

    $directives = Join-Path $ConfigDir 'config.yaml'
    if (-not (Test-Path -LiteralPath $directives)) {
        foreach ($src in @((Join-Path $Root 'userdata\config.yaml'), (Join-Path $Root 'config\docent\config.yaml.example'))) {
            if (Test-Path -LiteralPath $src) {
                Log "seed $directives from $src"
                Step "copy $src -> $directives" { Copy-Item -LiteralPath $src -Destination $directives }
                break
            }
        }
    }
}

# .env: secrets for collectors and, in remote mode, the docentd URL/token used
# by docent-reporter and manual launcher runs.
$envFile = Join-Path $ConfigDir '.env'
if (-not $DryRun) {
    if (-not (Test-Path -LiteralPath $envFile)) { New-Item -ItemType File -Force -Path $envFile | Out-Null }
    if ($Mode -eq 'remote') {
        Set-EnvLine -Key 'DOCENT_URL' -Value $RemoteUrl -File $envFile
        if ($Token) { Set-EnvLine -Key 'DOCENT_TOKEN' -Value $Token -File $envFile }
    }
}

# --- docent-setup (local, when directives are missing) -----------------------
if ($Mode -eq 'local') {
    $directives = Join-Path $ConfigDir 'config.yaml'
    $havePopulated = $false
    if (Test-Path -LiteralPath $directives) {
        $txt = Get-Content -LiteralPath $directives -Raw
        if ($txt -match '(?m)^directives:' -and $txt -match '(?m)^\s+-\s') { $havePopulated = $true }
    }
    if ($havePopulated) {
        Log "directives config present at $directives"
    }
    else {
        Log "running docent-setup to populate $directives"
        if ($DryRun) {
            Write-Host "[dry-run] go run ./apps/docent-setup --config-dir $ConfigDir"
        }
        elseif (Test-GoOk) {
            & go run (Join-Path $Root 'apps\docent-setup') --config-dir $ConfigDir
            if ($LASTEXITCODE -ne 0) { Write-Warning "docent-setup did not complete; you can re-run it later." }
        }
        else {
            Write-Warning "Go not available; run docent-setup later: go run ./apps/docent-setup --config-dir $ConfigDir"
        }
    }
}

# The window manager (wsm) and its VirtualDesktop module prerequisite are
# installed separately from the wsm repo; nothing to do here.

# --- Scheduled Tasks (hidden + watchdog) -------------------------------------
# A hidden, *waiting* .vbs runs the program with no console window. Waiting keeps
# the task instance alive for the program's lifetime, so the 1-minute watchdog
# trigger skips via MultipleInstances=IgnoreNew instead of stacking duplicates,
# and relaunches within ~1 min if the program dies.
function Write-HiddenVbs {
    param(
        [string]$Path,
        [string]$Exe,
        [string]$ArgLine,
        [string]$LogFile,
        [bool]$Wait = $true
    )
    # cmd /c idiom for a spaced exe path + redirection: cmd /c ""exe" args >> "log" 2>&1"
    $cmd = 'cmd /c "' + '"' + $Exe + '" ' + $ArgLine + ' >> "' + $LogFile + '" 2>&1' + '"'
    $literal = $cmd.Replace('"', '""')   # VBS escapes a quote by doubling it
    $waitArg = if ($Wait) { 'True' } else { 'False' }
    $content = @"
' Generated by install-docent-windows.ps1. Runs the command hidden (style 0).
Set shell = CreateObject("WScript.Shell")
shell.Run "$literal", 0, $waitArg
"@
    if ($DryRun) { Write-Host "[dry-run] write $Path"; return }
    Set-Content -LiteralPath $Path -Value $content -Encoding ASCII
    Write-Host "  wrote $Path"
}

# Tear down a previously-installed program so a re-run loads fresh code.
# Register-ScheduledTask -Force only rewrites the task definition, and
# Start-ScheduledTask is a no-op while an instance is already running
# (MultipleInstances=IgnoreNew). The program also runs detached from the task
# (wscript .vbs -> cmd -> leaf), and Stop-ScheduledTask doesn't reliably reap
# that leaf -- so stop the task and kill the whole tree, matched by a token that
# appears in both the wscript host (.vbs name) and the leaf command line
# (e.g. 'docent-launcher', 'docentd').
function Stop-DocentProgram {
    param([string]$TaskName, [string]$Match)
    if ($DryRun) { Write-Host "[dry-run] stop task '$TaskName' + kill processes matching '$Match'"; return }
    if (Get-ScheduledTask -TaskName $TaskName -ErrorAction SilentlyContinue) {
        Stop-ScheduledTask -TaskName $TaskName -ErrorAction SilentlyContinue
    }
    $procs = @(
        Get-CimInstance Win32_Process -Filter "Name='pwsh.exe' OR Name='docentd.exe' OR Name='docent-tunnel.exe' OR Name='wscript.exe' OR Name='cmd.exe'" -ErrorAction SilentlyContinue |
            Where-Object { $_.CommandLine -and ($_.CommandLine -match [regex]::Escape($Match)) -and $_.ProcessId -ne $PID }
    )
    foreach ($p in $procs) {
        try { Stop-Process -Id $p.ProcessId -Force -ErrorAction Stop; Write-Host "  stopped old $($p.Name) (pid $($p.ProcessId))" }
        catch { }
    }
}

function Register-DocentTask {
    param([string]$Name, [string]$Vbs, [string]$Match)
    Stop-DocentProgram -TaskName $Name -Match $Match
    if ($DryRun) { Write-Host "[dry-run] Register-ScheduledTask $Name -> $Vbs (+ Start)"; return }
    $me = "$env:USERDOMAIN\$env:USERNAME"
    $action = New-ScheduledTaskAction -Execute 'wscript.exe' -Argument ('"{0}"' -f $Vbs)
    $tLogon = New-ScheduledTaskTrigger -AtLogOn -User $me
    $tWatch = New-ScheduledTaskTrigger -Once -At (Get-Date) -RepetitionInterval (New-TimeSpan -Minutes 1)
    $principal = New-ScheduledTaskPrincipal -UserId $me -LogonType Interactive -RunLevel Limited
    $settings = New-ScheduledTaskSettingsSet -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries `
        -ExecutionTimeLimit ([TimeSpan]::Zero) -MultipleInstances IgnoreNew -StartWhenAvailable -Hidden
    Register-ScheduledTask -TaskName $Name -Action $action -Trigger @($tLogon, $tWatch) `
        -Principal $principal -Settings $settings -Force | Out-Null
    Start-ScheduledTask -TaskName $Name
    Write-Host "  registered + started task '$Name'"
}

if ($NoTasks) {
    Log "skipping Scheduled Task registration (-NoTasks)"
}
else {
    $extra = ''
    if ($Mode -eq 'local') { $extra += ', docentd' }
    if ($UseTunnel) { $extra += ', docent-tunnel' }
    Log "registering Scheduled Tasks (docent-launcher$extra) -- stopping any running instances first so fresh code loads"
    $tmp = $env:TEMP

    # docent-launcher-windows (always)
    $lnVbs = Join-Path $ConfigDir 'docent-launcher-hidden.vbs'
    $lnScript = Join-Path $Root 'apps\docent-launcher-windows\docent-launcher.ps1'
    $lnArgs = ('-NoLogo -NoProfile -File "{0}" -SessionsUrl "{1}" -WsmUrl "http://127.0.0.1:{2}" -Hotkey "{3}"' -f $lnScript, $Sessions, $WsmPort, $Hotkey)
    if ($Token) { $lnArgs += (' -Token "{0}"' -f $Token) }
    Write-HiddenVbs -Path $lnVbs -Exe $PwshExe -ArgLine $lnArgs `
        -LogFile (Join-Path $tmp 'docent-launcher.log')
    Register-DocentTask -Name 'docent-launcher' -Vbs $lnVbs -Match 'docent-launcher'

    # docent-tunnel (remote + tunnel): a local SSH forward to the dev box's docentd
    if ($UseTunnel) {
        $tunVbs = Join-Path $ConfigDir 'docent-tunnel-hidden.vbs'
        $tunArgs = ('-host "{0}" -local "127.0.0.1:{1}" -remote "127.0.0.1:{1}"' -f $SshHost, $Port)
        if ($SshIdentity) { $tunArgs += (' -identity "{0}"' -f $SshIdentity) }
        Write-HiddenVbs -Path $tunVbs -Exe $DocentTunnelBin -ArgLine $tunArgs `
            -LogFile (Join-Path $tmp 'docent-tunnel.log')
        Register-DocentTask -Name 'docent-tunnel' -Vbs $tunVbs -Match 'docent-tunnel'
    }

    # docentd (local only)
    if ($Mode -eq 'local') {
        $ddVbs = Join-Path $ConfigDir 'docentd-hidden.vbs'
        $ddArgs = ('-config "{0}" -web "{1}" -port {2}' -f $ConfigPath, $WebRoot, $Port)
        Write-HiddenVbs -Path $ddVbs -Exe $DocentdBin -ArgLine $ddArgs `
            -LogFile (Join-Path $tmp 'docentd.log')
        Register-DocentTask -Name 'docentd' -Vbs $ddVbs -Match 'docentd'
    }
}

# --- health checks -----------------------------------------------------------
function Test-Health {
    param([string]$Url)
    try { Invoke-WebRequest -UseBasicParsing -Uri $Url -TimeoutSec 8 | Out-Null; return $true } catch { return $false }
}

if (-not $DryRun -and -not $NoTasks) {
    Log "health checks"
    Start-Sleep -Seconds 2
    if ($Mode -eq 'local') {
        if (Test-Health "http://127.0.0.1:$Port/health") { Write-Host "  docentd       http://127.0.0.1:$Port/  ok" }
        else { Write-Warning "docentd FAIL - see $env:TEMP\docentd.log" }
    }
    elseif ($UseTunnel) {
        if (Test-Health "http://127.0.0.1:$Port/health") { Write-Host "  docentd       http://127.0.0.1:$Port/ (via docent-tunnel -> $SshHost)  ok" }
        else { Write-Warning "docentd not reachable through docent-tunnel yet - see $env:TEMP\docent-tunnel.log" }
    }
    else {
        Write-Host "  docentd       remote  $RemoteUrl"
    }
    if (Test-Health "http://127.0.0.1:$WsmPort/health") {
        Write-Host "  wsm           http://127.0.0.1:$WsmPort/  ok"
    }
    else { Write-Warning "wsm not reachable on :$WsmPort - install it from https://github.com/KurtPreston/wsm" }
}

# --- summary -----------------------------------------------------------------
Write-Host ""
Write-Host "Installed (docentd: $Mode):"
Write-Host "  docent-launcher-windows apps/docent-launcher-windows/docent-launcher.ps1  (hotkey $Hotkey)"
if ($Mode -eq 'local') {
    Write-Host "  docentd                 $DocentdBin   (127.0.0.1:$Port)"
    Write-Host "  dashboard               http://127.0.0.1:$Port/"
}
elseif ($UseTunnel) {
    Write-Host "  docent-tunnel           $DocentTunnelBin  (127.0.0.1:$Port -> $SshHost`:127.0.0.1:$Port)"
    Write-Host "  docentd                 $RemoteUrl  (remote - reached via docent-tunnel)"
    Write-Host "  dashboard               http://127.0.0.1:$Port/"
}
else {
    Write-Host "  docentd                 $RemoteUrl  (remote - not installed here)"
    Write-Host "  dashboard               $RemoteUrl/"
}

if (-not $NoTasks) {
    $extra = ''
    if ($Mode -eq 'local') { $extra += ', docentd' }
    if ($UseTunnel) { $extra += ', docent-tunnel' }
    Write-Host ""
    Write-Host "Scheduled Tasks (hidden, at-logon + 1-min watchdog):"
    Write-Host "  docent-launcher$extra"
    Write-Host "  logs: $env:TEMP\docent-launcher.log$(if ($Mode -eq 'local') { ", $env:TEMP\docentd.log" })$(if ($UseTunnel) { ", $env:TEMP\docent-tunnel.log" })"
    Write-Host ""
    Write-Host "Manage:"
    Write-Host "  Get-ScheduledTask docent-launcher | Get-ScheduledTaskInfo"
    Write-Host "  Stop-ScheduledTask -TaskName docent-launcher          # watchdog relaunches within ~1 min"
    Write-Host "  Disable-ScheduledTask -TaskName docent-launcher       # turn OFF autostart + watchdog"
    Write-Host "  Unregister-ScheduledTask -TaskName docent-launcher -Confirm:`$false"
}

Write-Host ""
Write-Host "Notes:"
Write-Host "  - The window manager is the separate wsm daemon; install it from"
Write-Host "    https://github.com/KurtPreston/wsm (it serves :$WsmPort and needs your"
Write-Host "    interactive desktop to enumerate/focus Cursor windows)."
Write-Host "  - The dashboard focuses windows directly via the local wsm at"
Write-Host "    http://127.0.0.1:$WsmPort (the browser's localhost), so focus works even when docentd is remote."
Write-Host "  - For a REMOTE docentd to also *list* this machine's windows, give docentd a"
Write-Host "    'wsm' directive whose base_url reaches this host's :$WsmPort (e.g. over a reverse SSH"
Write-Host "    tunnel) -- i.e. the local wsm daemon. Local docentd auto-adds that directive."
