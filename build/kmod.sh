#!/usr/bin/env bash
# lolssd: заголовки ядра WSL для сборки внешних модулей в режиме «Linux внутри Windows».
#   build/kmod.sh            (обычно make kmod; make wsl кладёт их в пусковой дистрибутив)
#
# Ядро WSL общее для всех дистрибутивов и модули DKMS хоста (они собраны под его ядро) не грузит.
# Поэтому при запуске lolssd-wsl прогоняет DKMS самой системы хоста против ядра WSL (amneziawg,
# v4l2loopback — всё, что там установлено) — для этого ей нужны заголовки этого ядра, как пакет
# linux-headers. Microsoft его не выпускает; всё нужное берётся из самого ядра WSL в MSI образа
# (Windows Update выключен, так что оно не меняется):
#   - версия и конфиг — из образа ядра (extract-ikconfig);
#   - Module.symvers — экспорты и контрольные суммы vmlinux и всех модулей WSL, прочитанные из
#     них самих (build/symvers). Вычислять суммы сборкой нельзя: они зависят от компилятора
#     (Microsoft собирает GCC 13), и ядро не принимает модуль с чужими суммами;
#   - заголовки и scripts — из ванильных исходников той же версии с kernel.org (исходники Microsoft
#     с GitHub качаются часами), подготовленных make modules_prepare.
# Защита остаётся в самом ядре: модуль с несовпадающим интерфейсом оно не загрузит.
set -euo pipefail

die() { echo "ошибка: $*" >&2; exit 1; }

repo=$(cd "$(dirname "$0")/.." && pwd)
dist=$repo/dist

for t in curl tar make gcc bc flex bison perl 7z go; do
	command -v "$t" >/dev/null || die "нет $t"
done
msi=$(ls "$dist"/wsl/*.msi 2>/dev/null | head -1)
[[ -n $msi ]] || die "нет MSI WSL в dist/wsl (make wsl)"

# Ядро и его модули из MSI: файл kernel и образ модулей artifacts.vhd.
x=$(mktemp -d)
trap 'rm -rf "$x"' EXIT
(cd "$x" && 7z e -y "$msi" >/dev/null 2>&1 || true)
for cab in "$x"/*.cab; do 7z e -y -o"$x" "$cab" kernel artifacts.vhd >/dev/null 2>&1 || true; done
[[ -s $x/kernel && -s $x/artifacts.vhd ]] || die "в $msi нет kernel или artifacts.vhd"
(cd "$x" && 7z x -y artifacts.vhd '*/modules/*' >/dev/null 2>&1)
krel=$(cd "$x" && ls -d */modules | head -1 | cut -d/ -f1)
[[ -n $krel ]] || die "в artifacts.vhd нет модулей"
krelver=${krel%%-*} # 6.18.54.1
base=${krelver%.*}  # 6.18.54 — ванильная версия
echo "ядро WSL $krel"

cache=$dist/cache/kernel-$krel
tree=$cache/linux-$base
mkdir -p "$cache"
tarball=$cache/linux-$base.tar.xz
[[ -s $tarball ]] || curl -fSL --retry 3 -o "$tarball" "https://cdn.kernel.org/pub/linux/kernel/v${base%%.*}.x/linux-$base.tar.xz"
rm -rf "$tree"
tar -xf "$tarball" -C "$cache"
sed -i "s/^EXTRAVERSION =.*/EXTRAVERSION = .${krelver##*.}/" "$tree/Makefile"
"$tree/scripts/extract-ikconfig" "$x/kernel" > "$tree/.config" || die "в ядре нет конфига (IKCONFIG)"
make -C "$tree" -s olddefconfig
[[ $(make -C "$tree" -s kernelrelease) == "$krel" ]] || die "kernelrelease не $krel"
echo "  подготовка заголовков (make modules_prepare)…"
make -C "$tree" -s -j"$(nproc)" modules_prepare
"$tree/scripts/extract-vmlinux" "$x/kernel" > "$x/vmlinux"
(cd "$repo" && go run ./build/symvers "$x/vmlinux" "$x/$krel/modules") > "$tree/Module.symvers"
echo "  Module.symvers: $(wc -l < "$tree/Module.symvers") экспортов ядра и модулей WSL"

out=$dist/wsl/kheaders/$krel
"$repo/build/kheaders.sh" "$tree" "$out"
echo "заголовки: $out ($(du -sh "$out" | cut -f1))"
