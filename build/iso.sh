#!/usr/bin/env bash
# lolssd: собрать установочный ISO Windows со всеми артефактами проекта внутри.
#   build/iso.sh Windows.iso [файл опций, по умолчанию build/options.default]
# Обычно его вызывает мастер (build/lolssd-setup) или make iso ISO=... [OPTIONS=...]. Root не нужен.
# Печатает путь к готовому ISO последней строкой; тот же ISO с теми же опциями собирается один раз.
#
# Что получается (dist/iso/lolssd-<опции>-<имя исходного ISO>):
#   - все редакции из исходного ISO: язык, редакцию и ключ спрашивает установщик Windows;
#   - в каждой редакции: настройки реестра (lolssd.reg и по опциям lolssd-*.reg), по опциям — без
#     Edge, OneDrive и специальных возможностей (build/debloat.sh); обои и экран блокировки —
#     WALLPAPER (по умолчанию арт K/DA FIRESTARTER из релиза assets) или запасные из build/art;
#     приложения и компоненты Windows по опциям удаляет DISM в ВМ установки (lolssd-setup.ps1);
#   - наши файлы прямо в образе, установщик ничего не докачивает:
#       \Windows\Setup\Scripts\   SetupComplete.cmd, lolssd-setup.ps1, lolssd.reg,
#                                 lolssd-options.txt (файл опций)
#       \Program Files\lolssd\    lolssd-win.exe, lolssd-drivers.ps1, lolssd-riot.ps1;
#                                 с vm=1: vncviewer.exe (TigerVNC), значки ярлыков Linux,
#                                 qemu\ (make qemu), tap\ (make tap); с sunshine=1: sunshine.msi
#   - файла ответов в ISO нет: его собирает build/install.sh, он знает раздел для Windows;
#   - образ больше 4 ГБ режется на install.swm: ISO 9660 не хранит файлы больше 4 ГБ;
#   - загрузка без «Press any key to boot from CD» (efisys_noprompt.bin).
# Драйверы в образ не кладутся: install.sh и lolssd drivers кладут их на раздел LOLSSD.
set -euo pipefail

die() { echo "ошибка: $*" >&2; exit 1; }

