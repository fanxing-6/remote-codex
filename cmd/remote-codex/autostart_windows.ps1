param(
    [Parameter(Mandatory=$true)][ValidateSet('Enable','Disable')][string]$Mode,
    [Parameter(Mandatory=$true)][string]$Executable,
    [Parameter(Mandatory=$true)][string]$ConfigPath,
    [Parameter(Mandatory=$true)][string]$TunnelPath
)
$ErrorActionPreference = 'Stop'
$taskName = 'RemoteCodex'
if ($Mode -eq 'Disable') {
    $tasks = @(Get-ScheduledTask | Where-Object { $_.TaskName -eq $taskName })
    if ($tasks.Count -gt 0) { Unregister-ScheduledTask -TaskName $taskName -Confirm:$false }
    exit 0
}
$identity = [System.Security.Principal.WindowsIdentity]::GetCurrent()
$principal = New-ScheduledTaskPrincipal -UserId $identity.Name -LogonType Interactive -RunLevel Limited
$trigger = New-ScheduledTaskTrigger -AtLogOn -User $identity.Name
$arguments = 'serve --config "' + $ConfigPath + '" --tunnel-config "' + $TunnelPath + '"'
$action = New-ScheduledTaskAction -Execute $Executable -Argument $arguments -WorkingDirectory ([System.IO.Path]::GetDirectoryName($Executable))
$settings = New-ScheduledTaskSettingsSet -MultipleInstances IgnoreNew -ExecutionTimeLimit ([TimeSpan]::Zero) -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries
Register-ScheduledTask -TaskName $taskName -Action $action -Trigger $trigger -Principal $principal -Settings $settings -Force | Out-Null
