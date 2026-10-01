# Build script for Windows
# Builds the React frontend, then the Go binary. Vite/esbuild uses temp files
# for large bundles; use a project-local scratch dir rather than the Windows
# Temp directory, where file scanning can race esbuild's cleanup.
$ErrorActionPreference = "Stop"
$root = $PSScriptRoot
$buildTemp = Join-Path $root ".build-tmp"
$oldTemp = $env:TEMP
$oldTmp = $env:TMP
$oldTmpDir = $env:TMPDIR

try {
    New-Item -ItemType Directory -Force -Path $buildTemp | Out-Null
    $env:TEMP = $buildTemp
    $env:TMP = $buildTemp
    $env:TMPDIR = $buildTemp

    Push-Location (Join-Path $root "web")
    try {
        npm run build
        if ($LASTEXITCODE -ne 0) { throw "Frontend build failed (exit $LASTEXITCODE)" }
    } finally {
        Pop-Location
    }

    Push-Location $root
    try {
        go-winres make
        if ($LASTEXITCODE -ne 0) { throw "go-winres failed (exit $LASTEXITCODE)" }
        go build -ldflags="-H windowsgui -X main.version=dev" -o prism.exe .
        if ($LASTEXITCODE -ne 0) { throw "Go build failed (exit $LASTEXITCODE)" }
    } finally {
        Pop-Location
    }

    Write-Host "Build complete: prism.exe" -ForegroundColor Green
} finally {
    $env:TEMP = $oldTemp
    $env:TMP = $oldTmp
    $env:TMPDIR = $oldTmpDir
    Remove-Item -Recurse -Force $buildTemp -ErrorAction SilentlyContinue
}
