<#
.SYNOPSIS
Runs the required local verification for Codexs changes.

.DESCRIPTION
Builds and tests the CLIProxyAPI server without modifying deployment files.
#>
param()

$ErrorActionPreference = 'Stop'
$temporaryBinaryPath = Join-Path $env:TEMP 'CLIProxyAPI-codexs-verify.exe'

go test ./...
if ($LASTEXITCODE -ne 0) {
  exit $LASTEXITCODE
}

go build -o $temporaryBinaryPath ./cmd/server
if ($LASTEXITCODE -ne 0) {
  exit $LASTEXITCODE
}

Get-Item -LiteralPath $temporaryBinaryPath | Select-Object FullName, Length
