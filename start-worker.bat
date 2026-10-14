@echo off
setlocal
cd /d "%~dp0"

if not exist "%~dp0DevFreight-worker.exe" (
  echo [ERROR] DevFreight-worker.exe not found next to this launcher.
  echo         Build it first:  go build -o DevFreight-worker.exe ./cmd/DevFreight-worker
  echo.
  pause
  exit /b 1
)

echo ==========================================
echo  DevFreight worker
echo ==========================================
echo.
echo  First run: discover a controller and wait for controller approval.
echo  Later runs: connect using saved configuration.
echo  Drag a folder onto this file to pair with that workspace.
echo  Press Ctrl+C to stop.
echo.

if "%~1"=="" (
  "%~dp0DevFreight-worker.exe" serve
) else (
  "%~dp0DevFreight-worker.exe" setup --workspace "%~1"
)
set "CODE=%ERRORLEVEL%"
echo.
echo worker exited with code %CODE%
pause
exit /b %CODE%
