# lolssd: add "Linux inside Windows" to a Windows that is already installed (for example, one put
# next to Linux by hand), without reinstalling it. build/attach.sh copies our files to
# C:\lolssd-setup from Linux; run attach.cmd there as administrator. It:
#   - makes the LOLSSD partition if there is none: 512 MB off the end of C: (Windows shrinks its
#     own NTFS), exFAT, and puts the Linux tool on it;
#   - runs lolssd-setup.ps1 with options that only add what the VM needs (our programs, QEMU,
#     TAP, TigerVNC, the Windows Hypervisor Platform, tasks, the VM shutdown service): no
#     removals, Windows Update and the registry stay as they are.
# A reboot is needed afterwards (the hypervisor platform).
# English only: Windows PowerShell 5.1 misreads non-ASCII in BOM-less files.

if (-not ([Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole('Administrators')) {
    Start-Process powershell.exe -Verb RunAs -ArgumentList '-NoProfile', '-ExecutionPolicy', 'Bypass', '-File', "`"$PSCommandPath`""
    exit
}
$ErrorActionPreference = 'Stop'
$src = $PSScriptRoot
$scripts = "$env:SystemRoot\Setup\Scripts\lolssd"
New-Item -ItemType Directory -Force $scripts | Out-Null
Copy-Item "$src\*" $scripts -Recurse -Force -Exclude 'LOLSSD', 'attach.cmd'

$log = "$env:SystemRoot\Setup\Scripts\lolssd-setup.log"
. "$scripts\lolssd-common.ps1"
Write-Host 'lolssd: setting up Linux inside Windows (log: C:\Windows\Setup\Scripts\lolssd-setup.log)'
Log 'attach'

Step 'files' {
    New-Item -ItemType Directory -Force $prog | Out-Null
    foreach ($f in 'lolssd-win.exe', 'lolssd-drivers.ps1', 'lolssd-gpu.ps1', 'lolssd-riot.ps1', 'linux.ico', 'terminal.ico') {
        Copy-Item "$payload\$f" $prog -Force
    }
    Copy-Item "$payload\options.txt" "$env:SystemRoot\Setup\Scripts\lolssd-options.txt" -Force
}

Step 'LOLSSD partition' {
    $vol = Get-Volume | Where-Object FileSystemLabel -eq 'LOLSSD' | Select-Object -First 1
    if (-not $vol) {
        Write-Host 'making LOLSSD: 512 MB off the end of C: ...'
        $c = Get-Partition -DriveLetter $env:SystemDrive[0]
        $size = $c.Size - 512MB
        if ($size -lt (Get-PartitionSupportedSize -DriveLetter $env:SystemDrive[0]).SizeMin) { throw 'C: cannot shrink by 512 MB' }
        Resize-Partition -DriveLetter $env:SystemDrive[0] -Size $size
        $p = New-Partition -DiskNumber $c.DiskNumber -Offset ($c.Offset + $size) -Size 512MB `
            -GptType '{ebd0a0a2-b9e5-4433-87c0-68b6b72699c7}' -AssignDriveLetter
        $vol = $p | Format-Volume -FileSystem exFAT -NewFileSystemLabel LOLSSD -Confirm:$false
    }
    $root = "$($vol.DriveLetter):\"
    New-Item -ItemType Directory -Force "$root\profiles", "$root\drivers" | Out-Null
    Copy-Item "$src\LOLSSD\*" $root -Recurse -Force
    Log "      LOLSSD is $root"
}

Write-Host 'downloading QEMU, TAP, TigerVNC and the GPU installers, registering tasks (a few minutes) ...'
& "$scripts\lolssd-setup.ps1"
$rc = $LASTEXITCODE
Get-Content $log -Tail 40 | Where-Object { $_ -match 'FAIL|done' } | ForEach-Object { Write-Host $_ }
if ($rc -eq 0 -and $failed -eq 0) {
    Write-Host 'Done. Restart Windows once (the hypervisor platform), then prepare Linux in the lolssd menu on Linux.'
} else {
    Write-Host 'Some steps failed, see the log above.'
}
Read-Host 'Enter to close'
