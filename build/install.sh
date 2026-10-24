#!/usr/bin/env bash
# lolssd: установить Windows на внешний SSD из Linux через QEMU (README.md, «Установка»).
#   sudo build/install.sh /dev/sdX dist/iso/lolssd-*.iso [РАЗМЕР_ПОД_WINDOWS, например 128G]
# ISO собирается заранее: make iso ISO=... — в нём уже всё: файл ответов, наши файлы, драйверы.
# Скрипт размечает диск: ESP, раздел LOLSSD (утилита для Linux и профили), место под Windows.
# Без размера Windows отдаётся весь остаток диска; с размером — столько гигабайт, а остаток
# становится разделом DATA (NTFS) для своих файлов. Затем запускает установку в QEMU прямо на
# этот диск. Окно QEMU открывается от имени пользователя, вызвавшего sudo.
set -euo pipefail

die() { echo "ошибка: $*" >&2; exit 1; }

[[ $# == 2 || $# == 3 ]] || die "использование: sudo $0 /dev/sdX dist/iso/lolssd-*.iso [размер под Windows, например 128G]"
dev=$1 iso=$(realpath "$2")
win_gib=
if [[ $# == 3 ]]; then
	[[ $3 =~ ^([0-9]+)[Gg]?$ ]] || die "размер — целое число гигабайт, например 128G"
	win_gib=${BASH_REMATCH[1]}
	(( win_gib >= 40 )) || die "под Windows с лолкой нужно не меньше 40G"
fi
repo=$(cd "$(dirname "$0")/../.." && pwd)
name=$(basename "$dev")

[[ $EUID == 0 ]] || die "нужен root (sudo)"
[[ -n ${SUDO_USER:-} ]] || die "запускайте через sudo от своего пользователя: окно QEMU откроется от его имени"
[[ -b $dev && -e /sys/block/$name ]] || die "$dev — не диск целиком (нужен /dev/sdX, не раздел)"
[[ $(readlink -f "/sys/block/$name") == */usb* ]] || die "$dev не на USB — отказ, чтобы не стереть внутренний диск"
! grep -q "^/dev/$name" /proc/mounts || die "разделы $dev смонтированы, отмонтируйте их"
[[ -f $iso ]] || die "нет ISO $iso (make iso ISO=...)"
[[ $(basename "$iso") == lolssd-* ]] || die "нужен ISO, собранный make iso (dist/iso/lolssd-*.iso), а не исходный"
[[ -d $repo/dist/LOLSSD ]] || die "сначала make dist"
for t in qemu-system-x86_64 sfdisk mkfs.fat swtpm; do
	command -v "$t" >/dev/null || die "нет $t (sudo pacman -S qemu-desktop edk2-ovmf dosfstools swtpm)"
done
# Прошивка с поддержкой Secure Boot и виртуальный TPM 2.0: установщик Windows 11 проверяет оба.
code=/usr/share/edk2/x64/OVMF_CODE.secboot.4m.fd vars=/usr/share/edk2/x64/OVMF_VARS.4m.fd
[[ -f $code && -f $vars ]] || die "нет OVMF ($code), sudo pacman -S edk2-ovmf"

# 1 МиБ выравнивания + ESP 512 МиБ + LOLSSD 512 МиБ, дальше место под Windows.
data_start_mib=
if [[ -n $win_gib ]]; then
	data_start_mib=$(( 1 + 512 + 512 + win_gib * 1024 ))
	disk_mib=$(( $(blockdev --getsize64 "$dev") / 1024 / 1024 ))
	(( disk_mib - data_start_mib >= 1024 )) || die "на диске $(( disk_mib / 1024 ))G — после ${win_gib}G под Windows не остаётся места под DATA; запустите без размера"
	command -v mkfs.ntfs >/dev/null || die "нет mkfs.ntfs (sudo pacman -S ntfs-3g)"
fi

lsblk -o NAME,SIZE,MODEL,TRAN "$dev"
if [[ -n $win_gib ]]; then
	echo "Разметка: EFI 512M, LOLSSD 512M, Windows ${win_gib}G, DATA (NTFS) — остальное."
else
	echo "Разметка: EFI 512M, LOLSSD 512M, Windows — весь остаток диска."
fi
read -rp "Всё на $dev будет стёрто. Введите $name для подтверждения: " ans
[[ $ans == "$name" ]] || die "отменено"

# ESP и LOLSSD; дальше свободное место, туда ставится Windows (MSR и NTFS она создаст сама).
# LOLSSD и DATA — тип «basic data», чтобы Windows дала им буквы.
basic=EBD0A0A2-B9E5-4433-87C0-68B6B72699C7
{
	echo "label: gpt"
	echo "size=512MiB, type=uefi, name=EFI"
	echo "size=512MiB, type=$basic, name=LOLSSD"
	[[ -z $data_start_mib ]] || echo "start=${data_start_mib}MiB, type=$basic, name=DATA"
} | sfdisk --wipe always --wipe-partitions always "$dev"
udevadm settle
mkfs.fat -F 32 -n EFI "${dev}1" >/dev/null
mkfs.fat -F 32 -n LOLSSD "${dev}2" >/dev/null
[[ -z $data_start_mib ]] || mkfs.ntfs --quick --label DATA "${dev}3" >/dev/null

mnt=$(mktemp -d)
mount "${dev}2" "$mnt"
cp -r "$repo/dist/LOLSSD/." "$mnt/"
umount "$mnt"
rmdir "$mnt"
sync

work=$(mktemp -d)
cp "$vars" "$work/OVMF_VARS.fd"
mkdir "$work/tpm"
chown -R "$SUDO_USER" "$work"
setfacl -m "u:$SUDO_USER:rw" "$dev"
trap 'setfacl -x "u:$SUDO_USER" "$dev"; rm -rf "$work"' EXIT

# swtpm живёт, пока к нему подключён QEMU, и завершается вместе с ним (--terminate).
sudo -u "$SUDO_USER" swtpm socket --tpm2 --tpmstate dir="$work/tpm" \
	--ctrl type=unixio,path="$work/tpm/sock" --terminate --daemon

echo "Запускаю QEMU. В установщике: язык, «У меня нет ключа», диск — единственное незанятое место."
echo "Дальше всё само: после первого входа Windows готова, выключите её (Пуск → Завершение работы)."
# Диск первым в порядке загрузки, ISO вторым: в ISO нет «Press any key to boot from CD»
# (efisys_noprompt), и с ISO первым каждая перезагрузка установщика начинала бы установку
# заново. При первом запуске ESP на диске пуст, и прошивка сама переходит к ISO.
# Диск по AHCI, сеть e1000e: драйверы для обоих есть в Windows из коробки. Учётку Microsoft OOBE
# не попросит: локальная учётная запись задана в файле ответов.
sudo -u "$SUDO_USER" --preserve-env=WAYLAND_DISPLAY,DISPLAY,XDG_RUNTIME_DIR \
	qemu-system-x86_64 -enable-kvm -machine q35,smm=on -cpu host -smp 4 -m 6G \
	-global driver=cfi.pflash01,property=secure,value=on \
	-chardev socket,id=tpm,path="$work/tpm/sock" -tpmdev emulator,id=tpm0,chardev=tpm -device tpm-crb,tpmdev=tpm0 \
	-drive if=pflash,format=raw,readonly=on,file="$code" \
	-drive if=pflash,format=raw,file="$work/OVMF_VARS.fd" \
	-device ahci,id=ahci \
	-drive if=none,id=ssd,format=raw,cache=none,file="$dev" -device ide-hd,drive=ssd,bus=ahci.0,bootindex=0 \
	-drive if=none,id=cd,media=cdrom,file="$iso" -device ide-cd,drive=cd,bus=ahci.1,bootindex=1 \
	-device qemu-xhci -device usb-tablet -nic user,model=e1000e

echo "QEMU закрыт. Если Windows выключилась после первого входа — можно грузиться с SSD:"
echo "  sudo sh /run/media/\$USER/LOLSSD/lolssd.sh boot"
