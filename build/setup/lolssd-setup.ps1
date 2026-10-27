# lolssd: one-time machine configuration that the registry cannot express.
# Runs as SYSTEM from SetupComplete.cmd (after setup, before the first logon), and again
# after sysprep /generalize on every new machine, so every step is idempotent.
# Plain registry settings live in lolssd.reg and are applied offline before the first boot.
# English only: Windows PowerShell 5.1 misreads non-ASCII in BOM-less files.

$ErrorActionPreference = 'Stop'
$log = "$env:SystemRoot\Setup\Scripts\lolssd-setup.log"
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

# Build options (build/options.default, written by the build wizard): key=value, 1 = yes.
$options = @{}
foreach ($line in Get-Content "$env:SystemRoot\Setup\Scripts\lolssd-options.txt" -ErrorAction SilentlyContinue) {
    if ($line -match '^([a-z]+)=(.*)$') { $options[$Matches[1]] = $Matches[2].Trim() }
}
function Opt($name) { $options[$name] -eq '1' }
Log "options: $(($options.GetEnumerator() | Where-Object Name -ne 'apps' | ForEach-Object { "$($_.Name)=$($_.Value)" }) -join ' ')"

# The registry blocks creating hiberfil.sys; this also removes one setup may have left.
Step 'hibernation off' {
    Run powercfg.exe /hibernate off
}

# Task definitions live in files plus the TaskCache registry, with ACLs on some;
# the services are already disabled offline, this is the second layer.
if (-not (Opt updates)) { Step 'disable update scheduled tasks' {
    $denied = @()
    $tasks = Get-ScheduledTask -TaskPath '\Microsoft\Windows\WindowsUpdate\', '\Microsoft\Windows\UpdateOrchestrator\' -ErrorAction SilentlyContinue
    foreach ($t in $tasks) {
        try { $t | Disable-ScheduledTask | Out-Null } catch { $denied += $t.TaskName }
    }
    if ($denied) { Log "      tasks left enabled (access denied): $($denied -join ', ')" }
} }

# In case encryption started before PreventDeviceEncryption took effect.
Step 'BitLocker off on system drive' {
    if (Get-Command Get-BitLockerVolume -ErrorAction SilentlyContinue) {
        $v = Get-BitLockerVolume -MountPoint $env:SystemDrive
        if ($v.VolumeStatus -ne 'FullyDecrypted') { Disable-BitLocker -MountPoint $env:SystemDrive | Out-Null }
    }
}

# Linux inside Windows: QEMU runs the host's Linux on Windows' own hypervisor (WHPX). Enabling
# it later would cost an extra reboot.
if (Opt vm) { Step 'Windows Hypervisor Platform' {
    Enable-WindowsOptionalFeature -Online -All -NoRestart -FeatureName HypervisorPlatform | Out-Null
    Run bcdedit.exe /set '{current}' hypervisorlaunchtype auto
} }

# ShippedWithReserves in lolssd.reg works only if set before setup created the reserve.
Step 'reserved storage off' {
    Run dism.exe /Online /Set-ReservedStorageState /State:Disabled /Quiet
}

# Keeps the host's NVRAM clean of entries pointing at this SSD (lolssd-win nvram-guard).
Step 'register NVRAM guard task' {
    $exe = "$env:ProgramFiles\lolssd\lolssd-win.exe"
    if (-not (Test-Path $exe)) { throw "$exe not found" }
    $action = New-ScheduledTaskAction -Execute $exe -Argument 'nvram-guard'
    $trigger = New-ScheduledTaskTrigger -AtStartup
    $principal = New-ScheduledTaskPrincipal -UserId 'SYSTEM' -LogonType ServiceAccount -RunLevel Highest
    Register-ScheduledTask -TaskPath '\lolssd\' -TaskName 'nvram-guard' -Action $action -Trigger $trigger -Principal $principal -Force | Out-Null
}

