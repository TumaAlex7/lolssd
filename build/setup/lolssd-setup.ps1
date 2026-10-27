# lolssd: the rest of the machine setup, with network. Runs as SYSTEM from SetupComplete.cmd
# (after setup, before the first logon); lolssd-specialize.ps1 has already put our files and
# registry settings in place. Downloads are verified (pinned SHA-256, or the vendor's signature
# for a URL that always serves the latest version) and cached in $cache, so a rerun is cheap.
# Every step is idempotent; a failed step is logged and the others go on.
# English only: Windows PowerShell 5.1 misreads non-ASCII in BOM-less files.

$log = "$env:SystemRoot\Setup\Scripts\lolssd-setup.log"
. "$PSScriptRoot\lolssd-common.ps1"
$cache = "$env:SystemRoot\Temp\lolssd"
New-Item -ItemType Directory -Force $cache | Out-Null
Log "setup"

# ---------------------------------------------------------------- downloads
# Wallpaper and lock screen: K/DA FIRESTARTER splash art (Riot's art, so only in the project's
# assets release), cut to the stock file names; our own art from the payload if it is not there.
Step 'wallpaper' {
    $src = "$payload\art"
    try {
        Get-Verified 'https://github.com/TumaAlex7/lolssd/releases/download/assets/kda-firestarter-4k.jpg' "$cache\kda.jpg" '1C2A64B2BC8781F88FB507DCFB8073C9D3A52D3FFD812B1C7A55E5B966035218'
        Add-Type -AssemblyName System.Drawing
        $img = [System.Drawing.Image]::FromFile("$cache\kda.jpg")
        $jpeg = [System.Drawing.Imaging.ImageCodecInfo]::GetImageEncoders() | Where-Object MimeType -eq 'image/jpeg'
        $q = New-Object System.Drawing.Imaging.EncoderParameters 1
        $q.Param[0] = New-Object System.Drawing.Imaging.EncoderParameter([System.Drawing.Imaging.Encoder]::Quality, [long]92)
        $src = "$cache\wall"
        New-Item -ItemType Directory -Force $src | Out-Null
        foreach ($size in '3840x2160', '1920x1080', '1920x1200', '2560x1440', '2560x1600') {
            $w, $h = $size -split 'x' | ForEach-Object { [int]$_ }
            $scale = [Math]::Max($w / $img.Width, $h / $img.Height)
            $nw, $nh = [int][Math]::Ceiling($img.Width * $scale), [int][Math]::Ceiling($img.Height * $scale)
            $bmp = New-Object System.Drawing.Bitmap $w, $h
            $g = [System.Drawing.Graphics]::FromImage($bmp)
            $g.InterpolationMode = 'HighQualityBicubic'
            $g.DrawImage($img, [int](($w - $nw) / 2), [int](($h - $nh) / 2), $nw, $nh)
            $bmp.Save("$src\img0_$size.jpg", $jpeg, $q)
            $g.Dispose(); $bmp.Dispose()
        }
        $img.Dispose()
        Copy-Item "$src\img0_3840x2160.jpg" "$src\img0.jpg"
        Copy-Item "$src\img0_3840x2160.jpg" "$src\img100.jpg"
    } catch { Log "      K/DA art: $_; our own wallpaper instead" }
    $web = "$env:SystemRoot\Web"
    Replace-Protected "$src\img0.jpg" "$web\Wallpaper\Windows\img0.jpg"
    Replace-Protected "$src\img100.jpg" "$web\Screen\img100.jpg"
    foreach ($f in Get-ChildItem "$src\img0_*.jpg") { Replace-Protected $f.FullName "$web\4K\Wallpaper\Windows\$($f.Name)" }
}

# GPU vendors' installers (lolssd-gpu.ps1 starts the right one on a machine without the driver):
# any version, they update themselves.
Step 'GPU driver installers' {
    New-Item -ItemType Directory -Force "$prog\gpu" | Out-Null
    Get-Verified 'https://drivers.amd.com/drivers/installer/26.10/whql/amd-software-adrenalin-edition-26.7.1-minimalsetup-260724_web.exe' "$prog\gpu\amd-software.exe" 'EF8BA07A74D59B577258820EAB35C0AE80E46AF21D51043D219C6EEF3E2D99B3' $null 'https://www.amd.com/'
    Get-Verified 'https://us.download.nvidia.com/nvapp/client/11.0.6.383/NVIDIA_app_v11.0.6.383.exe' "$prog\gpu\nvidia-app.exe" 'CE4CB43A778B177973DBEEA9C3FFEF5CFD6D04769CF106BC07EE11257E99E098'
    Get-Verified 'https://dsadata.intel.com/installer' "$prog\gpu\intel-dsa.exe" $null 'Intel Corporation'
}

