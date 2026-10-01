# Builds the Prism MSI on Windows.
#
#   ./installer/windows/build-msi.ps1                     # version from the latest git tag
#   ./installer/windows/build-msi.ps1 -Version 0.9.1
#   ./installer/windows/build-msi.ps1 -Scope perMachine
#
# Requires a .NET SDK (for the WiX tool) - https://dot.net - and a prism.exe
# produced by ./build.ps1 (or by CI, via -ExePath).
#
# The release workflow calls this same script, so "works locally" and "works in
# CI" mean the same thing.
[CmdletBinding()]
param(
    # Numeric product version (major.minor.build). Defaults to the newest git
    # tag with a leading "v" stripped.
    [string]$Version = "",

    # The built application binary to package.
    [string]$ExePath = "",

    # Output MSI path. Relative paths resolve against the repository root.
    [string]$Output = "Prism-Windows-x64.msi",

    # perUser (default) installs under %LOCALAPPDATA% with no elevation;
    # perMachine installs under %ProgramFiles% and needs an elevated msiexec.
    [ValidateSet("perUser", "perMachine")]
    [string]$Scope = "perUser",

    # WiX toolset version to install/use. WiX v6+ is published under the Open
    # Source Maintenance Fee; pin a version below 6.0.0 and author the older
    # <Directory> tree instead if that fee is a problem.
    [string]$WixVersion = "6.0.2"
)

$ErrorActionPreference = "Stop"

$root = (Resolve-Path (Join-Path $PSScriptRoot "..\..")).Path
$wxsPath = Join-Path $PSScriptRoot "Prism.wxs"

function Resolve-RepoPath([string]$path) {
    if ([System.IO.Path]::IsPathRooted($path)) { return $path }
    return (Join-Path $root $path)
}

function Get-WixPath {
    $cmd = Get-Command wix -ErrorAction SilentlyContinue
    if ($cmd) { return $cmd.Source }
    # The .NET global tool lands here; the directory is often missing from PATH
    # in the shell that installed it.
    $candidate = Join-Path $env:USERPROFILE ".dotnet\tools\wix.exe"
    if (Test-Path $candidate) { return $candidate }
    return $null
}

# WiX v6 silently discards anything that is not reachable from <Package>: a
# Feature, a CustomAction, or an extension action authored inside a Fragment
# disappears without a warning and wix still exits 0. That is exactly how a
# 28 KB Prism-Windows-x64.msi with no File rows at all came out of a green
# build. Read the built database instead of trusting the exit code.
function Assert-MsiFilePayload {
    param(
        [Parameter(Mandatory = $true)][string]$MsiPath,
        [Parameter(Mandatory = $true)][string]$FileName,
        [Parameter(Mandatory = $true)][long]$ExpectedSize
    )

    $installer = New-Object -ComObject WindowsInstaller.Installer
    # 0 = msiOpenDatabaseModeReadOnly
    $db = $installer.GetType().InvokeMember('OpenDatabase', 'InvokeMethod', $null, $installer, @($MsiPath, 0))
    $sql = 'SELECT `FileSize` FROM `File` WHERE `FileName` = ''{0}''' -f $FileName
    $view = $db.GetType().InvokeMember('OpenView', 'InvokeMethod', $null, $db, @($sql))
    $view.GetType().InvokeMember('Execute', 'InvokeMethod', $null, $view, $null) | Out-Null
    $rec = $view.GetType().InvokeMember('Fetch', 'InvokeMethod', $null, $view, $null)
    if ($null -eq $rec) {
        $view.GetType().InvokeMember('Close', 'InvokeMethod', $null, $view, $null) | Out-Null
        throw "$FileName is not in the built MSI, so the package carries no payload. WiX v6 drops any Feature, CustomAction, or extension action that is not inside <Package> - move it there from its own <Fragment>."
    }
    $actual = [long]$rec.GetType().InvokeMember('StringData', 'GetProperty', $null, $rec, @(1))
    $view.GetType().InvokeMember('Close', 'InvokeMethod', $null, $view, $null) | Out-Null
    if ($actual -ne $ExpectedSize) {
        throw "The MSI lists $FileName as $actual bytes but the built file is $ExpectedSize bytes."
    }
    return $actual
}

# --- version ----------------------------------------------------------------

if (-not $Version) {
    $tag = $null
    try { $tag = (& git -C $root describe --tags --abbrev=0 2>$null) } catch { $tag = $null }
    if (-not $tag) {
        throw "No git tag to derive a version from. Pass -Version <major.minor.build>."
    }
    $Version = $tag.Trim().TrimStart("v")
}

# Windows Installer only accepts numeric versions: major.minor.build[.revision],
# with a 255.255.65535 field limit.
if ($Version -notmatch '^\d{1,3}\.\d{1,3}\.\d{1,5}$') {
    throw "Invalid -Version '$Version': the MSI needs major.minor.build with numeric fields (e.g. 0.9.1). Pre-release tags like v0.9.1-beta are not supported."
}
$versionParts = $Version.Split('.') | ForEach-Object { [int]$_ }
if ($versionParts[0] -gt 255 -or $versionParts[1] -gt 255 -or $versionParts[2] -gt 65535) {
    throw "Invalid -Version '$Version': Windows Installer caps version fields at 255.255.65535."
}

