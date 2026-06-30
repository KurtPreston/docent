Set-StrictMode -Version Latest

# Thin wrappers over the MScholtes VirtualDesktop module
# (https://github.com/MScholtes/PSVirtualDesktop). Desktops are addressed by
# NAME, not index, so they survive reshuffles when desktops are added/removed.
# Windows backend only.
#
# Ported from docent-powershell/src/Private/Desktop.ps1. Unlike the legacy
# module, docent-wm treats virtual-desktop support as OPTIONAL: every wrapper
# tolerates a missing module so /windows + /focus still work (foreground only)
# on a box without VirtualDesktop installed. Desktop placement/switching is a
# best-effort enhancement layered on top.

function Test-DocentVirtualDesktopAvailable {
    if (Get-Module -Name VirtualDesktop) { return $true }
    if (-not (Get-Module -ListAvailable -Name VirtualDesktop)) { return $false }
    try {
        Import-Module VirtualDesktop -ErrorAction Stop -DisableNameChecking
        Write-DocentDebug "Imported VirtualDesktop module."
        return $true
    }
    catch {
        Write-DocentWarn "VirtualDesktop module present but failed to import: $($_.Exception.Message)"
        return $false
    }
}

# Returns the desktop object for a given name, or $null if none exists.
function Get-DocentDesktopByName {
    [CmdletBinding()]
    param([Parameter(Mandatory)][string]$Name)
    if (-not (Test-DocentVirtualDesktopAvailable)) { return $null }

    $count = Get-DesktopCount
    for ($i = 0; $i -lt $count; $i++) {
        $d = Get-Desktop -Index $i
        if ((Get-DesktopName -Desktop $d) -eq $Name) { return $d }
    }
    return $null
}

# Find-or-create a desktop with the given name. Returns the desktop object (or
# $null when virtual desktops are unavailable).
function Get-DocentOrNewDesktop {
    [CmdletBinding()]
    param([Parameter(Mandatory)][string]$Name)
    if (-not (Test-DocentVirtualDesktopAvailable)) { return $null }

    $existing = Get-DocentDesktopByName -Name $Name
    if ($existing) {
        Write-DocentInfo "Reusing virtual desktop '$Name'."
        return $existing
    }

    Write-DocentInfo "Creating virtual desktop '$Name'."
    $d = New-Desktop
    Set-DesktopName -Desktop $d -Name $Name | Out-Null
    return $d
}

function Move-DocentWindowToDesktop {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)]$Desktop,
        [Parameter(Mandatory)][IntPtr]$Hwnd
    )
    if (-not $Desktop) { return }
    if (-not (Test-DocentVirtualDesktopAvailable)) { return }
    Move-Window -Desktop $Desktop -Hwnd $Hwnd | Out-Null
    Write-DocentDebug "Moved window $Hwnd to desktop."
}

function Switch-DocentDesktop {
    [CmdletBinding()]
    param([Parameter(Mandatory)]$Desktop)
    if (-not $Desktop) { return }
    if (-not (Test-DocentVirtualDesktopAvailable)) { return }
    Switch-Desktop -Desktop $Desktop | Out-Null
}

# Switch to the virtual desktop that currently hosts $Hwnd. Best-effort: silent
# no-op when the module is missing or the window's desktop can't be resolved.
function Switch-DocentDesktopForWindow {
    [CmdletBinding()]
    param([Parameter(Mandatory)][IntPtr]$Hwnd)
    if (-not (Test-DocentVirtualDesktopAvailable)) { return }
    try {
        $d = Get-DesktopFromWindow -Hwnd $Hwnd
        if ($d) { Switch-Desktop -Desktop $d | Out-Null }
    }
    catch {
        Write-DocentDebug "Switch-DocentDesktopForWindow failed for $Hwnd : $($_.Exception.Message)"
    }
}

function Remove-DocentDesktopByName {
    [CmdletBinding()]
    param([Parameter(Mandatory)][string]$Name)
    if (-not (Test-DocentVirtualDesktopAvailable)) { return }
    $d = Get-DocentDesktopByName -Name $Name
    if ($d) {
        Remove-Desktop -Desktop $d | Out-Null
        Write-DocentInfo "Removed virtual desktop '$Name'."
    }
}
