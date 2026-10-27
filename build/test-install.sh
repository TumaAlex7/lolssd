#!/usr/bin/env bash
# lolssd: проверка установки целиком, в файл-образ, худший случай — рядом с «Linux» на том же диске:
# ESP с systemd-boot в \EFI\BOOT\BOOTX64.EFI, раздел ext4 с файлом-меткой и свободное место.
# install.sh --free в режиме проверки (язык и ключ заданы, без Riot, Windows выключается сама
# после первого входа), затем проверки: Linux цел, BOOTX64.EFI вернулся, Windows встала на свой
# раздел, шаги настройки без ошибок, lolssd на LOLSSD. 30–50 минут.
#   sudo build/test-install.sh dist/iso/lolssd-*.iso
set -euo pipefail

die() { echo "ошибка: $*" >&2; exit 1; }
[[ $EUID == 0 && -n ${SUDO_USER:-} ]] || die "запускайте через sudo от своего пользователя"
[[ $# == 1 ]] || die "использование: sudo $0 dist/iso/lolssd-*.iso"
iso=$(realpath "$1")
repo=$(cd "$(dirname "$0")/.." && pwd)
img=/var/tmp/lolssd-test.img
sdboot=/usr/lib/systemd/boot/efi/systemd-bootx64.efi
[[ -f $sdboot ]] || die "нет $sdboot (нужен настоящий systemd-boot для проверки)"

echo "[1/4] образ диска $img: ESP (systemd-boot), Linux (ext4), 180 ГиБ свободно…"
rm -f "$img"
truncate -s 200G "$img"
printf 'label: gpt\nsize=512MiB, type=uefi, name=ESP\nsize=16GiB, type=linux, name=linux\n' | sfdisk -q "$img"
dev=$(losetup --find --show --partscan "$img")
mnt=$(mktemp -d)
trap 'umount "$mnt" 2>/dev/null; losetup -d "$dev" 2>/dev/null; rmdir "$mnt"' EXIT
udevadm settle
mkfs.fat -F 32 -n ESP "${dev}p1" >/dev/null
mkfs.ext4 -q -L linux "${dev}p2"
mount "${dev}p1" "$mnt"
mkdir -p "$mnt/EFI/BOOT" && cp "$sdboot" "$mnt/EFI/BOOT/BOOTX64.EFI"
umount "$mnt"
mount "${dev}p2" "$mnt"
head -c 50M /dev/urandom >"$mnt/marker"
marker=$(sha256sum <"$mnt/marker")
umount "$mnt"

echo "[2/4] install.sh --free (без окна; экран — VNC 127.0.0.1:5909), $(date +%H:%M)…"
yes y | LOLSSD_TEST_KEY=VK7JG-NPHTM-C97JM-9MPGT-3V66T LOLSSD_HEADLESS=1 "$repo/build/install.sh" --free "$dev" "$iso"

echo "[3/4] проверки…"
fails=0
check() { if eval "$2"; then echo "  ok    $1"; else echo "  FAIL  $1"; fails=$((fails + 1)); fi; }
partx -u "$dev" 2>/dev/null || true
udevadm settle
check "раздел Linux цел (fsck)" "e2fsck -fn ${dev}p2 >/dev/null 2>&1"
mount -o ro "${dev}p2" "$mnt"
check "файл-метка на Linux не изменился" "[[ \$(sha256sum <$mnt/marker) == '$marker' ]]"
umount "$mnt"
mount -o ro "${dev}p1" "$mnt"
check "BOOTX64.EFI на месте и это systemd-boot" "cmp -s $mnt/EFI/BOOT/BOOTX64.EFI $sdboot"
check "загрузчик Windows на общем ESP" "[[ -f $mnt/EFI/Microsoft/Boot/bootmgfw.efi ]]"
check "резервной копии не осталось" "[[ ! -e $mnt/EFI/BOOT/BOOTX64.EFI.lolssd ]]"
umount "$mnt"
lolssd=$(lsblk -nro PATH,PARTLABEL "$dev" | awk '$2=="LOLSSD"{print $1}')
win=$(lsblk -nro PATH,PARTLABEL "$dev" | awk '$2=="Windows"{print $1}')
mount -o ro "$lolssd" "$mnt"
check "LOLSSD — exFAT с lolssd" "[[ \$(findmnt -no FSTYPE $mnt) == exfat && -f $mnt/lolssd ]]"
umount "$mnt"
mount -t ntfs3 -o ro "$win" "$mnt"
check "Windows на своём разделе" "[[ -f $mnt/Windows/System32/ntoskrnl.exe ]]"
log=$mnt/Windows/Setup/Scripts/lolssd-setup.log
check "настройка без ошибок (lolssd-setup.log)" "grep -q 'done, 0 step(s) failed' $log"
grep -a FAIL "$log" 2>/dev/null | sed 's/^/        /' || true
umount "$mnt"

echo "[4/4] итог: $([[ $fails == 0 ]] && echo 'всё прошло' || echo "не прошло проверок: $fails")"
echo "Образ оставлен: $img (rm $img — удалить; 200 ГиБ разреженный, на диске ~20 ГиБ)"
chown "$SUDO_USER" "$img"
exit "$fails"