# --- inputs -----------------------------------------------------------------

if (-not $ExePath) { $ExePath = Join-Path $root "prism.exe" }
$exeFull = Resolve-RepoPath $ExePath
if (-not (Test-Path $exeFull)) {
    throw "prism.exe not found at $exeFull. Run ./build.ps1 first, or pass -ExePath <path>."
}

$iconFull = Join-Path $root "internal\platform\logo_icon.ico"
if (-not (Test-Path $iconFull)) {
    throw "Icon not found at $iconFull."
}

$outputFull = Resolve-RepoPath $Output

# --- toolset ----------------------------------------------------------------

if (-not (Get-Command dotnet -ErrorAction SilentlyContinue)) {
    throw "The .NET SDK is required to run the WiX toolset. Install it from https://dot.net and re-run this script."
}

$wix = Get-WixPath
if (-not $wix) {
    Write-Host "Installing WiX toolset $WixVersion..." -ForegroundColor Cyan
    & dotnet tool install --global wix --version $WixVersion
    if ($LASTEXITCODE -ne 0) {
        # A runtime-only dotnet (common on developer machines) reports "No .NET
        # SDKs were found" here.
        throw "dotnet tool install wix failed (exit $LASTEXITCODE). This needs a .NET SDK, not just the runtime: https://dot.net"
    }
    $wix = Get-WixPath
    if (-not $wix) { throw "wix was installed but could not be located; add %USERPROFILE%\.dotnet\tools to PATH" }
}

# The tool reports its own version, e.g. "6.0.2+abc123". Anything unexpected
# falls back to the pinned version rather than failing: the build below reports
# a genuine toolset/extension mismatch on its own.
$toolVersion = ((& $wix --version 2>$null | Select-Object -First 1) -replace '\+.*$', '').Trim()
if (-not $toolVersion) {
    Write-Warning "Could not read the WiX tool version; using $WixVersion for the Util extension."
    $toolVersion = $WixVersion
} elseif ($toolVersion -ne $WixVersion) {
    Write-Host "Updating WiX toolset $toolVersion -> $WixVersion..." -ForegroundColor Cyan
    & dotnet tool update --global wix --version $WixVersion
    if ($LASTEXITCODE -ne 0) { throw "dotnet tool update wix failed (exit $LASTEXITCODE)" }
    $toolVersion = $WixVersion
}

# The Util extension (CloseApplication) is versioned in lockstep with the
# toolset, so pin it to the tool's own version instead of "latest".
Write-Host "Ensuring WixToolset.Util.wixext $toolVersion..." -ForegroundColor Cyan
$extensionAdded = $false
# PowerShell variables are case-insensitive: this loop variable must not be
# named $scope, which is also the -Scope parameter.
foreach ($extScope in @(@('-g'), @())) {
    & $wix (@('extension', 'add') + $extScope + @("WixToolset.Util.wixext/$toolVersion")) | Out-Null
    if ($LASTEXITCODE -eq 0) { $extensionAdded = $true; break }
}
if (-not $extensionAdded) {
    # Not fatal on its own: wix build below reports a missing extension clearly.
    Write-Warning "Could not add WixToolset.Util.wixext $toolVersion to the extension cache; relying on it already being present."
}

# --- build ------------------------------------------------------------------

Write-Host "Building $outputFull" -ForegroundColor Cyan
Write-Host "  version : $Version"
Write-Host "  scope   : $Scope"
Write-Host "  input   : $exeFull"

$wixArgs = @(
    "build", $wxsPath,
    "-arch", "x64",
    "-ext", "WixToolset.Util.wixext",
    "-o", $outputFull,
    "-d", "PrismVersion=$Version",
    "-d", "PrismExe=$exeFull",
    "-d", "IconFile=$iconFull",
    "-d", "Scope=$Scope"
)

& $wix @wixArgs
if ($LASTEXITCODE -ne 0) { throw "wix build failed (exit $LASTEXITCODE)" }

$msi = Get-Item $outputFull
$sizeMb = [math]::Round($msi.Length / 1MB, 2)

# A zero exit code does not mean the package is complete; see the function.
$exeName = Split-Path $exeFull -Leaf
$packed = Assert-MsiFilePayload -MsiPath $outputFull -FileName $exeName -ExpectedSize (Get-Item $exeFull).Length

Write-Host ""
Write-Host "Built $($msi.Name) ($sizeMb MB) - Prism $Version, $Scope" -ForegroundColor Green
Write-Host "  payload : $exeName ($packed bytes) verified inside the MSI" -ForegroundColor Green

if ($Scope -eq "perMachine") {
    Write-Warning "A per-machine MSI needs an elevated msiexec to install or upgrade. In-app updates do not elevate yet, so this package is for manual installs only."
}

Write-Host ""
Write-Host "Test it:" -ForegroundColor Cyan
Write-Host "  msiexec /i `"$outputFull`" /l*v `$env:TEMP\prism-msi.log"
Write-Host "  msiexec /x `"$outputFull`" /l*v `$env:TEMP\prism-msi-uninstall.log"
