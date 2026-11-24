[CmdletBinding()]
param()

$ErrorActionPreference = 'Stop'

$root = Split-Path -Parent $PSScriptRoot
$register = Join-Path $root 'internal/plugin/register.go'
$match = Select-String -LiteralPath $register -Pattern 'const pluginVersion = "([^"]+)"' | Select-Object -First 1
if (-not $match) {
    throw "plugin version not found in $register"
}
$version = $match.Matches[0].Groups[1].Value

$goos = 'windows'
$goarch = 'amd64'
$outDir = Join-Path $root "dist/$goos/$goarch"
$library = Join-Path $outDir "mistral-v$version.dll"
$archive = Join-Path $root "dist/mistral-v$version-$goos-$goarch.zip"

New-Item -ItemType Directory -Force -Path $outDir | Out-Null

$env:CGO_ENABLED = '1'
$env:GOOS = $goos
$env:GOARCH = $goarch

Push-Location $root
try {
    go build -trimpath -buildvcs=false -buildmode=c-shared -ldflags='-s -w' -o $library ./cmd/mistral
    if ($LASTEXITCODE -ne 0) {
        throw "go build failed with exit code $LASTEXITCODE"
    }
} finally {
    Pop-Location
}

if (Test-Path -LiteralPath $archive) {
    Remove-Item -LiteralPath $archive -Force
}
Compress-Archive -LiteralPath $library -DestinationPath $archive

Write-Host "built $library"
Write-Host "packaged $archive"
