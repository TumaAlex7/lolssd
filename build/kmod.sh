#!/usr/bin/env bash
# lolssd: заголовки ядра WSL для сборки внешних модулей в режиме «Linux внутри Windows».
#   build/kmod.sh            (обычно make kmod; make wsl кладёт их в пусковой дистрибутив)
#
# Ядро WSL общее для всех дистрибутивов и модули DKMS хоста (они собраны под его ядро) не грузит.
# Поэтому при запуске lolssd-wsl прогоняет DKMS самой системы хоста против ядра WSL (amneziawg,
# v4l2loopback — всё, что там установлено) — для этого ей нужны заголовки этого ядра, как пакет
# linux-headers. Ядро — из MSI WSL в образе (Windows Update выключен, так что оно не меняется).
#
# Дерево ядра: ванильное ядро той же версии с kernel.org и конфиг WSL из репозитория Microsoft
# (исходники Microsoft целиком с GitHub качаются часами). Это безопасно: у ядра WSL включены
# версии символов (MODVERSIONS, genksyms по исходникам), случайной раскладки структур нет — модуль
# с несовпадающим интерфейсом ядро просто не загрузит («disagrees about version of symbol»).
# Дерево собирается целиком ради Module.symvers и кэшируется в dist/cache.
set -euo pipefail

die() { echo "ошибка: $*" >&2; exit 1; }

repo=$(cd "$(dirname "$0")/.." && pwd)
dist=$repo/dist
krel=${WSL_KERNEL:-6.18.54.1}           # версия ядра в MSI WSL (uname -r без суффикса)
local=-microsoft-standard-WSL2
base=${krel%.*}                          # 6.18.54 — ванильная версия
cache=$dist/cache/kernel-$krel
tree=$cache/linux-$base
out=$dist/wsl/kheaders/$krel$local

for t in curl tar make gcc bc flex bison perl; do
	command -v "$t" >/dev/null || die "нет $t"
done
mkdir -p "$cache"

echo "ядро WSL $krel$local"
if [[ ! -s $tree/Module.symvers ]]; then
	tarball=$cache/linux-$base.tar.xz
	[[ -s $tarball ]] || curl -fSL --retry 3 -o "$tarball" "https://cdn.kernel.org/pub/linux/kernel/v${base%%.*}.x/linux-$base.tar.xz"
	config=$cache/config-wsl
	[[ -s $config ]] || curl -fsSL --retry 3 -o "$config" \
		"https://raw.githubusercontent.com/microsoft/WSL2-Linux-Kernel/linux-msft-wsl-$krel/arch/x86/configs/config-wsl"
	rm -rf "$tree"
	tar -xf "$tarball" -C "$cache"
	sed -i "s/^EXTRAVERSION =.*/EXTRAVERSION = .${krel##*.}/" "$tree/Makefile"
	cp "$config" "$tree/.config"
	# Отладочные сведения и BTF на версии символов не влияют, а сборку замедляют.
	"$tree/scripts/config" --file "$tree/.config" --disable DEBUG_INFO_BTF --disable DEBUG_INFO_BTF_MODULES \
		--disable DEBUG_INFO --enable DEBUG_INFO_NONE
	make -C "$tree" -s olddefconfig
	[[ $(make -C "$tree" -s kernelrelease) == "$krel$local" ]] || die "kernelrelease не $krel$local"
	echo "  сборка дерева ядра (один раз на версию, несколько минут)…"
	make -C "$tree" -s -j"$(nproc)"
fi

echo "заголовки для модулей"
"$repo/build/kheaders.sh" "$tree" "$out"
echo "  $out ($(du -sh "$out" | cut -f1))"
