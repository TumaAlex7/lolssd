#!/usr/bin/env bash
# lolssd: драйвер TAP-Windows (OpenVPN, подписан для Windows 10/11) для сети ВМ с системой хоста:
# через адаптер TAP пакеты гостя уходят в Windows как есть (это нужно zapret), наружу их выпускает
# общий доступ к интернету (lolssd-win desktop). Результат — dist/tap (в образе —
# \Program Files\lolssd\tap): OemVista.inf, tap0901.sys/.cat и devcon.exe, которым lolssd-win
# создаёт адаптер.   build/tap.sh   (обычно make tap)
set -euo pipefail

die() { echo "ошибка: $*" >&2; exit 1; }
for t in curl unzip sha256sum; do
	command -v "$t" >/dev/null || die "нет $t"
done
repo=$(cd "$(dirname "$0")/.." && pwd)
ver=9.27.0
sum=36e2609b7ceefedcb978ce5c48caf9e0e5af83423717c4e2e3c1d7ebca8f62a5
zip=$repo/dist/cache/tap-windows-$ver-dist.win10.zip
out=$repo/dist/tap
mkdir -p "$repo/dist/cache"

if [[ ! -s $zip ]]; then
	echo "скачиваю TAP-Windows $ver…"
	curl -fSL --retry 3 -o "$zip.part" "https://github.com/OpenVPN/tap-windows6/releases/download/$ver/dist.win10.zip"
	mv "$zip.part" "$zip"
fi
echo "$sum  $zip" | sha256sum -c --quiet || die "контрольная сумма $zip не сошлась"
rm -rf "$out"
mkdir -p "$out"
unzip -q -j "$zip" 'dist.win10/amd64/*' -d "$out"
echo "готово: $out ($(ls "$out" | tr '\n' ' '))"
