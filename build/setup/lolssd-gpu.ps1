# lolssd: the GPU vendor's own installer for a graphics card running without its driver. Runs at
# every logon of an administrator (scheduled task \lolssd\gpu), in the user's session, so the
# installer window is visible. For each AMD, NVIDIA or Intel adapter still on the Microsoft Basic
# Display driver (or without any driver) it starts that vendor's installer from
# C:\Program Files\lolssd\gpu -- AMD Software (auto-detect, also brings the Ryzen chipset driver),
# NVIDIA App, Intel Driver & Support Assistant -- once per machine and vendor: closed means no.
# The installers are whatever version the image was built with; they update themselves.
# A copy goes to the Drivers folder on the desktop for a later manual run.
# English only: Windows PowerShell 5.1 misreads non-ASCII in BOM-less files.

$dir = "$env:ProgramData\lolssd"
New-Item -ItemType Directory -Force $dir | Out-Null
$log = "$dir\lolssd-gpu.log"
$seen = "$dir\gpu-offered.txt"
function Log($msg) { "$(Get-Date -Format s) $msg" | Out-File -FilePath $log -Append -Encoding utf8 }

$gpu = "$env:ProgramFiles\lolssd\gpu"
$vendors = @{
    'VEN_1002' = 'amd-software.exe'
    'VEN_10DE' = 'nvidia-app.exe'
    'VEN_8086' = 'intel-dsa.exe'
}
$machine = (Get-CimInstance Win32_ComputerSystemProduct).UUID
$offered = if (Test-Path $seen) { @(Get-Content $seen) } else { @() }

# Display adapters: class Display, or (no driver yet, no class) a PCI device of class 03xx.
$adapters = Get-CimInstance Win32_PnPEntity | Where-Object {
    $_.PNPDeviceID -like 'PCI\*' -and ($_.PNPClass -eq 'Display' -or ($_.CompatibleID -like 'PCI\CC_03*'))
}
foreach ($a in $adapters) {
    $ven = [regex]::Match($a.PNPDeviceID, 'VEN_[0-9A-F]{4}').Value
    if (-not $vendors.ContainsKey($ven)) { continue }
    $basic = $a.ConfigManagerErrorCode -ne 0 -or $a.Name -match 'Basic Display|Basic Render'
    if (-not $basic) { continue }
    $exe = Join-Path $gpu $vendors[$ven]
    if (-not (Test-Path $exe)) { Log "no installer for $ven ($($a.Name))"; continue }

    $desk = "$env:PUBLIC\Desktop\Drivers"
    New-Item -ItemType Directory -Force $desk | Out-Null
    Copy-Item $exe $desk -Force

    $key = "$machine $ven"
    if ($offered -contains $key) { continue }
    Add-Content -Path $seen -Value $key
    $offered += $key
    Log "$($a.Name) [$($a.PNPDeviceID)] without its driver: starting $($vendors[$ven])"
    Start-Process $exe
}
