param(
    [string]$Version = "1.0.0"
)

$ErrorActionPreference = "Stop"
$projectRoot = Split-Path -Parent $MyInvocation.MyCommand.Path
Set-Location $projectRoot

Write-Host "Running tests..."
go test ./...

New-Item -ItemType Directory -Force -Path "release" | Out-Null
$env:CGO_ENABLED = "0"
$env:GOOS = "windows"
$env:GOARCH = "amd64"

Write-Host "Building AnalogOutputUtility.exe..."
go build `
    -trimpath `
    -ldflags "-H=windowsgui -s -w -X main.version=$Version" `
    -o "release/AnalogOutputUtility.exe" `
    .

$hash = Get-FileHash -Algorithm SHA256 "release/AnalogOutputUtility.exe"
Write-Host "Built: release/AnalogOutputUtility.exe"
Write-Host "SHA-256: $($hash.Hash.ToLowerInvariant())"
