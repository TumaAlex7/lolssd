# lolssd: preinstall Riot Client and League of Legends at the first logon, which happens in
# the VM during the install, so the real machine only gets updates. Official installer from
# Riot's CDN with --skip-to-install (installs without questions; the Chocolatey package uses it).
# The installer region only picks the default server; the account decides where you play.
# English only: Windows PowerShell 5.1 misreads non-ASCII in BOM-less files.
param([string]$Region = 'euw')

$dir = "$env:ProgramData\lolssd"
New-Item -ItemType Directory -Force $dir | Out-Null
$log = "$dir\lolssd-riot.log"
function Log($msg) { "$(Get-Date -Format s) $msg" | Out-File -FilePath $log -Append -Encoding utf8; Write-Host $msg }

if (Test-Path "$env:SystemDrive\Riot Games\League of Legends\LeagueClient.exe") {
    Log 'League of Legends is already installed'
    exit 0
}

$exe = "$dir\install-league-of-legends.$Region.exe"
Log "downloading the installer ($Region)"
& curl.exe -fsSL -o $exe "https://lol.secure.dyn.riotcdn.net/channels/public/x/installer/current/live.$Region.exe"
if ($LASTEXITCODE -ne 0) {
    Log "download failed (curl exit $LASTEXITCODE): no network? Run this again later."
    exit 1
}

Log 'installing Riot Client and League of Legends (~25 GB). Riot Client shows the progress;'
Log 'when it offers to play, the game is downloaded: shut Windows down (Vanguard may ask to restart first).'
$p = Start-Process $exe -ArgumentList '--skip-to-install' -PassThru -Wait
Log "installer exited with $($p.ExitCode)"
