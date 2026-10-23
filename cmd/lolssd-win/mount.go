//go:build windows

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"

	"lolssd/efi"
	"lolssd/hostprofile"
	"lolssd/keymap"
)

// Пусковой дистрибутив читает выбранный профиль отсюда (в WSL — /mnt/c/ProgramData/lolssd/).
var activeProfile = filepath.Join(os.Getenv("ProgramData"), "lolssd", "active-profile.json")

var monitorsPath = filepath.Join(os.Getenv("ProgramData"), "lolssd", "monitors.json")

// wslConfigMark отличает наш .wslconfig от написанного пользователем: чужой не трогаем.
const wslConfigMark = "# lolssd: generated, delete this line to keep your own settings"

// writeWSLConfig: половина памяти Windows под WSL (но не меньше 4 ГиБ) и возврат
// неиспользуемой памяти Windows — на 16 ГБ это важнее всего.
func writeWSLConfig() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	path := filepath.Join(home, ".wslconfig")
	if b, err := os.ReadFile(path); err == nil && !strings.HasPrefix(string(b), wslConfigMark) {
		log.Println("свой .wslconfig пользователя, оставляю как есть")
		return nil
	}
	mem := max(totalMemoryMiB()/2/1024, 4)
	// networkingMode=mirrored: WSL делит сеть с Windows, и 127.0.0.1 у них общий. В режиме NAT
	// по умолчанию до Windows доходит только пересылка портов (wslrelay), а через неё VNC
	// рабочего стола получал около 16–19 Мбит/с и уходил в JPEG.
	cfg := fmt.Sprintf("%s\r\n[wsl2]\r\nmemory=%dGB\r\nswap=4GB\r\nguiApplications=true\r\nnetworkingMode=mirrored\r\n\r\n[experimental]\r\nautoMemoryReclaim=gradual\r\n", wslConfigMark, mem)
	if b, err := os.ReadFile(path); err == nil && string(b) == cfg {
		return nil
	}
	if err := os.WriteFile(path, []byte(cfg), 0o644); err != nil {
		return err
	}
	// ВМ WSL могла уже стартовать (импорт дистрибутива): новые настройки — только после перезапуска.
	if out, err := hidden(time.Minute, "wsl.exe", "--shutdown").CombinedOutput(); err != nil {
		log.Printf("wsl --shutdown: %v %s", err, out)
	}
	log.Println(".wslconfig обновлён")
	return nil
}

