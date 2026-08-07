# Informational: try to map the daemon as a drive letter through Windows'
# WebDAV Redirector, the thing behind Explorer's "Map network drive".
#
# The redirector is an optional feature that is absent from Windows Server
# images, so this is expected to report "not available" on hosted CI runners.
# It exists so that the day it does run somewhere, the result is recorded
# rather than assumed. A real desktop Windows box is the only honest place to
# verify the Explorer path.

$ErrorActionPreference = 'Continue'

$work = Join-Path $env:RUNNER_TEMP 'fslite-mapdrive'
New-Item -ItemType Directory -Force -Path $work | Out-Null
$repo = Join-Path $work 'drive.fossil'
$base = 'http://127.0.0.1:8081'

$svc = Get-Service -Name WebClient -ErrorAction SilentlyContinue
if ($null -eq $svc) {
    Write-Host "WebClient service is not present on this image."
    Write-Host "=> The WebDAV Redirector (Explorer drive mapping) cannot be tested here."
    Write-Host "   Install-WindowsFeature WebDAV-Redirector would be needed, plus a reboot."
    exit 0
}

Write-Host "WebClient service found (status: $($svc.Status)). Attempting to start it."
try { Start-Service WebClient -ErrorAction Stop } catch { Write-Host "Start-Service failed: $_"; exit 0 }

$proc = Start-Process -FilePath .\fslite.exe `
    -ArgumentList @('serve', '--repo', $repo, '--http', '127.0.0.1:8081', '--no-nats', '--agent', 'windrive') `
    -PassThru -RedirectStandardOutput (Join-Path $work 'daemon.log') `
    -RedirectStandardError (Join-Path $work 'daemon.err')

foreach ($i in 1..100) {
    $code = (curl.exe -s -o NUL -w '%{http_code}' "$base/" 2>$null)
    if ($code -and $code -ne '000') { break }
    Start-Sleep -Milliseconds 200
}

Write-Host "net use Z: $base"
net use Z: $base 2>&1 | Write-Host

if (Test-Path Z:\) {
    Write-Host "Drive mapped. Exercising it like a normal folder."
    Set-Content -Path 'Z:\from-explorer.txt' -Value 'written through a mapped drive'
    Get-ChildItem Z:\ | Format-Table -AutoSize | Out-String | Write-Host

    # Atomic save, the way a Windows editor does it.
    Set-Content -Path 'Z:\doc.tmp' -Value 'edited'
    Move-Item -Path 'Z:\doc.tmp' -Destination 'Z:\doc.txt' -Force
    Write-Host "doc.txt contents: $(Get-Content 'Z:\doc.txt' -ErrorAction SilentlyContinue)"

    net use Z: /delete /y 2>&1 | Write-Host
} else {
    Write-Host "=> Drive did not map; see the net use output above."
}

.\fslite.exe stop --all 2>$null | Out-Null
if ($proc -and -not $proc.HasExited) { Stop-Process -Id $proc.Id -Force -ErrorAction SilentlyContinue }
