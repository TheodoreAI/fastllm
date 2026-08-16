@echo off
setlocal

set "APPDIR=%~dp0"
cd /d "%APPDIR%"

rem If something is already listening on 8080, check whether it's our own
rem fastllm.exe and whether the exe file on disk has been rebuilt since
rem that process started (Go embeds web/dist at build time, so a newer
rem exe on disk means the running process is serving an old embed). If
rem so, stop it so the staleness check below rebuilds/relaunches instead
rem of silently reopening a browser tab against stale code. If the port
rem is held by something else entirely, leave it alone.
for /f %%S in ('powershell -NoProfile -Command ^
    "$conn = Get-NetTCPConnection -LocalPort 8080 -State Listen -ErrorAction SilentlyContinue | Select-Object -First 1;" ^
    "if (-not $conn) { Write-Output 'FREE'; exit }" ^
    "$proc = Get-Process -Id $conn.OwningProcess -ErrorAction SilentlyContinue;" ^
    "$ourExe = (Resolve-Path 'fastllm.exe' -ErrorAction SilentlyContinue).Path;" ^
    "if (-not $proc -or -not $ourExe -or $proc.Path -ne $ourExe) { Write-Output 'RUNNING_OTHER'; exit }" ^
    "$exeTime = (Get-Item $ourExe).LastWriteTime;" ^
    "if ($exeTime -gt $proc.StartTime) {" ^
    "  Stop-Process -Id $proc.Id -Force;" ^
    "  Start-Sleep -Milliseconds 300;" ^
    "  Write-Output 'STOPPED_STALE'" ^
    "} else { Write-Output 'RUNNING_CURRENT' }"') do set "PORT_STATUS=%%S"

if "%PORT_STATUS%"=="RUNNING_CURRENT" (
    start "" "http://localhost:8080"
    exit /b 0
)

if "%PORT_STATUS%"=="RUNNING_OTHER" (
    echo Port 8080 is in use by something other than fastllm.exe — opening it as-is.
    start "" "http://localhost:8080"
    exit /b 0
)

rem PORT_STATUS is FREE or STOPPED_STALE here — proceed to the normal
rem staleness check (covers both "never ran" and "just stopped a stale
rem instance") and rebuild only what's actually out of date.

rem Decide whether a rebuild is needed: newest mtime among Go source,
rem the icon, and the frontend source tree vs. fastllm.exe / web\dist.
rem Skips node_modules (irrelevant + huge, would slow this down a lot).
for /f %%S in ('powershell -NoProfile -Command ^
    "$exeTime = if (Test-Path 'fastllm.exe') { (Get-Item 'fastllm.exe').LastWriteTime } else { [datetime]0 };" ^
    "$distTime = if (Test-Path 'web\dist\index.html') { (Get-Item 'web\dist\index.html').LastWriteTime } else { [datetime]0 };" ^
    "$srcPaths = @('cmd','internal','go.mod','go.sum') | Where-Object { Test-Path $_ };" ^
    "$goNewest = (Get-ChildItem -Path $srcPaths -Recurse -File -ErrorAction SilentlyContinue | Measure-Object -Property LastWriteTime -Maximum).Maximum;" ^
    "$webNewest = (Get-ChildItem -Path 'web\src','web\package.json','web\vite.config.js' -Recurse -File -ErrorAction SilentlyContinue | Measure-Object -Property LastWriteTime -Maximum).Maximum;" ^
    "$needBackend = ($null -ne $goNewest) -and ($goNewest -gt $exeTime);" ^
    "$needFrontend = ($null -ne $webNewest) -and ($webNewest -gt $distTime);" ^
    "if ($needBackend -or $needFrontend) { Write-Output ('REBUILD:' + [int]$needFrontend + [int]$needBackend) } else { Write-Output 'UPTODATE' }"') do set "BUILD_STATUS=%%S"

if "%BUILD_STATUS%"=="UPTODATE" goto :launch

echo Source changed since last build, rebuilding fastllm...

if "%BUILD_STATUS:~8,1%"=="1" (
    echo   Building frontend...
    pushd web
    call npm run build
    if errorlevel 1 (
        echo Frontend build failed. See output above.
        popd
        pause
        exit /b 1
    )
    popd
)

echo   Building backend...
go build -o fastllm.exe .\cmd\server
if errorlevel 1 (
    echo Backend build failed. See output above.
    pause
    exit /b 1
)

:launch
if not exist "%APPDIR%fastllm.exe" (
    echo fastllm.exe not found and could not be built.
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