[[ $# -ge 1 && $# -le 2 ]] || die "использование: $0 Windows.iso [файл опций]"
src=$(realpath "$1")
repo=$(cd "$(dirname "$0")/.." && pwd)
opts=$(realpath "${2:-$repo/build/options.default}")
dist=$repo/dist
[[ -f $src ]] || die "нет $src"
[[ -f $opts ]] || die "нет файла опций $opts"
opt() { sed -n "s/^$1=//p" "$opts" | tail -1; }
for k in placement vm updates edge onedrive packages accessibility riot sunshine; do
	[[ -n $(opt $k) ]] || die "в $opts нет $k="
done

# Имя ISO — по опциям без комментариев: с теми же опциями ISO не собирается заново.
tag=$(grep -v '^#' "$opts" | grep . | sort | sha256sum | cut -c1-8)
out=$dist/iso/lolssd-$tag-$(basename "$src")
if [[ -f $out && -z ${REBUILD:-} ]]; then
	echo "ISO с этими опциями уже собран (REBUILD=1 — собрать заново)"
	echo "$out"
	exit 0
fi
work=$dist/iso/work-$tag

for t in 7z wimlib-imagex xorriso hivexregedit curl; do
	command -v "$t" >/dev/null || die "нет $t (sudo pacman -S p7zip wimlib libisoburn hivex)"
done
[[ -d $dist/windows ]] || die "сначала make dist"
if [[ $(opt vm) == 1 ]]; then
	[[ -f $dist/qemu/qemu-system-x86_64w.exe ]] || die "нет dist/qemu (make qemu)"
	[[ -f $dist/tap/OemVista.inf ]] || die "нет dist/tap (make tap)"
fi

rm -rf "$work"
mkdir -p "$work/tree"
trap 'rm -rf "$work"' EXIT

echo "1/6 распаковка ISO"
7z x -y "$src" -o"$work/tree" -x'!sources/install.wim' -x'!sources/install.esd' >/dev/null
if 7z l "$src" sources/install.wim | grep -q install.wim; then
	7z e -y "$src" -o"$work/tree/sources" sources/install.wim >/dev/null
else
	# Образ в ESD (сжатие LZMS) пересжимается в WIM: wimlib update его не правит.
	7z e -y "$src" -o"$work" sources/install.esd >/dev/null || die "в ISO нет sources/install.wim и install.esd"
	wimlib-imagex export "$work/install.esd" all "$work/tree/sources/install.wim" --compress=LZX >/dev/null
	rm "$work/install.esd"
fi
wim=$work/tree/sources/install.wim
count=$(wimlib-imagex info "$wim" | sed -n 's/^Image Count: *//p')
echo "2/6 редакций в образе: $count ($(wimlib-imagex info "$wim" | sed -n 's/^Name: *//p' | paste -sd ';'))"

# Реестр: общие настройки и по опциям. noupdate — последним: в нём WaaSMedicSvc, который
# reg import на живой системе не меняет, и импорт на нём останавливается.
reg=$work/lolssd.reg
{
	cat "$dist/windows/lolssd.reg"
	[[ $(opt placement) != external ]] || sed 1d "$dist/windows/lolssd-external.reg"
	[[ $(opt onedrive) != 1 ]] || sed 1d "$dist/windows/lolssd-noonedrive.reg"
	[[ $(opt edge) != 1 ]] || sed 1d "$dist/windows/lolssd-noedge.reg"
	[[ $(opt updates) == 1 ]] || sed 1d "$dist/windows/lolssd-noupdate.reg"
} >"$reg"

# Просмотрщик для рабочего стола Linux (lolssd-win desktop): TigerVNC для Windows, один exe.
# Скачивается один раз на версию.
vnc=${TIGERVNC:-1.16.2}
viewer=$dist/cache/vncviewer64-$vnc.exe
mkdir -p "$dist/cache"
if [[ $(opt vm) == 1 && ! -s $viewer ]]; then
	curl -fSL --retry 3 -o "$viewer.part" \
		"https://sourceforge.net/projects/tigervnc/files/stable/$vnc/vncviewer64-$vnc.exe/download" &&
		mv "$viewer.part" "$viewer" || die "TigerVNC $vnc не скачан"
fi
# Sunshine: MSI с GitHub LizardByte, один раз на версию.
sv=${SUNSHINE:-v2026.914.233613}
sunshine=$dist/cache/Sunshine-$sv-AMD64-installer.msi
if [[ $(opt sunshine) == 1 && ! -s $sunshine ]]; then
	curl -fSL --retry 3 -o "$sunshine.part" \
		"https://github.com/LizardByte/Sunshine/releases/download/$sv/Sunshine-Windows-AMD64-installer.msi" &&
		mv "$sunshine.part" "$sunshine" || die "Sunshine $sv не скачан"
fi
# Обои: картинка по ссылке или файл из WALLPAPER (по умолчанию — сплэш-арт скинов K/DA
# FIRESTARTER в 4K из релиза assets; в исходниках его нет — это арт Riot), подогнанная под
# разрешения. Не скачалась или нет ImageMagick — свои обои из build/art.
wallsrc=${WALLPAPER:-https://github.com/TumaAlex7/lolssd/releases/download/assets/kda-firestarter-4k.jpg}
wall=$repo/build/art
if command -v magick >/dev/null; then
	pic=$wallsrc
	if [[ ! -f $pic ]]; then
		pic=$dist/cache/wallpaper-$(printf %s "$wallsrc" | sha256sum | cut -c1-12).jpg
		[[ -s $pic ]] || curl -fsSL --retry 3 -o "$pic" "$wallsrc" || rm -f "$pic"
	fi
	if [[ -s $pic ]]; then
		wall=$work/wall
		mkdir -p "$wall"
		magick "$pic" -filter Lanczos -resize 3840x2160^ -gravity center -extent 3840x2160 -quality 92 "$wall/img0.jpg"
		cp "$wall/img0.jpg" "$wall/img100.jpg"
		for size in 1920x1080 1920x1200 2560x1440 2560x1600 3840x2160; do
			magick "$pic" -filter Lanczos -resize "$size^" -gravity center -extent "$size" -quality 92 "$wall/img0_$size.jpg"
		done
	else
		echo "  внимание: обои $wallsrc не получены — в образе запасные из build/art" >&2
	fi
else
	echo "  (нет ImageMagick — в образе запасные обои из build/art)" >&2
fi

cmds=$work/update-commands
{
	echo "add '$dist/windows/C/Windows/Setup/Scripts' '/Windows/Setup/Scripts'"
	echo "add '$reg' '/Windows/Setup/Scripts/lolssd.reg'"
	echo "add '$opts' '/Windows/Setup/Scripts/lolssd-options.txt'"
	echo "add '$dist/windows/C/Program Files/lolssd' '/Program Files/lolssd'"
	echo "add '$wall/img0.jpg' '/Windows/Web/Wallpaper/Windows/img0.jpg'"
	for f in "$wall"/img0_*.jpg; do echo "add '$f' '/Windows/Web/4K/Wallpaper/Windows/$(basename "$f")'"; done
	echo "add '$wall/img100.jpg' '/Windows/Web/Screen/img100.jpg'"
	if [[ $(opt vm) == 1 ]]; then
		echo "add '$viewer' '/Program Files/lolssd/vncviewer.exe'"
		for i in linux terminal; do echo "add '$repo/build/art/$i.ico' '/Program Files/lolssd/$i.ico'"; done
		echo "add '$dist/qemu' '/Program Files/lolssd/qemu'"
		echo "add '$dist/tap' '/Program Files/lolssd/tap'"
	fi
	[[ $(opt sunshine) != 1 ]] || echo "add '$sunshine' '/Program Files/lolssd/sunshine.msi'"
} >"$cmds"

for i in $(seq 1 "$count"); do
	echo "3/6 редакция $i из $count: реестр и удаление по опциям"
	EDGE=$(opt edge) ONEDRIVE=$(opt onedrive) ACCESSIBILITY=$(opt accessibility) \
		"$repo/build/debloat.sh" "$wim" "$i" "$reg"
	echo "4/6 редакция $i из $count: файлы lolssd"
	wimlib-imagex update "$wim" "$i" <"$cmds" >/dev/null
done

echo "5/6 сжатие и разбиение образа"
# update дописывает данные в конец файла; optimize собирает его заново без удалённого.
wimlib-imagex optimize "$wim" >/dev/null
size=$(stat -c %s "$wim")
if (( size > 4000 * 1024 * 1024 )); then
	wimlib-imagex split "$wim" "$work/tree/sources/install.swm" 3800 >/dev/null
	rm "$wim"
fi

echo "6/6 сборка ISO"
# Копия опций в корне ISO: по ней build/install.sh собирает файл ответов (пользователь, Riot).
cp "$opts" "$work/tree/lolssd-options.txt"
mkdir -p "$(dirname "$out")"
xorriso -as mkisofs -quiet -iso-level 3 -full-iso9660-filenames -J -joliet-long -V LOLSSD_WIN \
	-b boot/etfsboot.com -no-emul-boot -boot-load-size 8 \
	-eltorito-alt-boot -e efi/microsoft/boot/efisys_noprompt.bin -no-emul-boot \
	-o "$out.tmp" "$work/tree"
mv "$out.tmp" "$out"
echo "готово ($(du -h "$out" | cut -f1)):"
echo "$out"