// mountHost готовит режим «Linux внутри Windows» (docs/ARCHITECTURE.md §6):
// находит на разделе LOLSSD профиль этого компьютера, подключает диски хоста в WSL целиком
// (--bare: разделы, LUKS и LVM поднимает уже пусковой дистрибутив) и сохраняет профиль для него.
// Профиля для этого компьютера нет — значит, пользователь этот режим не включал: молча выходим.
// Запускается при входе пользователя, с повышенными правами (так требует wsl --mount).
func mountHost() int {
	os.Remove(activeProfile)
	ensureDistro()
	fixShortcuts()

	vol, err := lolssdVolume()
	if err != nil {
		log.Println(err)
		return 1
	}
	files, _ := filepath.Glob(filepath.Join(vol, "profiles", "*.json"))
	if len(files) == 0 {
		log.Println("профилей нет, режим Linux внутри Windows выключен")
		return 0
	}

	// PARTUUID → номер диска для всех дисков, кроме системного (этого SSD).
	sys, err := systemDisk()
	if err != nil {
		log.Println(err)
		return 1
	}
	where := map[string]int{}
	for n := 0; n < 64; n++ {
		if n == sys {
			continue
		}
		parts, err := diskGPT(n)
		if err != nil {
			continue // диска с таким номером нет или на нём не GPT
		}
		for _, p := range parts {
			where[efi.GUIDString(p.GUID)] = n
		}
	}

	// Подходит профиль, все разделы которого есть на этом компьютере; из нескольких — самый свежий.
	var best hostprofile.Profile
	var bestRaw []byte
	for _, f := range files {
		raw, err := os.ReadFile(f)
		var p hostprofile.Profile
		if err == nil {
			err = json.Unmarshal(raw, &p)
		}
		if err != nil {
			log.Printf("%s: %v", f, err)
			continue
		}
		uuids := p.PartUUIDs()
		found := len(uuids) > 0
		for _, u := range uuids {
			if _, ok := where[u]; !ok {
				found = false
			}
		}
		if found && p.Created.After(best.Created) {
			best, bestRaw = p, raw
		}
	}
	if bestRaw == nil {
		log.Println("профиля для этого компьютера нет, режим Linux внутри Windows выключен")
		return 0
	}
	log.Printf("профиль %s (%s, %s)", best.MachineID, best.Distro.ID, best.User.Name)

	// .wslconfig читается при старте ВМ WSL, а её запускает уже первый wsl --mount.
	if err := writeWSLConfig(); err != nil {
		log.Println(".wslconfig:", err)
	}
	mons, err := activeMonitors()
	if err == nil {
		var b []byte
		if b, err = json.MarshalIndent(mons, "", "  "); err == nil {
			err = os.WriteFile(monitorsPath, b, 0o644)
		}
	}
	if err != nil {
		log.Println("мониторы:", err)
	}

	rebootForKeys := applyScancodeMap(best)

	disks := map[int]bool{}
	for _, u := range best.PartUUIDs() {
		disks[where[u]] = true
	}
	failed := 0
	for n := range disks {
		dev := fmt.Sprintf(`\\.\PHYSICALDRIVE%d`, n)
		cmd := hidden(2*time.Minute, "wsl.exe", "--mount", dev, "--bare")
		out, err := cmd.CombinedOutput()
		msg := strings.TrimSpace(string(out))
		// Повторный запуск (диск уже подключён) — не ошибка.
		if err != nil && !strings.Contains(msg, "ALREADY") {
			log.Printf("wsl --mount %s: %v: %s", dev, err, msg)
			failed++
			continue
		}
		log.Printf("подключён %s", dev)
	}
	if failed > 0 {
		return 1
	}

	if err := os.WriteFile(activeProfile, bestRaw, 0o644); err != nil {
		log.Println("запись активного профиля:", err)
		return 1
	}
	if rebootForKeys {
		const mbOK, mbIconInfo, mbSetForeground = 0x0, 0x40, 0x10000
		procMessageBox.Call(0, uintptr(unsafe.Pointer(windows.StringToUTF16Ptr(
			"Переназначения клавиш из профиля Linux записаны (Meta → F13/F14 для Hyprland и правила keyd).\n"+
				"Они начнут действовать после перезагрузки Windows."))),
			uintptr(unsafe.Pointer(windows.StringToUTF16Ptr("lolssd"))), mbOK|mbIconInfo|mbSetForeground)
	}
	return 0
}

var procMessageBox = user32.NewProc("MessageBoxW")

// applyScancodeMap записывает переназначения клавиш из профиля (docs/ARCHITECTURE.md §7) в
// Scancode Map. Драйвер клавиатуры читает его при загрузке, поэтому при изменении нужна
// перезагрузка — тогда возвращает true.
func applyScancodeMap(p hostprofile.Profile) bool {
	plan := keymap.Build(keymap.ParseKeyd(p.Keyboard.Keyd))
	for _, n := range plan.Notes {
		log.Println("клавиши, не переносится:", n)
	}
	k, _, err := registry.CreateKey(registry.LOCAL_MACHINE, `SYSTEM\CurrentControlSet\Control\Keyboard Layout`, registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		log.Println("Scancode Map:", err)
		return false
	}
	defer k.Close()
	cur, _, curErr := k.GetBinaryValue("Scancode Map")
	if len(plan.Scancodes) == 0 {
		// Переназначать нечего: значения быть не должно (в том числе старого транспорта
		// Win → F13/F14 у систем, установленных до перехода на TigerVNC).
		if curErr != nil {
			return false
		}
		if err := k.DeleteValue("Scancode Map"); err != nil {
			log.Println("Scancode Map:", err)
			return false
		}
		log.Println("Scancode Map удалён, нужна перезагрузка")
		return true
	}
	want := keymap.ScancodeMap(plan.Scancodes)
	if bytes.Equal(cur, want) {
		return false
	}
	if err := k.SetBinaryValue("Scancode Map", want); err != nil {
		log.Println("Scancode Map:", err)
		return false
	}
	log.Printf("Scancode Map обновлён (%d переназначений), нужна перезагрузка", len(plan.Scancodes))
	return true
}

