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

# Read the Property table into a hashtable. A property authored in a Fragment is
# dropped by WiX v6 without a word, so the built database - not the .wxs - is
# the source of truth for what the package actually declares.
function Get-MsiPropertyTable {
    param([Parameter(Mandatory = $true)][string]$MsiPath)

    $installer = New-Object -ComObject WindowsInstaller.Installer
    $db = $installer.GetType().InvokeMember('OpenDatabase', 'InvokeMethod', $null, $installer, @($MsiPath, 0))
    $view = $db.GetType().InvokeMember('OpenView', 'InvokeMethod', $null, $db, @('SELECT `Property`, `Value` FROM `Property`'))
    $view.GetType().InvokeMember('Execute', 'InvokeMethod', $null, $view, $null) | Out-Null

    $props = @{}
    while ($true) {
        $rec = $view.GetType().InvokeMember('Fetch', 'InvokeMethod', $null, $view, $null)
        if ($null -eq $rec) { break }
        $name = [string]$rec.GetType().InvokeMember('StringData', 'GetProperty', $null, $rec, @(1))
        $props[$name] = [string]$rec.GetType().InvokeMember('StringData', 'GetProperty', $null, $rec, @(2))
    }
    $view.GetType().InvokeMember('Close', 'InvokeMethod', $null, $view, $null) | Out-Null
    return $props
}

# Single-value query against the built database; returns $null when no row
# matches, so a missing table row never throws a COM error mid-check.
function Get-MsiScalar {
    param(
        [Parameter(Mandatory = $true)][string]$MsiPath,
        [Parameter(Mandatory = $true)][string]$Sql,
        [int]$Field = 1
    )

    $installer = New-Object -ComObject WindowsInstaller.Installer
    $db = $installer.GetType().InvokeMember('OpenDatabase', 'InvokeMethod', $null, $installer, @($MsiPath, 0))
    $view = $db.GetType().InvokeMember('OpenView', 'InvokeMethod', $null, $db, @($Sql))
    if ($null -eq $view) { return $null }
    $view.GetType().InvokeMember('Execute', 'InvokeMethod', $null, $view, $null) | Out-Null
    $rec = $view.GetType().InvokeMember('Fetch', 'InvokeMethod', $null, $view, $null)
    if ($null -eq $rec) {
        $view.GetType().InvokeMember('Close', 'InvokeMethod', $null, $view, $null) | Out-Null
        return $null
    }
    $value = [string]$rec.GetType().InvokeMember('StringData', 'GetProperty', $null, $rec, @($Field))
    $view.GetType().InvokeMember('Close', 'InvokeMethod', $null, $view, $null) | Out-Null
    return $value
}

# The install context comes from the package's privilege flag, not from
# ALLUSERS. Scope="perUser" marks the package UAC compliant, and Windows
# Installer then installs per-user without elevation and ignores everything
# else it finds - it deletes an authored ALLUSERS and logs "MSIINSTALLPERUSER
# property is not valid for UAC compliant package. Ignoring". Authoring
# ALLUSERS=2 + MSIINSTALLPERUSER=1 therefore *looks* like it pins the scope
# while doing nothing at all; that was measured, not assumed. What has to hold:
#   perUser    -> the per-user flag is set (Character Count, PID 15, bit 3) and
#                 no ALLUSERS=1 row forces a per-machine install
#   perMachine -> the flag is clear and ALLUSERS=1 is authored (WiX emits it)
function Assert-MsiScope {
    param(
        [Parameter(Mandatory = $true)][string]$MsiPath,
        [Parameter(Mandatory = $true)][string]$Scope
    )

    $installer = New-Object -ComObject WindowsInstaller.Installer
    $db = $installer.GetType().InvokeMember('OpenDatabase', 'InvokeMethod', $null, $installer, @($MsiPath, 0))
    $summary = $db.GetType().InvokeMember('SummaryInformation', 'GetProperty', $null, $db, @(0))
    $charCount = [int]$summary.GetType().InvokeMember('Property', 'GetProperty', $null, $summary, @(15))
    $perUserFlag = ($charCount -band 8) -eq 8

    $props = Get-MsiPropertyTable -MsiPath $MsiPath
    $allUsers = $props['ALLUSERS']

    if ($Scope -eq 'perMachine') {
        if ($perUserFlag) {
            throw "A per-machine package must not carry the per-user flag, but the built MSI has Character Count $charCount."
        }
        if ($allUsers -ne '1') {
            throw "A per-machine package must author ALLUSERS=1, but the built MSI has ALLUSERS='$allUsers'."
        }
        return "per-machine, ALLUSERS=1 (Character Count $charCount)"
    }

    if (-not $perUserFlag) {
        throw "A per-user package must carry the per-user (UAC compliant) flag, but the built MSI has Character Count $charCount - bit 3 is clear, so an administrator would get a per-machine install."
    }
    if ($allUsers -eq '1') {
        throw "ALLUSERS=1 forces a per-machine install; a per-user package must not author it."
    }
    return "per-user without elevation, flag set (Character Count $charCount)"
}

