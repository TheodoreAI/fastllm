@echo off
setlocal

set "APPDIR=%~dp0"
set "DESKTOPDIR=%APPDIR%cmd\desktop"
set "EXE=%DESKTOPDIR%\build\bin\fastllm-desktop.exe"

rem Decide whether a rebuild is needed: newest mtime among Go source, the
rem desktop entrypoint, and the frontend source tree vs. the built exe.
rem Mirrors start.bat's staleness check for cmd/server — skips
rem node_modules (irrelevant + huge, would slow this down a lot).
for /f %%S in ('powershell -NoProfile -Command ^
    "$exeTime = if (Test-Path '%EXE%') { (Get-Item '%EXE%').LastWriteTime } else { [datetime]0 };" ^
    "$srcPaths = @('cmd','internal','go.mod','go.sum') | Where-Object { Test-Path (Join-Path '%APPDIR%' $_) } | ForEach-Object { Join-Path '%APPDIR%' $_ };" ^
    "$goNewest = (Get-ChildItem -Path $srcPaths -Recurse -File -ErrorAction SilentlyContinue | Measure-Object -Property LastWriteTime -Maximum).Maximum;" ^
    "$webNewest = (Get-ChildItem -Path '%APPDIR%web\src','%APPDIR%web\package.json','%APPDIR%web\vite.config.js' -Recurse -File -ErrorAction SilentlyContinue | Measure-Object -Property LastWriteTime -Maximum).Maximum;" ^
    "$needed = (($null -ne $goNewest) -and ($goNewest -gt $exeTime)) -or (($null -ne $webNewest) -and ($webNewest -gt $exeTime));" ^
    "if ($needed) { Write-Output 'REBUILD' } else { Write-Output 'UPTODATE' }"') do set "BUILD_STATUS=%%S"

if "%BUILD_STATUS%"=="UPTODATE" goto :launch

echo Source changed since last build, rebuilding fastllm desktop app...
pushd "%DESKTOPDIR%"
call wails build
if errorlevel 1 (
    echo Desktop build failed. See output above.
    popd
    pause
    exit /b 1
)
popd

:launch
if not exist "%EXE%" (
    echo %EXE% not found and could not be built.
    pause
    exit /b 1
)

rem Wails' SingleInstanceLock (see cmd/desktop/main.go) means launching
rem this while the app is already open just brings the existing window
rem to front instead of starting a second instance, so no port/process
rem check is needed here the way start.bat needs one for cmd/server.
start "" "%EXE%"
