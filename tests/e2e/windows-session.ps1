param(
    [Parameter(Mandatory=$true)][string]$NodePath,
    [Parameter(Mandatory=$true)][string]$ProjectPath,
    [Parameter(Mandatory=$true)][string]$ToolsPath,
    [ValidateSet('local','secure-local','windows-lifecycle')][string]$Suite = 'local'
)
$ErrorActionPreference = 'Stop'
$env:CWB_PLAYWRIGHT_PATH = ([Uri]([System.IO.Path]::Combine($ToolsPath,'node_modules/playwright/index.mjs'))).AbsoluteUri
$env:CWB_HTTP_PROXY_PATH = ([Uri]([System.IO.Path]::Combine($ToolsPath,'node_modules/http-proxy/index.js'))).AbsoluteUri
Set-Location $ProjectPath
$directory = Join-Path $ProjectPath '.work/session'
New-Item -ItemType Directory -Force -Path $directory | Out-Null
& $NodePath (Join-Path $ProjectPath ('tests/e2e/' + $Suite + '.mjs')) | Out-File -Encoding utf8 (Join-Path $directory ($Suite + '.log'))
$code = $LASTEXITCODE
[System.IO.File]::WriteAllText((Join-Path $directory ($Suite + '.exit')), [string]$code)
exit $code
