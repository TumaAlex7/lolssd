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

// logon — задание при входе пользователя (с повышенными правами; в заданиях старых систем
// команда называется mount): ярлыки, Scancode Map для ВМ и уборка остатков режима WSL. Профиля для этого компьютера нет — режим «Linux внутри Windows» не включали.
func logon() int {
	fixShortcuts()
	removeWSL()
	vol, err := lolssdVolume()
	if err != nil {
		log.Println(err)
		return 1
	}
	p, _, err := findProfile(vol)
	if err != nil {
		log.Println(err)
		return 0
	}
	log.Printf("профиль %s (%s, %s)", p.MachineID, p.Distro.ID, p.User.Name)
	if applyScancodeMap(p) {
		const mbOK, mbIconInfo, mbSetForeground = 0x0, 0x40, 0x10000
		procMessageBox.Call(0, uintptr(unsafe.Pointer(windows.StringToUTF16Ptr(
			"Переназначения клавиш для Linux записаны (из keyd и F23 → F24 для клавиши Copilot).\n"+
				"Они начнут действовать после перезагрузки Windows."))),
			uintptr(unsafe.Pointer(windows.StringToUTF16Ptr("lolssd"))), mbOK|mbIconInfo|mbSetForeground)
	}
	return 0
}

// hostDisk — диск хоста в Windows: номер PhysicalDrive и что о нём знает профиль.
type hostDisk struct {
	N    int
	Info hostprofile.Disk
}

// findProfile находит на разделе LOLSSD профиль этого компьютера — тот, все разделы которого
// есть на его дисках (кроме этого SSD); из нескольких — самый свежий. Возвращает и диски хоста,
// на которых эти разделы, по возрастанию номера.
func findProfile(vol string) (hostprofile.Profile, []hostDisk, error) {
	files, _ := filepath.Glob(filepath.Join(vol, "profiles", "*.json"))
	if len(files) == 0 {
		return hostprofile.Profile{}, nil, fmt.Errorf("профилей нет (lolssd profile на хосте)")
	}
	// PARTUUID → номер диска для всех дисков, кроме системного (этого SSD).
	sys, err := systemDisk()
	if err != nil {
		return hostprofile.Profile{}, nil, err
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

	var best hostprofile.Profile
	found := false
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
		ok := len(uuids) > 0
		for _, u := range uuids {
			if _, in := where[u]; !in {
				ok = false
			}
		}
		if ok && p.Created.After(best.Created) {
			best, found = p, true
		}
	}
	if !found {
		return best, nil, fmt.Errorf("профиля для этого компьютера нет")
	}

	info := map[string]hostprofile.Disk{}
	var walk func(d hostprofile.BlockDev, disk *hostprofile.Disk)
	walk = func(d hostprofile.BlockDev, disk *hostprofile.Disk) {
		if d.Disk != nil {
			disk = d.Disk
		}
		if d.PartUUID != "" && disk != nil {
			info[d.PartUUID] = *disk
		}
		for _, l := range d.Lower {
			walk(l, disk)
		}
	}
	for _, m := range best.Mounts {
		walk(m.Device, nil)
	}
	for _, s := range best.Swaps {
		walk(s.Device, nil)
	}
	seen := map[int]bool{}
	var disks []hostDisk
	for n := 0; n < 64; n++ {
		for _, u := range best.PartUUIDs() {
			if where[u] == n && !seen[n] {
				seen[n] = true
				disks = append(disks, hostDisk{N: n, Info: info[u]})
			}
		}
	}
	return best, disks, nil
}

