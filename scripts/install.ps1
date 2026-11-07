# Copyright (c) The Bogform Authors
# SPDX-License-Identifier: MPL-2.0
#
# Installs Bogform on Windows:
#
#   irm https://raw.githubusercontent.com/bogware/bogform/main/scripts/install.ps1 | iex
#
# Settings, as environment variables:
#   BOGFORM_VERSION      A version such as 1.15.0. Defaults to the latest stable release.
#   BOGFORM_INSTALL_DIR  Where to put bogform.exe. Defaults to %LOCALAPPDATA%\Programs\bogform,
#                        which is added to your user PATH.
#
# The download is checked against the release's SHA256SUMS. If cosign is installed, the
# checksums' signature is checked too, proving the release was built by bogware/bogform's
# release workflow.

$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
$repo = 'bogware/bogform'

$arch = switch ($env:PROCESSOR_ARCHITECTURE) {
  'AMD64' { 'amd64' }
  'ARM64' { 'arm64' }
  default { throw "There's no Bogform build for $env:PROCESSOR_ARCHITECTURE. Download one from https://github.com/$repo/releases" }
}

$version = $env:BOGFORM_VERSION
if (-not $version) {
  $latest = Invoke-RestMethod "https://api.github.com/repos/$repo/releases/latest"
  $version = $latest.tag_name
}
$version = $version -replace '^v', ''

$dir = $env:BOGFORM_INSTALL_DIR
if (-not $dir) { $dir = Join-Path $env:LOCALAPPDATA 'Programs\bogform' }

$tmp = Join-Path ([IO.Path]::GetTempPath()) ("bogform-install-" + [guid]::NewGuid())
New-Item -ItemType Directory -Path $tmp | Out-Null
try {
  $archive = "bogform_${version}_windows_${arch}.zip"
  $sums = "bogform_${version}_SHA256SUMS"
  $base = "https://github.com/$repo/releases/download/v$version"

  Write-Host "Downloading Bogform $version for windows_$arch..."
  Invoke-WebRequest "$base/$archive" -OutFile (Join-Path $tmp $archive)
  Invoke-WebRequest "$base/$sums" -OutFile (Join-Path $tmp $sums)

  $line = Get-Content (Join-Path $tmp $sums) | Where-Object { $_ -match " $([regex]::Escape($archive))$" }
  if (-not $line) { throw "$archive isn't listed in the release's checksums." }
  $expected = ($line -split ' ')[0]
  $actual = (Get-FileHash (Join-Path $tmp $archive) -Algorithm SHA256).Hash.ToLower()
  if ($actual -ne $expected) { throw "The download doesn't match its checksum." }
  Write-Host 'Checksum verified.'

  if (Get-Command cosign -ErrorAction SilentlyContinue) {
    Invoke-WebRequest "$base/$sums.sig" -OutFile (Join-Path $tmp "$sums.sig")
    Invoke-WebRequest "$base/$sums.pem" -OutFile (Join-Path $tmp "$sums.pem")
    & cosign verify-blob `
      --certificate (Join-Path $tmp "$sums.pem") `
      --signature (Join-Path $tmp "$sums.sig") `
      --certificate-identity-regexp "^https://github.com/$repo/.github/workflows/release.yml@" `
      --certificate-oidc-issuer https://token.actions.githubusercontent.com `
      (Join-Path $tmp $sums) 2>$null | Out-Null
    if ($LASTEXITCODE -ne 0) { throw "The checksums' signature didn't verify." }
    Write-Host 'Signature verified.'
  } else {
    Write-Host "Install cosign to also verify the release's signature."
  }

  Expand-Archive (Join-Path $tmp $archive) -DestinationPath $tmp -Force
  New-Item -ItemType Directory -Path $dir -Force | Out-Null
  Move-Item (Join-Path $tmp 'bogform.exe') (Join-Path $dir 'bogform.exe') -Force
} finally {
  Remove-Item $tmp -Recurse -Force -ErrorAction SilentlyContinue
}

$userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
if (($userPath -split ';') -notcontains $dir) {
  [Environment]::SetEnvironmentVariable('Path', (($userPath.TrimEnd(';') + ";$dir").TrimStart(';')), 'User')
  Write-Host "Added $dir to your user PATH. Open a new terminal to use it."
}
$installed = & (Join-Path $dir 'bogform.exe') version | Select-Object -First 1
Write-Host "Installed $installed to $dir\bogform.exe."
