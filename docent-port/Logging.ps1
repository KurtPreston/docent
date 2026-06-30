# Minimal stderr logging shim for the ported Windows window-control helpers.
# Mirrors the docent-powershell logger API (Write-DocentDebug/Info/Warn/Error)
# but is self-contained so docent-wm-windows can dot-source the helpers without
# pulling in the whole legacy module. Level is controlled by DOCENT_LOG_LEVEL
# (debug|info|warn|error); everything goes to stderr so stdout/HTTP bodies stay
# clean.

$script:DocentLogLevels = @{ debug = 0; info = 1; warn = 2; error = 3 }

function Get-DocentLogLevel {
    $lvl = if ($env:DOCENT_LOG_LEVEL) { $env:DOCENT_LOG_LEVEL.ToLowerInvariant() } else { 'info' }
    if ($script:DocentLogLevels.ContainsKey($lvl)) { return $script:DocentLogLevels[$lvl] }
    return 1
}

function Write-DocentLog {
    param([string]$Level, [string]$Message)
    if ($script:DocentLogLevels[$Level] -lt (Get-DocentLogLevel)) { return }
    $ts = (Get-Date).ToString('o')
    [Console]::Error.WriteLine("[$ts] $($Level.ToUpperInvariant()) $Message")
}

function Write-DocentDebug { param([string]$Message) Write-DocentLog -Level 'debug' -Message $Message }
function Write-DocentInfo  { param([string]$Message) Write-DocentLog -Level 'info'  -Message $Message }
function Write-DocentWarn  { param([string]$Message) Write-DocentLog -Level 'warn'  -Message $Message }
function Write-DocentError { param([string]$Message) Write-DocentLog -Level 'error' -Message $Message }
