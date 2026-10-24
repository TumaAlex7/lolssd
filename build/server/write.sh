#!/bin/sh
# lolssd: записать готовый образ Windows с Sunshine на диск сервера. Нужны только gzip, dd и
# sfdisk — они есть в любом Linux. Диск — аргументом, скрипт переспросит его имя.
#   sudo sh write.sh /dev/sdX [lolssd-server.img.gz]
# Образ занимает начало диска; раздел Windows затем растягивается на весь диск, а её ФС Windows
# растянет сама при первом входе. На диске будет и раздел LOLSSD с утилитой загрузки:
#   sudo mount /dev/sdX2 /mnt && sudo sh /mnt/lolssd.sh boot     — один раз загрузиться в Windows.
# Выключение или перезагрузка Windows всегда возвращает в Linux.
set -eu

die() { echo "ошибка: $*" >&2; exit 1; }

[ $# -ge 1 ] || die "использование: sudo sh $0 /dev/sdX [образ.img.gz]"
dev=$1
img=${2:-$(dirname "$0")/lolssd-server.img.gz}
name=$(basename "$dev")
[ "$(id -u)" = 0 ] || die "нужен root (sudo)"
[ -b "$dev" ] && [ -e "/sys/block/$name" ] || die "$dev — не диск целиком (нужен /dev/sdX или /dev/nvmeXnY, не раздел)"
[ -f "$img" ] || die "нет образа $img"
! grep -q "^/dev/$name" /proc/mounts || die "разделы $dev смонтированы: отмонтируйте их и уберите из /etc/fstab"
! grep -q "^/dev/$name" /proc/swaps || die "на $dev включён swap: swapoff и уберите из /etc/fstab"
for t in gzip dd sfdisk blockdev; do
	command -v "$t" >/dev/null || die "нет $t"
done

have=$(blockdev --getsize64 "$dev")
# Образ — 40 ГиБ, раздел Windows затем растягивается на весь диск.
[ "$have" -ge 42949672960 ] || die "на $dev меньше 40 ГиБ — образ не поместится"

lsblk -o NAME,SIZE,MODEL,TRAN,MOUNTPOINTS "$dev"
printf 'Всё на %s будет стёрто. Введите %s для подтверждения: ' "$dev" "$name"
read -r ans
[ "$ans" = "$name" ] || die "отменено"

echo "[1/3] пишу образ на $dev ($(date +%H:%M); 40 ГиБ, несколько минут)…"
gzip -dc "$img" | dd of="$dev" bs=4M conv=fsync status=progress
sync
echo "[2/3] растягиваю раздел Windows на весь диск…"
sfdisk --relocate gpt-bak-std "$dev" >/dev/null
echo ', +' | sfdisk --no-reread -N 4 "$dev" >/dev/null
blockdev --rereadpt "$dev" || true
echo "[3/3] готово ($(date +%H:%M)):"
lsblk -o NAME,SIZE,LABEL,PARTLABEL "$dev"
p2=${dev}2
case $dev in *[0-9]) p2=${dev}p2 ;; esac
echo
echo "Загрузиться в Windows один раз (следующая загрузка снова в Linux):"
echo "  sudo mount $p2 /mnt && sudo sh /mnt/lolssd.sh boot"
echo "Первый вход — уже на сервере: Windows растянет диск C:, поставит драйвер видеокарты и"
echo "клиент Riot. Sunshine: https://<адрес сервера>:47990 — задать логин и пароль, связать Moonlight."
