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

set "LISTEN=%~1"
if "%LISTEN%"=="" set "LISTEN=:8764"

echo ==========================================
echo  AgentRelay controller
echo ==========================================
echo  listen : %LISTEN%
echo.
echo  Workers discover this controller and connect outbound.
echo  Approve or reject connection requests shown below.
echo.
echo  Press Ctrl+C to stop.
echo.

"%~dp0AgentRelay.exe" controller serve --listen %LISTEN%
set "CODE=%ERRORLEVEL%"
echo.
echo controller exited with code %CODE%
pause
exit /b %CODE%