// removeWSL убирает остатки режима WSL у систем, установленных раньше: пусковой дистрибутив
// (регистрируется на пользователя, поэтому здесь, при входе) и его файлы. Сам WSL остаётся.
func removeWSL() {
	dir := filepath.Join(os.Getenv("ProgramData"), "lolssd", "wsl")
	if _, err := os.Stat(dir); err != nil {
		return
	}
	if out, err := hidden(5*time.Minute, "wsl.exe", "--unregister", "lolssd").CombinedOutput(); err != nil {
		log.Printf("wsl --unregister lolssd: %v %s", err, strings.ReplaceAll(string(out), "\x00", ""))
	}
	if err := os.RemoveAll(dir); err != nil {
		log.Println("удаление", dir, err)
		return
	}
	for _, f := range []string{"active-profile.json", "monitors.json"} {
		os.Remove(filepath.Join(os.Getenv("ProgramData"), "lolssd", f))
	}
	log.Println("режим WSL убран")
}

var procMessageBox = user32.NewProc("MessageBoxW")

// f23Transport — перевозка F23 в ВМ: PS/2-клавиатура QEMU не умеет F16–F23, и клавиша Copilot
// (Win+Shift+F23) доходила бы без F23. Windows шлёт её как F24 (0x76), а в ВМ правило udev
// (winctl.VMUdevRules) возвращает этому скан-коду F23 — keyd получает тот же аккорд, что на железе.
var f23Transport = map[uint16]uint16{0x006e: 0x0076}

// applyScancodeMap записывает в Scancode Map переназначения keyd из профиля и f23Transport.
// В ВМ весь ввод из Windows приходит одной клавиатурой 0001:0001, и keyd применяет к нему правила
// встроенной клавиатуры ноутбука; правила остальных клавиатур (например, Menu → правая Super из
// default.conf) поэтому делает Windows, как раньше (docs/ARCHITECTURE.md §7). Драйвер клавиатуры
// читает Scancode Map при загрузке: при изменении нужна перезагрузка — тогда возвращает true.
func applyScancodeMap(p hostprofile.Profile) bool {
	plan := keymap.Build(keymap.ParseKeyd(p.Keyboard.Keyd))
	for _, n := range plan.Notes {
		log.Println("клавиши, не переносится:", n)
	}
	for from, to := range f23Transport {
		plan.Scancodes[from] = to
	}
	k, _, err := registry.CreateKey(registry.LOCAL_MACHINE, `SYSTEM\CurrentControlSet\Control\Keyboard Layout`, registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		log.Println("Scancode Map:", err)
		return false
	}
	defer k.Close()
	cur, _, _ := k.GetBinaryValue("Scancode Map")
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
// и иначе каждая открывала бы своё окно. Без окна программа не может ничего спросить у
// пользователя, поэтому каждый вызов ограничен по времени.
func hidden(timeout time.Duration, name string, args ...string) *exec.Cmd {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	time.AfterFunc(timeout+time.Second, cancel)
	cmd := exec.CommandContext(ctx, name, args...)
	const createNoWindow = 0x08000000
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
	return cmd
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
# Перезагрузка обратно в эту Windows: ярлык ведёт в lolssd-win.exe restart (права — через UAC).
# Задание \lolssd\restart от прежних версий больше не нужно, как и ярлыки режима WSL.
Unregister-ScheduledTask -TaskPath '\lolssd\' -TaskName 'restart' -Confirm:$false -ErrorAction SilentlyContinue
foreach ($d in "$env:PUBLIC\Desktop", "$env:ProgramData\Microsoft\Windows\Start Menu\Programs\Linux") {
  foreach ($n in 'Linux terminal (SSD)', 'Linux shell (host)') {
    if (Test-Path "$d\$n.lnk") { Remove-Item "$d\$n.lnk"; "removed $d\$n.lnk" }
  }
}
$p = "$env:ProgramData\Microsoft\Windows\Start Menu\Programs\Restart into Windows (SSD).lnk"
$l = $s.CreateShortcut($p)
if ($l.TargetPath -ne $exe -or $l.Arguments -ne 'restart') {
  $l.TargetPath = $exe; $l.Arguments = 'restart'
  $l.Description = 'Restart straight back into this Windows instead of the host system'
  $l.IconLocation = "$env:SystemRoot\System32\shell32.dll,238"
  $l.Save(); "updated $p"
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
