@echo off
setlocal
cd /d "%~dp0"

if not exist "%~dp0DevFreight.exe" (
  echo [ERROR] DevFreight.exe not found next to this launcher.
  echo         Build it first:  go build -o DevFreight.exe ./cmd/DevFreight
  echo.
  pause
  exit /b 1
)

set "CONTROLLER=%~1"
if "%CONTROLLER%"=="" set "CONTROLLER=https://localhost:8764"

echo ==========================================
echo  MCP configuration for your coding agent
echo ==========================================
echo  controller : %CONTROLLER%
echo.
echo  Paste the JSON below into the mcpServers section
echo  of your agent configuration.
echo.

"%~dp0DevFreight.exe" mcp config --controller "%CONTROLLER%"
set "CODE=%ERRORLEVEL%"
echo.
echo exit code %CODE%
pause
exit /b %CODE%
