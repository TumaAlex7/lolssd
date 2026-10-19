# Скрипты установки Windows

Выполняются один раз при установке Windows на SSD, а также при каждой повторной установке после
`sysprep /generalize`, то есть при первой загрузке на новом компьютере. Поэтому все шаги
идемпотентны: повторный запуск ничего не ломает.

| Файл | Куда в образе | Когда и от чьего имени |
|---|---|---|
| `lolssd-setup.ps1` | `C:\Windows\Setup\Scripts\` | вызывается двумя фазами ниже |
| `SetupComplete.cmd` | `C:\Windows\Setup\Scripts\` | Windows запускает его сама от SYSTEM после установки, до первого входа → `-Phase Complete` |
| — | — | `autounattend.xml`, проход `specialize`, `RunSynchronous` от SYSTEM, до OOBE → `-Phase Specialize` |

В образ файлы попадают при сборке: либо добавляются в WIM офлайн, либо через папку
`sources\$OEM$\$$\Setup\Scripts\` на установочном носителе. Её содержимое копируется в
`C:\Windows\Setup\Scripts\` до прохода `specialize`.

Команда для `autounattend.xml`:
```
powershell.exe -NoProfile -ExecutionPolicy Bypass -File C:\Windows\Setup\Scripts\lolssd-setup.ps1 -Phase Specialize
```

## Что делается

**Specialize** — то, что должно случиться до OOBE:
- запрет автоматического шифрования устройства (иначе диск привяжется к TPM машины, на которой
  его ставили);
- SAN policy `OfflineInternal`: диски хоста остаются Offline, Windows их не монтирует;
- `PortableOperatingSystem` (режим Windows To Go);
- `RealTimeIsUniversal`: часы RTC в UTC, как у Linux.

**Complete**:
- гибернация и быстрый запуск выключены;
- Windows Update: политики, службы `wuauserv`/`UsoSvc`/`WaaSMedicSvc`, задания планировщика,
  автообновление Store;
- BitLocker выключается, если всё же включился;
- VBS и HVCI (требование Vanguard, VAN 9005);
- WSL и Virtual Machine Platform (для режима Linux внутри Windows);
- отключение зарезервированного хранилища (~7 ГБ) и восстановления системы;
- выключение экономии энергии для USB-портов и остановки дисков: системный диск висит на USB.

Журнал: `C:\Windows\Setup\Scripts\lolssd-setup.log`, по строке на шаг (`ok` / `FAIL` с причиной).
Сбой одного шага не останавливает остальные. Ожидаемые отказы — защищённые ACL `WaaSMedicSvc`
и часть заданий `UpdateOrchestrator`: тогда обновления держат политики.

Скрипты на английском: Windows PowerShell 5.1 неправильно читает не-ASCII в файлах без BOM,
а `cmd` — в любой кодировке, отличной от системной.

## Проверено / не проверено

- Синтаксис `lolssd-setup.ps1` разобран парсером PowerShell 7, вспомогательные функции прогнаны
  на Linux.
- **На Windows не запускался.** [проверить] каждый шаг на Windows 10 и 11, в том числе какие
  из них реально отказывают по ACL.
