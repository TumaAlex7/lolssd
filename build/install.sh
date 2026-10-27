#!/usr/bin/env bash
# lolssd: установить Windows со стокового ISO на диск, из Linux, через QEMU. Обычно его вызывает
# мастер (lolssd-setup): он распаковывает свои файлы (payload) и пишет в них options.txt.
#   sudo install.sh --whole /dev/sdX Windows.iso PAYLOAD [папка драйверов]   весь диск (стирается)
#   sudo install.sh --free  /dev/sdX Windows.iso PAYLOAD [папка драйверов]   самый большой свободный участок
#   install.sh --whole образ.img Windows.iso PAYLOAD РАЗМЕР                   в файл-образ, без root (проверки)
# Нужно не меньше 120 ГБ: диски «на 128» бывают чуть меньше 128·10⁹ байт. Из пакетов — только
# QEMU, OVMF, swtpm и util-linux.
#
# Разметка (sfdisk): ESP (если на диске его нет), LOLSSD, MSR и раздел Windows. Форматирует их не
# Linux: ESP и раздел Windows — установщик Windows по файлу ответов, LOLSSD в exFAT — сама Windows
# в конце установки (lolssd-setup.ps1); утилиту lolssd и драйверы на LOLSSD этот скрипт копирует
# после установки (exFAT ядро Linux монтирует само). Другие разделы установщик не трогает; язык,
# редакцию и ключ спрашивает он сам.
#
# Наши файлы и файл ответов — на «конфигурационном диске»: папке, которую QEMU показывает Windows
# как флешку FAT (vvfat, до ~500 МБ). Стоковый ISO при загрузке ждёт «Press any key to boot from
# CD» — скрипт в первые секунды нажимает Enter через монитор QEMU.
#
# Диск, на котором работает Linux (--free): ESP на время установки размонтируется — в него пишет
# установщик Windows, — а запасной загрузчик \EFI\BOOT\BOOTX64.EFI (часто systemd-boot или GRUB)
# переносится в сторону: иначе прошивка ВМ загрузила бы с этого ESP сам работающий Linux, вторую
# копию на той же файловой системе. После установки всё возвращается; файл-резерв лежит рядом
# (BOOTX64.EFI.lolssd), если скрипт прервут.
set -euo pipefail

die() { echo "ошибка: $*" >&2; exit 1; }
say() { echo "== $*"; }

