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

echo ==========================================
echo  AgentRelay worker
echo ==========================================
echo.
echo  First run: discover a controller and paste its pairing code.
echo  Later runs: connect using saved configuration.
echo  Drag a folder onto this file to pair with that workspace.
echo  Press Ctrl+C to stop.
echo.

if "%~1"=="" (
  "%~dp0AgentRelay.exe" worker serve
) else (
  "%~dp0AgentRelay.exe" worker setup --workspace "%~1"
)
set "CODE=%ERRORLEVEL%"
echo.
echo worker exited with code %CODE%
pause
exit /b %CODE%
