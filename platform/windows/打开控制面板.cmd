@echo off
setlocal
cd /d "%~dp0"
if exist "%~dp0remote-codex.exe" (
  "%~dp0remote-codex.exe"
) else if exist "%~dp0remote-codex-windows-amd64.exe" (
  "%~dp0remote-codex-windows-amd64.exe"
) else (
  "%~dp0..\..\bin\remote-codex-windows-amd64.exe"
)
if errorlevel 1 pause
