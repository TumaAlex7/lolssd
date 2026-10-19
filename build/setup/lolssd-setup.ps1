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

Log "done, $failed step(s) failed"
exit $failed
