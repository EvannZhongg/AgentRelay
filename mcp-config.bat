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

set "BRIDGE=%~1"
if "%BRIDGE%"=="" set "BRIDGE=https://localhost:8764"

echo ==========================================
echo  MCP configuration for your coding agent
echo ==========================================
echo  bridge : %BRIDGE%
echo.
echo  Paste the JSON below into the mcpServers section
echo  of your agent configuration.
echo.

"%~dp0AgentRelay.exe" mcp config --bridge "%BRIDGE%"
set "CODE=%ERRORLEVEL%"
echo.
echo exit code %CODE%
pause
exit /b %CODE%
