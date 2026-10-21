#!/usr/bin/env bash
# lolssd: почистить образ Windows офлайн, из Linux: wimlib (файлы образа) и hivex (реестр
# образа). Вызывается из build/iso.sh:
#   build/debloat.sh install.wim <номер образа> lolssd.reg
#
#   - lolssd.reg применяется к кустам SYSTEM и SOFTWARE (CurrentControlSet → ControlSet001);
#   - Edge (без WebView2) и OneDrive: файлы, ярлык, службы обновления, автозапуск у новых
#     пользователей.
# Приложения магазина здесь не трогаются: их удаляет lolssd-setup.ps1 через DISM
# (Remove-AppxProvisionedPackage) в виртуальной машине при установке, до первого входа. Вручную
# из Linux не обновить внутреннюю базу пакетов Windows (StateRepository), а DISM делает это сам.
set -euo pipefail

die() { echo "ошибка: $*" >&2; exit 1; }

wim=$1 idx=$2 reg=$3
for t in wimlib-imagex hivexregedit; do
	command -v "$t" >/dev/null || die "нет $t (sudo pacman -S wimlib hivex)"
done

w=$(mktemp -d)
trap 'rm -rf "$w"' EXIT
sys=Windows/System32/config/SYSTEM sw=Windows/System32/config/SOFTWARE def=Users/Default/NTUSER.DAT
wimlib-imagex extract "$wim" "$idx" "/$sys" "/$sw" "/$def" --dest-dir="$w/h" --no-acls >/dev/null

hklm='HKEY_LOCAL_MACHINE'
cmds=$w/wim-commands
: >"$cmds"

# lolssd.reg → два файла по кустам.
awk -v sys="$w/system.reg" -v sw="$w/software.reg" '
	BEGIN { print "Windows Registry Editor Version 5.00\n" > sys; print "Windows Registry Editor Version 5.00\n" > sw }
	/^\[HKEY_LOCAL_MACHINE\\SYSTEM\\/ { out = sys; sub(/\\CurrentControlSet\\/, "\\ControlSet001\\") }
	/^\[HKEY_LOCAL_MACHINE\\SOFTWARE\\/ { out = sw }
	/^\[/ || /^"/ { if (out) print > out }
	/^$/ { if (out) print "" > out }
' <(tr -d '\r' <"$reg")

# Edge: службы обновления и сам браузер; WebView2 (Microsoft\EdgeWebView) остаётся.
for svc in edgeupdate edgeupdatem MicrosoftEdgeElevationService; do
	printf '[-%s\\SYSTEM\\ControlSet001\\Services\\%s]\n\n' "$hklm" "$svc" >>"$w/system.reg"
done
for p in 'Program Files (x86)/Microsoft/Edge' 'Program Files (x86)/Microsoft/EdgeUpdate' 'Program Files (x86)/Microsoft/EdgeCore' \
	'ProgramData/Microsoft/Windows/Start Menu/Programs/Microsoft Edge.lnk' 'Windows/System32/OneDriveSetup.exe'; do
	echo "delete --force --recursive '/$p'" >>"$cmds"
done

# OneDrive ставится каждому новому пользователю из Run профиля по умолчанию.
printf 'Windows Registry Editor Version 5.00\n\n[HKEY_CURRENT_USER\\Software\\Microsoft\\Windows\\CurrentVersion\\Run]\n"OneDriveSetup"=-\n' >"$w/default.reg"

hivexregedit --merge --prefix "$hklm\\SYSTEM" "$w/h/$sys" "$w/system.reg"
hivexregedit --merge --prefix "$hklm\\SOFTWARE" "$w/h/$sw" "$w/software.reg"
hivexregedit --merge --prefix 'HKEY_CURRENT_USER' "$w/h/$def" "$w/default.reg"

for f in "$sys" "$sw" "$def"; do
	echo "add '$w/h/$f' '/$f'" >>"$cmds"
done
wimlib-imagex update "$wim" "$idx" <"$cmds" >/dev/null
