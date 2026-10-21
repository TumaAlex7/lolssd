# Прототип: первая установка на SSD

Цель — за один прогон ответить на главные вопросы проекта, прежде всего: **запускается ли лолка
под Vanguard с внешнего SSD.** Всё, что помечено в ARCHITECTURE.md как «[проверить]», собрано
здесь в порядке, в котором до этого доходят руки. Результаты — в таблицу в конце.

**Итог первого прогона (стоковая Windows, наши настройки не применились):** Vanguard не
ругается, тренировка запускается. Остальное переделано: всё теперь собирается в один ISO.

Windows ставится на SSD из Linux через QEMU: SSD подключается к виртуалке как обычный диск,
и установщик Windows не знает, что это USB. Установщик — ISO, собранный `make iso`: в нём одна
редакция, почищенный образ, наши файлы, драйверы, WSL и файл ответов.

> **Важно про перезагрузки.** Порядок загрузки ноутбука не меняется: **любая перезагрузка
> Windows приводит в Linux.** После установки Vanguard и т. п. снова
> `sudo sh .../lolssd.sh boot` (или меню загрузки прошивки, у ASUS — Esc/F8 при включении).

Пути ниже: `/run/media/$USER/LOLSSD` — раздел LOLSSD в Linux (смонтирует udisks).

---

## 0. Подготовка

1. Пакеты:
   ```
   sudo pacman -S --needed qemu-desktop edk2-ovmf dosfstools acl swtpm ntfs-3g p7zip wimlib libisoburn
   ```
2. ISO Windows 11 с https://www.microsoft.com/software-download/windows11 (язык любой).
3. **Драйверы** — INF-пакеты (папки с `.inf`, `.sys`, `.cat`), не установщики, в одной папке,
   например `~/lolssd-drivers`. Фирменные `.exe` обычно распаковываются: `7z x файл.exe`.
   Попадут в образ в `C:\Drivers`; при первой загрузке на компьютере их поставит задание
   `\lolssd\drivers`. У автора: Wi-Fi MediaTek MT7922 и AMD 680M со страницы ASUS M5506NA.
4. Сборка (`make wsl` — пусковой WSL-дистрибутив на Debian и MSI WSL; без Docker и без sudo):
   ```
   cd ~/repos/lolssd
   make wsl
   make iso ISO=~/Downloads/Windows11_*.iso DRIVERS=~/lolssd-drivers
   ```
   Редакция — `EDITION="Windows 11 Pro"`, по умолчанию Home. Берите ту, что была на ноутбуке:
   ключа в прошивке (таблица `/sys/firmware/acpi/tables/MSDM`) у автора нет, лицензия цифровая,
   привязана к железу, и Windows той же редакции активируется сама при выходе в интернет.
5. **Снимок хоста до первой загрузки с SSD** — потом сравним, не тронула ли Windows ноутбук
   (у автора уже есть в `~/lolssd-proto`):
   ```
   mkdir -p ~/lolssd-proto && cd ~/lolssd-proto
   sudo efibootmgr -v > nvram-before.txt
   sudo find /boot -type f -exec sha256sum {} + | sort -k2 > esp-before.txt
   timedatectl > time-before.txt
   ```

---

## 1. Установка Windows в QEMU

1. Подключить SSD, найти его: `lsblk -o NAME,SIZE,MODEL,TRAN` (у него `TRAN=usb`).
2. Запустить (сотрёт диск, спросит подтверждение; откажется работать с не-USB диском):
   ```
   sudo build/prototype/vm-install.sh /dev/sdX dist/iso/lolssd-*.iso 128G
   ```
   Последний аргумент — сколько отдать под Windows; остаток диска станет разделом `DATA` (NTFS).
