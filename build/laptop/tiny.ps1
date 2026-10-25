# lolssd laptop: tiny11 on a stock install, next to Linux on the same disk.
# Runs as SYSTEM in the specialize pass (before OOBE, no user yet), extracted from
# autounattend.xml. Unlike the SSD build it keeps Windows Update, driver updates, Store, winget,
# Defender, recovery and WebView2: only tiny11's app list, Edge, OneDrive and ads go.
# -Boot, at the first logon: only the boot order (see the last step); in specialize it would
# send setup's own next reboot to Linux.
# English only: Windows PowerShell 5.1 misreads non-ASCII in BOM-less files.
param([switch]$Boot)

$ErrorActionPreference = 'Stop'
$log = "$env:SystemRoot\Setup\Scripts\lolssd-tiny.log"
New-Item -ItemType Directory -Force (Split-Path $log) | Out-Null

function Log($msg) {
    "$(Get-Date -Format s) $msg" | Out-File -FilePath $log -Append -Encoding utf8
}

function Step($name, [scriptblock]$body) {
    try {
        & $body
        Log "ok    $name"
    } catch {
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

# Setup put Windows Boot Manager first in BootOrder and may have replaced the removable-media
# path \EFI\BOOT\BOOTX64.EFI, through which the firmware's "UEFI OS" entry starts systemd-boot.
# Windows leaves BootOrder altogether: systemd-boot finds \EFI\Microsoft\Boot\bootmgfw.efi on
# the ESP and lists Windows in its menu by itself.
if ($Boot) {
    Step 'Linux first at boot' {
        Run mountvol.exe S: /S
        try {
            $sdb = 'S:\EFI\systemd\systemd-bootx64.efi'
            if (Test-Path $sdb) {
                New-Item -ItemType Directory -Force 'S:\EFI\BOOT' | Out-Null
                Copy-Item $sdb 'S:\EFI\BOOT\BOOTX64.EFI' -Force
                Run bcdedit.exe /set '{fwbootmgr}' displayorder '{bootmgr}' /remove
            } else { Log '      no systemd-boot on the ESP, boot order left to Windows' }
        } finally { mountvol.exe S: /D | Out-Null }
    }
    Log 'done (boot)'
    exit
    }

# tiny11maker.ps1's list, except Windows Terminal. Photos, Calculator, Snipping Tool, Notepad and
# the media codecs stay, as in tiny11.
$removeApps = 'AppUp.IntelManagementandSecurityStatus', 'Clipchamp.Clipchamp', 'Microsoft.BingNews',
    'Microsoft.BingSearch', 'Microsoft.BingWeather', 'Microsoft.Copilot', 'Microsoft.Windows.CrossDevice',
    'Microsoft.GamingApp', 'Microsoft.GetHelp', 'Microsoft.Getstarted', 'Microsoft.Microsoft3DViewer',
    'Microsoft.MicrosoftOfficeHub', 'Microsoft.MicrosoftSolitaireCollection', 'Microsoft.MicrosoftStickyNotes',
    'Microsoft.MixedReality.Portal', 'Microsoft.MSPaint', 'Microsoft.Office.OneNote',
    'Microsoft.OfficePushNotificationUtility', 'Microsoft.OutlookForWindows', 'Microsoft.Paint',
    'Microsoft.People', 'Microsoft.PowerAutomateDesktop', 'Microsoft.SkypeApp', 'Microsoft.StartExperiencesApp',
    'Microsoft.Todos', 'Microsoft.Wallet', 'Microsoft.Windows.DevHome', 'Microsoft.Windows.Copilot',
    'Microsoft.Windows.Teams', 'Microsoft.WindowsAlarms', 'Microsoft.WindowsCamera', 'microsoft.windowscommunicationsapps',
    'Microsoft.WindowsFeedbackHub', 'Microsoft.WindowsMaps', 'Microsoft.WindowsSoundRecorder',
    'Microsoft.Xbox.TCUI', 'Microsoft.XboxApp', 'Microsoft.XboxGameOverlay', 'Microsoft.XboxGamingOverlay',
    'Microsoft.XboxIdentityProvider', 'Microsoft.XboxSpeechToTextOverlay', 'Microsoft.YourPhone',
    'Microsoft.ZuneMusic', 'Microsoft.ZuneVideo', 'MicrosoftCorporationII.MicrosoftFamily',
    'MicrosoftCorporationII.QuickAssist', 'MSTeams', 'MicrosoftTeams', 'Microsoft.549981C3F5F10'

Step 'remove provisioned apps' {
    $removed = @()
    foreach ($p in Get-AppxProvisionedPackage -Online) {
        if ($removeApps -notcontains $p.DisplayName) { continue }
        try {
            Remove-AppxProvisionedPackage -Online -PackageName $p.PackageName -AllUsers -ErrorAction Stop | Out-Null
            $removed += $p.DisplayName
        } catch { Log "      could not remove $($p.DisplayName): $_" }
    }
    Log "      removed: $($removed -join ', ')"
}

# Outside the EEA the Edge uninstaller refuses unless EdgeUpdateDev allows it. WebView2 stays.
Step 'remove Edge' {
    $setup = Get-ChildItem "${env:ProgramFiles(x86)}\Microsoft\Edge\Application\*\Installer\setup.exe" -ErrorAction SilentlyContinue | Select-Object -First 1
    if ($setup) {
        Set-Reg 'HKLM:\SOFTWARE\Microsoft\EdgeUpdateDev' 'AllowUninstall' 1
        & $setup.FullName --uninstall --system-level --force-uninstall | Out-Null
        if (Test-Path "${env:ProgramFiles(x86)}\Microsoft\Edge\Application\msedge.exe") { throw 'Edge is still installed (uninstall refused)' }
    }
    Remove-Item "$env:PUBLIC\Desktop\Microsoft Edge.lnk", "$env:ProgramData\Microsoft\Windows\Start Menu\Programs\Microsoft Edge.lnk" -ErrorAction SilentlyContinue
}

# Machine policies: tiny11's, plus no cloud restore of an old install (Windows Backup pulled
# half of a previous Windows onto the server) and dual boot with Linux.
Step 'machine policies' {
    $p = 'HKLM:\SOFTWARE\Policies\Microsoft'
    Set-Reg "$p\Windows\CloudContent" 'DisableWindowsConsumerFeatures' 1
    Set-Reg "$p\Windows\CloudContent" 'DisableCloudOptimizedContent' 1
    Set-Reg "$p\Windows\CloudContent" 'DisableConsumerAccountStateContent' 1
    Set-Reg "$p\PushToInstall" 'DisablePushToInstall' 1
    Set-Reg "$p\MRT" 'DontOfferThroughWUAU' 1
    Set-Reg 'HKLM:\SOFTWARE\Microsoft\PolicyManager\current\device\Start' 'ConfigureStartPins' '{"pinnedList": [{}]}' 'String'
    Set-Reg "$p\Windows\Windows Chat" 'ChatIcon' 3
    Set-Reg "$p\Windows\OneDrive" 'DisableFileSyncNGSC' 1
    Set-Reg "$p\Windows\DataCollection" 'AllowTelemetry' 0
    Set-Reg "$p\Windows\WindowsCopilot" 'TurnOffWindowsCopilot' 1
    Set-Reg "$p\Windows\Explorer" 'DisableSearchBoxSuggestions' 1
    Set-Reg "$p\Dsh" 'AllowNewsAndInterests' 0
    Set-Reg "$p\Teams" 'DisableInstallation' 1
    Set-Reg "$p\Windows\Windows Mail" 'PreventRun' 1
    Set-Reg "$p\Windows\SettingSync" 'DisableSettingSync' 2
    Set-Reg "$p\Windows\SettingSync" 'DisableSettingSyncUserOverride' 1
    Set-Reg "$p\Windows\System" 'EnableCdp' 0
    # Vendor apps and icons for devices (Razer Synapse and the like); drivers still come.
    Set-Reg 'HKLM:\SOFTWARE\Microsoft\Windows\CurrentVersion\Device Metadata' 'PreventDeviceMetadataFromNetwork' 1
    $u = 'HKLM:\SOFTWARE\Microsoft\Windows\CurrentVersion\WindowsUpdate\Orchestrator'
    Set-Reg "$u\UScheduler_Oobe\OutlookUpdate" 'workCompleted' 1
    Set-Reg "$u\UScheduler\OutlookUpdate" 'workCompleted' 1
    Set-Reg "$u\UScheduler\DevHomeUpdate" 'workCompleted' 1
    Set-Reg 'HKLM:\SOFTWARE\Microsoft\Windows\CurrentVersion\ReserveManager' 'ShippedWithReserves' 0
    Set-Reg 'HKLM:\SYSTEM\CurrentControlSet\Control\BitLocker' 'PreventDeviceEncryption' 1
    # Linux keeps the RTC in UTC.
    Set-Reg 'HKLM:\SYSTEM\CurrentControlSet\Control\TimeZoneInformation' 'RealTimeIsUniversal' 1
    # Fast startup leaves NTFS half-mounted (Linux then refuses to write) and holds the RTC.
    Set-Reg 'HKLM:\SYSTEM\CurrentControlSet\Control\Session Manager\Power' 'HiberbootEnabled' 0
}

Step 'hibernation off' {
    Run powercfg.exe /hibernate off
}

Step 'reserved storage off' {
    Run dism.exe /Online /Set-ReservedStorageState /State:Disabled /Quiet
}

# Default profile: tiny11's ad and suggestion switches; OneDrive installs itself per user from Run.
Step 'default user profile' {
    Run reg.exe load 'HKU\lolssdDefault' "$env:SystemDrive\Users\Default\NTUSER.DAT"
    try {
        $d = 'Registry::HKU\lolssdDefault\Software\Microsoft'
        $cdm = "$d\Windows\CurrentVersion\ContentDeliveryManager"
        foreach ($n in 'ContentDeliveryAllowed', 'FeatureManagementEnabled', 'OemPreInstalledAppsEnabled',
            'PreInstalledAppsEnabled', 'PreInstalledAppsEverEnabled', 'SilentInstalledAppsEnabled', 'SoftLandingEnabled',
            'SubscribedContentEnabled', 'SubscribedContent-310093Enabled', 'SubscribedContent-338388Enabled',
            'SubscribedContent-338389Enabled', 'SubscribedContent-338393Enabled', 'SubscribedContent-353694Enabled',
            'SubscribedContent-353696Enabled', 'SystemPaneSuggestionsEnabled') {
            Set-Reg $cdm $n 0
        }
        Remove-Item "$cdm\Subscriptions", "$cdm\SuggestedApps" -Recurse -ErrorAction SilentlyContinue
        Set-Reg "$d\Windows\CurrentVersion\AdvertisingInfo" 'Enabled' 0
        Set-Reg "$d\Windows\CurrentVersion\Privacy" 'TailoredExperiencesWithDiagnosticDataEnabled' 0
        Set-Reg "$d\Windows\CurrentVersion\Explorer\Advanced" 'TaskbarMn' 0
        Set-Reg "$d\Windows\CurrentVersion\Explorer\Advanced" 'Start_IrisRecommendations' 0
        Set-Reg "$d\InputPersonalization" 'RestrictImplicitInkCollection' 1
        Set-Reg "$d\InputPersonalization" 'RestrictImplicitTextCollection' 1
        Remove-ItemProperty "$d\Windows\CurrentVersion\Run" -Name OneDriveSetup -ErrorAction SilentlyContinue
    } finally {
        [gc]::Collect()
        Run reg.exe unload 'HKU\lolssdDefault'
    }
}

# K/DA wallpaper and lock screen from \lolssd\wallpaper on the installer (make.sh puts it there;
# the installer stays attached through specialize). The stock files belong to TrustedInstaller.
Step 'wallpaper' {
    $src = Get-PSDrive -PSProvider FileSystem | ForEach-Object { Join-Path $_.Root 'lolssd\wallpaper' } |
        Where-Object { Test-Path "$_\img0.jpg" } | Select-Object -First 1
    if (-not $src) { throw 'no \lolssd\wallpaper on any drive' }
    $web = "$env:SystemRoot\Web"
    $map = @{ "$src\img0.jpg" = "$web\Wallpaper\Windows\img0.jpg"; "$src\img100.jpg" = "$web\Screen\img100.jpg" }
    foreach ($f in Get-ChildItem "$src\img0_*.jpg") { $map[$f.FullName] = "$web\4K\Wallpaper\Windows\$($f.Name)" }
    foreach ($from in $map.Keys) {
        $to = $map[$from]
        if (Test-Path $to) {
            Run takeown.exe /F $to /A
            Run icacls.exe $to /grant '*S-1-5-32-544:F'
        }
        Copy-Item $from $to -Force
    }
}

# tiny11 deletes these task files; disabling keeps the task store consistent.
Step 'telemetry tasks off' {
    foreach ($t in '\Microsoft\Windows\Application Experience\Microsoft Compatibility Appraiser',
        '\Microsoft\Windows\Application Experience\ProgramDataUpdater',
        '\Microsoft\Windows\Customer Experience Improvement Program\Consolidator',
        '\Microsoft\Windows\Customer Experience Improvement Program\UsbCeip',
        '\Microsoft\Windows\Windows Error Reporting\QueueReporting') {
        $i = $t.LastIndexOf('\')
        Get-ScheduledTask -TaskPath $t.Substring(0, $i + 1) -TaskName $t.Substring($i + 1) -ErrorAction SilentlyContinue |
            Disable-ScheduledTask | Out-Null
    }
}

Log 'done'
