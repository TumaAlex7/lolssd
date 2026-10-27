@echo off
rem Windows runs this as SYSTEM after setup, before the first logon. lolssd-specialize.ps1 put it
rem here; our scripts live in lolssd\ next to it.
powershell.exe -NoProfile -ExecutionPolicy Bypass -File "%~dp0lolssd\lolssd-setup.ps1"
