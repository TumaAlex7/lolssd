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

# Needed only for Linux inside Windows, but enabling later would cost an extra reboot.
Step 'WSL and Virtual Machine Platform' {
    Enable-WindowsOptionalFeature -Online -All -NoRestart -FeatureName VirtualMachinePlatform | Out-Null
    Enable-WindowsOptionalFeature -Online -All -NoRestart -FeatureName Microsoft-Windows-Subsystem-Linux | Out-Null
    Run bcdedit.exe /set '{current}' hypervisorlaunchtype auto
}

# wsl.exe in Windows is only a stub; WSL itself is a package. The MSI from Microsoft's GitHub
# ships in the image (make wsl), so no Store or network is needed.
Step 'install WSL' {
    $msi = Get-ChildItem "$env:ProgramData\lolssd\wsl\*.msi" -ErrorAction SilentlyContinue | Select-Object -First 1
    if (-not $msi) { throw 'no WSL MSI in \ProgramData\lolssd\wsl (make wsl)' }
    $p = Start-Process msiexec.exe -ArgumentList '/i', "`"$($msi.FullName)`"", '/qn', '/norestart' -Wait -PassThru
    if ($p.ExitCode -notin 0, 3010) { throw "msiexec exited with $($p.ExitCode)" }
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

# Linux inside Windows: at every logon of an administrator (the autologon user), elevated,
# because wsl --mount requires it. Without a profile for this machine it exits quietly.
Step 'register host disk mount task' {
    $exe = "$env:ProgramFiles\lolssd\lolssd-win.exe"
    $action = New-ScheduledTaskAction -Execute $exe -Argument 'mount'
    $trigger = New-ScheduledTaskTrigger -AtLogOn
    $principal = New-ScheduledTaskPrincipal -GroupId 'BUILTIN\Administrators' -RunLevel Highest
    Register-ScheduledTask -TaskPath '\lolssd\' -TaskName 'mount' -Action $action -Trigger $trigger -Principal $principal -Force | Out-Null
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

# Places the nested Hyprland output windows and handles Ctrl+Alt+F12. Runs in the user's
# session, not elevated: it only moves the user's own windows.
Step 'register window controller task' {
    $exe = "$env:ProgramFiles\lolssd\lolssd-win.exe"
    $action = New-ScheduledTaskAction -Execute $exe -Argument 'controller'
    $trigger = New-ScheduledTaskTrigger -AtLogOn
    $principal = New-ScheduledTaskPrincipal -GroupId 'BUILTIN\Users' -RunLevel Limited
    $settings = New-ScheduledTaskSettingsSet -ExecutionTimeLimit ([TimeSpan]::Zero) -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries
    Register-ScheduledTask -TaskPath '\lolssd\' -TaskName 'controller' -Action $action -Trigger $trigger -Principal $principal -Settings $settings -Force | Out-Null
}

# Desktop shortcut that starts the host's desktop (asks for the LUKS password if any).
# Start menu folder and desktop. The launcher distro is imported at the first logon after
# a reboot (lolssd-win mount); the host entries need a profile (lolssd profile on the host).
Step 'Linux shortcuts' {
    $shell = New-Object -ComObject WScript.Shell
    $menu = "$env:ProgramData\Microsoft\Windows\Start Menu\Programs\Linux"
    New-Item -ItemType Directory -Force $menu | Out-Null
    $links = @(
        @('Linux terminal (SSD)', '-d lolssd', 'Shell in the Arch system that lives on this disk'),
        @('Linux shell (host)', '-d lolssd -u root -- /usr/local/bin/lolssd-wsl shell', "The host's own Linux: your shell and files"),
        @('Linux desktop (host)', '-d lolssd -u root -- /usr/local/bin/lolssd-wsl session', "The host's Hyprland, one window per monitor")
    )
    foreach ($l in $links) {
        foreach ($dir in $menu, "$env:PUBLIC\Desktop") {
            $lnk = $shell.CreateShortcut("$dir\$($l[0]).lnk")
            $lnk.TargetPath = "$env:SystemRoot\System32\wsl.exe"
            $lnk.Arguments = $l[1]
            $lnk.Description = $l[2]
            $lnk.Save()
        }
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