# Pin the FileRef source used by the post-install launch. The PE version check
# below ensures Windows Installer schedules the file, avoiding error 2753.
function Assert-MsiLaunch {
    param([Parameter(Mandatory = $true)][string]$MsiPath)

    $installer = New-Object -ComObject WindowsInstaller.Installer
    $db = $installer.GetType().InvokeMember('OpenDatabase', 'InvokeMethod', $null, $installer, @($MsiPath, 0))

    # A column named like its table cannot be referenced in MSI SQL
    # (SELECT `CustomAction` FROM `CustomAction` fails to open), so read the rows
    # and pick the one we want here.
    $view = $db.GetType().InvokeMember('OpenView', 'InvokeMethod', $null, $db, @('SELECT * FROM `CustomAction`'))
    $view.GetType().InvokeMember('Execute', 'InvokeMethod', $null, $view, $null) | Out-Null
    $caType = $null
    $source = $null
    $target = $null
    while ($true) {
        $rec = $view.GetType().InvokeMember('Fetch', 'InvokeMethod', $null, $view, $null)
        if ($null -eq $rec) { break }
        if ([string]$rec.GetType().InvokeMember('StringData', 'GetProperty', $null, $rec, @(1)) -ne 'LaunchPrism') { continue }
        # Columns: CustomAction, Type, Source, Target
        $caType = [int]$rec.GetType().InvokeMember('StringData', 'GetProperty', $null, $rec, @(2))
        $source = [string]$rec.GetType().InvokeMember('StringData', 'GetProperty', $null, $rec, @(3))
        $target = [string]$rec.GetType().InvokeMember('StringData', 'GetProperty', $null, $rec, @(4))
    }
    $view.GetType().InvokeMember('Close', 'InvokeMethod', $null, $view, $null) | Out-Null

    if ($null -eq $caType) {
        throw 'The LaunchPrism custom action is missing from the built MSI.'
    }

    # Bit 4 (0x10) of the base type means the source is a File table key.
    if (-not (($caType -band 0x3F) -band 0x10) -or $source -ne 'PrismExe' -or $target) {
        throw "LaunchPrism must use FileRef=PrismExe and no arguments (built MSI has type '$caType', source '$source', target '$target')."
    }

    return 'launches PrismExe with asyncNoWait'
}

# Auto-start takeover = a search property plus a component conditioned on it.
# A search property is populated by the AppSearch action at run time and has no
# Property row, so assert the wiring (all of it silently vanishes when authored
# in a Fragment) instead of looking for the property itself.
function Assert-MsiAutostartAdoption {
    param([Parameter(Mandatory = $true)][string]$MsiPath)

    $signature = Get-MsiScalar -MsiPath $MsiPath -Sql 'SELECT `Signature_` FROM `AppSearch` WHERE `Property` = ''PRISM_AUTOSTART_PATH'''
    if (-not $signature) {
        throw "AppSearch has no entry for PRISM_AUTOSTART_PATH, so the auto-start takeover can never fire. WiX v6 drops a search property authored in a Fragment - keep it inside <Package>."
    }

    # Single-quoted so PowerShell leaves the MSI backticks alone.
    $searchSql = 'SELECT `Key` FROM `RegLocator` WHERE `Signature_` = ''{0}''' -f $signature
    $searched = Get-MsiScalar -MsiPath $MsiPath -Sql $searchSql
    if ($searched -ne 'Software\Microsoft\Windows\CurrentVersion\Run') {
        throw "PRISM_AUTOSTART_PATH searches '$searched' instead of the per-user Run key."
    }

    $condition = Get-MsiScalar -MsiPath $MsiPath -Sql 'SELECT `Condition` FROM `Component` WHERE `Component` = ''AutoStartValue'''
    if ($condition -ne 'PRISM_AUTOSTART_PATH') {
        throw "AutoStartValue must be conditioned on PRISM_AUTOSTART_PATH (built MSI has '$condition'), otherwise installing switches auto-start on for users who never enabled it."
    }

    $value = Get-MsiScalar -MsiPath $MsiPath -Sql 'SELECT `Value` FROM `Registry` WHERE `Name` = ''Prism'' AND `Key` = ''Software\Microsoft\Windows\CurrentVersion\Run'''
    if ($value -ne '"[INSTALLFOLDER]prism.exe"') {
        throw "The auto-start row must point at the installed exe, but the built MSI has '$value'."
    }

    return 'repoints HKCU Run\Prism when auto-start was already on'
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

# Legacy PR MSI builds stamped prism.exe as 1.0.0.0. A lower FileVersion makes
# Windows Installer skip the payload before RemoveExistingProducts deletes that
# old copy, leaving the new install registered but broken. The release workflow
# stamps 2.<run high bits>.<run low bits>.0; reject an unstamped payload here.
$peVersion = [System.Diagnostics.FileVersionInfo]::GetVersionInfo($exeFull).FileVersion
if ($peVersion -notmatch '^2\.\d+\.\d+\.0$') {
    throw "prism.exe PE FileVersion '$peVersion' is not a monotonic 2.<run high bits>.<run low bits>.0 version. Run go-winres make before building the MSI."
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
$scopeInfo = Assert-MsiScope -MsiPath $outputFull -Scope $Scope
$autostartInfo = Assert-MsiAutostartAdoption -MsiPath $outputFull
$launchInfo = Assert-MsiLaunch -MsiPath $outputFull

Write-Host ""
Write-Host "Built $($msi.Name) ($sizeMb MB) - Prism $Version, $Scope" -ForegroundColor Green
Write-Host "  payload   : $exeName ($packed bytes) verified inside the MSI" -ForegroundColor Green
Write-Host "  scope     : $scopeInfo" -ForegroundColor Green
Write-Host "  autostart : $autostartInfo" -ForegroundColor Green
Write-Host "  launch    : $launchInfo" -ForegroundColor Green

if ($Scope -eq "perMachine") {
    Write-Warning "A per-machine MSI needs an elevated msiexec to install or upgrade. In-app updates do not elevate yet, so this package is for manual installs only."
}

Write-Host ""
Write-Host "Test it:" -ForegroundColor Cyan
Write-Host "  msiexec /i `"$outputFull`" /l*v `$env:TEMP\prism-msi.log"
Write-Host "  msiexec /x `"$outputFull`" /l*v `$env:TEMP\prism-msi-uninstall.log"
