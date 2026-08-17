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

echo Building fastllm-desktop.exe (Wails desktop app)...
pushd cmd\desktop
call wails build -s
if errorlevel 1 (
    echo cmd/desktop build failed. See output above.
    popd
    exit /b 1
)
popd

echo.
echo Done:
echo   %APPDIR%fastllm.exe
echo   %APPDIR%cmd\desktop\build\bin\fastllm-desktop.exe
