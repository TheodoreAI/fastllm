@echo off
setlocal

set "APPDIR=%~dp0"
cd /d "%APPDIR%"

echo Building frontend...
pushd web
call npm run build
if errorlevel 1 (
    echo Frontend build failed. See output above.
    popd
    exit /b 1
)
popd

echo Building fastllm.exe (browser/server mode)...
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

echo.
echo Done:
echo   %APPDIR%fastllm.exe
