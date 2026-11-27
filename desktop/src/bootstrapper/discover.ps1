$ErrorActionPreference = 'Stop'
$ownSession = (Get-Process -Id $PID).SessionId
if ($ownSession -eq 0) { throw 'INTERACTIVE_SESSION_REQUIRED' }
$owner = [System.Security.Principal.WindowsIdentity]::GetCurrent().Name
$candidates = @(Get-CimInstance Win32_Process -Filter "Name = 'ChatGPT.exe'" | Where-Object {
    if ($_.SessionId -ne $ownSession -or $_.CommandLine -match '--type=') { return $false }
    $user = Invoke-CimMethod -InputObject $_ -MethodName GetOwner
    if ($user.ReturnValue -ne 0) { throw 'PROCESS_OWNER_UNAVAILABLE' }
    return "$($user.Domain)\$($user.User)" -eq $owner
})
if ($candidates.Count -ne 1) { throw 'MAIN_PROCESS_NOT_UNIQUE' }
$target = $candidates[0]
$directory = Split-Path -Parent $target.ExecutablePath
[xml]$manifest = Get-Content -LiteralPath (Join-Path (Split-Path -Parent $directory) 'AppxManifest.xml') -Raw
if ($manifest.Package.Identity.Name -ne 'OpenAI.Codex') { throw 'PACKAGE_IDENTITY_INVALID' }
$asar = Join-Path $directory 'resources\app.asar'
if (-not (Test-Path -LiteralPath $asar -PathType Leaf)) { throw 'ASAR_MISSING' }
$created = [DateTimeOffset]$target.CreationDate.ToUniversalTime()
$result = @{
    pid = [int]$target.ProcessId
    startedAt = $created.ToUnixTimeMilliseconds() / 1000.0
    executablePath = $target.ExecutablePath
    asarPath = $asar
    iniPath = (Join-Path $directory 'resources\owl-app.ini')
}
[Console]::OutputEncoding = [System.Text.UTF8Encoding]::new($false)
$result | ConvertTo-Json -Compress
