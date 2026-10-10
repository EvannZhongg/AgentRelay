@echo off
setlocal
cd /d "%~dp0"

if not exist "%~dp0AgentRelay.exe" (
  echo [ERROR] AgentRelay.exe not found next to this launcher.
  echo         Build it first:  go build -o AgentRelay.exe ./cmd/AgentRelay
  echo.
  pause
  exit /b 1
)

set "WORKSPACE=%~1"
if "%WORKSPACE%"=="" set "WORKSPACE=%~dp0workspace"

if not exist "%WORKSPACE%" mkdir "%WORKSPACE%"

echo ==========================================
echo  AgentRelay worker
echo ==========================================
echo  workspace : %WORKSPACE%
echo  listen    : :8765
echo.
echo  Tip: drag a folder onto this file to use that folder as the workspace.
echo  Press Ctrl+C to stop.
echo.

"%~dp0AgentRelay.exe" worker serve --workspace "%WORKSPACE%" --listen :8765
set "CODE=%ERRORLEVEL%"
echo.
echo worker exited with code %CODE%
pause
exit /b %CODE%
