# Install fslite from a GitHub release on Windows.
#
#   irm https://raw.githubusercontent.com/danmestas/fslite/main/install.ps1 | iex
#
# FSLITE_VERSION      release tag to install, e.g. v0.1.7 (default: the latest)
# FSLITE_INSTALL_DIR  where fslite.exe goes (default: %LOCALAPPDATA%\Programs\fslite)
#
# The archive is checked against the release's SHA256SUMS before anything is
# installed.
$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'

$repo = 'danmestas/fslite'
$installDir = if ($env:FSLITE_INSTALL_DIR) { $env:FSLITE_INSTALL_DIR } else { Join-Path $env:LOCALAPPDATA 'Programs\fslite' }

$arch = switch ($env:PROCESSOR_ARCHITECTURE) {
    'AMD64' { 'amd64' }
    'ARM64' { 'arm64' }
    default { throw "fslite install: unsupported CPU $($env:PROCESSOR_ARCHITECTURE)" }
}

$tag = $env:FSLITE_VERSION
if (-not $tag) {
    $tag = (Invoke-RestMethod -UseBasicParsing "https://api.github.com/repos/$repo/releases/latest").tag_name
}
if ($tag -notmatch '^v[0-9]') { throw "fslite install: no release found (got '$tag')" }

$name = "fslite_$($tag.Substring(1))_windows_$arch"
$base = "https://github.com/$repo/releases/download/$tag"
$tmp = Join-Path ([IO.Path]::GetTempPath()) ([IO.Path]::GetRandomFileName())
New-Item -ItemType Directory -Path $tmp | Out-Null
try {
    Write-Host "Downloading fslite $tag for windows/$arch"
    Invoke-WebRequest -UseBasicParsing "$base/$name.zip" -OutFile (Join-Path $tmp "$name.zip")
    Invoke-WebRequest -UseBasicParsing "$base/SHA256SUMS" -OutFile (Join-Path $tmp 'SHA256SUMS')

    $line = Get-Content (Join-Path $tmp 'SHA256SUMS') | Where-Object { ($_ -split '\s+')[1] -eq "$name.zip" }
    if (-not $line) { throw "fslite install: SHA256SUMS does not list $name.zip" }
    $want = ($line -split '\s+')[0].ToLower()
    $got = (Get-FileHash -Algorithm SHA256 (Join-Path $tmp "$name.zip")).Hash.ToLower()
    if ($got -ne $want) { throw "fslite install: checksum mismatch for $name.zip: got $got, want $want" }

    Expand-Archive -Path (Join-Path $tmp "$name.zip") -DestinationPath $tmp
    $exe = Get-ChildItem -Path $tmp -Recurse -Filter 'fslite.exe' | Select-Object -First 1
    if (-not $exe) { throw "fslite install: $name.zip has no fslite.exe" }
    New-Item -ItemType Directory -Force -Path $installDir | Out-Null
    Copy-Item $exe.FullName (Join-Path $installDir 'fslite.exe') -Force
} finally {
    Remove-Item -Recurse -Force $tmp
}

Write-Host "Installed $(Join-Path $installDir 'fslite.exe') ($tag)"
$userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
if (($userPath -split ';') -notcontains $installDir) {
    Write-Host "Add $installDir to your PATH to run fslite from anywhere."
}
