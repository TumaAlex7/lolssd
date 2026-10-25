#!/usr/bin/env bash
# lolssd: весь внешний диск — стоковый установщик Windows, как загрузочная флешка от Rufus:
# MBR, один раздел FAT32 на 16 ГиБ (его видит любая прошивка UEFI), остаток пустой. Стирает диск.
#   sudo build/winsetup-disk.sh /dev/sdX [папка с распакованным ISO, по умолчанию dist/installer]
set -euo pipefail

die() { echo "ошибка: $*" >&2; exit 1; }
[[ $# -ge 1 ]] || die "использование: sudo $0 /dev/sdX [папка установщика]"
dev=$1
name=$(basename "$dev")
src=${2:-$(cd "$(dirname "$0")/.." && pwd)/dist/installer}
[[ $(id -u) == 0 ]] || die "нужен root (sudo)"
[[ -b $dev && -e /sys/block/$name ]] || die "$dev — не диск целиком"
[[ $(lsblk -dnro TRAN "$dev") == usb ]] || die "$dev — не USB-диск, отказ"
[[ -f $src/efi/boot/bootx64.efi && -f $src/sources/install.swm ]] || die "в $src нет установщика"
! grep -q "^$dev" /proc/mounts || die "разделы $dev смонтированы: отмонтируйте их"
part() { [[ $dev == *[0-9] ]] && echo "${dev}p$1" || echo "$dev$1"; }

lsblk -o NAME,SIZE,FSTYPE,LABEL,MODEL "$dev"
read -rp "Всё на $dev будет стёрто. Введите $name для подтверждения: " ans
[[ $ans == "$name" ]] || die "отменено"

echo "[1/3] размечаю $dev: MBR, раздел FAT32 16 ГиБ…"
wipefs -aq "$dev"
printf 'label: dos\nsize=16GiB, type=c, bootable\n' | sfdisk -q --wipe always "$dev"
udevadm settle
mkfs.fat -F 32 -n WINSETUP "$(part 1)" >/dev/null
echo "[2/3] копирую установщик (8.4 ГБ, ~3–5 минут)…"
mnt=$(mktemp -d)
mount "$(part 1)" "$mnt"
cp -r "$src/." "$mnt/"
echo "[3/3] сбрасываю на диск…"
sync
umount "$mnt"
rmdir "$mnt"
lsblk -o NAME,SIZE,FSTYPE,LABEL "$dev"
echo "Готово. На сервере: меню загрузки → «UEFI: TOSHIBA…»."
