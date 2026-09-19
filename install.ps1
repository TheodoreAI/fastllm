# fastllm Windows Installer
# Run with:
#   irm https://raw.githubusercontent.com/theodoreai/fastllm/main/install.ps1 | iex

$ErrorActionPreference = "Stop"

$installDir = Join-Path $env:USERPROFILE ".local\bin"
if (-not (Test-Path $installDir)) {
    New-Item -ItemType Directory -Path $installDir -Force | Out-Null
}

$targetExe = Join-Path $installDir "fastllm.exe"

Write-Host "Installing fastllm to $installDir..." -ForegroundColor Cyan

# If run inside the repo or Go is available locally
$scriptDir = Split-Path -Parent $MyInvocation.MyCommand.Definition
if (Test-Path (Join-Path $scriptDir "go.mod")) {
    Write-Host "Building from source..." -ForegroundColor Yellow
    Push-Location $scriptDir
    go build -o $targetExe ./cmd/server
    Pop-Location
} elseif (Get-Command go -ErrorAction SilentlyContinue) {
    Write-Host "Compiling via Go..." -ForegroundColor Yellow
    $tempDir = New-TemporaryFile | ForEach-Object { Remove-Item $_; New-Item -ItemType Directory -Path $_ }
    git clone --depth 1 https://github.com/theodoreai/fastllm.git $tempDir | Out-Null
    Push-Location $tempDir
    go build -o $targetExe ./cmd/server
    Pop-Location
    Remove-Item -Recurse -Force $tempDir
} else {
    Write-Host "Downloading prebuilt fastllm.exe..." -ForegroundColor Yellow
    $url = "https://github.com/theodoreai/fastllm/releases/latest/download/fastllm-windows-amd64.exe"
    Invoke-WebRequest -Uri $url -OutFile $targetExe
}

# Ensure install directory is in User PATH
$userPath = [Environment]::GetEnvironmentVariable("Path", "User")
$paths = $userPath -split ";"
if ($paths -notcontains $installDir) {
    Write-Host "Adding $installDir to User PATH..." -ForegroundColor Cyan
    $newPath = if ($userPath) { "$userPath;$installDir" } else { $installDir }
    [Environment]::SetEnvironmentVariable("Path", $newPath, "User")
    $env:Path = "$env:Path;$installDir"
}

Write-Host ""
Write-Host "fastllm installed successfully!" -ForegroundColor Green
Write-Host "Run 'fastllm' in any terminal to start the interactive REPL." -ForegroundColor Green
