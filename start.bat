@echo off
setlocal

set "APPDIR=%~dp0"
cd /d "%APPDIR%"

rem If something is already listening on 8080, check whether it's our own
rem fastllm.exe and whether the exe file on disk has been rebuilt since
rem that process started (a newer exe on disk means the running process
rem is serving stale code). If so, stop it so the staleness check below
rem rebuilds/relaunches instead of silently reusing it. If the port
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
    echo fastllm API server listening on http://localhost:8080
echo For the interactive UI, run: fastllm.exe (no arguments)
    exit /b 0
)

if "%PORT_STATUS%"=="RUNNING_OTHER" (
    echo Port 8080 is in use by something other than fastllm.exe — opening it as-is.
    echo fastllm API server listening on http://localhost:8080
echo For the interactive UI, run: fastllm.exe (no arguments)
    exit /b 0
)

rem PORT_STATUS is FREE or STOPPED_STALE here — proceed to the normal
rem staleness check (covers both "never ran" and "just stopped a stale
rem instance") and rebuild only what's actually out of date.

rem Decide whether a rebuild is needed: newest mtime among Go source and
rem the icon vs. fastllm.exe.
for /f %%S in ('powershell -NoProfile -Command ^
    "$exeTime = if (Test-Path 'fastllm.exe') { (Get-Item 'fastllm.exe').LastWriteTime } else { [datetime]0 };" ^
    "$srcPaths = @('cmd','internal','go.mod','go.sum') | Where-Object { Test-Path $_ };" ^
    "$goNewest = (Get-ChildItem -Path $srcPaths -Recurse -File -ErrorAction SilentlyContinue | Measure-Object -Property LastWriteTime -Maximum).Maximum;" ^
    "if (($null -ne $goNewest) -and ($goNewest -gt $exeTime)) { Write-Output 'REBUILD' } else { Write-Output 'UPTODATE' }"') do set "BUILD_STATUS=%%S"

if "%BUILD_STATUS%"=="UPTODATE" goto :launch

echo Source changed since last build, rebuilding fastllm...

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

rem Wait for the server to come up, then report where it is.
powershell -NoProfile -Command ^
    "for ($i=0; $i -lt 30; $i++) {" ^
    "  if (Test-NetConnection -ComputerName localhost -Port 8080 -InformationLevel Quiet -WarningAction SilentlyContinue) { break };" ^
    "  Start-Sleep -Milliseconds 300" ^
    "}"

echo fastllm API server listening on http://localhost:8080
echo For the interactive UI, run: fastllm.exe (no arguments)
