# Настройка Windows при установке

Два слоя:

| Файл | Что | Когда применяется |
|---|---|---|
| `lolssd*.reg` | все настройки, которые сводятся к значениям реестра | **офлайн**, в кусты реестра образа до первой загрузки |
| `lolssd-setup.ps1` + `SetupComplete.cmd` | то, что реестром не выразить | Windows сама запускает `SetupComplete.cmd` от SYSTEM после установки, до первого входа |

Что включено — решает файл опций сборки (`build/options.default`, мастер пишет свой): он лежит в
образе как `C:\Windows\Setup\Scripts\lolssd-options.txt`, и по нему работают `build/iso.sh`,
`lolssd-setup.ps1` и `lolssd-win`.

## Почему реестр офлайн

- **Действует с первой загрузки.** Запрет шифрования и часы в UTC работают до OOBE и до первого
  выключения, не нужно успевать скриптом.
- **Права доступа Windows не действуют.** Ключ службы `WaaSMedicSvc` защищён ACL даже от SYSTEM
  на работающей системе. Если править куст снаружи, он выключается так же, как остальные.
- **Нагляднее.** Список «ключ = значение» без логики, которая может упасть.

Файл написан с `CurrentControlSet`, чтобы его можно было и вручную выполнить через `reg import` на
работающей Windows (кроме `WaaSMedicSvc`, который там не изменится). В образ он попадает дважды:
`build/iso.sh` применяет его к реестру образа прямо из Linux (`build/debloat.sh`, через `hivex`;
`CurrentControlSet` → `ControlSet001`), и файл ответов ещё раз импортирует его на проходе specialize.

`build/iso.sh` склеивает файлы по опциям в один `lolssd.reg`:
- `lolssd.reg`, всегда: часы RTC в UTC, запрет автошифрования, SAN policy `OfflineInternal`
  (чужие диски Offline), без гибернации и быстрого запуска, загрузка с NVMe и SATA, VBS и HVCI
  (Vanguard), без зарезервированного хранилища и точек восстановления, без рекламы, синхронизации
  и облачного восстановления, без телеметрии;
- `lolssd-external.reg`, `placement=external`: `PortableOperatingSystem`, питание USB-порта и
  диска, без SysMain и WSearch;
- `lolssd-noonedrive.reg`, `onedrive=1`: политика против OneDrive;
- `lolssd-noupdate.reg`, `updates=0` — последним: политики Windows Update, службы
  `wuauserv`/`UsoSvc`/`WaaSMedicSvc` выключены, драйверы не ищутся в Windows Update.

## Скрипт

`lolssd-setup.ps1` запускается при установке и снова после `sysprep /generalize`, то есть при первой
загрузке на каждом новом компьютере. Все шаги идемпотентны:
- `powercfg /hibernate off` — удалить `hiberfil.sys`, если установка его оставила;
- без обновлений — выключить задания планировщика Windows Update (второй слой поверх служб);
- выключить BitLocker, если шифрование успело начаться;
- с ВМ — включить Windows Hypervisor Platform (WHPX, для ВМ с Linux хоста), `hypervisorlaunchtype auto`;
- `DISM /Set-ReservedStorageState /State:Disabled` (значение в реестре срабатывает, только если
  задано до создания резерва).
- зарегистрировать задания `\lolssd\nvram-guard`, `\lolssd\logon`, `\lolssd\drivers`;
- удалить по опциям приложения (списком), компоненты Windows, OneDrive, Edge (WebView2 остаётся);
- с Sunshine — поставить его, приложение Moonlight «Reboot to Linux», без сна.

Журнал: `C:\Windows\Setup\Scripts\lolssd-setup.log`, по строке на шаг (`ok` / `FAIL` с причиной).
Сбой одного шага не останавливает остальные.

В образ `lolssd-setup.ps1` и `SetupComplete.cmd` кладутся в `C:\Windows\Setup\Scripts\` (офлайн в
образ или через `sources\$OEM$\$$\Setup\Scripts\` на установочном носителе).

Файлы для Windows на английском: Windows PowerShell 5.1 неправильно читает не-ASCII в файлах без
BOM, `cmd` и `reg import` — тоже кодировкозависимы.

## Проверено

`build/test-install.sh`: установка целиком в QEMU, все шаги `lolssd-setup.ps1` проходят
(журнал без `FAIL`). На реальном железе: SSD автора и сервер.