// hidden запускает консольную программу без окна: lolssd-win собран без консоли (windowsgui),
// и иначе каждый wsl.exe открывал бы своё окно при входе в Windows.
// Без окна программа не может ничего спросить у пользователя, поэтому каждый вызов ограничен
// по времени: зависший wsl.exe (например, до перезагрузки после установки WSL) не держит
// задание при входе вечно.
func hidden(timeout time.Duration, name string, args ...string) *exec.Cmd {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	time.AfterFunc(timeout+time.Second, cancel)
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), "WSL_UTF8=1") // иначе wsl.exe пишет в UTF-16
	const createNoWindow = 0x08000000
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
	return cmd
}

// ensureDistro импортирует пусковой WSL-дистрибутив из образа (\ProgramData\lolssd\wsl), если он
// ещё не зарегистрирован у этого пользователя: WSL регистрирует дистрибутивы на пользователя.
// Если lolssd.tar сменился (обновление SSD), дистрибутив импортируется заново: в нём ничего
// своего нет, только кэш (пропатченный aquamarine), он соберётся снова.
// Не выйдет до первой перезагрузки после включения компонентов WSL — тогда при следующем входе.
// Нужен и без профиля хоста: в нём открывается ярлык «Linux terminal (SSD)».
func ensureDistro() {
	// Его вызывают и задание mount (с повышенными правами, при входе, на медленном диске — с
	// задержкой), и ярлык рабочего стола. Второй ждёт первого, а потом видит, что всё уже
	// обновлено. Мьютекс доступен обоим: в дескрипторе разрешён полный доступ вошедшим.
	sd, _ := windows.SecurityDescriptorFromString("D:(A;;GA;;;AU)")
	sa := &windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: sd}
	if m, err := windows.CreateMutex(sa, false, windows.StringToUTF16Ptr(`Local\lolssd-distro`)); err == nil || m != 0 {
		windows.WaitForSingleObject(m, 15*60*1000)
		defer windows.CloseHandle(m)
		defer windows.ReleaseMutex(m)
	}
	dir := filepath.Join(os.Getenv("ProgramData"), "lolssd", "wsl")
	tar := filepath.Join(dir, "lolssd.tar")
	st, err := os.Stat(tar)
	if err != nil {
		return
	}
	stampFile := filepath.Join(dir, "lolssd.tar.imported")
	stamp := fmt.Sprintf("%d %d", st.Size(), st.ModTime().Unix())
	wsl := func(timeout time.Duration, args ...string) (string, error) {
		cmd := hidden(timeout, "wsl.exe", args...)
		out, err := cmd.CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
	// wsl --list пишет в UTF-16 даже при WSL_UTF8=1: убираем нулевые байты, иначе имя не узнать.
	if list, err := wsl(time.Minute, "--list", "--quiet"); err == nil {
		for _, name := range strings.Fields(strings.ReplaceAll(list, "\x00", "")) {
			if !strings.EqualFold(name, "lolssd") {
				continue
			}
			if old, _ := os.ReadFile(stampFile); string(old) == stamp {
				return
			}
			if out, err := wsl(5*time.Minute, "--unregister", "lolssd"); err != nil {
				log.Printf("lolssd.tar обновлён, но wsl --unregister lolssd: %v: %s", err, out)
				return
			}
			log.Println("lolssd.tar обновлён, пусковой дистрибутив импортируется заново")
		}
	}
	if out, err := wsl(10*time.Minute, "--import", "lolssd", filepath.Join(dir, "distro"), tar); err != nil {
		if strings.Contains(out, "ALREADY_EXISTS") {
			log.Println("пусковой дистрибутив уже есть, но в wsl --list его не было")
			return
		}
		log.Printf("wsl --import lolssd: %v: %s", err, out)
		return
	}
	os.WriteFile(stampFile, []byte(stamp), 0o644)
	log.Println("пусковой дистрибутив lolssd импортирован")
}