if (Opt vm) {
    # TigerVNC: the viewer for the host's desktop in the VM (lolssd-win desktop).
    Step 'TigerVNC' {
        Get-Verified 'https://sourceforge.net/projects/tigervnc/files/stable/1.16.2/vncviewer64-1.16.2.exe/download' "$prog\vncviewer.exe" '58396D99556026DA6B906C9ED51AD6CB5C840CC3FB65E53653C52CA5A80BFAD9'
    }

    # QEMU for Windows (Stefan Weil's build): its installer, silently into a scratch folder; only
    # what the VM needs (qemu-files.txt: the exe, its DLLs, firmware) goes to $prog\qemu.
    Step 'QEMU' {
        if (Test-Path "$prog\qemu\qemu-system-x86_64w.exe") { return }
        $setup = "$cache\qemu-w64-setup.exe"
        Get-Verified 'https://qemu.weilnetz.de/w64/qemu-w64-setup-20260811.exe' $setup 'F98A8AEB5F7FAEA9765B6DEE28316C266CD179D80354A2FED8E50176F9A2E59F'
        $tmp = "$cache\qemu"
        # NSIS: /D= must come last, without quotes.
        $p = Start-Process $setup -ArgumentList '/S', "/D=$tmp" -Wait -PassThru
        if ($p.ExitCode -ne 0) { throw "QEMU installer exited with $($p.ExitCode)" }
        foreach ($f in Get-Content "$payload\qemu-files.txt" | Where-Object { $_ -and -not $_.StartsWith('#') }) {
            New-Item -ItemType Directory -Force (Split-Path "$prog\qemu\$f") | Out-Null
            Copy-Item "$tmp\$f" "$prog\qemu\$f" -Force
        }
        # The installer registered itself for Apps & features; its own uninstaller removes that.
        if (Test-Path "$tmp\qemu-uninstall.exe") { Start-Process "$tmp\qemu-uninstall.exe" -ArgumentList '/S' -Wait }
        Remove-Item $tmp -Recurse -Force -ErrorAction SilentlyContinue
        Remove-Item $setup -ErrorAction SilentlyContinue
    }

    # TAP-Windows (OpenVPN, signed for Windows 10/11): the VM's network adapter; lolssd-win
    # creates it with devcon.
    Step 'TAP-Windows' {
        $zip = "$cache\tap.zip"
        Get-Verified 'https://github.com/OpenVPN/tap-windows6/releases/download/9.27.0/dist.win10.zip' $zip '36E2609B7CEEFEDCB978CE5C48CAF9E0E5AF83423717C4E2E3C1D7EBCA8F62A5'
        Expand-Archive $zip "$cache\tap" -Force
        New-Item -ItemType Directory -Force "$prog\tap" | Out-Null
        Copy-Item "$cache\tap\dist.win10\amd64\*" "$prog\tap" -Force
    }
}

# ---------------------------------------------------------------- the LOLSSD partition
# build/install.sh creates it unformatted (no exFAT tools needed on Linux) and passes its GPT
# partition GUID; it copies the Linux tool and the drivers onto it after the install.
if (Opt lolssd_new) { Step 'format LOLSSD (exFAT)' {
    $part = Get-Partition | Where-Object { $_.Guid -eq "{$($options['lolssd_guid'])}" }
    if (-not $part) { throw "no partition {$($options['lolssd_guid'])}" }
    $part | Format-Volume -FileSystem exFAT -NewFileSystemLabel LOLSSD -Confirm:$false | Out-Null
} }

# ---------------------------------------------------------------- the machine
# The registry blocks creating hiberfil.sys; this also removes one setup may have left.
Step 'hibernation off' {
    Run powercfg.exe /hibernate off
}

