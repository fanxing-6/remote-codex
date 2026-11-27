param([string]$VenvPath = (Join-Path $PSScriptRoot '../bin/.venv'))
$ErrorActionPreference = 'Stop'
python -m venv $VenvPath
if ($LASTEXITCODE -ne 0) { throw 'PYTHON_VENV_FAILED' }
$python = Join-Path $VenvPath 'Scripts/python.exe'
& $python -m pip install --upgrade pip
if ($LASTEXITCODE -ne 0) { throw 'PYTHON_PIP_FAILED' }
& $python -m pip install -r (Join-Path $PSScriptRoot '../requirements.txt')
if ($LASTEXITCODE -ne 0) { throw 'PYTHON_DEPENDENCIES_FAILED' }
Write-Output "Python environment: $VenvPath"
