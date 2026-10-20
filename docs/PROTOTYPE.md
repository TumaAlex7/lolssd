# Прототип: первая установка на SSD

Цель — за один прогон ответить на главные вопросы проекта, прежде всего: **запускается ли лолка
под Vanguard с внешнего SSD.** Всё, что помечено в ARCHITECTURE.md как «[проверить]», собрано
здесь в порядке, в котором до этого доходят руки. Результаты — в таблицу в конце.

Windows ставится на SSD из Linux через QEMU: SSD подключается к виртуалке как обычный диск,
и установщик Windows не знает, что это USB. На настоящем железе Windows потом грузится с USB
благодаря `PortableOperatingSystem` из `lolssd.reg` (режим Windows To Go).

> **Важно про перезагрузки.** Порядок загрузки ноутбука не меняется: **любая перезагрузка
> Windows приводит в Linux.** После установки Vanguard, WSL и т. п. снова
> `sudo sh .../lolssd.sh boot` (или меню загрузки прошивки, у ASUS — Esc/F8 при включении).

Пути ниже: `/run/media/$USER/LOLSSD` — раздел LOLSSD в Linux (смонтирует udisks), `D:` — он же
в Windows (буква может отличаться).

---

## 0. Подготовка (пока SSD в пути)

1. Пакеты:
   ```
   sudo pacman -S --needed qemu-desktop edk2-ovmf dosfstools acl swtpm ntfs-3g
   ```
2. ISO Windows 11 с https://www.microsoft.com/software-download/windows11 («Download Windows 11
   Disk Image (ISO)», язык любой).
3. Сборка:
   ```
   cd ~/repos/lolssd && make dist
   ```
   По желанию `sudo systemctl start docker && sudo make wsl` — для раздела 5.
4. **Драйверы для Windows на ноутбуке.** Windows Update в образе выключен, а без драйвера Wi-Fi
   не будет сети. Положить в `dist/LOLSSD/drivers/` (скопируется на SSD; на FAT32 файл ≤ 4 ГБ):
   - Wi-Fi MediaTek MT7922 и графика AMD (Radeon 680M) — со страницы поддержки ASUS для
     Vivobook S 15 M5506NA или с сайтов MediaTek/AMD;
   - запасной путь к сети без драйверов — **USB-модем с телефона**: драйвер RNDIS в Windows есть.
5. **Снимок хоста до первой загрузки с SSD** — потом сравним, не тронула ли Windows ноутбук:
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
   sudo build/prototype/vm-install.sh /dev/sdX ~/Downloads/Win11_*.iso 128G
   ```
   Последний аргумент — сколько отдать под Windows; остаток диска станет разделом `DATA` (NTFS)
   для своих файлов. Без него Windows получит весь диск.
3. В окне QEMU:
   - сразу нажать любую клавишу — загрузка с ISO;
   - язык → «Установить» → **«У меня нет ключа продукта»** → редакция **как у заводской Windows
     ноутбука** (обычно Home). Ключ заводской Windows зашит в прошивку (таблица ACPI MSDM), и на
     настоящем железе Windows той же редакции активируется сама; другая останется без активации
     (работает, но с водяным знаком). Если таблицы `/sys/firmware/acpi/tables/MSDM` нет (как на
     ноутбуке автора), лицензия цифровая: она привязана к железу на серверах Microsoft, и Windows
     той же редакции активируется на этом ноутбуке при первом выходе в интернет, учётка Microsoft
     для этого не нужна;
   - «Выборочная установка» → выбрать **«Незанятое место на диске 0»** (единственное: между
     LOLSSD и DATA) → Далее;
   - после перезагрузок OOBE почти не задаёт вопросов (может спросить регион и раскладку).
4. При первом входе откроется окно `apply.cmd`: ставит файлы lolssd, проверяет запасной путь
   загрузчика, применяет `lolssd.reg` и скрипт настройки, показывает журнал.
   - **Ожидаемо:** ошибка `reg import` на последнем ключе (`WaaSMedicSvc`, защищён ACL),
     возможно `FAIL disable update scheduled tasks` с частью заданий.
   - Должно быть: `Fallback boot path: OK`.
   - Если окно не открылось: открыть `D:\windows\apply.cmd` → «Запуск от имени администратора».
   - Сфотографировать/переписать журнал → таблица, строки 1–3.
5. Пуск → Завершение работы. QEMU закроется.

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

1. **Сеть и драйверы:** Диспетчер устройств → поставить драйверы из `D:\drivers` (Wi-Fi, AMD)
   или USB-модем с телефона → таблица 7.
2. **Безопасность** (PowerShell от администратора):
   ```
   Confirm-SecureBootUEFI
   Get-Tpm | Select TpmPresent, TpmReady, ManufacturerVersion
   Get-CimInstance -Namespace root\Microsoft\Windows\DeviceGuard -ClassName Win32_DeviceGuard |
     Select VirtualizationBasedSecurityStatus, SecurityServicesRunning
   ```
   `VirtualizationBasedSecurityStatus = 2` — VBS работает; `2` в `SecurityServicesRunning` — HVCI.
   → таблица 8.
3. **Лолка.** Скачать установщик с leagueoflegends.com и запустить из `cmd`, проверяя ключ:
   ```
   "%USERPROFILE%\Downloads\Install League of Legends ru.exe" --skip-to-install
   ```
   Поставил без вопросов? → таблица 9. Vanguard попросит перезагрузку → **Linux** → снова
   `lolssd.sh boot`.
4. **Главное:** войти, запустить тренировку/бота. Точный текст и код ошибок `VAN …`, если есть
   → таблица 10.
   - Если Vanguard требует Secure Boot: включить его в BIOS, проверить ещё раз, **выключить
     обратно** (неподписанный Linux с ним не загрузится) → таблица 11.
5. FPS (Ctrl+F в игре), ощущение мыши/тачпада → таблица 12.
6. **Наши инструменты:** сохранить на SSD журналы и состояние NVRAM глазами Windows:
   ```
   mkdir D:\results
   copy C:\ProgramData\lolssd\lolssd-win.log D:\results\
   copy C:\Windows\Setup\Scripts\lolssd-setup.log D:\results\
   bcdedit /enum firmware > D:\results\bcdedit-firmware.txt
   schtasks /query /tn \lolssd\ /fo list > D:\results\tasks.txt
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