# At every logon of an administrator (the autologon user), elevated: shortcuts and the Scancode
# Map for the VM (lolssd-win logon). Without a profile for this machine it exits quietly.
Step 'register logon task' {
    $exe = "$env:ProgramFiles\lolssd\lolssd-win.exe"
    $action = New-ScheduledTaskAction -Execute $exe -Argument 'logon'
    $trigger = New-ScheduledTaskTrigger -AtLogOn
    $principal = New-ScheduledTaskPrincipal -GroupId 'BUILTIN\Administrators' -RunLevel Highest
    Register-ScheduledTask -TaskPath '\lolssd\' -TaskName 'logon' -Action $action -Trigger $trigger -Principal $principal -Force | Out-Null
}

# Drivers for a new machine from \drivers on the disk's volumes (Windows Update is off).
Step 'register driver install task' {
    $ps = "$env:ProgramFiles\lolssd\lolssd-drivers.ps1"
    if (-not (Test-Path $ps)) { throw "$ps not found" }
    $action = New-ScheduledTaskAction -Execute 'powershell.exe' -Argument "-NoProfile -ExecutionPolicy Bypass -File `"$ps`""
    $trigger = New-ScheduledTaskTrigger -AtStartup
    $principal = New-ScheduledTaskPrincipal -UserId 'SYSTEM' -LogonType ServiceAccount -RunLevel Highest
    Register-ScheduledTask -TaskPath '\lolssd\' -TaskName 'drivers' -Action $action -Trigger $trigger -Principal $principal -Force | Out-Null
}

# Shortcuts (lolssd-win shortcuts): "Restart into Windows" in System Tools -- a plain restart
# boots the host's own system, because nvram-guard keeps Windows out of BootOrder; and, with the
# vm option, "Linux" (the host's desktop in a VM) and "Linux terminal" in the Start menu root and
# on the desktop.
Step 'shortcuts' {
    $p = Start-Process "$env:ProgramFiles\lolssd\lolssd-win.exe" -ArgumentList 'shortcuts' -Wait -PassThru
    if ($p.ExitCode -ne 0) { throw "lolssd-win shortcuts exited with $($p.ExitCode)" }
}

# ---------------------------------------------------------------- tiny
# Runs before the first logon, so removing the provisioned packages is enough: no user has
# them installed yet. The apps option lists them by package name (tiny11's list by default);
# runtimes, Store, winget, Terminal and Windows Security are never on it.
$removeApps = @($options['apps'] -split ',' | ForEach-Object { $_.Trim() } | Where-Object { $_ })
if ($removeApps) { Step 'remove provisioned apps' {
    $removed = @()
    foreach ($p in Get-AppxProvisionedPackage -Online) {
        if ($removeApps -notcontains $p.DisplayName) { continue }
        try {
            Remove-AppxProvisionedPackage -Online -PackageName $p.PackageName -AllUsers -ErrorAction Stop | Out-Null
            $removed += $p.DisplayName
        } catch { Log "      could not remove $($p.DisplayName): $_" }
    }
    Log "      removed: $($removed -join ', ')"
} }

# tiny11's set of Windows packages (IE, WordPad, Media Player, handwriting, OCR, speech...).
# The same DISM servicing tiny11builder uses, on the full Windows of the install VM: WinPE's
# DISM cannot service packages of a newer image (and has no AppX provider at all).
$removePackages = 'Microsoft-Windows-(InternetExplorer-Optional-Package|Kernel-LA57-FoD-Package|LanguageFeatures-(Handwriting|OCR|Speech|TextToSpeech)|MediaPlayer-Package|TabletPCMath-Package|Wallpaper-Content-Extended-FoD-Package|WordPad-FoD-Package|StepsRecorder-Package)'
if (Opt packages) { Step 'remove Windows packages' {
    $removed = @()
    # Only base packages (Name~Key~Arch~~Version): language parts (~en-US~) go away with them.
    foreach ($p in Get-WindowsPackage -Online | Where-Object { $_.PackageName -match $removePackages -and $_.PackageState -eq 'Installed' -and ($_.PackageName -split '~')[3] -eq '' }) {
        try {
            Remove-WindowsPackage -Online -PackageName $p.PackageName -NoRestart -ErrorAction Stop | Out-Null
            $removed += $p.PackageName
        } catch { Log "      could not remove $($p.PackageName): $_" }
    }
    Log "      removed: $($removed -join ', ')"
} }

# OneDrive installs itself per user from the default profile's Run key.
if (Opt onedrive) { Step 'no OneDrive for new users' {
    Run reg.exe load 'HKU\lolssdDefault' "$env:SystemDrive\Users\Default\NTUSER.DAT"
    try {
        Remove-ItemProperty 'Registry::HKU\lolssdDefault\Software\Microsoft\Windows\CurrentVersion\Run' -Name OneDriveSetup -ErrorAction SilentlyContinue
    } finally {
        [gc]::Collect()
        Run reg.exe unload 'HKU\lolssdDefault'
    }
} }

# Outside the EEA the Edge uninstaller refuses unless EdgeUpdateDev allows it. WebView2 stays.
if (Opt edge) { Step 'remove Edge' {
    $setup = Get-ChildItem "${env:ProgramFiles(x86)}\Microsoft\Edge\Application\*\Installer\setup.exe" -ErrorAction SilentlyContinue | Select-Object -First 1
    if ($setup) {
        Set-Reg 'HKLM:\SOFTWARE\Microsoft\EdgeUpdateDev' 'AllowUninstall' 1
        & $setup.FullName --uninstall --system-level --force-uninstall | Out-Null
        if (Test-Path "${env:ProgramFiles(x86)}\Microsoft\Edge\Application\msedge.exe") { throw 'Edge is still installed (uninstall refused)' }
    }
    Remove-Item "$env:PUBLIC\Desktop\Microsoft Edge.lnk" -ErrorAction SilentlyContinue
} }

# The real machine has no NVRAM entry from this install (setup wrote it to the VM's NVRAM);
# lolssd boot and firmware boot menus use the removable-media path \EFI\BOOT\BOOTX64.EFI.
Step 'fallback boot path on the ESP' {
    Run mountvol.exe S: /S
    try {
        if (-not (Test-Path 'S:\EFI\BOOT\BOOTX64.EFI')) {
            New-Item -ItemType Directory -Force 'S:\EFI\BOOT' | Out-Null
            Copy-Item 'S:\EFI\Microsoft\Boot\bootmgfw.efi' 'S:\EFI\BOOT\BOOTX64.EFI'
        }
    } finally { mountvol.exe S: /D | Out-Null }
}

# ---------------------------------------------------------------- Sunshine
if (Opt sunshine) {
    # Sunshine (game streaming host for Moonlight): installs its service and firewall rules.
    # The web UI asks for a user name and password on the first visit, https://<server>:47990.
    Step 'Sunshine' {
        $msi = "$env:ProgramFiles\lolssd\sunshine.msi"
        if (-not (Test-Path $msi)) { throw "$msi not found" }
        $p = Start-Process msiexec.exe -ArgumentList '/i', "`"$msi`"", '/qn', '/norestart' -Wait -PassThru
        if ($p.ExitCode -notin 0, 3010) { throw "msiexec exited with $($p.ExitCode)" }
    }

    # A Moonlight app that reboots the server: nvram-guard keeps Windows out of BootOrder, so
    # this lands in Linux.
    Step 'Sunshine app: Reboot to Linux' {
        $apps = "$env:ProgramFiles\Sunshine\config\apps.json"
        $j = if (Test-Path $apps) { Get-Content $apps -Raw | ConvertFrom-Json } else { [pscustomobject]@{ env = @{}; apps = @() } }
        if (-not ($j.apps | Where-Object { $_.name -eq 'Reboot to Linux' })) {
            $j.apps = @($j.apps) + [pscustomobject]@{ name = 'Reboot to Linux'; detached = @('shutdown.exe /r /t 3') }
            $j | ConvertTo-Json -Depth 10 | Set-Content $apps -Encoding UTF8
        }
    }

    # A streaming host: never sleep, never turn the monitor off (Sunshine needs an active display).
    Step 'never sleep' {
        foreach ($k in 'standby-timeout-ac', 'standby-timeout-dc', 'monitor-timeout-ac', 'monitor-timeout-dc') {
            Run powercfg.exe /change $k 0
        }
    }
}

Log "done, $failed step(s) failed"
exit $failed