3. В окне QEMU: язык → «Установить» → **«У меня нет ключа продукта»** (если спросит) →
   «Выборочная установка» → **единственное незанятое место** → Далее. Дальше без вопросов:
   - до OOBE применяется `lolssd.reg`, локальная учётка `lol` создаётся сама, без Microsoft;
   - до первого входа `SetupComplete.cmd` удаляет приложения магазина (через DISM), ставит WSL,
     регистрирует задания, создаёт ярлыки Linux; журнал `C:\Windows\Setup\Scripts\lolssd-setup.log`;
   - при первом входе открывается окно `lolssd-riot.ps1` и запускает установщик Riot: Riot
     Client с игрой (~25 ГБ) качается в виртуалке. Дождаться, пока Riot Client предложит играть.
4. Пуск → Завершение работы. QEMU закроется → таблица, строки 1–3, 9.

---

## 2. Первая загрузка ноутбука с SSD

1. В Linux, SSD подключён к порту **USB 10 Гбит/с**:
   ```
   sh /run/media/$USER/LOLSSD/lolssd.sh check
   ```
   Строки `Порт USB` и `UASP` → таблица 4. (Тут же видно состояние Secure Boot.)
2. Подготовить загрузку и посмотреть, что легло в NVRAM:
   ```
   sudo sh /run/media/$USER/LOLSSD/lolssd.sh boot --no-reboot
   sudo efibootmgr -v | grep -iE 'lolssd|BootNext'
   systemctl reboot
   ```
   → таблица 5.
3. Если вместо Windows загрузился Linux — запись не сработала: меню загрузки прошивки
   (Esc/F8), выбрать SSD → таблица 5.
4. Первая загрузка на новом железе — несколько минут «подготовки устройств».
   Синий экран `INACCESSIBLE_BOOT_DEVICE` → таблица 6 (значит, режим Windows To Go
   не включился; лечится — напишите, что было).

---

## 3. В Windows на ноутбуке

1. **Сеть и драйверы.** Задание `\lolssd\drivers` при старте само ставит драйверы из папок
   `drivers` на всех разделах (`C:\Drivers` из образа, а также LOLSSD и DATA), если компьютер
   новый или у устройства нет драйвера. Журнал: `C:\ProgramData\lolssd\lolssd-drivers.log`.
   Вручную: `pnputil /add-driver C:\Drivers\*.inf /subdirs /install`. Запасной путь к сети —
   USB-модем с телефона → таблица 7.
2. **Безопасность** (PowerShell от администратора):
   ```
   Confirm-SecureBootUEFI
   Get-Tpm | Select TpmPresent, TpmReady, ManufacturerVersion
   Get-CimInstance -Namespace root\Microsoft\Windows\DeviceGuard -ClassName Win32_DeviceGuard |
     Select VirtualizationBasedSecurityStatus, SecurityServicesRunning
   ```
   `VirtualizationBasedSecurityStatus = 2` — VBS работает; `2` в `SecurityServicesRunning` — HVCI.
   → таблица 8.
3. **Лолка** уже стоит (предустановлена в виртуалке). Если нет — запустить
   `C:\Program Files\lolssd\lolssd-riot.ps1`, журнал `C:\ProgramData\lolssd\lolssd-riot.log`
   → таблица 9. Vanguard может попросить перезагрузку → **Linux** → снова `lolssd.sh boot`.
4. **Главное:** войти, запустить тренировку/бота. Точный текст и код ошибок `VAN …`, если есть
   → таблица 10.
   - Если Vanguard требует Secure Boot: включить его в BIOS, проверить ещё раз, **выключить
     обратно** (неподписанный Linux с ним не загрузится) → таблица 11.
5. FPS (Ctrl+F в игре), ощущение мыши/тачпада → таблица 12.
6. **Наши инструменты:** состояние NVRAM и заданий глазами Windows — в журналы (журналы
   `C:\ProgramData\lolssd\*.log` и `C:\Windows\Setup\Scripts\lolssd-setup.log` потом можно
   прочитать из Linux, смонтировав раздел Windows только для чтения):
   ```
   bcdedit /enum firmware > C:\ProgramData\lolssd\bcdedit-firmware.txt
   schtasks /query /tn \lolssd\ /fo list > C:\ProgramData\lolssd\tasks.txt
   ```
   Что `nvram-guard` написал при старте → таблица 13.
