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
// расширенный рабочий стол вместо дублирования мониторов, C: на весь диск, Scancode Map для ВМ. Профиля для этого компьютера нет — режим «Linux внутри Windows» не включали.
func logon() int {
	extendDisplays()
	growSystemDrive()
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
	if applyScancodeMap() {
		const mbOK, mbIconInfo, mbSetForeground = 0x0, 0x40, 0x10000
		procMessageBox.Call(0, uintptr(unsafe.Pointer(windows.StringToUTF16Ptr(
			"Клавиша Copilot переназначена для Linux (F23 → F24).\n"+
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

var procMessageBox = user32.NewProc("MessageBoxW")

// vmScancodes — Scancode Map Windows для ВМ. Клавиши переназначает keyd самой системы в ВМ, и
// переносить его правила в Windows нельзя: keyd выдаёт нажатую правую Win как левую (модификаторы
// у него всегда левые, правая получается только из правила «= rightmeta»), поэтому Menu → RWin в
// Windows давала в ВМ левую Super. Остаётся только перевозка F23 (0x6E): PS/2-клавиатура QEMU не
// умеет F16–F23, и клавиша Copilot (Win+Shift+F23) доходила бы без F23. Windows шлёт её как F24
// (0x76), а в ВМ правило udev (winctl.VMUdevRules) возвращает этому скан-коду F23 — keyd получает
// тот же аккорд, что на железе.
var vmScancodes = map[uint16]uint16{0x006e: 0x0076}

// applyScancodeMap приводит Scancode Map к vmScancodes. Драйвер клавиатуры читает его при
// загрузке, поэтому при изменении нужна перезагрузка — тогда возвращает true.
func applyScancodeMap() bool {
	k, _, err := registry.CreateKey(registry.LOCAL_MACHINE, `SYSTEM\CurrentControlSet\Control\Keyboard Layout`, registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		log.Println("Scancode Map:", err)
		return false
	}
	defer k.Close()
	cur, _, _ := k.GetBinaryValue("Scancode Map")
	want := keymap.ScancodeMap(vmScancodes)
	if bytes.Equal(cur, want) {
		return false
	}
	if err := k.SetBinaryValue("Scancode Map", want); err != nil {
		log.Println("Scancode Map:", err)
		return false
	}
	log.Println("Scancode Map: F23 → F24 для ВМ, нужна перезагрузка")
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

// shortcuts раскладывает ярлыки lolssd при установке Windows (lolssd-setup.ps1):
//   - «Linux» (рабочий стол хоста в ВМ, lolssd-win desktop) и «Linux terminal» (консоль,
//     lolssd-win terminal) — в корне «Пуска» и на общем рабочем столе, со своими значками;
//     на сервере (VARIANT=server) их нет;
//   - «Restart into Windows (SSD)» — во встроенной папке «Служебные — Windows» (System Tools),
//     с системным значком.
func shortcuts() int {
	prog := filepath.Join(os.Getenv("ProgramFiles"), "lolssd")
	variant := strings.TrimSpace(readFirstLine(filepath.Join(os.Getenv("SystemRoot"), "Setup", "Scripts", "lolssd-variant.txt")))
	script := `$prog = '` + prog + `'
$exe = "$prog\lolssd-win.exe"
$server = '` + variant + `' -eq 'server'
$s = New-Object -ComObject WScript.Shell
$menu = "$env:ProgramData\Microsoft\Windows\Start Menu\Programs"
function Link($path, $arguments, $desc, $icon) {
  $l = $s.CreateShortcut($path)
  $l.TargetPath = $exe; $l.Arguments = $arguments; $l.Description = $desc; $l.IconLocation = $icon
  $l.Save()
}
New-Item -ItemType Directory -Force "$menu\System Tools" | Out-Null
Link "$menu\System Tools\Restart into Windows (SSD).lnk" 'restart' 'Restart straight back into this Windows instead of the host system' "$env:SystemRoot\System32\shell32.dll,238"
if (-not $server) {
  foreach ($d in $menu, "$env:PUBLIC\Desktop") {
    Link "$d\Linux.lnk" 'desktop' "The host's Linux in a VM, full screen on every monitor" "$prog\linux.ico,0"
    Link "$d\Linux terminal.lnk" 'terminal' "The host's Linux in a VM, console only" "$prog\terminal.ico,0"
  }
}`
	out, err := hidden(time.Minute, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script).CombinedOutput()
	if msg := strings.TrimSpace(string(out)); err != nil || msg != "" {
		log.Printf("ярлыки: %v %s", err, strings.ReplaceAll(msg, "\r\n", "; "))
	}
	if err != nil {
		return 1
	}
	return 0
}

func readFirstLine(path string) string {
	b, _ := os.ReadFile(path)
	line, _, _ := strings.Cut(string(b), "\n")
	return line
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

// growSystemDrive растягивает C: на свободное место после него: образ диска (сервер,
// build/server/write.sh) пишется на диск больше себя, и write.sh растягивает раздел, а ФС —
// здесь, при входе.
func growSystemDrive() {
	script := `$max = (Get-PartitionSupportedSize -DriveLetter C).SizeMax
$cur = (Get-Partition -DriveLetter C).Size
if ($max - $cur -gt 1GB) { Resize-Partition -DriveLetter C -Size $max; "C: $([math]::Round($cur/1GB)) -> $([math]::Round($max/1GB)) GiB" }`
	out, err := hidden(5*time.Minute, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script).CombinedOutput()
	if msg := strings.TrimSpace(string(out)); err != nil || msg != "" {
		log.Printf("диск C: %v %s", err, msg)
	}
}
