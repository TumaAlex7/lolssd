@echo off
rem lolssd: WinPE for the tiny11-style image cleanup (build/tiny.sh). Finds the payload CD and
rem runs tiny.cmd from it; tiny.cmd shuts the VM down when done.
wpeinit
for %%d in (C D E F G H I J K L M N O P Q R S T U V) do if exist %%d:\lolssd-tiny.txt call %%d:\tiny.cmd %%d:
wpeutil shutdown