[[ $# -ge 4 && ($1 == --whole || $1 == --free) ]] ||
	die "использование: sudo $0 --whole|--free /dev/sdX Windows.iso PAYLOAD [папка драйверов]"
mode=${1#--} dev=$2 iso=$(realpath "$3") payload=$(realpath "$4") extra=${5:-}
min=120000000000
opts=$payload/options.txt
[[ -f $opts && -f $payload/autounattend.xml ]] || die "$payload — не набор файлов lolssd (нет options.txt)"
opt() { sed -n "s/^$1=//p" "$opts" | tail -1; }
[[ -f $iso ]] || die "нет $iso"

for t in qemu-system-x86_64 swtpm sfdisk; do
	command -v "$t" >/dev/null || die "нет $t: поставьте QEMU, OVMF и swtpm (мастер подскажет команду)"
done
code= vars=
for c in /usr/share/edk2/x64/OVMF_CODE.secboot.4m.fd:/usr/share/edk2/x64/OVMF_VARS.4m.fd \
	/usr/share/OVMF/OVMF_CODE_4M.secboot.fd:/usr/share/OVMF/OVMF_VARS_4M.fd \
	/usr/share/edk2/ovmf/OVMF_CODE.secboot.fd:/usr/share/edk2/ovmf/OVMF_VARS.fd \
	/usr/share/OVMF/OVMF_CODE.secboot.fd:/usr/share/OVMF/OVMF_VARS.fd; do
	if [[ -f ${c%%:*} && -f ${c#*:} ]]; then code=${c%%:*} vars=${c#*:}; break; fi
done
[[ -n $code ]] || die "нет прошивки OVMF с Secure Boot (пакет edk2-ovmf или ovmf)"

# Файл-образ (проверки): размечается и отдаётся QEMU как есть, root не нужен.
image=
if [[ ! -b $dev ]]; then
	[[ $mode == whole && ! -e $dev && $extra =~ ^[0-9]+G$ ]] || die "образ: --whole новый.img ISO PAYLOAD РАЗМЕР (например 130G)"
	image=$dev
	truncate -s "$extra" "$image"
	extra=
	user=$(id -un)
else
	[[ $EUID == 0 && -n ${SUDO_USER:-} ]] || die "для диска нужен root: sudo $0 … (QEMU запускается от вашего пользователя)"
	user=$SUDO_USER
	name=$(basename "$dev")
	[[ -e /sys/block/$name ]] || die "$dev — не диск целиком (нужен /dev/sdX или /dev/nvmeXnY, не раздел)"
	! grep -q "^/dev/$name" /proc/swaps || die "на $dev включён swap"
	lsblk -o NAME,SIZE,FSTYPE,LABEL,PARTLABEL,MOUNTPOINTS "$dev"
	[[ $(readlink -f "/sys/block/$name") == */usb* ]] || echo "ВНИМАНИЕ: $dev — внутренний диск компьютера, не USB."
fi
[[ -z $extra || -d $extra ]] || die "нет папки с драйверами $extra"
part() { [[ $dev == *[0-9] ]] && echo "${dev}p$1" || echo "$dev$1"; }
mounted() { grep -q "^$1 " /proc/mounts; }
size() { [[ -n $image ]] && stat -c %s "$image" || blockdev --getsize64 "$dev"; }
num() { sed -E 's/^[^ ]*[^0-9]([0-9]+) .*/\1/'; }

basic=EBD0A0A2-B9E5-4433-87C0-68B6B72699C7 msr=E3C9E316-0B5C-4DB8-817D-F92DF00215AE
esp_type=C12A7328-F81F-11D2-BA4B-00A0C93EC93B

# ---------------------------------------------------------------- разметка
new_esp= new_lolssd=
if [[ $mode == whole ]]; then
	[[ -n $image ]] || ! grep -q "^/dev/$name" /proc/mounts || die "разделы $dev смонтированы: весь диск занятого диска не отдать"
	(( $(size) >= min )) || die "$dev меньше 120 ГБ"
	if [[ -z $image ]]; then
		read -rp "Всё на $dev будет стёрто. Введите $name для подтверждения: " ans
		[[ $ans == "$name" ]] || die "отменено"
	fi
	say "размечаю: ESP, LOLSSD, MSR, Windows — весь остаток"
	printf 'label: gpt\nsize=512MiB, type=%s, name=EFI\nsize=512MiB, type=%s, name=LOLSSD\nsize=16MiB, type=%s, name=MSR\ntype=%s, name=Windows\n' \
		"$esp_type" "$basic" "$msr" "$basic" | sfdisk -q --wipe always --wipe-partitions always "$dev"
	esp_num=1 lolssd_num=2 win_num=4 new_esp=1 new_lolssd=1
else
	sfdisk -d "$dev" >/dev/null 2>&1 || die "на $dev нет таблицы разделов: для пустого диска — весь диск"
	[[ $(sfdisk -d "$dev" | sed -n 's/^label: //p') == gpt ]] || die "на $dev разметка не GPT"
	ss=$(blockdev --getss "$dev")
	read -r start end < <(sfdisk -F -q "$dev" | awk 'NR>1 && $3>max {max=$3; s=$1; e=$2} END {print s, e}')
	[[ -n ${start:-} ]] || die "на $dev нет свободного места"
	esp_num=$(sfdisk -d "$dev" | grep -i "type=$esp_type" | num | head -1 || true)
	lolssd_num=$(sfdisk -d "$dev" | grep 'name="LOLSSD"' | num | head -1 || true)
	need=$min
	[[ -n $esp_num ]] || { need=$(( need + 512 * 1048576 )); new_esp=1; }
	[[ -n $lolssd_num ]] || { need=$(( need + 512 * 1048576 )); new_lolssd=1; }
	(( (end - start + 1) * ss >= need + 16 * 1048576 )) ||
		die "самый большой свободный участок $(( (end - start + 1) * ss / 1000000000 )) ГБ, нужно не меньше 120 ГБ"
	echo "Свободный участок: $(( (end - start + 1) * ss / 1000000000 )) ГБ (сектора $start–$end)."
	echo "Будет создано:${new_esp:+ ESP 512 МиБ,}${new_lolssd:+ LOLSSD 512 МиБ,} MSR 16 МиБ, Windows — остаток участка."
	echo "Существующие разделы не меняются."
	read -rp "Продолжить? [y/N] " ans
	[[ $ans == [yY] ]] || die "отменено"
	al=$(( 1048576 / ss ))
	at=$(( (start + al - 1) / al * al ))
	table=$(
		[[ -z $new_esp ]] || { echo "start=$at, size=$(( 512 * al )), type=$esp_type, name=EFI"; at=$(( at + 512 * al )); }
		[[ -z $new_lolssd ]] || { echo "start=$at, size=$(( 512 * al )), type=$basic, name=LOLSSD"; at=$(( at + 512 * al )); }
		echo "start=$at, size=$(( 16 * al )), type=$msr, name=MSR"; at=$(( at + 16 * al ))
		echo "start=$at, size=$(( (end + 1 - at) / al * al )), type=$basic, name=Windows"
	)
	echo "$table" | sfdisk -q --append --no-reread "$dev"
	partx -u "$dev" 2>/dev/null || partx -a "$dev" 2>/dev/null || true
	# Номера новых разделов — по их началу: sfdisk может занять свободную запись в середине таблицы.
	num_at() { sfdisk -d "$dev" | grep -E "start= *$1," | num || true; }
	at_of() { echo "$table" | sed -n "s/^start=\([0-9]*\),.*name=$1\$/\1/p"; }
	win_num=$(num_at "$(at_of Windows)")
	[[ -n $esp_num ]] || esp_num=$(num_at "$(at_of EFI)")
	[[ -n $lolssd_num ]] || lolssd_num=$(num_at "$(at_of LOLSSD)")
fi
[[ -n $image ]] || udevadm settle
table=$(sfdisk -d "$dev")
echo "$table" | grep -E "^[^ ]*[^0-9]$win_num : .*name=\"Windows\"" >/dev/null || die "раздел Windows не на месте ($win_num)"
lolssd_guid=$(echo "$table" | grep -E "^[^ ]*[^0-9]$lolssd_num : " | sed -n 's/.*uuid=\([0-9A-Fa-f-]*\).*/\1/p')
# Установщик Windows нумерует разделы по порядку занятых записей таблицы, без пропусков.
id_of() { echo "$table" | grep '^/' | num | awk -v n="$1" '$1 <= n' | wc -l; }
win_id=$(id_of "$win_num") esp_id=$(id_of "$esp_num")

# ---------------------------------------------------------------- ESP хоста
mnt=$(mktemp -d)
esp_mp= esp_opts= fallback=
esp_dev=$( [[ -n $image ]] || part "$esp_num")
restore() {
	if [[ -n $fallback ]]; then
		mount "$esp_dev" "$mnt"
		mv -f "$mnt/EFI/BOOT/BOOTX64.EFI.lolssd" "$mnt/EFI/BOOT/BOOTX64.EFI" && echo "вернул \\EFI\\BOOT\\BOOTX64.EFI"
		umount "$mnt"
		fallback=
	fi
	if [[ -n $esp_mp ]]; then
		mount -o "$esp_opts" "$esp_dev" "$esp_mp" && echo "ESP снова смонтирован в $esp_mp"
		esp_mp=
	fi
}
work=$(mktemp -d)
trap 'restore; rm -rf "$mnt" "$work"' EXIT
if [[ -z $image && -z $new_esp ]]; then
	if mounted "$esp_dev"; then
		esp_mp=$(findmnt -no TARGET "$esp_dev" | head -1) esp_opts=$(findmnt -no OPTIONS "$esp_dev" | head -1)
		say "ESP хоста ($esp_mp) — размонтирую на время установки"
		umount "$esp_dev"
	fi
	mount "$esp_dev" "$mnt"
	if [[ -f $mnt/EFI/BOOT/BOOTX64.EFI ]]; then
		say "переношу \\EFI\\BOOT\\BOOTX64.EFI в BOOTX64.EFI.lolssd на время установки"
		mv "$mnt/EFI/BOOT/BOOTX64.EFI" "$mnt/EFI/BOOT/BOOTX64.EFI.lolssd"
		fallback=1
	fi
	umount "$mnt"
fi

# ---------------------------------------------------------------- конфигурационный диск
test=${LOLSSD_TEST:-}
cfg=$work/cfg
mkdir -p "$cfg/lolssd" "$work/tpm"
cp -r "$payload/windows/." "$cfg/lolssd/"
cp "$opts" "$cfg/lolssd/options.txt"
printf 'lolssd_new=%s\nlolssd_guid=%s\n' "${new_lolssd:-0}" "$lolssd_guid" >"$cfg/lolssd/install.txt"
cut=()
[[ -n $new_esp ]] || cut+=(ESP)
[[ -n $(opt user) ]] || cut+=(USER)
[[ $(opt riot) == 1 && -z $test ]] || cut+=(RIOT)
[[ -n $test ]] || cut+=(TEST)
sedargs=(-e "s/@WIN@/$win_id/g" -e "s/@ESP@/$esp_id/g" -e "s/@USER@/$(opt user)/g")
for c in "${cut[@]}"; do sedargs+=(-e "/<!--$c-BEGIN-->/,/<!--$c-END-->/d"); done
sed "${sedargs[@]}" -e '/<!--[A-Z]*-\(BEGIN\|END\)-->/d' "$payload/autounattend.xml" >"$cfg/autounattend.xml"
(( $(du -sm "$cfg" | cut -f1) < 480 )) || die "конфигурационный диск больше 480 МБ"
cp "$vars" "$work/OVMF_VARS.fd"
chown -R "$user" "$work"
[[ -n $image ]] || setfacl -m "u:$user:rw" "$dev"
trap '[[ -n $image ]] || setfacl -x "u:$user" "$dev" 2>/dev/null; restore; rm -rf "$mnt" "$work"' EXIT

# ---------------------------------------------------------------- установка
as_user() { if [[ -n $image ]]; then "$@"; else sudo -u "$user" --preserve-env=WAYLAND_DISPLAY,DISPLAY,XDG_RUNTIME_DIR "$@"; fi; }
# swtpm живёт, пока к нему подключён QEMU, и завершается вместе с ним (--terminate).
as_user swtpm socket --tpm2 --tpmstate dir="$work/tpm" --ctrl type=unixio,path="$work/tpm/sock" --terminate --daemon
display=()
if [[ -n ${LOLSSD_HEADLESS:-} ]] || ! as_user sh -c '[ -n "$WAYLAND_DISPLAY$DISPLAY" ]'; then
	display=(-display none -vnc 127.0.0.1:9)
	echo "Графики нет: экран установки — VNC 127.0.0.1:5909 (с другого компьютера: ssh -L 5909:127.0.0.1:5909)."
fi
monitor=$(( 55900 + RANDOM % 1000 ))
say "запускаю установку в QEMU ($(date +%H:%M)), обычно 20–40 минут"
[[ -n $test ]] || echo "В установщике: язык, «У меня нет ключа продукта», редакция (та, что была на компьютере)."
echo "Раздел выбран заранее. Дальше всё само: несколько перезагрузок, скачивание компонентов, первый вход$( [[ $(opt riot) != 1 || -n $test ]] || echo ", установка Riot")."
[[ -n $test ]] || echo "Когда Windows готова, выключите её: Пуск → Завершение работы. Тогда этот скрипт закончит."
# Стоковый ISO ждёт клавишу («Press any key to boot from CD»): Enter через монитор QEMU, первые
# 25 секунд. Потом загрузка идёт с диска: диск первым в порядке загрузки, а на первом запуске
# загрузчика Windows на ESP ещё нет (и BOOTX64.EFI убран) — прошивка переходит к ISO.
(
	for _ in $(seq 25); do
		sleep 1
		{ exec 3<>"/dev/tcp/127.0.0.1/$monitor" && echo 'sendkey ret' >&3 && exec 3>&-; } 2>/dev/null || true
	done
) &
# Диск по AHCI, сеть e1000e: драйверы для обоих есть в Windows из коробки.
as_user qemu-system-x86_64 -enable-kvm -machine q35,smm=on -cpu host -smp 4 -m 6G \
	-global driver=cfi.pflash01,property=secure,value=on \
	-chardev socket,id=tpm,path="$work/tpm/sock" -tpmdev emulator,id=tpm0,chardev=tpm -device tpm-crb,tpmdev=tpm0 \
	-drive if=pflash,format=raw,readonly=on,file="$code" \
	-drive if=pflash,format=raw,file="$work/OVMF_VARS.fd" \
	-device ahci,id=ahci \
	-drive if=none,id=disk,format=raw,cache=none,file="$dev" -device ide-hd,drive=disk,bus=ahci.0,bootindex=0 \
	-drive if=none,id=cd,media=cdrom,readonly=on,file="$iso" -device ide-cd,drive=cd,bus=ahci.1,bootindex=1 \
	-device qemu-xhci -drive if=none,id=cfg,format=raw,readonly=on,file="fat:$cfg" -device usb-storage,drive=cfg,removable=on \
	-device usb-tablet -nic user,model=e1000e -monitor "tcp:127.0.0.1:$monitor,server,nowait" "${display[@]}"

say "QEMU закрыт ($(date +%H:%M))"
restore
if [[ -n $image ]]; then
	echo "Образ: $image (LOLSSD в нём без утилиты: её копирует установка на диск)"
	exit 0
fi
partx -u "$dev" 2>/dev/null || true
udevadm settle
say "LOLSSD: утилита для Linux${extra:+ и драйверы}"
if mount -t exfat "$(part "$lolssd_num")" "$mnt" 2>/dev/null; then
	mkdir -p "$mnt/profiles" "$mnt/drivers"
	cp -r "$payload/LOLSSD/." "$mnt/"
	[[ -z $extra ]] || cp -r "$extra" "$mnt/drivers/"
	umount "$mnt"
else
	echo "ВНИМАНИЕ: LOLSSD не exFAT — установка Windows не дошла до конца? Утилиту можно положить позже."
fi
echo "Готово. Загрузиться в Windows:"
[[ -z $esp_mp ]] || echo "  в меню загрузчика Linux (systemd-boot находит Windows на том же ESP сам) или"
echo "  sudo /run/media/\$USER/LOLSSD/lolssd boot — один раз, следующая загрузка снова в Linux."
