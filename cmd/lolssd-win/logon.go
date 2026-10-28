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

// logon — задание при входе пользователя (с повышенными правами): C: на весь диск, Scancode Map
// для ВМ (мониторы расставляет отдельное задание, lolssd-win displays). Профиля для этого компьютера нет — режим «Linux внутри Windows» не включали.
func logon() int {
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
	if removeOldScancodeMap() {
		const mbOK, mbIconInfo, mbSetForeground = 0x0, 0x40, 0x10000
		procMessageBox.Call(0, uintptr(unsafe.Pointer(windows.StringToUTF16Ptr(
			"Убрано старое переназначение клавиши Copilot (F23 → F24): теперь она доходит до Linux сама.\n"+
				"Изменение подействует после перезагрузки Windows."))),
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
	// PARTUUID → номер диска для всех дисков, включая системный: Windows может стоять рядом с
	// Linux на одном диске. ВМ тогда получает и системный диск, но записать в разделы, которые
	// Windows держит смонтированными (C: и т. п.), ей не даст сама Windows: запись через
	// \\.\PhysicalDriveN в сектора смонтированного тома запрещена.
	where := map[string]int{}
	for n := 0; n < 64; n++ {
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

// oldScancodeMap — Scancode Map прежних версий: F23 → F24 для клавиши Copilot в ВМ (эмуляция PS/2
// в QEMU не отдавала F23). Клавиатура ВМ теперь virtio и передаёт F23 сама.
var oldScancodeMap = keymap.ScancodeMap(map[uint16]uint16{0x006e: 0x0076})

// removeOldScancodeMap убирает эту запись, если её поставила прежняя версия (чужую не трогает).
// Драйвер клавиатуры читает Scancode Map при загрузке: true — нужна перезагрузка.
func removeOldScancodeMap() bool {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SYSTEM\CurrentControlSet\Control\Keyboard Layout`, registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	cur, _, _ := k.GetBinaryValue("Scancode Map")
	if !bytes.Equal(cur, oldScancodeMap) {
		return false
	}
	if err := k.DeleteValue("Scancode Map"); err != nil {
		log.Println("Scancode Map:", err)
		return false
	}
	log.Println("Scancode Map прежней версии убран, нужна перезагрузка")
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
//     только с опцией vm;
//   - «Restart into Windows» — во встроенной папке «Служебные — Windows» (System Tools),
//     с системным значком.
func shortcuts() int {
	prog := filepath.Join(os.Getenv("ProgramFiles"), "lolssd")
	script := `$prog = '` + prog + `'
$exe = "$prog\lolssd-win.exe"
$vm = '` + option("vm") + `' -eq '1'
$s = New-Object -ComObject WScript.Shell
$menu = "$env:ProgramData\Microsoft\Windows\Start Menu\Programs"
function Link($path, $arguments, $desc, $icon) {
  $l = $s.CreateShortcut($path)
  $l.TargetPath = $exe; $l.Arguments = $arguments; $l.Description = $desc; $l.IconLocation = $icon
  $l.Save()
}
New-Item -ItemType Directory -Force "$menu\System Tools" | Out-Null
Remove-Item "$menu\System Tools\Restart into Windows (SSD).lnk" -ErrorAction SilentlyContinue
Link "$menu\System Tools\Restart into Windows.lnk" 'restart' 'Restart straight back into this Windows instead of the host system' "$env:SystemRoot\System32\shell32.dll,238"
if ($vm) {
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

// option возвращает значение из файла опций сборки (build/options.default):
// C:\Windows\Setup\Scripts\lolssd-options.txt, строки key=value.
func option(name string) string {
	b, _ := os.ReadFile(filepath.Join(os.Getenv("SystemRoot"), "Setup", "Scripts", "lolssd-options.txt"))
	val := ""
	for _, line := range strings.Split(string(b), "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(line), "="); ok && k == name {
			val = v
		}
	}
	return val
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
