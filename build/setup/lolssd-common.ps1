# lolssd: helpers shared by lolssd-specialize.ps1 and lolssd-setup.ps1 (dot-sourced; $log is set
# by the caller). Our files come from the install's config disk and sit in
# C:\Windows\Setup\Scripts\lolssd ($payload).
# English only: Windows PowerShell 5.1 misreads non-ASCII in BOM-less files.

$ErrorActionPreference = 'Stop'
$payload = "$env:SystemRoot\Setup\Scripts\lolssd"
$prog = "$env:ProgramFiles\lolssd"
$failed = 0

function Log($msg) {
    "$(Get-Date -Format s) $msg" | Out-File -FilePath $log -Append -Encoding utf8
}

function Step($name, [scriptblock]$body) {
    try {
        & $body
        Log "ok    $name"
    } catch {
        $script:failed++
        Log "FAIL  ${name}: $_"
    }
}

function Set-Reg($path, $name, $value, $type = 'DWord') {
    if (-not (Test-Path $path)) { New-Item -Path $path -Force | Out-Null }
    New-ItemProperty -Path $path -Name $name -Value $value -PropertyType $type -Force | Out-Null
}

function Run($exe) {
    & $exe @args | Out-Null
    if ($LASTEXITCODE -ne 0) { throw "$exe $args exited with $LASTEXITCODE" }
}

# Build options (build/options.default, written by the build wizard) and install facts
# (install.txt, written by build/install.sh): key=value, 1 = yes.
$options = @{}
foreach ($f in "$payload\options.txt", "$payload\install.txt") {
    foreach ($line in Get-Content $f -ErrorAction SilentlyContinue) {
        if ($line -match '^([a-z_]+)=(.*)$') { $options[$Matches[1]] = $Matches[2].Trim() }
    }
}
function Opt($name) { $options[$name] -eq '1' }

# Files and folders owned by TrustedInstaller (System32, the stock wallpapers): take ownership
# for Administrators first; SYSTEM is one of them.
function Remove-Protected($path) {
    if (-not (Test-Path $path)) { return }
    try { Remove-Item $path -Recurse -Force -ErrorAction Stop; return } catch {}
    # Through cmd: in PowerShell 5.1 a native command's stderr redirected under
    # ErrorActionPreference Stop turns into a terminating error.
    cmd.exe /c "takeown /F `"$path`" /A /R /D Y >nul 2>&1"
    cmd.exe /c "icacls `"$path`" /grant *S-1-5-32-544:F /T /C /Q >nul 2>&1"
    Remove-Item $path -Recurse -Force
}

function Replace-Protected($from, $to) {
    if (Test-Path $to) {
        cmd.exe /c "takeown /F `"$to`" /A >nul 2>&1"
        cmd.exe /c "icacls `"$to`" /grant *S-1-5-32-544:F /Q >nul 2>&1"
    }
    Copy-Item $from $to -Force
}

# Download with a pinned SHA-256 (versions with a stable URL), or, for a URL that always serves
# the latest version, a valid Authenticode signature by the expected publisher. $referer: AMD
# answers without it with a redirect to its site, where the connection just hangs.
function Get-Verified($url, $to, $sha256, $signer, $referer) {
    if (-not (Test-Path $to)) {
        # --ssl-no-revoke: Windows' curl checks certificate revocation through Schannel, which
        # fails this early in setup (curl 35); the file is verified below anyway. A stalled
        # download (under 10 KB/s for a minute) is cut, it must not hold setup.
        $ref = if ($referer) { @('-e', $referer) } else { @() }
        & curl.exe -fsSL --ssl-no-revoke --retry 3 --connect-timeout 30 --speed-limit 10240 --speed-time 60 @ref -o "$to.part" $url
        if ($LASTEXITCODE -ne 0) { throw "download failed (curl $LASTEXITCODE): $url" }
        Move-Item "$to.part" $to -Force
    }
    if ($sha256) {
        $h = (Get-FileHash $to -Algorithm SHA256).Hash
        if ($h -ne $sha256) { Remove-Item $to; throw "checksum mismatch for $url" }
    } else {
        $s = Get-AuthenticodeSignature $to
        if ($s.Status -ne 'Valid' -or $s.SignerCertificate.Subject -notmatch $signer) {
            Remove-Item $to; throw "bad signature on $url ($($s.Status), $($s.SignerCertificate.Subject))"
        }
    }
}
