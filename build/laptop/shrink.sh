#!/usr/bin/env bash
# lolssd laptop: free space for Windows at the end of the disk by shrinking the root ext4 — no
# live USB: a separate initramfs (initcpio/) does it before the root is mounted, on one boot.
#   sudo build/laptop/shrink.sh [Windows GiB, default 100]    then reboot once
#   sudo build/laptop/shrink.sh --cleanup                     afterwards: remove the boot entry
# The free space holds Windows plus its MSR (16 MiB) and recovery (~1 GiB); setup creates them.
# The usual initramfs-linux.img and mkinitcpio.conf stay untouched.
set -euo pipefail

die() { echo "ошибка: $*" >&2; exit 1; }
here=$(cd "$(dirname "$0")" && pwd)
[[ $(id -u) == 0 ]] || die "нужен root (sudo)"
esp=$(bootctl --print-esp-path 2>/dev/null) || die "не найден ESP (systemd-boot)"
entry=$esp/loader/entries/lolshrink.conf
img=$esp/initramfs-lolshrink.img

if [[ ${1:-} == --cleanup ]]; then
	rm -f "$entry" "$img"
	echo "Пункт загрузки и initramfs для ужатия удалены. Разметка:"
	lsblk -o NAME,SIZE,FSTYPE,LABEL "$(lsblk -no PKNAME "$(findmnt -no SOURCE /)" | sed 's|^|/dev/|')"
	exit 0
fi

win_gib=${1:-100}
[[ $win_gib =~ ^[0-9]+$ ]] && (( win_gib >= 64 )) || die "размер Windows — целое число ГиБ, не меньше 64"

root=$(findmnt -no SOURCE /)
[[ $(findmnt -no FSTYPE /) == ext4 ]] || die "корень не ext4"
part=$(basename "$root")
sys=/sys/class/block/$part
[[ -e $sys/partition ]] || die "$root — не раздел"
disk=$(basename "$(readlink -f "$sys/..")")
[[ $(cat "/sys/block/$disk/queue/logical_block_size") == 512 ]] || die "сектор диска не 512 байт"
start=$(cat "$sys/start") cur=$(cat "$sys/size")
for p in /sys/block/"$disk"/"$disk"*; do
	[[ -e $p/start ]] || continue
	(( $(cat "$p/start") <= start )) || die "после $root есть ещё раздел ($(basename "$p")): место в конце не освободить"
done

gib=2097152 # секторов в ГиБ
new=$(( (cur - (win_gib + 1) * gib - 32768) / 2048 * 2048 ))
used=$(df -B1 --output=used / | tail -1)
(( new * 512 >= used + 20 * 1024 ** 3 )) ||
	die "Linux занято $(( used / 1024 ** 3 )) ГиБ, после ужатия останется $(( new / gib )) ГиБ — меньше, чем нужно с запасом 20 ГиБ"

echo "Корень $root: $(( cur / gib )) ГиБ → $(( new / gib )) ГиБ (занято $(( used / 1024 ** 3 )) ГиБ)."
echo "Освободится $(( (cur - new) / gib )) ГиБ в конце диска под Windows."
read -rp "Продолжить? [y/N] " ans
[[ $ans == [yY] ]] || die "отменено"

echo "[1/3] собираю initramfs с resize2fs (около минуты)…"
conf=$(mktemp)
trap 'rm -f "$conf"' EXIT
{ cat /etc/mkinitcpio.conf; echo 'HOOKS+=(lolshrink)'; } >"$conf"
dirs=(-D /usr/lib/initcpio -D "$here/initcpio")
[[ ! -d /etc/initcpio ]] || dirs=(-D /etc/initcpio "${dirs[@]}")
mkinitcpio "${dirs[@]}" -c "$conf" -k "$esp/vmlinuz-linux" -g "$img" >/dev/null

echo "[2/3] пункт загрузки «Shrink Linux for Windows» (только на одну загрузку)…"
default=$(sed -n 's/^default[[:space:]]\+//p' "$esp/loader/loader.conf" | sed 's/\.conf$//')
base=$esp/loader/entries/${default:-arch}.conf
[[ -f $base ]] || die "нет $base"
sed -e 's/^title .*/title Shrink Linux for Windows/' -e 's|^initrd[[:space:]]\+/initramfs-linux\.img|initrd /initramfs-lolshrink.img|' \
	-e "s|^options .*|& lolssd.shrink=$part:$new|" "$base" >"$entry"
grep -q initramfs-lolshrink "$entry" && grep -q lolssd.shrink "$entry" || die "не получилось собрать $entry из $base"

echo "[3/3] следующая загрузка — в него…"
bootctl set-oneshot lolshrink.conf
echo
echo "Готово. Перезагрузись: на экране будут строки lolssd-shrink (проверка, ужатие — до 20 минут,"
echo "не выключай), потом загрузится обычный Linux. После этого: sudo $0 --cleanup"
