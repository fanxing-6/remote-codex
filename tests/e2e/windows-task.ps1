param(
    [Parameter(Mandatory=$true)][ValidateSet('Start','Status','Remove')][string]$Mode,
    [Parameter(Mandatory=$true)][string]$ProjectPath,
    [string]$NodePath,
    [string]$ToolsPath,
    [ValidateSet('local','secure-local','windows-lifecycle')][string]$Suite = 'local'
)
$ErrorActionPreference = 'Stop'
$taskName = 'CWBValidation-' + $Suite
$tasks = @(Get-ScheduledTask | Where-Object { $_.TaskName -eq $taskName })
if ($Mode -eq 'Remove') {
    if ($tasks.Count -eq 1) {
        if ($tasks[0].State -eq 'Running') { throw 'VALIDATION_STILL_RUNNING' }
        Unregister-ScheduledTask -TaskName $taskName -Confirm:$false
    }
    exit 0
}
if ($Mode -eq 'Status') {
    $tasks | Select-Object TaskName,State | ConvertTo-Json -Compress
    Get-ScheduledTaskInfo -TaskName $taskName | Select-Object LastTaskResult | ConvertTo-Json -Compress
    exit 0
}
if ($tasks.Count -ne 0) { throw 'VALIDATION_TASK_EXISTS' }
$identity = [System.Security.Principal.WindowsIdentity]::GetCurrent()
$principal = New-ScheduledTaskPrincipal -UserId $identity.Name -LogonType Interactive -RunLevel Limited
$script = Join-Path $ProjectPath 'tests/e2e/windows-session.ps1'
$arguments = '-NoProfile -NonInteractive -ExecutionPolicy Bypass -WindowStyle Hidden -File "' + $script + '" -NodePath "' + $NodePath + '" -ProjectPath "' + $ProjectPath + '" -ToolsPath "' + $ToolsPath + '" -Suite ' + $Suite
$action = New-ScheduledTaskAction -Execute (Join-Path $PSHOME 'powershell.exe') -Argument $arguments -WorkingDirectory $ProjectPath
$settings = New-ScheduledTaskSettingsSet -ExecutionTimeLimit (New-TimeSpan -Minutes 15) -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries
Register-ScheduledTask -TaskName $taskName -Action $action -Principal $principal -Settings $settings | Out-Null
Start-ScheduledTask -TaskName $taskName
