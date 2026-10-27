# lolssd: the specialize pass of setup (before OOBE, as SYSTEM, no network yet). The answer file
# copies our files from the config disk to C:\Windows\Setup\Scripts\lolssd and runs this:
#   - our programs to C:\Program Files\lolssd, SetupComplete.cmd where setup looks for it;
#   - registry settings (lolssd*.reg by the build options);
#   - by the options: Edge, OneDrive and the accessibility programs off the system -- they are
#     files guarded by Windows' own ACLs, hence taking ownership first.
# The rest, with network, runs from SetupComplete.cmd (lolssd-setup.ps1).
# English only: Windows PowerShell 5.1 misreads non-ASCII in BOM-less files.

$log = "$env:SystemRoot\Setup\Scripts\lolssd-setup.log"
. "$PSScriptRoot\lolssd-common.ps1"
Log "specialize; options: $(($options.GetEnumerator() | Where-Object Name -ne 'apps' | ForEach-Object { "$($_.Name)=$($_.Value)" }) -join ' ')"

Step 'files' {
    New-Item -ItemType Directory -Force $prog | Out-Null
    foreach ($f in 'lolssd-win.exe', 'lolssd-drivers.ps1', 'lolssd-gpu.ps1', 'lolssd-riot.ps1', 'linux.ico', 'terminal.ico') {
        Copy-Item "$payload\$f" $prog -Force
    }
    Copy-Item "$payload\SetupComplete.cmd" "$env:SystemRoot\Setup\Scripts\" -Force
    # lolssd-win reads the options from here.
    Copy-Item "$payload\options.txt" "$env:SystemRoot\Setup\Scripts\lolssd-options.txt" -Force
}

# Registry. noupdate last: its services are reg-importable, WaaSMedicSvc is not (below).
Step 'registry' {
    $files = @('lolssd.reg')
    if ($options['placement'] -eq 'external') { $files += 'lolssd-external.reg' }
    if (Opt onedrive) { $files += 'lolssd-noonedrive.reg' }
    if (Opt edge) { $files += 'lolssd-noedge.reg' }
    if (-not (Opt updates)) { $files += 'lolssd-noupdate.reg' }
    foreach ($f in $files) { Run reg.exe import "$payload\$f" }
    # No Microsoft account in OOBE: a local account is created from the answer file.
    Set-Reg 'HKLM:\SOFTWARE\Microsoft\Windows\CurrentVersion\OOBE' 'BypassNRO' 1
}

# WaaSMedicSvc turns Windows Update back on. Its key is ACL-protected even from SYSTEM: take
# ownership for Administrators (needs SeTakeOwnershipPrivilege enabled in the token), grant them
# full control, then set Start=4.
if (-not (Opt updates)) { Step 'WaaSMedicSvc off' {
    Add-Type @'
using System;
using System.Runtime.InteropServices;
public static class LolssdPriv {
    [StructLayout(LayoutKind.Sequential, Pack = 1)]
    struct TokPriv { public int Count; public long Luid; public int Attr; }
    [DllImport("advapi32.dll", SetLastError = true)]
    static extern bool OpenProcessToken(IntPtr process, int access, out IntPtr token);
    [DllImport("advapi32.dll", SetLastError = true)]
    static extern bool LookupPrivilegeValue(string system, string name, out long luid);
    [DllImport("advapi32.dll", SetLastError = true)]
    static extern bool AdjustTokenPrivileges(IntPtr token, bool disableAll, ref TokPriv state, int len, IntPtr prev, IntPtr retLen);
    [DllImport("kernel32.dll")]
    static extern IntPtr GetCurrentProcess();
    public static bool Enable(string name) {
        IntPtr token;
        TokPriv tp;
        tp.Count = 1; tp.Attr = 2; // SE_PRIVILEGE_ENABLED
        return OpenProcessToken(GetCurrentProcess(), 0x28, out token) // ADJUST_PRIVILEGES | QUERY
            && LookupPrivilegeValue(null, name, out tp.Luid)
            && AdjustTokenPrivileges(token, false, ref tp, 0, IntPtr.Zero, IntPtr.Zero);
    }
}
'@
    foreach ($p in 'SeTakeOwnershipPrivilege', 'SeRestorePrivilege') {
        if (-not [LolssdPriv]::Enable($p)) { throw "cannot enable $p" }
    }
    $path = 'SYSTEM\CurrentControlSet\Services\WaaSMedicSvc'
    $hklm = [Microsoft.Win32.Registry]::LocalMachine
    $tree = [Microsoft.Win32.RegistryKeyPermissionCheck]::ReadWriteSubTree
    $rights = [System.Security.AccessControl.RegistryRights]
    $admins = New-Object System.Security.Principal.SecurityIdentifier 'S-1-5-32-544'
    $k = $hklm.OpenSubKey($path, $tree, $rights::TakeOwnership)
    $acl = $k.GetAccessControl([System.Security.AccessControl.AccessControlSections]::None)
    $acl.SetOwner($admins)
    $k.SetAccessControl($acl)
    $k.Close()
    $k = $hklm.OpenSubKey($path, $tree, $rights::ChangePermissions)
    $acl = $k.GetAccessControl()
    $acl.AddAccessRule((New-Object System.Security.AccessControl.RegistryAccessRule($admins, 'FullControl', 'ContainerInherit', 'None', 'Allow')))
    $k.SetAccessControl($acl)
    $k.Close()
    $k = $hklm.OpenSubKey($path, $true)
    $k.SetValue('Start', 4, [Microsoft.Win32.RegistryValueKind]::DWord)
    $k.Close()
} }

