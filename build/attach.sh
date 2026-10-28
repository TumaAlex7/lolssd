#!/usr/bin/env bash
# lolssd: добавить «Linux внутри Windows» в уже установленную Windows (например, поставленную рядом
# с Linux вручную), не переустанавливая её. Кладёт наши файлы в C:\lolssd-setup — только новые
# файлы, существующие не трогаются; дальше в Windows один раз запустить C:\lolssd-setup\attach.cmd
# (lolssd-attach.ps1): раздел LOLSSD (512 МБ с конца C:, средствами самой Windows), QEMU, TAP,
# TigerVNC, задания и служба — без удаления приложений и без правок обновлений и реестра.
#   make payload && sudo build/attach.sh /dev/nvme0n1p4     (раздел Windows; Windows выключена полностью)
set -euo pipefail

die() { echo "ошибка: $*" >&2; exit 1; }
[[ $EUID == 0 ]] || die "нужен root (sudo)"
[[ $# == 1 && -b $1 ]] || die "использование: sudo $0 /dev/РАЗДЕЛ_WINDOWS"
dev=$1
repo=$(cd "$(dirname "$0")/.." && pwd)
p=$repo/dist/payload
[[ -f $p/windows/lolssd-attach.ps1 && -f $p/LOLSSD/lolssd ]] || die "нет набора файлов: make payload"
! grep -q "^$dev " /proc/mounts || die "$dev смонтирован: отмонтируйте"

mnt=$(mktemp -d)
trap 'umount "$mnt" 2>/dev/null; rmdir "$mnt"' EXIT
echo "[1/3] монтирую $dev (ntfs3, запись)…"
mount -t ntfs3 "$dev" "$mnt" ||
	die "не монтируется на запись: Windows выключена не до конца — загрузитесь в неё и выключите через Пуск → Завершение работы с зажатым Shift"
[[ -d $mnt/Windows/System32 ]] || die "$dev — не раздел Windows"

echo "[2/3] кладу файлы в C:\\lolssd-setup…"
d=$mnt/lolssd-setup
rm -rf "$d"
mkdir -p "$d"
cp -r "$p/windows/." "$d/"
cp -r "$p/LOLSSD" "$d/LOLSSD"
# Только то, что нужно ВМ: без удаления приложений, обновления и реестр — как есть.
printf '%s\r\n' "placement=internal" "vm=1" "updates=1" "edge=0" "onedrive=0" "apps=" \
	"packages=0" "accessibility=0" "riot=0" "sunshine=0" "user=" >"$d/options.txt"
printf '@echo off\r\npowershell.exe -NoProfile -ExecutionPolicy Bypass -File "%%~dp0lolssd-attach.ps1"\r\n' >"$d/attach.cmd"
sync

echo "[3/3] готово. Дальше:"
echo "  1. Загрузитесь в Windows и запустите C:\\lolssd-setup\\attach.cmd (спросит права администратора;"
echo "     несколько минут — скачивает QEMU, TAP, TigerVNC)."
echo "  2. Перезагрузите Windows один раз — и вернитесь в Linux."
echo "  3. В Linux: раздел LOLSSD смонтируется сам; /run/media/\$USER/LOLSSD/lolssd → «Подготовить Linux…»."
