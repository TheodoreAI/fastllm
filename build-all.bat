@echo off
setlocal

set "APPDIR=%~dp0"
cd /d "%APPDIR%"

echo Building fastllm-cli.exe (terminal UI)...
go build -o fastllm-cli.exe .\cmd\cli
if errorlevel 1 (
    echo cmd/cli build failed. See output above.
    exit /b 1
)

echo Building fastllm.exe (headless API server)...
go build -o fastllm.exe .\cmd\server
if errorlevel 1 (
    echo cmd/server build failed. See output above.
    exit /b 1
)

rem Keep the copy on PATH in step with the repo build. Without this the two
rem drift: start.bat runs the repo exe while typing `fastllm` runs the installed
rem one, and a stale install looks exactly like a missing feature.
set "INSTALLED=%USERPROFILE%\.local\bin\fastllm.exe"
if exist "%INSTALLED%" (
    echo Refreshing installed copy at "%INSTALLED%" ...
    go build -o "%INSTALLED%" .\cmd\server
    if errorlevel 1 echo   (skipped: it is probably locked by a running fastllm session)
)
set "INSTALLEDCLI=%USERPROFILE%\.local\bin\fastllm-cli.exe"
if exist "%INSTALLEDCLI%" (
    go build -o "%INSTALLEDCLI%" .\cmd\cli
    if errorlevel 1 echo   (skipped: it is probably locked by a running fastllm session)
)

echo.
echo Done:
echo   %APPDIR%fastllm-cli.exe   (terminal UI)
echo   %APPDIR%fastllm.exe       (headless API server)
