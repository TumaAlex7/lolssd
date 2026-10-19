@echo off
rem Windows runs this as SYSTEM after setup, before the first logon (again after sysprep).
powershell.exe -NoProfile -ExecutionPolicy Bypass -File "%~dp0lolssd-setup.ps1" -Phase Complete
