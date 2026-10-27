# lolssd: drivers from \drivers folders on the volumes (LOLSSD; lolssd drivers on Linux and the
# build wizard put them there). Runs as SYSTEM at every startup (scheduled task \lolssd\drivers):
#   - installers (.exe, .msi) are copied to the Drivers folder on the desktop: the user runs
#     them, their switches are not uniform enough to run them unattended (GPU vendors' own
#     installers come with the image and start by themselves: lolssd-gpu.ps1);
#   - INF packages are installed via pnputil, but only when needed:
#       - first boot on a machine this disk has not seen yet (by SMBIOS UUID), or
#       - some device has no driver (code 28), or the display runs on the Microsoft Basic driver, or
#       - the \drivers folders changed since the last install (a driver added later).
# Windows Update does not bring the drivers that really matter (Wi-Fi, chipset, audio).
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

$roots = @(Get-Volume | Where-Object DriveLetter | ForEach-Object { "$($_.DriveLetter):\drivers" } | Where-Object { Test-Path $_ })

$desk = "$env:PUBLIC\Desktop\Drivers"
foreach ($root in $roots) {
    foreach ($f in Get-ChildItem $root -Recurse -File -Include *.exe, *.msi -ErrorAction SilentlyContinue) {
        $to = Join-Path $desk $f.Name
        if ((Test-Path $to) -and (Get-Item $to).Length -eq $f.Length) { continue }
        New-Item -ItemType Directory -Force $desk | Out-Null
        Copy-Item $f.FullName $to -Force
        Log "installer on the desktop: $($f.Name)"
    }
}

# What the \drivers folders hold now: an added package is a reason to run pnputil again.
$sigFile = "$dir\drivers-signature.txt"
$sig = ($roots | ForEach-Object { Get-ChildItem $_ -Recurse -File -Filter *.inf -ErrorAction SilentlyContinue } |
    ForEach-Object { "$($_.Name):$($_.Length)" } | Sort-Object) -join ';'
$changed = $sig -ne [string](Get-Content $sigFile -Raw -ErrorAction SilentlyContinue)

$machine = (Get-CimInstance Win32_ComputerSystemProduct).UUID
$known = (Test-Path $seen) -and ((Get-Content $seen) -contains $machine)
$missing = @(Get-CimInstance Win32_PnPEntity | Where-Object { $_.ConfigManagerErrorCode -eq 28 })
$basicDisplay = @(Get-CimInstance Win32_VideoController | Where-Object { $_.Name -match 'Basic Display' })

# Devices that stayed without a driver after the last install on this machine (ACPI helpers,
# SM Bus...): we have nothing for them, so they alone are no reason to run pnputil every boot.
$skipFile = "$dir\drivers-none-$machine.txt"
$skip = if (Test-Path $skipFile) { @(Get-Content $skipFile) } else { @() }
$newMissing = @($missing | Where-Object { $skip -notcontains $_.PNPDeviceID })

if (-not $Force -and $known -and -not $newMissing -and -not $basicDisplay -and -not $changed) { exit 0 }
Log "machine $machine ($($cs.Manufacturer) $($cs.Model)): new=$(-not $known) missing=$($missing.Count) basicDisplay=$($basicDisplay.Count) changed=$changed force=$Force"
foreach ($d in $missing) { Log "      no driver: $($d.Name) [$($d.PNPDeviceID)]" }

$failed = 0
foreach ($root in $roots) {
    if (-not (Get-ChildItem $root -Recurse -File -Filter *.inf -ErrorAction SilentlyContinue | Select-Object -First 1)) { continue }
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
if ($failed -eq 0) { Set-Content -Path $sigFile -Value $sig -NoNewline }
@(Get-CimInstance Win32_PnPEntity | Where-Object { $_.ConfigManagerErrorCode -eq 28 } | ForEach-Object { $_.PNPDeviceID }) |
    Set-Content -Path $skipFile
Log "done, $failed failed"
exit $failed
