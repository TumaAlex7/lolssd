@echo off
rem lolssd prototype: put lolssd files in place and configure Windows. Run as administrator
rem (autounattend runs it at the first logon). Lives in \windows\ on the LOLSSD partition.
setlocal
set SRC=%~dp0
net session >nul 2>&1 || (echo Run this as administrator: right click, "Run as administrator". & pause & exit /b 1)

xcopy /e /i /y "%SRC%C\Program Files\lolssd" "%ProgramFiles%\lolssd" >nul
xcopy /e /i /y "%SRC%C\Windows\Setup\Scripts" "%SystemRoot%\Setup\Scripts" >nul

rem Fallback boot path: the real machine has no NVRAM entry from this install (it went to the
rem VM's NVRAM), and lolssd boot / the firmware boot menu use \EFI\BOOT\BOOTX64.EFI.
mountvol S: /S
if not exist S:\EFI\BOOT\BOOTX64.EFI (
    mkdir S:\EFI\BOOT 2>nul
    copy /y S:\EFI\Microsoft\Boot\bootmgfw.efi S:\EFI\BOOT\BOOTX64.EFI >nul
)
if exist S:\EFI\BOOT\BOOTX64.EFI (echo Fallback boot path: OK) else (echo Fallback boot path: MISSING)
mountvol S: /D

rem WaaSMedicSvc is the last key of lolssd.reg and is expected to fail here (ACL).
reg import "%SRC%lolssd.reg"

powershell -NoProfile -ExecutionPolicy Bypass -File "%SystemRoot%\Setup\Scripts\lolssd-setup.ps1"
echo.
type "%SystemRoot%\Setup\Scripts\lolssd-setup.log"
echo.
echo Done. Shut down this VM, then boot the SSD on the real machine (sudo lolssd boot).
pause
