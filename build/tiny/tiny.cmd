@echo off
rem lolssd: tiny11-style cleanup of install.wim with DISM, inside WinPE in QEMU (build/tiny.sh).
rem %1 = drive with the payload: this script, install*.swm (one edition),
rem remove-packages.txt, lolssd-offline.reg. The result and the log go to disk 0 (blank),
rem which build/tiny.sh reads back: \clean.wim, \tiny.log, \done.txt.
rem English only: cmd reads scripts in the OEM code page.
setlocal enabledelayedexpansion
set IN=%~1
set M=W:\mount

(echo select disk 0
 echo clean
 echo create partition primary
 echo format fs=ntfs quick label=WORK
 echo assign letter=W) > X:\diskpart.txt
diskpart /s X:\diskpart.txt > X:\diskpart.log
if not exist W:\ (type X:\diskpart.log & goto :eof)
set LOG=W:\tiny.log
echo lolssd tiny: %date% %time% > %LOG%
rem DISM keeps temporary files on X:, the WinPE RAM disk of a few tens of MB: without a scratch
rem directory on the work disk it stops with error 112 (disk full) halfway through an export.
mkdir W:\scratch
set DISM=dism /English /ScratchDir:W:\scratch /LogPath:W:\dism.log

call :run %DISM% /Export-Image /SourceImageFile:%IN%\install.swm /SWMFile:%IN%\install*.swm /SourceIndex:1 /DestinationImageFile:W:\install.wim /Compress:fast || goto fail
mkdir %M%
call :run %DISM% /Mount-Image /ImageFile:W:\install.wim /Index:1 /MountDir:%M% || goto fail

rem Store apps are not removed here: WinPE's DISM has no AppX provider (AppxProvider.dll), only
rem CBS. They are removed by DISM in the installing VM, in full Windows (lolssd-setup.ps1).

rem Windows packages from the remove-list (tiny11's set: IE, WordPad, Media Player, handwriting...).
%DISM% /Image:%M% /Get-Packages > W:\packages.txt
echo. >> %LOG%
echo packages in the image: >> %LOG%
findstr /c:"Package Identity" W:\packages.txt >> %LOG%
for /f "tokens=3 delims=: " %%p in ('findstr /c:"Package Identity" W:\packages.txt') do (
    echo %%p| findstr /i /g:%IN%\remove-packages.txt >nul
    if not errorlevel 1 call :run %DISM% /Image:%M% /Remove-Package /PackageName:%%p
)

rem Edge (WebView2 stays) and OneDrive.
for %%d in ("Program Files (x86)\Microsoft\Edge" "Program Files (x86)\Microsoft\EdgeUpdate" "Program Files (x86)\Microsoft\EdgeCore") do (
    if exist "%M%\%%~d" (
        takeown /f "%M%\%%~d" /r /d y >nul 2>&1
        icacls "%M%\%%~d" /grant *S-1-5-32-544:F /t /c /q >nul 2>&1
        rd /s /q "%M%\%%~d"
        if exist "%M%\%%~d" (echo FAIL  remove %%~d >> %LOG%) else (echo ok    remove %%~d >> %LOG%)
    )
)
for %%f in ("ProgramData\Microsoft\Windows\Start Menu\Programs\Microsoft Edge.lnk" "Windows\System32\OneDriveSetup.exe") do (
    if exist "%M%\%%~f" (
        takeown /f "%M%\%%~f" >nul 2>&1
        icacls "%M%\%%~f" /grant *S-1-5-32-544:F /c /q >nul 2>&1
        del /f /q "%M%\%%~f"
        if exist "%M%\%%~f" (echo FAIL  remove %%~f >> %LOG%) else (echo ok    remove %%~f >> %LOG%)
    )
)

rem Registry of the image: lolssd.reg (paths mapped to these hives by build/tiny.sh), Edge
rem update services, OneDrive autostart for new users.
call :run reg load HKLM\zSYSTEM %M%\Windows\System32\config\SYSTEM || goto fail
call :run reg load HKLM\zSOFTWARE %M%\Windows\System32\config\SOFTWARE || goto fail
call :run reg load HKLM\zDEFAULT %M%\Users\Default\NTUSER.DAT || goto fail
call :run reg import %IN%\lolssd-offline.reg
for %%s in (edgeupdate edgeupdatem MicrosoftEdgeElevationService) do reg delete HKLM\zSYSTEM\ControlSet001\Services\%%s /f >nul 2>&1
reg delete "HKLM\zDEFAULT\Software\Microsoft\Windows\CurrentVersion\Run" /v OneDriveSetup /f >nul 2>&1
call :run reg unload HKLM\zDEFAULT
call :run reg unload HKLM\zSOFTWARE
call :run reg unload HKLM\zSYSTEM

call :run %DISM% /Unmount-Image /MountDir:%M% /Commit || goto fail
rem Export rebuilds the image without the removed data.
call :run %DISM% /Export-Image /SourceImageFile:W:\install.wim /SourceIndex:1 /DestinationImageFile:W:\clean.wim /Compress:max || goto fail
del W:\install.wim
echo done > W:\done.txt
echo lolssd tiny: done %date% %time% >> %LOG%
goto :eof

:fail
echo FAILED, see the lines above >> %LOG%
%DISM% /Unmount-Image /MountDir:%M% /Discard >nul 2>&1
goto :eof

:run
echo. >> %LOG%
echo ^> %* >> %LOG%
%* >> %LOG% 2>&1
exit /b %errorlevel%
