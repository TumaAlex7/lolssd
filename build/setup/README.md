# Настройка Windows при установке

Образ Windows не переделывается: ставится стоковый ISO, а всё наше Windows делает сама по ходу
установки. Файлы лежат на «конфигурационном диске» — папке, которую QEMU показывает установке
как флешку FAT (`build/install.sh`):

| Файл | Что | Когда |
|---|---|---|
| `autounattend.xml` | шаблон файла ответов: разделы, пользователь, запуск наших скриптов | установщик находит его в корне диска сам |
| `lolssd-specialize.ps1` | наши программы в `C:\Program Files\lolssd`, реестр (`lolssd*.reg`), удаление Edge, OneDrive и специальных возможностей | проход specialize, до OOBE, от SYSTEM, без сети |
| `SetupComplete.cmd` → `lolssd-setup.ps1` | скачивание и проверка компонентов, обои, формат LOLSSD, задания, удаление приложений и компонентов, Sunshine | после установки, до первого входа, от SYSTEM, с сетью |
| `lolssd-riot.ps1` | Riot Client и игра | первый вход |
| `lolssd-drivers.ps1`, `lolssd-gpu.ps1` | драйверы с LOLSSD; установщик видеодрайвера для карты без драйвера | задания при каждом старте и входе |
| `lolssd-common.ps1` | общие функции: журнал, опции, захват прав, скачивание с проверкой | — |

Что включено — решает файл опций сборки (`build/options.default`, мастер пишет свой): он лежит
на конфигурационном диске как `lolssd\options.txt`, в системе — как
`C:\Windows\Setup\Scripts\lolssd-options.txt`, и по нему работают скрипты и `lolssd-win`.

## Реестр

`lolssd-specialize.ps1` импортирует (`reg import`) по опциям:
- `lolssd.reg`, всегда: часы RTC в UTC, запрет автошифрования, SAN policy `OfflineInternal`
  (чужие диски Offline), без гибернации и быстрого запуска, загрузка с NVMe и SATA, VBS и HVCI
  (Vanguard), без зарезервированного хранилища и точек восстановления, без рекламы, синхронизации
  и облачного восстановления, без телеметрии;
- `lolssd-external.reg`, `placement=external`: `PortableOperatingSystem`, питание USB-порта и
  диска, без SysMain и WSearch;
- `lolssd-noonedrive.reg`, `onedrive=1`; `lolssd-noedge.reg`, `edge=1`: против их переустановки;
- `lolssd-noupdate.reg`, `updates=0`: политики Windows Update, службы `wuauserv`/`UsoSvc`
  выключены, драйверы не ищутся в Windows Update. Ключ `WaaSMedicSvc` (включает их обратно)
  защищён ACL даже от SYSTEM: скрипт сначала забирает владение ключом (привилегия
  `SeTakeOwnershipPrivilege`), потом выключает службу.

Файлы с `CurrentControlSet`, чтобы их можно было и вручную выполнить через `reg import`.

## Защищённые файлы

Edge, `OneDriveSetup.exe`, программы специальных возможностей и стоковые обои принадлежат
TrustedInstaller. Скрипт забирает владение (`takeown`, `icacls` для Администраторов — SYSTEM в
их числе) и удаляет или заменяет. Штатный деинсталлятор Edge вне ЕЭЗ отказывает, поэтому его
файлы, службы и задания удаляются напрямую; WebView2 остаётся.

## Скачивание

С проверкой: SHA-256 для версий со стабильной ссылкой, подпись Authenticode производителя — для
ссылки, которая всегда отдаёт последнюю версию (Intel DSA). QEMU для Windows ставится своим
установщиком во временную папку, и из неё берутся только файлы из `qemu-files.txt` (exe, его DLL,
прошивка).

Журнал: `C:\Windows\Setup\Scripts\lolssd-setup.log`, по строке на шаг (`ok` / `FAIL` с причиной).
Сбой одного шага не останавливает остальные.

Файлы для Windows на английском: Windows PowerShell 5.1 неправильно читает не-ASCII в файлах без
BOM, `cmd` и `reg import` — тоже кодировкозависимы. Внешние команды, чей stderr надо спрятать,
запускаются через `cmd /c … 2>&1`: в PowerShell 5.1 перенаправленный stderr при
`ErrorActionPreference Stop` становится исключением.

## Проверено

`build/test-install.sh`: установка целиком в QEMU, все шаги проходят (журнал без `FAIL`).
