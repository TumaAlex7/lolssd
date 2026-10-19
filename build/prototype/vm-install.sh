#!/usr/bin/env bash
# lolssd, прототип: установить Windows на внешний SSD из Linux через QEMU (docs/PROTOTYPE.md).
#   sudo build/prototype/vm-install.sh /dev/sdX Win11.iso
# Размечает SSD (ESP + раздел LOLSSD, остальное — под Windows), кладёт на LOLSSD содержимое
# dist/, затем запускает установку Windows в QEMU прямо на этот SSD. Окно QEMU открывается от
# имени пользователя, вызвавшего sudo.
set -euo pipefail

die() { echo "ошибка: $*" >&2; exit 1; }

[[ $# == 2 ]] || die "использование: sudo $0 /dev/sdX Windows.iso"
dev=$1 iso=$(realpath "$2")
repo=$(cd "$(dirname "$0")/../.." && pwd)
name=$(basename "$dev")

[[ $EUID == 0 ]] || die "нужен root (sudo)"
[[ -n ${SUDO_USER:-} ]] || die "запускайте через sudo от своего пользователя: окно QEMU откроется от его имени"
[[ -b $dev && -e /sys/block/$name ]] || die "$dev — не диск целиком (нужен /dev/sdX, не раздел)"
[[ $(readlink -f "/sys/block/$name") == */usb* ]] || die "$dev не на USB — отказ, чтобы не стереть внутренний диск"
! grep -q "^/dev/$name" /proc/mounts || die "разделы $dev смонтированы, отмонтируйте их"
[[ -f $iso ]] || die "нет ISO $iso"
[[ -d $repo/dist/LOLSSD && -d $repo/dist/windows ]] || die "сначала make dist"
for t in qemu-system-x86_64 sfdisk mkfs.fat; do
	command -v "$t" >/dev/null || die "нет $t (sudo pacman -S qemu-desktop edk2-ovmf dosfstools)"
done
code=/usr/share/edk2/x64/OVMF_CODE.4m.fd vars=/usr/share/edk2/x64/OVMF_VARS.4m.fd
[[ -f $code && -f $vars ]] || die "нет OVMF ($code), sudo pacman -S edk2-ovmf"

lsblk -o NAME,SIZE,MODEL,TRAN "$dev"
read -rp "Всё на $dev будет стёрто. Введите $name для подтверждения: " ans
[[ $ans == "$name" ]] || die "отменено"

# ESP и LOLSSD; дальше свободное место, туда ставится Windows (MSR и NTFS она создаст сама).
# LOLSSD — тип «basic data», чтобы Windows дала ему букву.
sfdisk --wipe always --wipe-partitions always "$dev" <<SFDISK
label: gpt
size=512MiB, type=uefi, name=EFI
size=256MiB, type=EBD0A0A2-B9E5-4433-87C0-68B6B72699C7, name=LOLSSD
SFDISK
udevadm settle
mkfs.fat -F 32 -n EFI "${dev}1" >/dev/null
mkfs.fat -F 32 -n LOLSSD "${dev}2" >/dev/null

mnt=$(mktemp -d)
mount "${dev}2" "$mnt"
cp -r "$repo/dist/LOLSSD/." "$mnt/"
mkdir -p "$mnt/windows"
cp -r "$repo/dist/windows/." "$mnt/windows/"
cp "$repo/build/prototype/apply.cmd" "$mnt/windows/"
umount "$mnt"
rmdir "$mnt"
sync

work=$(mktemp -d)
cp "$repo/build/prototype/autounattend.xml" "$work/"
cp "$vars" "$work/OVMF_VARS.fd"
chown -R "$SUDO_USER" "$work"
setfacl -m "u:$SUDO_USER:rw" "$dev"
trap 'setfacl -x "u:$SUDO_USER" "$dev"; rm -rf "$work"' EXIT

echo "Запускаю QEMU. В начале нажмите любую клавишу, чтобы загрузиться с ISO."
echo "В установщике: «У меня нет ключа», редакция Pro, диск — незанятое место после LOLSSD."
# Диск по AHCI: драйвер есть в Windows из коробки. Сети нет: так OOBE не просит учётку Microsoft.
# autounattend.xml — на виртуальной USB-флешке из каталога (vvfat), установщик ищет его там.
sudo -u "$SUDO_USER" --preserve-env=WAYLAND_DISPLAY,DISPLAY,XDG_RUNTIME_DIR \
	qemu-system-x86_64 -enable-kvm -machine q35 -cpu host -smp 4 -m 6G \
	-drive if=pflash,format=raw,readonly=on,file="$code" \
	-drive if=pflash,format=raw,file="$work/OVMF_VARS.fd" \
	-device ahci,id=ahci \
	-drive if=none,id=ssd,format=raw,cache=none,file="$dev" -device ide-hd,drive=ssd,bus=ahci.0,bootindex=1 \
	-drive if=none,id=cd,media=cdrom,file="$iso" -device ide-cd,drive=cd,bus=ahci.1,bootindex=0 \
	-device qemu-xhci -drive if=none,id=unattend,format=raw,file="fat:$work" -device usb-storage,drive=unattend \
	-device usb-tablet -nic none

echo "QEMU закрыт. Если apply.cmd отработал и Windows выключена — можно грузиться с SSD: sudo lolssd boot"
