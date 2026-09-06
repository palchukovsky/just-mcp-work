# Copyright (c) Eugene V. Palchukovsky
# SPDX-License-Identifier: MIT
# Please see https://github.com/palchukovsky/just-mcp-work for details.

[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [string]$Version,

    [Parameter(Mandatory = $true)]
    [ValidatePattern('^[0-9a-fA-F]{64}$')]
    [string]$Sha256,

    [Parameter(Mandatory = $true)]
    [string]$Destination
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

$destinationPath = [System.IO.Path]::GetFullPath($Destination)
$toolchainBin = Join-Path $destinationPath 'w64devkit\bin'
$gcc = Join-Path $toolchainBin 'gcc.exe'

function Assert-CompatibleCompiler {
    if (-not (Test-Path -LiteralPath $gcc -PathType Leaf)) {
        throw "w64devkit did not provide $gcc"
    }
    $synchronizationLibrary = & $gcc --print-file-name libsynchronization.a
    if ($LASTEXITCODE -ne 0) {
        throw "w64devkit compiler probe failed with exit code $LASTEXITCODE"
    }
    if ([string]::IsNullOrWhiteSpace($synchronizationLibrary) -or
        $synchronizationLibrary.Trim() -eq 'libsynchronization.a') {
        throw 'w64devkit does not provide the MinGW synchronization library required by Go race builds'
    }
}

if (Test-Path -LiteralPath $gcc -PathType Leaf) {
    Assert-CompatibleCompiler
    Write-Output "Windows race toolchain is ready: $gcc"
    exit 0
}

$tempRoot = Split-Path -Parent $destinationPath
$downloadDir = Join-Path $tempRoot 'downloads'
$archiveName = "w64devkit-x64-$Version.7z.exe"
$archive = Join-Path $downloadDir $archiveName
$downloadUri = "https://github.com/skeeto/w64devkit/releases/download/v$Version/$archiveName"

New-Item -ItemType Directory -Force -Path $downloadDir | Out-Null
if (-not (Test-Path -LiteralPath $archive -PathType Leaf)) {
    Invoke-WebRequest -UseBasicParsing -Uri $downloadUri -OutFile $archive
}

$hashAlgorithm = [System.Security.Cryptography.SHA256]::Create()
try {
    $archiveStream = [System.IO.File]::OpenRead($archive)
    try {
        $hashBytes = $hashAlgorithm.ComputeHash($archiveStream)
    }
    finally {
        $archiveStream.Dispose()
    }
}
finally {
    $hashAlgorithm.Dispose()
}
$actualHash = [System.BitConverter]::ToString($hashBytes).Replace('-', '').ToLowerInvariant()
if ($actualHash -ne $Sha256.ToLowerInvariant()) {
    throw "w64devkit SHA256 mismatch: got $actualHash"
}

New-Item -ItemType Directory -Force -Path $destinationPath | Out-Null
$extract = Start-Process -FilePath $archive `
    -ArgumentList @('-y', "-o`"$destinationPath`"") `
    -Wait `
    -PassThru `
    -WindowStyle Hidden
if ($extract.ExitCode -ne 0) {
    throw "w64devkit extraction failed with exit code $($extract.ExitCode)"
}

Assert-CompatibleCompiler
Write-Output "Installed Windows race toolchain: $gcc"
