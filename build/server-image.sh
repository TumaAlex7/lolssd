#!/usr/bin/env bash
# lolssd: готовый образ диска с Windows и Sunshine для сервера (без монитора у себя и без пакетов
# на сервере). Собирает серверный ISO (VARIANT=server, драйвер NVIDIA для GTX 10 внутри), ставит
# из него Windows в файл-образ здесь же, в QEMU (без вопросов, ~30 минут), и сжимает его.
#   build/server-image.sh ~/Downloads/Windows11.iso ["Windows 11 Pro"]
# Результат — dist/server: lolssd-server.img.gz и write.sh. На сервере:
#   sudo sh write.sh /dev/sdX            (диск — тот, что отдаёте Windows; он будет стёрт)
set -euo pipefail

die() { echo "ошибка: $*" >&2; exit 1; }
[[ $# -ge 1 ]] || die "использование: $0 Windows.iso [редакция, по умолчанию «Windows 11 Pro»]"
src=$(realpath "$1")
edition=${2:-Windows 11 Pro}
repo=$(cd "$(dirname "$0")/.." && pwd)
out=$repo/dist/server
cd "$repo"
mkdir -p "$out" dist/cache

# Драйвер NVIDIA: обновления Windows в образе выключены, а GTX 10 (Pascal) поддерживает только
# ветка обновлений безопасности. Распакованный Display.Driver ставит задание drivers при старте.
nv=${NVIDIA:-582.78}
drv=$out/drivers/nvidia-$nv
if [[ ! -d $drv/Display.Driver ]]; then
	echo "== драйвер NVIDIA $nv"
	exe=dist/cache/nvidia-$nv.exe
	[[ -s $exe ]] || { curl -fSL --retry 3 -o "$exe.part" \
		"https://us.download.nvidia.com/Windows/$nv/$nv-desktop-win10-win11-64bit-international-dch-whql.exe" &&
		mv "$exe.part" "$exe"; }
	rm -rf "$out/drivers"
	mkdir -p "$drv"
	7z x -y -o"$drv" "$exe" Display.Driver >/dev/null
fi

iso=dist/iso/lolssd-server-$(basename "$src")
if [[ -f $iso && -z ${REBUILD:-} ]]; then
	echo "== серверный ISO уже собран: $iso (REBUILD=1 — собрать заново)"
	make dist >/dev/null
else
	echo "== серверный ISO ($edition)"
	make iso ISO="$src" VARIANT=server DRIVERS="$out/drivers" EDITION="$edition"
fi

echo "== установка Windows в образ (нужен sudo; QEMU без окна, VNC 127.0.0.1:5909)"
rm -f "$out/lolssd-server.img" "$out/lolssd-server.img.gz"
sudo build/install.sh "$out/lolssd-server.img" "$iso" 40G

echo "== сжатие ($(date +%H:%M))"
if command -v pigz >/dev/null; then pigz -6 "$out/lolssd-server.img"; else gzip -6 "$out/lolssd-server.img"; fi
cp build/server/write.sh "$out/write.sh"
ls -la "$out"
echo "Готово. На сервер: $out/lolssd-server.img.gz и $out/write.sh, там — sudo sh write.sh /dev/sdX"
