@echo off
setlocal

set "APPDIR=%~dp0"
cd /d "%APPDIR%"

rem Skip straight to launch if the server is already running.
powershell -NoProfile -Command "if ((Test-NetConnection -ComputerName localhost -Port 8080 -InformationLevel Quiet -WarningAction SilentlyContinue)) { exit 0 } else { exit 1 }" >nul 2>&1
if %errorlevel%==0 (
    start "" "http://localhost:8080"
    exit /b 0
)

if not exist "%APPDIR%fastllm.exe" (
    echo fastllm.exe not found. Build it first:
    echo   cd web ^&^& npm install ^&^& npm run build ^&^& cd ..
    echo   go build -o fastllm.exe .\cmd\server
    pause
    exit /b 1
)

start "" /B "%APPDIR%fastllm.exe" > "%APPDIR%fastllm.log" 2>&1

rem Wait for the server to come up, then open the browser.
powershell -NoProfile -Command ^
    "for ($i=0; $i -lt 30; $i++) {" ^
    "  if (Test-NetConnection -ComputerName localhost -Port 8080 -InformationLevel Quiet -WarningAction SilentlyContinue) { break };" ^
    "  Start-Sleep -Milliseconds 300" ^
    "}"

start "" "http://localhost:8080"
