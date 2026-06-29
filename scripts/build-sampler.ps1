# Rebuilds the embedded Linux sampler. Run before `wails build` whenever
# cmd/sampler changes.
$ErrorActionPreference = "Stop"
Set-Location (Join-Path $PSScriptRoot "..")
$env:GOOS = "linux"; $env:GOARCH = "amd64"; $env:CGO_ENABLED = "0"
go build -ldflags="-s -w" -o internal/agent/sampler-linux-amd64 ./cmd/sampler
Remove-Item Env:GOOS, Env:GOARCH, Env:CGO_ENABLED
Write-Host "built internal/agent/sampler-linux-amd64"
