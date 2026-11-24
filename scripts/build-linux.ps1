[CmdletBinding()]
param(
    [ValidateSet('amd64', 'arm64')]
    [string]$Arch = 'amd64'
)

$ErrorActionPreference = 'Stop'

$root = Split-Path -Parent $PSScriptRoot
$register = Join-Path $root 'internal/plugin/register.go'
$match = Select-String -LiteralPath $register -Pattern 'const pluginVersion = "([^"]+)"' | Select-Object -First 1
if (-not $match) {
    throw "plugin version not found in $register"
}
$version = $match.Matches[0].Groups[1].Value

$goos = 'linux'
$outDir = Join-Path $root "dist/$goos/$Arch"
$library = Join-Path $outDir "mistral-v$version.so"
$archive = Join-Path $root "dist/mistral-v$version-$goos-$Arch.zip"
$containerOut = "/src/dist/$goos/$Arch/mistral-v$version.so"
$dockerRoot = (Resolve-Path -LiteralPath $root).Path -replace '\\', '/'

New-Item -ItemType Directory -Force -Path $outDir | Out-Null

docker run --rm --platform "linux/$Arch" `
    -v "${dockerRoot}:/src" -w /src `
    -e CGO_ENABLED=1 -e GOOS=$goos -e GOARCH=$Arch `
    golang:1.27-bookworm sh -ec "apt-get update && apt-get install -y --no-install-recommends gcc libc6-dev && go build -trimpath -buildvcs=false -buildmode=c-shared -ldflags='-s -w' -o '$containerOut' ./cmd/mistral && chmod a+r '$containerOut'"
if ($LASTEXITCODE -ne 0) {
    throw "docker build failed with exit code $LASTEXITCODE"
}

if (Test-Path -LiteralPath $archive) {
    Remove-Item -LiteralPath $archive -Force
}
Compress-Archive -LiteralPath $library -DestinationPath $archive

Write-Host "built $library"
Write-Host "packaged $archive"