# Setup puts wuauserv back to manual after specialize (seen in the install test), so the services
# are set again here; the tasks are a second layer (task files plus the TaskCache registry,
# some ACL-protected).
if (-not (Opt updates)) { Step 'Windows Update off' {
    foreach ($svc in 'wuauserv', 'UsoSvc') {
        Set-ItemProperty "HKLM:\SYSTEM\CurrentControlSet\Services\$svc" Start 4
    }
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

# Keeps the host's NVRAM clean of entries pointing at this disk (lolssd-win nvram-guard).
Step 'register NVRAM guard task' {
    $exe = "$prog\lolssd-win.exe"
    if (-not (Test-Path $exe)) { throw "$exe not found" }
    $action = New-ScheduledTaskAction -Execute $exe -Argument 'nvram-guard'
    $trigger = New-ScheduledTaskTrigger -AtStartup
    $principal = New-ScheduledTaskPrincipal -UserId 'SYSTEM' -LogonType ServiceAccount -RunLevel Highest
    Register-ScheduledTask -TaskPath '\lolssd\' -TaskName 'nvram-guard' -Action $action -Trigger $trigger -Principal $principal -Force | Out-Null
}

# At every logon of an administrator (the autologon user), elevated: shortcuts and the Scancode
# Map for the VM (lolssd-win logon). Without a profile for this machine it exits quietly.
Step 'register logon task' {
    $action = New-ScheduledTaskAction -Execute "$prog\lolssd-win.exe" -Argument 'logon'
    $trigger = New-ScheduledTaskTrigger -AtLogOn
    $principal = New-ScheduledTaskPrincipal -GroupId 'BUILTIN\Administrators' -RunLevel Highest
    Register-ScheduledTask -TaskPath '\lolssd\' -TaskName 'logon' -Action $action -Trigger $trigger -Principal $principal -Force | Out-Null
}

# Drivers from \drivers on the disk's volumes (LOLSSD), at every startup when needed.
Step 'register driver install task' {
    $action = New-ScheduledTaskAction -Execute 'powershell.exe' -Argument "-NoProfile -ExecutionPolicy Bypass -File `"$prog\lolssd-drivers.ps1`""
    $trigger = New-ScheduledTaskTrigger -AtStartup
    $principal = New-ScheduledTaskPrincipal -UserId 'SYSTEM' -LogonType ServiceAccount -RunLevel Highest
    Register-ScheduledTask -TaskPath '\lolssd\' -TaskName 'drivers' -Action $action -Trigger $trigger -Principal $principal -Force | Out-Null
}

# At every logon of an administrator, in the user's session (the installer window must show):
# the GPU vendor's installer for a card still without its driver (lolssd-gpu.ps1).
Step 'register GPU installer task' {
    $action = New-ScheduledTaskAction -Execute 'powershell.exe' -Argument "-NoProfile -WindowStyle Hidden -ExecutionPolicy Bypass -File `"$prog\lolssd-gpu.ps1`""
    $trigger = New-ScheduledTaskTrigger -AtLogOn
    $principal = New-ScheduledTaskPrincipal -GroupId 'BUILTIN\Administrators' -RunLevel Highest
    Register-ScheduledTask -TaskPath '\lolssd\' -TaskName 'gpu' -Action $action -Trigger $trigger -Principal $principal -Force | Out-Null
}

# Shortcuts (lolssd-win shortcuts): "Restart into Windows" in System Tools -- a plain restart
# boots the host's own system, because nvram-guard keeps Windows out of BootOrder; and, with the
# vm option, "Linux" (the host's desktop in a VM) and "Linux terminal" in the Start menu root and
# on the desktop.
Step 'shortcuts' {
    $p = Start-Process "$prog\lolssd-win.exe" -ArgumentList 'shortcuts' -Wait -PassThru
    if ($p.ExitCode -ne 0) { throw "lolssd-win shortcuts exited with $($p.ExitCode)" }
}

# ---------------------------------------------------------------- tiny
# Runs before the first logon, so removing the provisioned packages is enough: no user has
# them installed yet. The apps option lists them by package name (tiny11's list by default);
# runtimes, Store, winget, Terminal and Windows Security are never on it. With edge, Edge's Store
# packages (the browser stub and Game Assist) go too.
$removeApps = @($options['apps'] -split ',' | ForEach-Object { $_.Trim() } | Where-Object { $_ })
if (Opt edge) { $removeApps += 'Microsoft.MicrosoftEdge.Stable', 'Microsoft.Edge.GameAssist' }
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
$removePackages = 'Microsoft-Windows-(InternetExplorer-Optional-Package|Kernel-LA57-FoD-Package|LanguageFeatures-(Handwriting|OCR|Speech|TextToSpeech)|MediaPlayer-Package|TabletPCMath-Package|Wallpaper-Content-Extended-FoD-Package|WordPad-FoD-Package|StepsRecorder-Package)'
if (Opt packages) { Step 'remove Windows packages' {
    $removed = @()
    # Only base amd64 packages (Name~Key~amd64~~Version): language parts (~en-US~) and the wow64
    # halves go away with them.
    foreach ($p in Get-WindowsPackage -Online | Where-Object { $_.PackageName -match $removePackages -and $_.PackageState -eq 'Installed' -and ($_.PackageName -split '~')[2] -eq 'amd64' -and ($_.PackageName -split '~')[3] -eq '' }) {
        try {
            Remove-WindowsPackage -Online -PackageName $p.PackageName -NoRestart -ErrorAction Stop | Out-Null
            $removed += $p.PackageName
        } catch { Log "      could not remove $($p.PackageName): $_" }
    }
    Log "      removed: $($removed -join ', ')"
} }

# The real machine has no NVRAM entry from this install (setup wrote it to the VM's NVRAM);
# firmware boot menus use the removable-media path \EFI\BOOT\BOOTX64.EFI. On an ESP shared with
# Linux that path is Linux's loader: build/install.sh moves it aside for the install and puts it
# back afterwards.
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
    # The web UI asks for a user name and password on the first visit, https://<host>:47990.
    Step 'Sunshine' {
        $msi = "$cache\sunshine.msi"
        Get-Verified 'https://github.com/LizardByte/Sunshine/releases/download/v2026.914.233613/Sunshine-Windows-AMD64-installer.msi' $msi '1D7FED8BEECD5889DC7FF14CF9F42D6D38F37C3066C13C6C2A5F4E91847E0CCF'
        $p = Start-Process msiexec.exe -ArgumentList '/i', "`"$msi`"", '/qn', '/norestart' -Wait -PassThru
        if ($p.ExitCode -notin 0, 3010) { throw "msiexec exited with $($p.ExitCode)" }
    }

    # A Moonlight app that reboots the machine: nvram-guard keeps Windows out of BootOrder, so
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

Remove-Item $cache -Recurse -Force -ErrorAction SilentlyContinue
Log "done, $failed step(s) failed"
exit $failed
