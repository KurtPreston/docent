# Report the completed Windows installation.

# --- summary -----------------------------------------------------------------
Write-Host ""
Write-Host "Installed (docentd: $Mode):"
Write-Host "  docent                  $DocentBin (maintenance CLI; lifecycle/update unsupported on Windows)"
$lnNote = if ($DashboardHotkey) { "hotkey $Hotkey, dashboard $DashboardHotkey" } else { "hotkey $Hotkey" }
Write-Host "  docent-launcher-windows apps/docent-launcher-windows/docent-launcher.ps1  ($lnNote)"
if (-not $NoCockpit) {
    $deskNote = if ($CockpitDesktop) { "hotkey $CockpitHotkey, desktop '$CockpitDesktop'" } else { "hotkey $CockpitHotkey" }
    Write-Host "  docent-cockpit          apps/docent-launcher-windows/docent-cockpit.ps1  ($deskNote)"
}
if ($Mode -eq 'local') {
    Write-Host "  docentd                 $DocentdBin   (127.0.0.1:$Port)"
    Write-Host "  dashboard               http://127.0.0.1:$Port/"
}
elseif ($UseTunnel) {
    Write-Host "  docent-tunnel           $DocentTunnelBin  (127.0.0.1:$Port -> $SshHost`:127.0.0.1:$Port)"
    Write-Host "  docentd                 127.0.0.1:$Port on $SshHost  (remote - reached via docent-tunnel)"
    Write-Host "  dashboard               http://127.0.0.1:$Port/"
}
else {
    Write-Host "  docentd                 $RemoteUrl  (remote - not installed here)"
    Write-Host "  dashboard               $RemoteUrl/"
}
Write-Host "  IDE extension           $ExtensionStatus"

if (-not $NoTasks) {
    $extra = ''
    if (-not $NoCockpit) { $extra += ', docent-cockpit' }
    if ($Mode -eq 'local') { $extra += ', docentd' }
    if ($UseTunnel) { $extra += ', docent-tunnel' }
    Write-Host ""
    Write-Host "Scheduled Tasks (hidden, at-logon + 1-min watchdog):"
    Write-Host "  docent-launcher$extra"
    Write-Host "  logs: $env:TEMP\docent-launcher.log$(if (-not $NoCockpit) { ", $env:TEMP\docent-cockpit.log" })$(if ($Mode -eq 'local') { ", $env:TEMP\docentd.log" })$(if ($UseTunnel) { ", $env:TEMP\docent-tunnel.log" })"
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
