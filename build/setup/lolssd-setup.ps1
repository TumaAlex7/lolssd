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

# The registry blocks creating hiberfil.sys; this also removes one setup may have left.
Step 'hibernation off' {
    Run powercfg.exe /hibernate off
}

# Task definitions live in files plus the TaskCache registry, with ACLs on some;
# the services are already disabled offline, this is the second layer.
Step 'disable update scheduled tasks' {
    $denied = @()
    $tasks = Get-ScheduledTask -TaskPath '\Microsoft\Windows\WindowsUpdate\', '\Microsoft\Windows\UpdateOrchestrator\' -ErrorAction SilentlyContinue
    foreach ($t in $tasks) {
        try { $t | Disable-ScheduledTask | Out-Null } catch { $denied += $t.TaskName }
    }
    if ($denied) { Log "      tasks left enabled (access denied): $($denied -join ', ')" }
}

# In case encryption started before PreventDeviceEncryption took effect.
Step 'BitLocker off on system drive' {
    if (Get-Command Get-BitLockerVolume -ErrorAction SilentlyContinue) {
        $v = Get-BitLockerVolume -MountPoint $env:SystemDrive
        if ($v.VolumeStatus -ne 'FullyDecrypted') { Disable-BitLocker -MountPoint $env:SystemDrive | Out-Null }
    }
}

# Linux inside Windows: QEMU runs the host's Linux on Windows' own hypervisor (WHPX). Enabling
# it later would cost an extra reboot.
Step 'Windows Hypervisor Platform' {
    Enable-WindowsOptionalFeature -Online -All -NoRestart -FeatureName HypervisorPlatform | Out-Null
    Run bcdedit.exe /set '{current}' hypervisorlaunchtype auto
}

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

# "Restart into Windows (SSD)": a plain restart boots the host's own system, because
# nvram-guard keeps this SSD out of BootOrder. lolssd-win restart sets BootNext to the SSD and
# restarts; it asks for admin rights (UAC) itself.
Step 'restart shortcut' {
    $lnk = (New-Object -ComObject WScript.Shell).CreateShortcut("$env:ProgramData\Microsoft\Windows\Start Menu\Programs\Restart into Windows (SSD).lnk")
    $lnk.TargetPath = "$env:ProgramFiles\lolssd\lolssd-win.exe"
    $lnk.Arguments = 'restart'
    $lnk.Description = 'Restart straight back into this Windows instead of the host system'
    $lnk.IconLocation = "$env:SystemRoot\System32\shell32.dll,238"
    $lnk.Save()
}

# The host's Linux in a VM, full screen on every monitor (lolssd-win desktop; needs a profile:
# lolssd profile on the host). Start menu folder and desktop.
Step 'Linux shortcuts' {
    $shell = New-Object -ComObject WScript.Shell
    $menu = "$env:ProgramData\Microsoft\Windows\Start Menu\Programs\Linux"
    New-Item -ItemType Directory -Force $menu | Out-Null
    foreach ($dir in $menu, "$env:PUBLIC\Desktop") {
        $lnk = $shell.CreateShortcut("$dir\Linux desktop (host).lnk")
        $lnk.TargetPath = "$env:ProgramFiles\lolssd\lolssd-win.exe"
        $lnk.Arguments = 'desktop'
        $lnk.Description = "The host's Linux in a VM, full screen on every monitor"
        $lnk.Save()
    }
}

# ---------------------------------------------------------------- tiny
# Runs before the first logon, so removing the provisioned packages is enough: no user has
# them installed yet. Kept: runtimes the rest depends on, Terminal (Linux shells open in it),
# Windows Security (memory integrity status), winget, Store, Notepad.
$keepApps = '^(Microsoft\.(VCLibs|UI\.Xaml|NET\.|WindowsAppRuntime|DesktopAppInstaller|WindowsTerminal|SecHealthUI|WindowsStore|StorePurchaseApp|WindowsNotepad))'
Step 'remove provisioned apps' {
    $removed = @()
    foreach ($p in Get-AppxProvisionedPackage -Online) {
        if ($p.DisplayName -match $keepApps) { continue }
        try {
            Remove-AppxProvisionedPackage -Online -PackageName $p.PackageName -AllUsers -ErrorAction Stop | Out-Null
            $removed += $p.DisplayName
        } catch { Log "      could not remove $($p.DisplayName): $_" }
    }
    Log "      removed: $($removed -join ', ')"
}

# tiny11's set of Windows packages (IE, WordPad, Media Player, handwriting, OCR, speech...).
# The same DISM servicing tiny11builder uses, on the full Windows of the install VM: WinPE's
# DISM cannot service packages of a newer image (and has no AppX provider at all).
$removePackages = 'Microsoft-Windows-(InternetExplorer-Optional-Package|Kernel-LA57-FoD-Package|LanguageFeatures-(Handwriting|OCR|Speech|TextToSpeech)|MediaPlayer-Package|TabletPCMath-Package|Wallpaper-Content-Extended-FoD-Package|WordPad-FoD-Package|StepsRecorder-Package)'
Step 'remove Windows packages' {
    $removed = @()
    # Only base packages (Name~Key~Arch~~Version): language parts (~en-US~) go away with them.
    foreach ($p in Get-WindowsPackage -Online | Where-Object { $_.PackageName -match $removePackages -and $_.PackageState -eq 'Installed' -and ($_.PackageName -split '~')[3] -eq '' }) {
        try {
            Remove-WindowsPackage -Online -PackageName $p.PackageName -NoRestart -ErrorAction Stop | Out-Null
            $removed += $p.PackageName
        } catch { Log "      could not remove $($p.PackageName): $_" }
    }
    Log "      removed: $($removed -join ', ')"
}

# OneDrive installs itself per user from the default profile's Run key.
Step 'no OneDrive for new users' {
    Run reg.exe load 'HKU\lolssdDefault' "$env:SystemDrive\Users\Default\NTUSER.DAT"
    try {
        Remove-ItemProperty 'Registry::HKU\lolssdDefault\Software\Microsoft\Windows\CurrentVersion\Run' -Name OneDriveSetup -ErrorAction SilentlyContinue
    } finally {
        [gc]::Collect()
        Run reg.exe unload 'HKU\lolssdDefault'
    }
}

# Outside the EEA the Edge uninstaller refuses unless EdgeUpdateDev allows it. WebView2 stays.
Step 'remove Edge' {
    $setup = Get-ChildItem "${env:ProgramFiles(x86)}\Microsoft\Edge\Application\*\Installer\setup.exe" -ErrorAction SilentlyContinue | Select-Object -First 1
    if ($setup) {
        Set-Reg 'HKLM:\SOFTWARE\Microsoft\EdgeUpdateDev' 'AllowUninstall' 1
        & $setup.FullName --uninstall --system-level --force-uninstall | Out-Null
        if (Test-Path "${env:ProgramFiles(x86)}\Microsoft\Edge\Application\msedge.exe") { throw 'Edge is still installed (uninstall refused)' }
    }
    Remove-Item "$env:PUBLIC\Desktop\Microsoft Edge.lnk" -ErrorAction SilentlyContinue
}

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

Log "done, $failed step(s) failed"
exit $failed