// fixShortcuts доводит ярлыки систем, установленных раньше, до текущих: ярлык рабочего стола Linux
// ведёт в lolssd-win desktop (раньше — прямо в wsl.exe), есть задание и ярлык перезагрузки в
// эту Windows. Новые системы получают всё это от lolssd-setup.ps1.
func fixShortcuts() {
	exe := filepath.Join(os.Getenv("ProgramFiles"), "lolssd", "lolssd-win.exe")
	script := `$exe = '` + exe + `'
$s = New-Object -ComObject WScript.Shell
foreach ($d in "$env:PUBLIC\Desktop", "$env:ProgramData\Microsoft\Windows\Start Menu\Programs\Linux") {
  $p = "$d\Linux desktop (host).lnk"
  if (-not (Test-Path $p)) { continue }
  $l = $s.CreateShortcut($p)
  if ($l.TargetPath -eq $exe) { continue }
  $l.TargetPath = $exe; $l.Arguments = 'desktop'
  $l.Description = "The host's Hyprland, full screen on every monitor"
  $l.Save(); "fixed $p"
}
# Перезагрузка обратно в эту Windows (lolssd-setup.ps1, 'register restart task'):
# задание и ярлык в «Пуске», если система установлена до них.
# Задание — на самого пользователя (Interactive, Highest): такое он запускает через
# schtasks /run без запроса UAC. На группу «Администраторы» запуск из обычного сеанса не проходил.
$me = "$env:USERDOMAIN\$env:USERNAME"
$t = Get-ScheduledTask -TaskPath '\lolssd\' -TaskName 'restart' -ErrorAction SilentlyContinue
if (-not $t -or $t.Principal.UserId -ne $env:USERNAME) {
  $a = New-ScheduledTaskAction -Execute $exe -Argument 'restart'
  $pr = New-ScheduledTaskPrincipal -UserId $me -LogonType Interactive -RunLevel Highest
  Register-ScheduledTask -TaskPath '\lolssd\' -TaskName 'restart' -Action $a -Principal $pr -Force | Out-Null
  "registered task \lolssd\restart for $me"
}
$p = "$env:ProgramData\Microsoft\Windows\Start Menu\Programs\Restart into Windows (SSD).lnk"
if (-not (Test-Path $p)) {
  $l = $s.CreateShortcut($p)
  $l.TargetPath = "$env:SystemRoot\System32\schtasks.exe"; $l.Arguments = '/run /tn \lolssd\restart'
  $l.Description = 'Restart straight back into this Windows instead of the host system'
  $l.IconLocation = "$env:SystemRoot\System32\shell32.dll,238"; $l.WindowStyle = 7
  $l.Save(); "created $p"
}`
	out, err := hidden(time.Minute, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command",
		script).CombinedOutput()
	if err != nil || len(out) > 0 {
		log.Printf("ярлыки: %v %s", err, strings.TrimSpace(string(out)))
	}
}

// lolssdVolume находит том раздела LOLSSD по метке ФС и возвращает его корень, например `D:\`.
func lolssdVolume() (string, error) {
	for c := 'A'; c <= 'Z'; c++ {
		root := string(c) + `:\`
		label := make([]uint16, windows.MAX_PATH+1)
		if windows.GetVolumeInformation(windows.StringToUTF16Ptr(root), &label[0], uint32(len(label)), nil, nil, nil, nil, 0) != nil {
			continue
		}
		if windows.UTF16ToString(label) == "LOLSSD" {
			return root, nil
		}
	}
	return "", fmt.Errorf("том с меткой LOLSSD не найден")
}
