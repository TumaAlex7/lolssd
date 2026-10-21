#!/usr/bin/env bash
# lolssd: собрать установочный ISO Windows со всеми артефактами проекта внутри.
#   build/iso.sh Windows.iso [папка с INF-драйверами] [редакция, по умолчанию «Windows 11 Home»]
# Обычно через make iso ISO=... [DRIVERS=...] [EDITION=...]. Root не нужен.
#
# Что получается (dist/iso/lolssd-<имя исходного ISO>):
#   - в install.wim только одна редакция: установщик не спрашивает какую;
#   - из образа удалены Edge и OneDrive, lolssd.reg применён к его реестру (build/debloat.sh);
#     приложения и компоненты Windows удаляет DISM в виртуалке установки (lolssd-setup.ps1);
#   - наши файлы лежат прямо в образе Windows, установщик ничего не докачивает и не копирует:
#       \Windows\Setup\Scripts\   SetupComplete.cmd, lolssd-setup.ps1, lolssd.reg
#       \Program Files\lolssd\    lolssd-win.exe, lolssd-drivers.ps1, vncviewer.exe (TigerVNC)
#       \ProgramData\lolssd\wsl\  пусковой WSL-дистрибутив и MSI WSL (если собраны make wsl)
#       \Drivers\                 INF-драйверы (если указаны)
#   - autounattend.xml в корне ISO (установщик ищет его там);
#   - образ больше 4 ГБ режется на install.swm: ISO 9660 не хранит файлы больше 4 ГБ, а
#     установщик Windows ставит из частей .swm сам;
#   - загрузка без «Press any key to boot from CD» (efisys_noprompt.bin).
set -euo pipefail

die() { echo "ошибка: $*" >&2; exit 1; }

[[ $# -ge 1 && $# -le 3 ]] || die "использование: $0 Windows.iso [драйверы] [редакция]"
src=$(realpath "$1")
drivers=${2:-}
edition=${3:-Windows 11 Home}
repo=$(cd "$(dirname "$0")/.." && pwd)
dist=$repo/dist
out=$dist/iso/lolssd-$(basename "$src")
work=$dist/iso/work

for t in 7z wimlib-imagex xorriso hivexregedit curl; do
	command -v "$t" >/dev/null || die "нет $t (sudo pacman -S p7zip wimlib libisoburn hivex)"
done
[[ -f $src ]] || die "нет $src"
[[ -d $dist/windows ]] || die "сначала make dist"
[[ -z $drivers || -d $drivers ]] || die "нет папки с драйверами $drivers"

rm -rf "$work"
mkdir -p "$work/tree"
trap 'rm -rf "$work"' EXIT

echo "1/6 распаковка ISO"
7z x -y "$src" -o"$work/tree" -x'!sources/install.wim' -x'!sources/install.esd' >/dev/null
7z e -y "$src" -o"$work" sources/install.wim >/dev/null || die "в ISO нет sources/install.wim"

echo "2/6 редакция «$edition»"
wimlib-imagex info "$work/install.wim" "$edition" >/dev/null 2>&1 ||
	die "в образе нет редакции «$edition»; есть: $(wimlib-imagex info "$work/install.wim" | sed -n 's/^Name: *//p' | paste -sd ';')"
wimlib-imagex export "$work/install.wim" "$edition" "$work/tree/sources/install.wim" >/dev/null
rm "$work/install.wim"

echo "3/6 чистка образа: Edge, OneDrive, lolssd.reg в реестр образа"
"$repo/build/debloat.sh" "$work/tree/sources/install.wim" 1 "$dist/windows/lolssd.reg"

echo "4/6 файлы lolssd в образ"
# Просмотрщик для рабочего стола Linux (lolssd-win desktop): TigerVNC для Windows, один exe.
# Скачивается один раз на версию.
vnc=${TIGERVNC:-1.16.2}
viewer=$dist/cache/vncviewer64-$vnc.exe
if [[ ! -s $viewer ]]; then
	mkdir -p "$dist/cache"
	curl -fSL --retry 3 -o "$viewer.part" \
		"https://sourceforge.net/projects/tigervnc/files/stable/$vnc/vncviewer64-$vnc.exe/download" &&
		mv "$viewer.part" "$viewer" ||
		echo "  внимание: TigerVNC $vnc не скачан — в образе не будет рабочего стола Linux" >&2
fi
{
	echo "add '$dist/windows/C/Windows/Setup/Scripts' '/Windows/Setup/Scripts'"
	echo "add '$dist/windows/lolssd.reg' '/Windows/Setup/Scripts/lolssd.reg'"
	echo "add '$dist/windows/C/Program Files/lolssd' '/Program Files/lolssd'"
	[[ -s $viewer ]] && echo "add '$viewer' '/Program Files/lolssd/vncviewer.exe'"
	compgen -G "$dist/wsl/*.msi" >/dev/null ||
		echo "  (нет MSI WSL в dist/wsl — WSL в образе не будет; make wsl докачает)" >&2
	if [[ -f $dist/wsl/lolssd.tar ]]; then
		# По файлу: недокачанный MSI (*.part) в образ не попадает.
		for f in "$dist"/wsl/lolssd.tar "$dist"/wsl/*.msi; do
			[[ -f $f ]] && echo "add '$f' '/ProgramData/lolssd/wsl/$(basename "$f")'"
		done
	else
		echo "  (нет dist/wsl/lolssd.tar — без пускового WSL-дистрибутива; make wsl)" >&2
	fi
	[[ -z $drivers ]] || echo "add '$(realpath "$drivers")' '/Drivers'"
} | wimlib-imagex update "$work/tree/sources/install.wim" 1 >/dev/null

echo "5/6 разбиение образа"
# update дописывает данные в конец файла; optimize собирает его заново без удалённого.
wimlib-imagex optimize "$work/tree/sources/install.wim" >/dev/null
size=$(stat -c %s "$work/tree/sources/install.wim")
if (( size > 4000 * 1024 * 1024 )); then
	wimlib-imagex split "$work/tree/sources/install.wim" "$work/tree/sources/install.swm" 3800 >/dev/null
	rm "$work/tree/sources/install.wim"
fi

cp "$repo/build/prototype/autounattend.xml" "$work/tree/autounattend.xml"

echo "6/6 сборка ISO"
mkdir -p "$(dirname "$out")"
xorriso -as mkisofs -quiet -iso-level 3 -full-iso9660-filenames -J -joliet-long -V LOLSSD_WIN \
	-b boot/etfsboot.com -no-emul-boot -boot-load-size 8 \
	-eltorito-alt-boot -e efi/microsoft/boot/efisys_noprompt.bin -no-emul-boot \
	-o "$out.tmp" "$work/tree"
mv "$out.tmp" "$out"
echo "готово: $out ($(du -h "$out" | cut -f1))"