Нужен `make wsl` из подготовки. Каждая перезагрузка Windows — через Linux и `lolssd.sh boot`.

1. В Linux: `sh /run/media/$USER/LOLSSD/lolssd.sh profile` — профиль ляжет на SSD; заодно
   покажет, какие переназначения keyd не переносятся. Скопировать `dist/wsl/lolssd.tar` на SSD:
   `cp ~/repos/lolssd/dist/wsl/lolssd.tar /run/media/$USER/LOLSSD/`.
2. В Windows (нужен интернет): `wsl --install --no-distribution` → перезагрузка (через Linux).
3. Импорт пускового дистрибутива и монтирование:
   ```
   wsl --import lolssd C:\ProgramData\lolssd\wsl D:\lolssd.tar
   "C:\Program Files\lolssd\lolssd-win.exe" mount
   type C:\ProgramData\lolssd\lolssd-win.log
   ```
   (`mount` — из командной строки администратора.) Попросит перезагрузку из-за `Scancode Map`
   → перезагрузиться (через Linux).
4. **Ядро WSL:**
   ```
   wsl -d lolssd -- sh -c "cat /proc/filesystems | grep -E 'btrfs|ext4'; ls -l /dev/dxg /dev/dri; ls /dev/mapper"
   ```
   → таблица 15.
5. **Режим «Приложения»:** `wsl -d lolssd -u root -- lolssd-wsl run -- kitty` (ваш терминал).
   Открылось окно, ваши конфиги на месте? → таблица 16.
6. **Главный риск — Hyprland в WSL:**
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
| 2 | `apply.cmd`: шаги `FAIL` в журнале (какие) | |
| 3 | `Fallback boot path` | |
| 4 | `lolssd check`: порт USB, UASP, Secure Boot | |
| 5 | `lolssd boot`: запись в NVRAM, загрузилась ли Windows по ней | |
| 6 | Первая загрузка на железе (синий экран?) | |
| 7 | Сеть/драйверы: что понадобилось | |
| 8 | Secure Boot / TPM / VBS / HVCI в Windows | |
| 9 | `--skip-to-install` у установщика лолки | |
| 10 | **Лолка под Vanguard: запустилась? Коды VAN** | |
| 11 | Нужен ли Secure Boot | |
| 12 | FPS, ощущение ввода | |
| 13 | `nvram-guard`: что увидел и сделал | |
| 14 | Хост после Windows: NVRAM, `/boot`, часы, ошибки ФС | |
| 15 | Ядро WSL: btrfs, `/dev/dxg`, `/dev/dri`, dm | |
| 16 | `lolssd-wsl run`: окна, конфиги, ускорение | |
| 17 | **`lolssd-wsl session`: Hyprland запустился? Ошибка** | |
| 18 | Заголовки окон, Super L/R, Copilot, Ctrl+Alt+F12, Гц | |
