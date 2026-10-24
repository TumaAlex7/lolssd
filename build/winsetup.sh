#!/usr/bin/env bash
# lolssd: раздел WINSETUP со стоковым установщиком Windows на внешнем SSD — 10 ГиБ с конца DATA.
# Загружаться с него из меню загрузки прошивки (F8/F11/F12): «UEFI: <диск>, раздел WINSETUP».
#   sudo build/winsetup.sh /dev/sdX [папка с распакованным ISO, по умолчанию dist/installer]
# Папку готовит 7z x из ISO, install.wim разрезан на install*.swm (FAT32 не держит файлы > 4 ГиБ).
set -euo pipefail

die() { echo "ошибка: $*" >&2; exit 1; }
[[ $# -ge 1 ]] || die "использование: sudo $0 /dev/sdX [папка установщика]"
dev=$1
src=${2:-$(cd "$(dirname "$0")/.." && pwd)/dist/installer}
[[ $(id -u) == 0 ]] || die "нужен root (sudo)"
[[ -f $src/efi/boot/bootx64.efi && -f $src/sources/install.swm ]] || die "в $src нет установщика"
part() { [[ $dev == *[0-9] ]] && echo "${dev}p$1" || echo "$dev$1"; }
! grep -q "^$dev" /proc/mounts || die "разделы $dev смонтированы: отмонтируйте их"

ws=$(lsblk -nro PATH,PARTLABEL "$dev" | awk '$2=="WINSETUP"{print $1}')
if [[ -z $ws ]]; then
	data=$(lsblk -nro PATH,PARTLABEL "$dev" | awk '$2=="DATA"{print $1}')
	[[ -n $data ]] || die "на $dev нет раздела DATA"
	num=$(cat "/sys/class/block/$(basename "$data")/partition")
	ss=$(cat "/sys/block/$(basename "$dev")/queue/logical_block_size")
	size=$(cat "/sys/class/block/$(basename "$data")/size")  # в 512-байтных секторах
	cut=$(( 10 * 1024 * 1024 * 1024 ))
	new=$(( size * 512 - cut ))
	lsblk -o NAME,SIZE,FSTYPE,LABEL "$dev"
	echo "DATA ($data) уменьшится на 10 ГиБ, файлы на нём останутся."
	read -rp "Продолжить? [y/N] " ans
	[[ $ans == [yY] ]] || die "отменено"

	echo "[1/4] проверяю, можно ли ужать NTFS на DATA…"
	ntfsresize --no-action -f -s $new "$data" >/dev/null ||
		die "ntfsresize отказался (DATA не закрыт чисто? подключите к Windows и выключите её полностью)"
	echo "[2/4] ужимаю NTFS на DATA (если в конце раздела есть файлы, это может занять время)…"
	echo y | ntfsresize -f -s $new "$data" >/dev/null
	echo "[3/4] меняю разметку: DATA меньше, новый раздел WINSETUP в конце…"
	echo "size=$(( new / ss ))" | sfdisk --no-reread -N "$num" "$dev" >/dev/null
	echo "type=EBD0A0A2-B9E5-4433-87C0-68B6B72699C7, name=WINSETUP" | sfdisk --no-reread --append "$dev" >/dev/null
	partprobe "$dev" 2>/dev/null || blockdev --rereadpt "$dev" || true
	udevadm settle
	ws=$(lsblk -nro PATH,PARTLABEL "$dev" | awk '$2=="WINSETUP"{print $1}')
	[[ -n $ws ]] || die "раздел WINSETUP не появился"
fi

echo "[4/4] форматирую $ws в FAT32 и копирую установщик (8.4 ГБ, на USB-диск ~3–5 минут)…"
mkfs.fat -F 32 -n WINSETUP "$ws" >/dev/null
mnt=$(mktemp -d)
mount "$ws" "$mnt"
cp -r "$src/." "$mnt/"
sync
umount "$mnt"
rmdir "$mnt"
lsblk -o NAME,SIZE,FSTYPE,LABEL,PARTLABEL "$dev"
echo "Готово. На сервере: меню загрузки прошивки → «UEFI: … WINSETUP» (или «Partition 7»)."
