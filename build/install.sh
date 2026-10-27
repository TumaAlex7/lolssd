#!/usr/bin/env bash
# lolssd: установить Windows из собранного ISO на диск, из Linux, через QEMU. Обычно его вызывает
# мастер (build/lolssd-setup).
#   sudo build/install.sh --whole /dev/sdX ISO [папка драйверов]   весь диск (стирается)
#   sudo build/install.sh --free  /dev/sdX ISO [папка драйверов]   самый большой свободный участок
#   sudo build/install.sh --whole образ.img ISO РАЗМЕР             в файл-образ (проверки), РАЗМЕР — 130G
# Нужно не меньше 120 ГБ: диски «на 128» бывают чуть меньше 128·10⁹ байт.
#
# Разметка: ESP (если на диске его нет), LOLSSD (exFAT: утилита для Linux, профили, драйверы),
# MSR и раздел Windows. Файл ответов ставит Windows именно на этот раздел — другие разделы
# установщик не трогает; язык, редакцию и ключ спрашивает он сам. Установка идёт в окне QEMU
# (без графики — VNC на 127.0.0.1:5909), со всеми перезагрузками внутри ВМ.
#
# Диск, на котором работает Linux (--free): ESP на время установки размонтируется — в него пишет
# установщик Windows, — а запасной загрузчик \EFI\BOOT\BOOTX64.EFI (часто systemd-boot или GRUB)
# переносится в сторону: иначе прошивка ВМ загрузила бы с этого ESP сам работающий Linux, вторую
# копию на той же файловой системе. После установки всё возвращается; файл-резерв лежит рядом
# (BOOTX64.EFI.lolssd), если скрипт прервут.
set -euo pipefail

die() { echo "ошибка: $*" >&2; exit 1; }
say() { echo "== $*"; }

