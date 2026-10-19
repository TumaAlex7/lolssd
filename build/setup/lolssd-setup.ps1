# lolssd: one-time machine configuration, runs as SYSTEM during Windows Setup.
#   -Phase Specialize  from autounattend.xml, specialize pass (before OOBE)
#   -Phase Complete    from SetupComplete.cmd (after setup, before the first logon)
# Setup runs again after sysprep /generalize on every new machine, so every step is idempotent.
# What and why: docs/ARCHITECTURE.md, sections 2 and 4.
# English only: Windows PowerShell 5.1 misreads non-ASCII in BOM-less files.
param(
    [Parameter(Mandatory)]
    [ValidateSet('Specialize', 'Complete')]
    [string]$Phase
)

$ErrorActionPreference = 'Stop'
$log = "$env:SystemRoot\Setup\Scripts\lolssd-setup.log"
$failed = 0

function Log($msg) {
    "$(Get-Date -Format s) [$Phase] $msg" | Out-File -FilePath $log -Append -Encoding utf8
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

if ($Phase -eq 'Specialize') {
    # Before OOBE: device encryption starts right after it and would bind the disk
    # to the TPM of whatever machine it was set up on.
    Step 'prevent device encryption' {
        Set-Reg 'HKLM:\SYSTEM\CurrentControlSet\Control\BitLocker' 'PreventDeviceEncryption' 1
    }

    # Host disks must stay offline, so Windows never mounts or writes to them
    # (wsl --mount takes them offline-only anyway).
    Step 'SAN policy OfflineInternal' {
        $tmp = (New-TemporaryFile).FullName
        'san policy=OfflineInternal' | Out-File -FilePath $tmp -Encoding ascii
        try { Run diskpart.exe /s $tmp } finally { Remove-Item $tmp }
    }

    # Windows To Go behaviour: tolerates being moved between machines, no hibernation.
    Step 'portable operating system' {
        Set-Reg 'HKLM:\SYSTEM\CurrentControlSet\Control' 'PortableOperatingSystem' 1
    }

    # Linux keeps the RTC in UTC; without this the host clock shifts on every boot.
    Step 'RTC in UTC' {
        Set-Reg 'HKLM:\SYSTEM\CurrentControlSet\Control\TimeZoneInformation' 'RealTimeIsUniversal' 1
    }
}

if ($Phase -eq 'Complete') {
    # A hibernated or fast-started Windows leaves NTFS dirty and state on a disk
    # that may next boot on another machine.
    Step 'hibernation and fast startup off' {
        Run powercfg.exe /hibernate off
        Set-Reg 'HKLM:\SYSTEM\CurrentControlSet\Control\Session Manager\Power' 'HiberbootEnabled' 0
    }

    # Windows is frozen: an update needing several reboots would end up half-applied,
    # and an unattended reboot loop on someone else's machine is the worst case.
    Step 'Windows Update policies' {
        $wu = 'HKLM:\SOFTWARE\Policies\Microsoft\Windows\WindowsUpdate'
        Set-Reg $wu 'DoNotConnectToWindowsUpdateInternetLocations' 1
        Set-Reg $wu 'ExcludeWUDriversInQualityUpdate' 1
        Set-Reg "$wu\AU" 'NoAutoUpdate' 1
        Set-Reg "$wu\AU" 'NoAutoRebootWithLoggedOnUsers' 1
        Set-Reg 'HKLM:\SOFTWARE\Policies\Microsoft\WindowsStore' 'AutoDownload' 2
    }

    # WaaSMedicSvc re-enables the others and its key is ACL-protected; a failure here
    # is logged and the policies above still hold.
    foreach ($svc in 'wuauserv', 'UsoSvc', 'WaaSMedicSvc') {
        Step "disable service $svc" {
            Set-Reg "HKLM:\SYSTEM\CurrentControlSet\Services\$svc" 'Start' 4
        }
    }

    Step 'disable update scheduled tasks' {
        $denied = @()
        $tasks = Get-ScheduledTask -TaskPath '\Microsoft\Windows\WindowsUpdate\', '\Microsoft\Windows\UpdateOrchestrator\' -ErrorAction SilentlyContinue
        foreach ($t in $tasks) {
            try { $t | Disable-ScheduledTask | Out-Null } catch { $denied += $t.TaskName }
        }
        if ($denied) { Log "      tasks left enabled (access denied): $($denied -join ', ')" }
    }

    # In case encryption started anyway (e.g. image built before the Specialize step existed).
    Step 'BitLocker off on system drive' {
        if (Get-Command Get-BitLockerVolume -ErrorAction SilentlyContinue) {
            $v = Get-BitLockerVolume -MountPoint $env:SystemDrive
            if ($v.VolumeStatus -ne 'FullyDecrypted') { Disable-BitLocker -MountPoint $env:SystemDrive | Out-Null }
        }
    }

    # Vanguard wants VBS + HVCI (VAN 9005). VBS itself needs Secure Boot on the host.
    Step 'VBS and HVCI on' {
        $dg = 'HKLM:\SYSTEM\CurrentControlSet\Control\DeviceGuard'
        Set-Reg $dg 'EnableVirtualizationBasedSecurity' 1
        Set-Reg $dg 'RequirePlatformSecurityFeatures' 1
        Set-Reg "$dg\Scenarios\HypervisorEnforcedCodeIntegrity" 'Enabled' 1
    }

    # Needed only for Linux inside Windows, but enabling later would cost an extra reboot.
    Step 'WSL and Virtual Machine Platform' {
        Enable-WindowsOptionalFeature -Online -All -NoRestart -FeatureName VirtualMachinePlatform | Out-Null
        Enable-WindowsOptionalFeature -Online -All -NoRestart -FeatureName Microsoft-Windows-Subsystem-Linux | Out-Null
        Run bcdedit.exe /set '{current}' hypervisorlaunchtype auto
    }

    # Space on a 64-128 GB disk: reserved storage holds ~7 GB for updates we never install.
    Step 'reserved storage off' {
        Run dism.exe /Online /Set-ReservedStorageState /State:Disabled /Quiet
    }

    Step 'system restore off' {
        Disable-ComputerRestore -Drive "$env:SystemDrive\"
    }

    # The system disk is on USB: suspending the port or spinning the disk down
    # can drop it from under the running system.
    Step 'USB selective suspend and disk idle off' {
        $usb = '2a737441-1930-4402-8d77-b2bebba308a3', '48e6b7a6-50f5-4782-a5d4-53bb8f07e226'
        Run powercfg.exe /setacvalueindex SCHEME_CURRENT @usb 0
        Run powercfg.exe /setdcvalueindex SCHEME_CURRENT @usb 0
        Run powercfg.exe /change disk-timeout-ac 0
        Run powercfg.exe /change disk-timeout-dc 0
        Run powercfg.exe /setactive SCHEME_CURRENT
    }
}

Log "done, $failed step(s) failed"
exit $failed
