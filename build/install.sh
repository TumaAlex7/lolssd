#!/usr/bin/env bash
# lolssd: установить Windows на диск из Linux через QEMU (README.md, «Установка»): на внешний SSD
# или на внутренний диск (например, сервера).
#   sudo build/install.sh /dev/sdX dist/iso/lolssd-*.iso [РАЗМЕР_ПОД_WINDOWS, например 128G]
# ISO собирается заранее: make iso ISO=... — в нём уже всё: файл ответов, наши файлы, драйверы.
# Скрипт размечает диск: ESP, раздел LOLSSD (утилита для Linux и профили), место под Windows.
# Без размера Windows отдаётся весь остаток диска; с размером — столько гигабайт, а остаток
# становится разделом DATA (NTFS) для своих файлов. Затем запускает установку в QEMU прямо на
# этот диск. Окно QEMU открывается от имени пользователя, вызвавшего sudo.
# ISO, собранный с VARIANT=server, ставится без вопросов и без окна: скрипт размечает и раздел
# Windows (с MSR), файл ответов ставит на него, а Windows сама выключается в конце установки.
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
[[ -n ${SUDO_USER:-} ]] || die "запускайте через sudo от своего пользователя: QEMU работает от его имени"
[[ -b $dev && -e /sys/block/$name ]] || die "$dev — не диск целиком (нужен /dev/sdX, не раздел)"
! grep -q "^/dev/$name" /proc/mounts || die "разделы $dev смонтированы, отмонтируйте их (и уберите из /etc/fstab)"
! grep -q "^/dev/$name" /proc/swaps || die "на $dev включён swap: swapoff и уберите из /etc/fstab"
[[ -f $iso ]] || die "нет ISO $iso (make iso ISO=...)"
[[ $(basename "$iso") == lolssd-* ]] || die "нужен ISO, собранный make iso (dist/iso/lolssd-*.iso), а не исходный"
[[ -d $repo/dist/LOLSSD ]] || die "сначала make dist"
for t in 7z; do command -v "$t" >/dev/null || die "нет $t (sudo pacman -S p7zip)"; done
# Серверный ISO (VARIANT=server): в файле ответов задан раздел для Windows.
auto=
7z e -so "$iso" autounattend.xml 2>/dev/null | grep -q '<ImageInstall>' && auto=1
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
[[ $(readlink -f "/sys/block/$name") == */usb* ]] ||
	echo "ВНИМАНИЕ: $dev — внутренний диск компьютера, не USB. Проверьте, что это тот диск."
[[ -z $auto ]] || command -v mkfs.ntfs >/dev/null || die "нет mkfs.ntfs (sudo pacman -S ntfs-3g)"
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
	if [[ -n $auto ]]; then
		# MSR и раздел Windows (4-й, на него ставит файл ответов) — до DATA или до конца диска.
		echo "size=16MiB, type=E3C9E316-0B5C-4DB8-817D-F92DF00215AE, name=MSR"
		if [[ -n $data_start_mib ]]; then
			echo "size=$(( data_start_mib - 1 - 512 - 512 - 16 ))MiB, type=$basic, name=Windows"
		else
			echo "type=$basic, name=Windows"
		fi
	fi
	[[ -z $data_start_mib ]] || echo "start=${data_start_mib}MiB, type=$basic, name=DATA"
} | sfdisk --wipe always --wipe-partitions always "$dev"
udevadm settle
# Разделы: sda1 или nvme0n1p1.
part() { [[ $dev == *[0-9] ]] && echo "${dev}p$1" || echo "$dev$1"; }
mkfs.fat -F 32 -n EFI "$(part 1)" >/dev/null
mkfs.fat -F 32 -n LOLSSD "$(part 2)" >/dev/null
if [[ -n $auto ]]; then
	mkfs.ntfs --quick --label Windows "$(part 4)" >/dev/null
	[[ -z $data_start_mib ]] || mkfs.ntfs --quick --label DATA "$(part 5)" >/dev/null
else
	[[ -z $data_start_mib ]] || mkfs.ntfs --quick --label DATA "$(part 3)" >/dev/null
fi

mnt=$(mktemp -d)
mount "$(part 2)" "$mnt"
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

display=()
if [[ -n $auto ]]; then
	# Без окна: экран — VNC на 127.0.0.1:5909, если захочется посмотреть (ssh -L 5909:127.0.0.1:5909).
	display=(-display none -vnc 127.0.0.1:9)
	echo "Запускаю установку без окна ($(date +%H:%M)). Всё идёт само, обычно 20–40 минут;"
	echo "в конце Windows выключится, и этот скрипт завершится. Посмотреть: VNC 127.0.0.1:5909."
else
	echo "Запускаю QEMU. В установщике: язык, «У меня нет ключа», диск — единственное незанятое место."
	echo "Дальше всё само: после первого входа Windows готова, выключите её (Пуск → Завершение работы)."
fi
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
	-device qemu-xhci -device usb-tablet -nic user,model=e1000e "${display[@]}"

echo "QEMU закрыт ($(date +%H:%M)). Если Windows выключилась сама (или после первого входа) — готово."
echo "Загрузиться в неё один раз (следующая загрузка снова в Linux):"
echo "  sudo mount $(part 2) /mnt && sudo sh /mnt/lolssd.sh boot"
