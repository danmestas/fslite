# WebDAV protocol verification on Windows.
#
# Mirrors the Linux container suite: serve a repo, exercise the WebDAV verbs a
# real client uses during an ordinary save — including the LOCK + MOVE-with-If
# exchange that both davfs2 and the Windows redirector produce — then commit
# and check the result came back out intact.
#
# curl.exe ships with Windows Server 2022 and desktop Windows 10+.

$ErrorActionPreference = 'Continue'
$script:pass = 0
$script:fail = 0
function Ok([string]$m)  { Write-Host "  PASS  $m"; $script:pass++ }
function Bad([string]$m) { Write-Host "  FAIL  $m"; $script:fail++ }
function Step([string]$m) { Write-Host ""; Write-Host "== $m ==" }

$work = Join-Path $env:RUNNER_TEMP 'fslite-webdav'
New-Item -ItemType Directory -Force -Path $work | Out-Null
$repo = Join-Path $work 'biz.fossil'
$base = 'http://127.0.0.1:8080'

Step "environment"
cmd /c ver
(curl.exe --version | Select-Object -First 1)

Step "start the daemon"
# fslite creates the repo when it does not exist, so no fossil CLI is needed.
$proc = Start-Process -FilePath .\fslite.exe `
    -ArgumentList @('serve', '--repo', $repo, '--http', '127.0.0.1:8080', '--no-nats', '--agent', 'winbiz') `
    -PassThru -RedirectStandardOutput (Join-Path $work 'daemon.log') `
    -RedirectStandardError (Join-Path $work 'daemon.err')

$up = $false
foreach ($i in 1..100) {
    $code = (curl.exe -s -o NUL -w '%{http_code}' "$base/" 2>$null)
    if ($code -and $code -ne '000') { $up = $true; break }
    Start-Sleep -Milliseconds 200
}
if ($up) { Ok "daemon answering on :8080" } else { Bad "daemon did not come up" }

Step "PROPFIND"
$propfind = curl.exe -s -X PROPFIND -H 'Depth: 1' "$base/" 2>$null
if ($propfind -match 'multistatus') { Ok "PROPFIND returns a multistatus" } else { Bad "PROPFIND response unexpected" }

Step "PUT / GET"
Set-Content -Path (Join-Path $work 'msa.txt') -Value 'Term: 12 months.' -NoNewline
$code = curl.exe -s -o NUL -w '%{http_code}' -T (Join-Path $work 'msa.txt') "$base/contracts/msa.txt" 2>$null
if ($code -eq '201' -or $code -eq '204') { Ok "PUT created a file (status $code)" } else { Bad "PUT status $code" }

$got = curl.exe -s "$base/contracts/msa.txt" 2>$null
if ($got -match 'Term: 12 months\.') { Ok "GET returns what was written" } else { Bad "GET returned '$got'" }

Step "atomic save: LOCK the temp file, then MOVE it onto the target"
# This is the exchange that fails when the server demands a lock token for the
# (unlocked) destination — see the davfs2 findings on Linux.
Set-Content -Path (Join-Path $work 'tmp.txt') -Value 'Term: 24 months.' -NoNewline
curl.exe -s -o NUL -T (Join-Path $work 'tmp.txt') "$base/contracts/.msa.tmp" 2>$null

$lockBody = '<?xml version="1.0" encoding="utf-8"?><D:lockinfo xmlns:D="DAV:"><D:lockscope><D:exclusive/></D:lockscope><D:locktype><D:write/></D:locktype><D:owner><D:href>ci</D:href></D:owner></D:lockinfo>'
$lockResp = curl.exe -s -i -X LOCK "$base/contracts/.msa.tmp" `
    -H 'Timeout: Second-3600' -H 'Depth: 0' -H 'Content-Type: application/xml' `
    --data $lockBody 2>$null | Out-String

if ($lockResp -match 'Lock-Token:\s*<([^>]+)>') {
    $token = $Matches[1]
    Ok "LOCK granted a token"

    $code = curl.exe -s -o NUL -w '%{http_code}' -X MOVE "$base/contracts/.msa.tmp" `
        -H "Destination: $base/contracts/msa.txt" `
        -H "If: <$base/contracts/.msa.tmp> (<$token>)" 2>$null
    if ($code -like '2*') { Ok "MOVE with a source-only lock token (status $code)" }
    else { Bad "MOVE with a source-only lock token: status $code" }

    $got = curl.exe -s "$base/contracts/msa.txt" 2>$null
    if ($got -match 'Term: 24 months\.') { Ok "atomic save landed on the target" }
    else { Bad "target content after atomic save: '$got'" }
} else {
    Bad "LOCK did not return a token"
}

Step "LOCK on an unmapped URL creates an empty resource (RFC 4918 9.10.4)"
curl.exe -s -o NUL -X LOCK "$base/contracts/brand-new.txt" `
    -H 'Timeout: Second-3600' -H 'Depth: 0' -H 'Content-Type: application/xml' `
    --data $lockBody 2>$null
$code = curl.exe -s -o NUL -w '%{http_code}' -I "$base/contracts/brand-new.txt" 2>$null
if ($code -like '2*') { Ok "the locked resource exists afterwards (HEAD $code)" }
else { Bad "HEAD after LOCK on an unmapped URL: status $code" }

Step "commit"
$commit = curl.exe -s -X POST --data 'edits from windows' "$base/_admin/commit" 2>$null
if ($commit -match 'uuid=([0-9a-f]{40,64})') {
    Ok "commit produced check-in $($Matches[1].Substring(0,10))"
    if ($Matches[1].Length -eq 64) { Ok "SHA3-256 artifact id (repo hash-policy respected)" }
    else { Bad "expected a 64-hex SHA3 check-in id, got length $($Matches[1].Length)" }
} else {
    Bad "commit response: '$commit'"
}

Step "content survives the commit"
$got = curl.exe -s "$base/contracts/msa.txt" 2>$null
if ($got -match 'Term: 24 months\.') { Ok "file reads back correctly after commit" }
else { Bad "post-commit content: '$got'" }

Step "daemon log"
Get-Content (Join-Path $work 'daemon.log') -ErrorAction SilentlyContinue | Select-Object -Last 5
Get-Content (Join-Path $work 'daemon.err') -ErrorAction SilentlyContinue | Select-Object -Last 5

.\fslite.exe stop --all 2>$null | Out-Null
if ($proc -and -not $proc.HasExited) { Stop-Process -Id $proc.Id -Force -ErrorAction SilentlyContinue }

Write-Host ""
Write-Host "==================== RESULT ===================="
Write-Host "  passed: $script:pass    failed: $script:fail"
Write-Host "==============================================="
if ($script:fail -gt 0) { exit 1 }