7. Пуск → Завершение работы (не гибернация).

---

## 4. Снова в Linux: хост цел?

```
cd ~/lolssd-proto
sudo efibootmgr -v > nvram-after.txt; diff nvram-before.txt nvram-after.txt
sudo find /boot -type f -exec sha256sum {} + | sort -k2 > esp-after.txt; diff esp-before.txt esp-after.txt
timedatectl
journalctl -b -1 -p err --no-pager | grep -iE 'ext4|nvme|i/o error'
```
Ожидаемо в NVRAM: появилась запись `lolssd` (удаляется `sudo sh .../lolssd.sh boot --cleanup`),
больше ничего. В `/boot` — никаких отличий. Часы верные → таблица 14.

---

## 5. Linux внутри Windows (если осталось время)

WSL и пусковой дистрибутив уже в образе; дистрибутив импортируется при первом входе после
перезагрузки на железе. Каждая перезагрузка Windows — через Linux и `lolssd.sh boot`.

1. **Терминал в Arch на диске** — ярлык «Linux terminal (SSD)» (Пуск → Linux или рабочий стол).
   Работает без хоста → таблица 15.
2. В Linux: `sh /run/media/$USER/LOLSSD/lolssd.sh profile` — профиль ляжет на SSD; заодно
   покажет, какие переназначения keyd не переносятся.
3. Загрузиться в Windows: при входе `lolssd-win mount` найдёт профиль, подключит диск хоста и,
   если поменялся `Scancode Map`, попросит перезагрузку (через Linux).
4. **Ядро WSL:**
   ```
   wsl -d lolssd -- sh -c "cat /proc/filesystems | grep -E 'btrfs|ext4'; ls -l /dev/dxg /dev/dri; ls /dev/mapper"
   ```
   → таблица 15.
5. **Оболочка хоста** — ярлык «Linux shell (host)». **Режим «Приложения»:**
   `wsl -d lolssd -u root -- lolssd-wsl run -- kitty` (ваш терминал).
   Открылось окно, ваши конфиги на месте? → таблица 16.
6. **Главный риск — Hyprland в WSL:** ярлык «Linux desktop (host)», то же самое:
   ```
   wsl -d lolssd -u root -- lolssd-wsl session
   ```
   Запустился? Если нет — **полный текст ошибки** → таблица 17. Если да:
   - заголовки окон: PowerShell `Get-Process msrdc | Select MainWindowTitle`;
   - Super левая/правая, Copilot как правая Meta, Ctrl+Alt+F12, частота кадров → таблица 18.

---

## Результаты

| # | Что | Результат |
|---|---|---|
| 1 | Установка Windows в QEMU на SSD прошла | |
| 2 | `lolssd-setup.log`: шаги `FAIL` (какие); что осталось из приложений | |
| 3 | Учётка Microsoft не понадобилась, OOBE без вопросов | |
| 4 | `lolssd check`: порт USB, UASP, Secure Boot | |
| 5 | `lolssd boot`: запись в NVRAM, загрузилась ли Windows по ней | |
| 6 | Первая загрузка на железе (синий экран?) | |
| 7 | Сеть/драйверы: что понадобилось | |
| 8 | Secure Boot / TPM / VBS / HVCI в Windows | |
| 9 | Предустановка Riot (`--skip-to-install`) в виртуалке: скачалось? | |
| 10 | **Лолка под Vanguard: запустилась? Коды VAN** | |
| 11 | Нужен ли Secure Boot | |
| 12 | FPS, ощущение ввода | |
| 13 | `nvram-guard`: что увидел и сделал | |
| 14 | Хост после Windows: NVRAM, `/boot`, часы, ошибки ФС | |
| 15 | Ядро WSL: btrfs, `/dev/dxg`, `/dev/dri`, dm | |
| 16 | `lolssd-wsl run`: окна, конфиги, ускорение | |
| 17 | **`lolssd-wsl session`: Hyprland запустился? Ошибка** | |
| 18 | Заголовки окон, Super L/R, Copilot, Ctrl+Alt+F12, Гц | |