# Edge: the browser, its updater and its services; WebView2 (EdgeWebView) stays. The stock
# uninstaller refuses outside the EEA, so the files go directly.
if (Opt edge) { Step 'remove Edge' {
    # The updater may be running (its service, or a task): stop it and wait, or its DLLs stay
    # locked.
    foreach ($svc in 'edgeupdate', 'edgeupdatem', 'MicrosoftEdgeElevationService') {
        if (Get-Service $svc -ErrorAction SilentlyContinue) {
            Stop-Service $svc -Force -ErrorAction SilentlyContinue
            Run sc.exe delete $svc
        }
    }
    Get-Process 'MicrosoftEdge*', 'msedge*', 'elevation_service' -ErrorAction SilentlyContinue | Stop-Process -Force
    Start-Sleep -Seconds 3
    Get-ScheduledTask -TaskName 'MicrosoftEdgeUpdate*' -ErrorAction SilentlyContinue | Unregister-ScheduledTask -Confirm:$false
    $x86 = ${env:ProgramFiles(x86)}
    foreach ($p in "$x86\Microsoft\Edge", "$x86\Microsoft\EdgeUpdate", "$x86\Microsoft\EdgeCore",
        "$env:ProgramData\Microsoft\Windows\Start Menu\Programs\Microsoft Edge.lnk", "$env:PUBLIC\Desktop\Microsoft Edge.lnk",
        'HKLM:\SOFTWARE\WOW6432Node\Microsoft\Windows\CurrentVersion\Uninstall\Microsoft Edge',
        'HKLM:\SOFTWARE\WOW6432Node\Microsoft\Windows\CurrentVersion\Uninstall\Microsoft Edge Update') {
        Remove-Protected $p
    }
    if (Test-Path "$x86\Microsoft\Edge\Application\msedge.exe") { throw 'msedge.exe is still there' }
} }

if (Opt onedrive) { Step 'remove OneDrive' {
    Remove-Protected "$env:SystemRoot\System32\OneDriveSetup.exe"
    Remove-Protected "$env:SystemRoot\SysWOW64\OneDriveSetup.exe"
} }

# Narrator, Magnifier, On-Screen Keyboard, Live Captions, Voice Access: the programs and their
# Start menu folder. The touch keyboard (TabTip) and utilman stay.
if (Opt accessibility) { Step 'remove accessibility programs' {
    foreach ($f in 'Narrator.exe', 'NarratorControlTemplates.xml', 'NarratorControlTemplates.xsd', 'NarratorMCAT', 'Magnify.exe', 'osk.exe', 'LiveCaptions.exe', 'VoiceAccess.exe') {
        Remove-Protected "$env:SystemRoot\System32\$f"
    }
    Remove-Protected "$env:SystemRoot\SysWOW64\Magnify.exe"
    Remove-Protected "$env:ProgramData\Microsoft\Windows\Start Menu\Programs\Accessibility"
    Remove-Protected "$env:SystemDrive\Users\Default\AppData\Roaming\Microsoft\Windows\Start Menu\Programs\Accessibility"
} }

# The default profile: OneDrive installs itself per user from its Run key; Win+Ctrl+Enter starts
# Narrator.
if ((Opt onedrive) -or (Opt accessibility)) { Step 'default user profile' {
    Run reg.exe load 'HKU\lolssdDefault' "$env:SystemDrive\Users\Default\NTUSER.DAT"
    try {
        if (Opt onedrive) {
            Remove-ItemProperty 'Registry::HKU\lolssdDefault\Software\Microsoft\Windows\CurrentVersion\Run' -Name OneDriveSetup -ErrorAction SilentlyContinue
        }
        if (Opt accessibility) { Set-Reg 'Registry::HKU\lolssdDefault\Software\Microsoft\Narrator\NoRoam' 'WinEnterLaunchEnabled' 0 }
    } finally {
        [gc]::Collect()
        Run reg.exe unload 'HKU\lolssdDefault'
    }
} }

Log "specialize done, $failed step(s) failed"
exit 0
