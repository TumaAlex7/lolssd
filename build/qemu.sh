#!/usr/bin/env bash
# lolssd: QEMU для Windows — то, что нужно ВМ с системой хоста (lolssd-win desktop), из сборки
# Стефана Вайля (qemu.weilnetz.de): qemu-system-x86_64w.exe, его DLL и прошивка SeaBIOS.
# Из 1,2 ГБ установщика остаётся около 125 МБ. Результат — dist/qemu (в образе —
# \Program Files\lolssd\qemu). Установщик скачивается один раз на версию в dist/cache.
#   build/qemu.sh            (обычно make qemu; версия — QEMU_W64=ГГГГММДД)
set -euo pipefail

die() { echo "ошибка: $*" >&2; exit 1; }
for t in 7z curl objdump sha512sum; do
	command -v "$t" >/dev/null || die "нет $t"
done
repo=$(cd "$(dirname "$0")/.." && pwd)
ver=${QEMU_W64:-20260811}
cache=$repo/dist/cache
setup=$cache/qemu-w64-setup-$ver.exe
out=$repo/dist/qemu
mkdir -p "$cache"

if [[ ! -s $setup ]]; then
	echo "скачиваю QEMU для Windows $ver (~200 МБ)…"
	curl -fSL --retry 3 -o "$setup.part" "https://qemu.weilnetz.de/w64/qemu-w64-setup-$ver.exe"
	curl -fsSL --retry 3 -o "$setup.sha512" "https://qemu.weilnetz.de/w64/qemu-w64-setup-$ver.sha512"
	(cd "$cache" && sed "s/qemu-w64-setup-$ver.exe/&.part/" "$setup.sha512" | sha512sum -c --quiet) ||
		die "контрольная сумма установщика не сошлась"
	mv "$setup.part" "$setup"
fi

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
echo "распаковываю…"
7z x -y -o"$work" "$setup" >/dev/null

rm -rf "$out"
mkdir -p "$out/share/keymaps"
cp "$work/qemu-system-x86_64w.exe" "$work/COPYING" "$work/COPYING.LIB" "$work/VERSION" "$out/"
# DLL — замыкание импортов exe по тем, что есть в сборке (системные Windows не в ней).
declare -A have
for f in "$work"/*.dll; do
	b=$(basename "$f")
	have[${b,,}]=$b
done
todo=(qemu-system-x86_64w.exe)
declare -A seen
while ((${#todo[@]})); do
	f=${todo[-1]}
	unset 'todo[-1]'
	while read -r d; do
		d=${d,,}
		[[ -n ${have[$d]:-} && -z ${seen[$d]:-} ]] || continue
		seen[$d]=1
		todo+=("${have[$d]}")
		cp "$work/${have[$d]}" "$out/"
	done < <(objdump -p "$work/$f" | sed -n 's/^\s*DLL Name: //p')
done
# Прошивка для прямого запуска ядра (-kernel) и ПЗУ сетевой карты virtio.
for f in bios-256k.bin kvmvapic.bin linuxboot_dma.bin multiboot_dma.bin efi-virtio.rom; do
	cp "$work/share/$f" "$out/share/"
done
cp "$work/share/keymaps/en-us" "$out/share/keymaps/" 2>/dev/null || true
echo "готово: $out ($(du -sh "$out" | cut -f1), QEMU $(cat "$out/VERSION"), DLL: ${#seen[@]})"