[[ $# -ge 3 && ($1 == --whole || $1 == --free) ]] ||
	die "использование: sudo $0 --whole|--free /dev/sdX ISO [папка драйверов]"
mode=${1#--} dev=$2 iso=$(realpath "$3") extra=${4:-}
repo=$(cd "$(dirname "$0")/.." && pwd)
min=120000000000

[[ $EUID == 0 ]] || die "нужен root (sudo)"
[[ -n ${SUDO_USER:-} ]] || die "запускайте через sudo от своего пользователя: QEMU работает от его имени"
for t in qemu-system-x86_64 sfdisk mkfs.fat mkfs.exfat mkfs.ntfs swtpm xorriso 7z python3; do
	command -v "$t" >/dev/null ||
		die "нет $t (sudo pacman -S qemu-desktop edk2-ovmf dosfstools exfatprogs ntfs-3g swtpm libisoburn p7zip python)"
done
code=/usr/share/edk2/x64/OVMF_CODE.secboot.4m.fd vars=/usr/share/edk2/x64/OVMF_VARS.4m.fd
[[ -f $code && -f $vars ]] || die "нет OVMF ($code), sudo pacman -S edk2-ovmf"
[[ -f $iso && $(basename "$iso") == lolssd-* ]] || die "нужен ISO, собранный build/iso.sh (dist/iso/lolssd-*.iso)"
[[ -d $repo/dist/LOLSSD ]] || die "сначала make dist"
opts=$(mktemp)
7z e -so "$iso" lolssd-options.txt >"$opts" 2>/dev/null && [[ -s $opts ]] || die "в ISO нет lolssd-options.txt: соберите его заново"
opt() { sed -n "s/^$1=//p" "$opts" | tail -1; }

# Файл-образ вместо диска (проверки): создаётся и подключается как loop-устройство.
image=
if [[ $dev == *.img ]]; then
	[[ $mode == whole && ! -e $dev && $extra =~ ^[0-9]+G$ ]] || die "образ: --whole новый.img ISO РАЗМЕР (например 130G)"
	image=$dev extra=
	truncate -s "$4" "$image"
	dev=$(losetup --find --show --partscan "$image")
fi
name=$(basename "$dev")
[[ -b $dev && -e /sys/block/$name ]] || die "$dev — не диск целиком (нужен /dev/sdX или /dev/nvmeXnY, не раздел)"
[[ -z $extra || -d $extra ]] || die "нет папки с драйверами $extra"
part() { [[ $dev == *[0-9] ]] && echo "${dev}p$1" || echo "$dev$1"; }
mounted() { grep -q "^$1 " /proc/mounts; }
! grep -q "^/dev/$name" /proc/swaps || die "на $dev включён swap"

basic=EBD0A0A2-B9E5-4433-87C0-68B6B72699C7 msr=E3C9E316-0B5C-4DB8-817D-F92DF00215AE
esp_type=C12A7328-F81F-11D2-BA4B-00A0C93EC93B
lsblk -o NAME,SIZE,FSTYPE,LABEL,PARTLABEL,MOUNTPOINTS "$dev"
[[ -n $image || $(readlink -f "/sys/block/$name") == */usb* ]] ||
	echo "ВНИМАНИЕ: $dev — внутренний диск компьютера, не USB."

# ---------------------------------------------------------------- разметка
new_esp= new_lolssd=
if [[ $mode == whole ]]; then
	! grep -q "^/dev/$name" /proc/mounts || die "разделы $dev смонтированы: весь диск занятого диска не отдать"
	(( $(blockdev --getsize64 "$dev") >= min )) || die "$dev меньше 120 ГБ"
	if [[ -z $image ]]; then
		read -rp "Всё на $dev будет стёрто. Введите $name для подтверждения: " ans
		[[ $ans == "$name" ]] || die "отменено"
	fi
	say "размечаю $dev: ESP, LOLSSD, MSR, Windows — весь остаток"
	printf 'label: gpt\nsize=512MiB, type=%s, name=EFI\nsize=512MiB, type=%s, name=LOLSSD\nsize=16MiB, type=%s, name=MSR\ntype=%s, name=Windows\n' \
		"$esp_type" "$basic" "$msr" "$basic" | sfdisk -q --wipe always --wipe-partitions always "$dev"
	esp_num=1 lolssd_num=2 win_num=4 new_esp=1 new_lolssd=1
else
	sfdisk -d "$dev" >/dev/null 2>&1 || die "на $dev нет таблицы разделов: для пустого диска — весь диск"
	[[ $(sfdisk -d "$dev" | sed -n 's/^label: //p') == gpt ]] || die "на $dev разметка не GPT"
	ss=$(blockdev --getss "$dev")
	read -r start end < <(sfdisk -F -q "$dev" | awk 'NR>1 && $3>max {max=$3; s=$1; e=$2} END {print s, e}')
	[[ -n ${start:-} ]] || die "на $dev нет свободного места"
	esp_num=$(sfdisk -d "$dev" | awk -v t="$esp_type" -F'[ ,=:]+' 'toupper($0) ~ t {sub(/.*[^0-9]/,"",$1); print $1; exit}')
	lolssd_num=$(sfdisk -d "$dev" | grep 'name="LOLSSD"' | sed -E 's/^[^ ]*[^0-9]([0-9]+) .*/\1/' | head -1 || true)
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
	esp_at= lolssd_at=
	table=$(
		[[ -z $new_esp ]] || { esp_at=$at; echo "start=$at, size=$(( 512 * al )), type=$esp_type, name=EFI"; at=$(( at + 512 * al )); }
		[[ -z $new_lolssd ]] || { echo "start=$at, size=$(( 512 * al )), type=$basic, name=LOLSSD"; at=$(( at + 512 * al )); }
		echo "start=$at, size=$(( 16 * al )), type=$msr, name=MSR"; at=$(( at + 16 * al ))
		echo "start=$at, size=$(( (end + 1 - at) / al * al )), type=$basic, name=Windows"
	)
	echo "$table" | sfdisk -q --append --no-reread "$dev"
	partx -u "$dev" 2>/dev/null || partx -a "$dev" 2>/dev/null || true
	# Номера новых разделов — по их началу: sfdisk может занять свободную запись в середине таблицы.
	num_at() { sfdisk -d "$dev" | grep -E "start= *$1," | sed -E 's/^[^ ]*[^0-9]([0-9]+) .*/\1/' || true; }
	win_num=$(num_at "$(echo "$table" | sed -n 's/^start=\([0-9]*\),.*name=Windows$/\1/p')")
	[[ -n $esp_num ]] || esp_num=$(num_at "$(echo "$table" | sed -n 's/^start=\([0-9]*\),.*name=EFI$/\1/p')")
	[[ -n $lolssd_num ]] || lolssd_num=$(num_at "$(echo "$table" | sed -n 's/^start=\([0-9]*\),.*name=LOLSSD$/\1/p')")
fi
udevadm settle
sfdisk -d "$dev" | grep -q "^$(part "$win_num") .*name=\"Windows\"" || die "раздел Windows не на месте: $(part "$win_num")"
# Установщик Windows нумерует разделы по порядку занятых записей таблицы, без пропусков.
win_id=$(sfdisk -d "$dev" | grep '^/dev' | sed -E 's/^[^ ]*[^0-9]([0-9]+) .*/\1/' | awk -v n="$win_num" '$1 <= n' | wc -l)

[[ -z $new_esp ]] || mkfs.fat -F 32 -n EFI "$(part "$esp_num")" >/dev/null
[[ -z $new_lolssd ]] || mkfs.exfat -q -L LOLSSD "$(part "$lolssd_num")" >/dev/null
# Начало раздела и геометрия — в загрузочный сектор NTFS: на loop-устройстве mkfs.ntfs их не узнаёт сам.
mkfs.ntfs --quick --label Windows -p "$(cat "/sys/class/block/$(basename "$(part "$win_num")")/start")" -H 255 -S 63 \
	"$(part "$win_num")" >/dev/null

say "LOLSSD: утилита для Linux${extra:+ и драйверы}"
mnt=$(mktemp -d)
mounted "$(part "$lolssd_num")" && die "LOLSSD смонтирован — отмонтируйте"
mount "$(part "$lolssd_num")" "$mnt"
cp -r "$repo/dist/LOLSSD/." "$mnt/"
[[ -z $extra ]] || cp -r "$extra" "$mnt/drivers/"
umount "$mnt"

# ---------------------------------------------------------------- ESP хоста
esp_mp= esp_opts= fallback=
esp_dev=$(part "$esp_num")
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
if mounted "$esp_dev"; then
	esp_mp=$(findmnt -no TARGET "$esp_dev" | head -1) esp_opts=$(findmnt -no OPTIONS "$esp_dev" | head -1)
	say "ESP хоста ($esp_mp) — размонтирую на время установки"
	umount "$esp_dev"
fi
trap 'restore; [[ -z $image ]] || losetup -d "$dev"; rm -rf "$mnt" "$opts" "${work:-}"' EXIT
mount "$esp_dev" "$mnt"
if [[ -f $mnt/EFI/BOOT/BOOTX64.EFI ]]; then
	say "переношу \\EFI\\BOOT\\BOOTX64.EFI в BOOTX64.EFI.lolssd на время установки"
	mv "$mnt/EFI/BOOT/BOOTX64.EFI" "$mnt/EFI/BOOT/BOOTX64.EFI.lolssd"
	fallback=1
fi
umount "$mnt"

# ---------------------------------------------------------------- файл ответов
work=$(mktemp -d)
mkdir "$work/answer" "$work/tpm"
cp "$vars" "$work/OVMF_VARS.fd"
python3 -I - "$repo/build/setup/autounattend.xml" "$work/answer/autounattend.xml" "$win_id" "$(opt user)" "$(opt riot)" "${LOLSSD_TEST_KEY:-}" <<'PY'
import re, sys
src, dst, part, user, riot, key = sys.argv[1:7]
s = open(src, encoding="utf-8").read()
def sub(pat, rep, flags=re.S):
    global s
    s, n = re.subn(pat, rep, s, count=1, flags=flags)
    assert n == 1, pat
# Раздел Windows: диск в ВМ один (DiskID 0), раздел — тот, что создал install.sh.
sub(r"(      <UserData>\s*<AcceptEula>true</AcceptEula>)", r"\1" + (
    "\n        <ProductKey><Key>" + key + "</Key></ProductKey>" if key else ""))
sub(r"(      </UserData>\n)", r"\1      <ImageInstall>\n        <OSImage>\n          <InstallTo>\n"
    "            <DiskID>0</DiskID>\n            <PartitionID>" + part + "</PartitionID>\n"
    "          </InstallTo>\n          <WillShowUI>OnError</WillShowUI>\n        </OSImage>\n      </ImageInstall>\n")
if key:  # проверки без вопросов: язык установщика
    sub(r'(  <settings pass="windowsPE">\n)', r'\1    <component name="Microsoft-Windows-International-Core-WinPE" processorArchitecture="amd64" publicKeyToken="31bf3856ad364e35" language="neutral" versionScope="nonSxS">\n      <SetupUILanguage><UILanguage>en-US</UILanguage></SetupUILanguage>\n      <InputLocale>en-US</InputLocale><SystemLocale>en-US</SystemLocale><UILanguage>en-US</UILanguage><UserLocale>en-US</UserLocale>\n    </component>\n')
if user:
    s = s.replace("<Name>lol</Name>", "<Name>" + user + "</Name>").replace("<Username>lol</Username>", "<Username>" + user + "</Username>")
else:  # учётку создаёт установщик Windows
    sub(r"\s*<UserAccounts>.*?</UserAccounts>", "")
    sub(r"\s*<AutoLogon>.*?</AutoLogon>", "")
    sub(r"\s*<HideOnlineAccountScreens>true</HideOnlineAccountScreens>", "")
if riot != "1" or key:
    sub(r"\s*<FirstLogonCommands>.*?</FirstLogonCommands>", "")
if key:  # проверки: после первого входа Windows выключается сама, и QEMU завершается
    sub(r"(      </OOBE>\n)", r'\1      <FirstLogonCommands>\n        <SynchronousCommand wcm:action="add">\n          <Order>1</Order>\n          <CommandLine>shutdown.exe /s /t 60</CommandLine>\n        </SynchronousCommand>\n      </FirstLogonCommands>\n')
open(dst, "w", encoding="utf-8").write(s)
PY
xorriso -as mkisofs -quiet -J -V ANSWER -o "$work/answer.iso" "$work/answer"
chown -R "$SUDO_USER" "$work"
setfacl -m "u:$SUDO_USER:rw" "$dev"
trap 'setfacl -x "u:$SUDO_USER" "$dev" 2>/dev/null; restore; [[ -z $image ]] || losetup -d "$dev"; rm -rf "$mnt" "$opts" "$work"' EXIT

# ---------------------------------------------------------------- установка
# swtpm живёт, пока к нему подключён QEMU, и завершается вместе с ним (--terminate).
sudo -u "$SUDO_USER" swtpm socket --tpm2 --tpmstate dir="$work/tpm" \
	--ctrl type=unixio,path="$work/tpm/sock" --terminate --daemon
display=()
if [[ -n ${LOLSSD_HEADLESS:-} ]] || ! sudo -u "$SUDO_USER" --preserve-env=WAYLAND_DISPLAY,DISPLAY sh -c '[ -n "$WAYLAND_DISPLAY$DISPLAY" ]'; then
	display=(-display none -vnc 127.0.0.1:9)
	echo "Графики нет: экран установки — VNC 127.0.0.1:5909 (с другого компьютера: ssh -L 5909:127.0.0.1:5909)."
fi
say "запускаю установку в QEMU ($(date +%H:%M)), обычно 20–40 минут"
echo "В установщике: язык, «У меня нет ключа продукта», редакция (та, что была на компьютере)."
echo "Раздел выбран заранее. Дальше всё само: несколько перезагрузок, первый вход$( [[ $(opt riot) != 1 || -n ${LOLSSD_TEST_KEY:-} ]] || echo ", установка Riot")."
echo "Когда Windows готова, выключите её: Пуск → Завершение работы. Тогда этот скрипт закончит."
# Диск первым в порядке загрузки, ISO вторым: в ISO нет «Press any key to boot from CD», и с ISO
# первым каждая перезагрузка начинала бы установку заново. На первом запуске загрузчика Windows
# на ESP ещё нет (а BOOTX64.EFI убран), и прошивка переходит к ISO. Файл ответов — на втором CD:
# установщик ищет autounattend.xml в корне всех носителей. Диск по AHCI, сеть e1000e: драйверы
# для обоих есть в Windows из коробки.
sudo -u "$SUDO_USER" --preserve-env=WAYLAND_DISPLAY,DISPLAY,XDG_RUNTIME_DIR \
	qemu-system-x86_64 -enable-kvm -machine q35,smm=on -cpu host -smp 4 -m 6G \
	-global driver=cfi.pflash01,property=secure,value=on \
	-chardev socket,id=tpm,path="$work/tpm/sock" -tpmdev emulator,id=tpm0,chardev=tpm -device tpm-crb,tpmdev=tpm0 \
	-drive if=pflash,format=raw,readonly=on,file="$code" \
	-drive if=pflash,format=raw,file="$work/OVMF_VARS.fd" \
	-device ahci,id=ahci \
	-drive if=none,id=disk,format=raw,cache=none,file="$dev" -device ide-hd,drive=disk,bus=ahci.0,bootindex=0 \
	-drive if=none,id=cd,media=cdrom,file="$iso" -device ide-cd,drive=cd,bus=ahci.1,bootindex=1 \
	-drive if=none,id=answer,media=cdrom,file="$work/answer.iso" -device ide-cd,drive=answer,bus=ahci.2 \
	-device qemu-xhci -device usb-tablet -nic user,model=e1000e "${display[@]}"

say "QEMU закрыт ($(date +%H:%M))"
restore
partx -u "$dev" 2>/dev/null || true
if [[ -n $image ]]; then
	chown "$SUDO_USER" "$image"
	echo "Образ: $image"
	exit 0
fi
echo "Готово. Загрузиться в Windows:"
if [[ $mode == free && -n $esp_mp ]]; then
	echo "  в меню загрузчика Linux (systemd-boot находит Windows на том же ESP сам) или"
fi
echo "  sudo /run/media/\$USER/LOLSSD/lolssd boot — один раз, следующая загрузка снова в Linux."
