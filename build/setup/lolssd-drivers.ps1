# lolssd: install drivers from \drivers folders on the volumes of this disk (LOLSSD, DATA, ...),
# as INF packages via pnputil. Runs as SYSTEM at every startup (scheduled task \lolssd\drivers)
# and does real work only when needed:
#   - first boot on a machine this disk has not seen yet (by SMBIOS UUID), or
#   - some device has no driver (code 28), or the display runs on the Microsoft Basic driver.
# Windows Update is off, so this is how a new computer gets its network and GPU drivers.
# -Force: install regardless. In a VM (the image is installed in QEMU) it does nothing.
# English only: Windows PowerShell 5.1 misreads non-ASCII in BOM-less files.
param([switch]$Force)

$dir = "$env:ProgramData\lolssd"
New-Item -ItemType Directory -Force $dir | Out-Null
$log = "$dir\lolssd-drivers.log"
$seen = "$dir\drivers-machines.txt"
function Log($msg) { "$(Get-Date -Format s) $msg" | Out-File -FilePath $log -Append -Encoding utf8 }

$cs = Get-CimInstance Win32_ComputerSystem
if ($cs.Manufacturer -match 'QEMU|VMware|Xen|innotek' -or $cs.Model -match 'Virtual|KVM') {
    Log "virtual machine ($($cs.Manufacturer) $($cs.Model)), skipping"
    exit 0
}

$machine = (Get-CimInstance Win32_ComputerSystemProduct).UUID
$known = (Test-Path $seen) -and ((Get-Content $seen) -contains $machine)
$missing = @(Get-CimInstance Win32_PnPEntity | Where-Object { $_.ConfigManagerErrorCode -eq 28 })
$basicDisplay = @(Get-CimInstance Win32_VideoController | Where-Object { $_.Name -match 'Basic Display' })

if (-not $Force -and $known -and -not $missing -and -not $basicDisplay) { exit 0 }
Log "machine $machine ($($cs.Manufacturer) $($cs.Model)): new=$(-not $known) missing=$($missing.Count) basicDisplay=$($basicDisplay.Count) force=$Force"
foreach ($d in $missing) { Log "      no driver: $($d.Name) [$($d.PNPDeviceID)]" }

$failed = 0
foreach ($v in Get-Volume | Where-Object DriveLetter) {
    $root = "$($v.DriveLetter):\drivers"
    if (-not (Test-Path $root)) { continue }
    Log "installing from $root"
    # /install puts each package on matching present devices; packages for absent hardware
    # are only staged in the driver store.
    $out = & pnputil.exe /add-driver "$root\*.inf" /subdirs /install 2>&1
    $out | Select-Object -Last 8 | ForEach-Object { Log "      $_" }
    # 0 = done, 3010 = done, reboot required, 259 = nothing new to install
    if ($LASTEXITCODE -notin 0, 3010, 259) { $failed++ ; Log "      pnputil exit $LASTEXITCODE" }
    if ($LASTEXITCODE -eq 3010) { Log "      reboot required" }
}

if (-not $known) { Add-Content -Path $seen -Value $machine }
Log "done, $failed failed"
exit $failed
