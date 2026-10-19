//go:build linux

package main

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"

	"lolssd/efi"
	"lolssd/hostprofile"
)

// up поднимает систему хоста в rootDir. До любой записи проверяет, что хост выключился
// чисто: не в гибернации (иначе запись испортит ФС спящей системы) и без незавершённого
// журнала ext4. Повторный вызов, когда всё уже поднято, ничего не делает.
func up() int {
	if mounted(rootDir) {
		fmt.Println("система хоста уже поднята в", rootDir)
		return 0
	}
	p, err := loadProfile()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	parts, err := partitions()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}

	a := activator{parts: parts}
	devs := map[string]string{} // точка монтирования → устройство
	for _, m := range p.Mounts {
		if devs[m.Target], err = a.activate(m.Device); err != nil {
			return fail(err)
		}
	}

	var problems []string
	for _, s := range p.Swaps {
		if s.Type != "partition" {
			continue
		}
		dev, err := a.activate(s.Device)
		if err != nil {
			return fail(err)
		}
		if msg := checkSwap(dev, s.Path); msg != "" {
			problems = append(problems, msg)
		}
	}
	for _, m := range p.Mounts {
		if msg := checkFS(devs[m.Target], m); msg != "" {
			problems = append(problems, msg)
		}
	}

	// swap-файлы лежат внутри ФС: смотрим их через монтирование только для чтения, без
	// восстановления журналов, чтобы не записать на диск ни байта до проверки.
	if err := mountTree(p.Mounts, devs, checkDir, true); err != nil {
		return fail(err)
	}
	for _, s := range p.Swaps {
		if s.Type == "file" {
			if msg := checkSwap(filepath.Join(checkDir, s.Path), s.Path); msg != "" {
				problems = append(problems, msg)
			}
		}
	}
	unmountTree(checkDir)

	if len(problems) > 0 {
		fmt.Fprintln(os.Stderr, "Система хоста выключилась не чисто, поднимать её на запись нельзя:")
		for _, msg := range problems {
			fmt.Fprintln(os.Stderr, "  -", msg)
		}
		fmt.Fprintln(os.Stderr, "Загрузите хост в Linux, выключите его обычным способом (не гибернацией) и повторите.")
		down()
		return 1
	}

	if err := mountTree(p.Mounts, devs, rootDir, false); err != nil {
		return fail(err)
	}
	for _, m := range sortedMounts(p.Mounts) {
		fmt.Printf("  %-12s %-6s %s\n", m.Target, m.FSType, devs[m.Target])
	}
	fmt.Println("система хоста поднята в", rootDir)
	return 0
}

func fail(err error) int {
	fmt.Fprintln(os.Stderr, err)
	down()
	return 1
}

// down опускает всё, что поднял up. Ошибки не важны: что-то могло быть и не поднято.
func down() int {
	unmountTree(checkDir)
	unmountTree(rootDir)
	exec.Command("vgchange", "-an").Run()
	names, _ := filepath.Glob("/dev/mapper/lolssd-*")
	for _, n := range names {
		exec.Command("cryptsetup", "close", filepath.Base(n)).Run()
	}
	exec.Command("vgchange", "-an").Run() // на случай LVM под LUKS
	return 0
}

func loadProfile() (hostprofile.Profile, error) {
	var p hostprofile.Profile
	b, err := os.ReadFile(profilePath)
	if err != nil {
		return p, fmt.Errorf("нет активного профиля (%w): диски хоста подключает lolssd-win mount при входе в Windows", err)
	}
	if err := json.Unmarshal(b, &p); err != nil {
		return p, fmt.Errorf("%s: %w", profilePath, err)
	}
	if len(p.Mounts) == 0 {
		return p, errors.New("в профиле нет файловых систем")
	}
	return p, nil
}

// partitions строит PARTUUID → /dev/<раздел> по GPT всех дисков, которые видит WSL.
// Не зависит от udev и blkid.
func partitions() (map[string]string, error) {
	res := map[string]string{}
	disks, _ := filepath.Glob("/sys/block/*")
	for _, d := range disks {
		f, err := os.Open("/dev/" + filepath.Base(d))
		if err != nil {
			continue
		}
		parts, err := efi.ReadGPT(f)
		f.Close()
		if err != nil {
			continue
		}
		byNum := map[int]string{}
		children, _ := os.ReadDir(d)
		for _, c := range children {
			if b, err := os.ReadFile(filepath.Join(d, c.Name(), "partition")); err == nil {
				n, _ := strconv.Atoi(strings.TrimSpace(string(b)))
				byNum[n] = "/dev/" + c.Name()
			}
		}
		for _, p := range parts {
			if dev := byNum[p.Num]; dev != "" {
				res[efi.GUIDString(p.GUID)] = dev
			}
		}
	}
	if len(res) == 0 {
		return nil, errors.New("WSL не видит ни одного раздела GPT: диски хоста не подключены (lolssd-win mount)")
	}
	return res, nil
}

// activator поднимает цепочки устройств профиля снизу вверх: раздел → LUKS → LVM.
type activator struct {
	parts  map[string]string
	lvmUp  bool
	opened map[string]string
}

func (a *activator) activate(d hostprofile.BlockDev) (string, error) {
	switch d.Kind {
	case "partition":
		if dev := a.parts[d.PartUUID]; dev != "" {
			return dev, nil
		}
		return "", fmt.Errorf("раздел %s (%s) не найден среди подключённых дисков", d.PartUUID, d.Name)

	case "luks":
		if len(d.Lower) != 1 || len(d.LUKSUUID) < 8 {
			return "", fmt.Errorf("LUKS %s: неполное описание в профиле", d.Name)
		}
		if dev := a.opened[d.LUKSUUID]; dev != "" {
			return dev, nil
		}
		lower, err := a.activate(d.Lower[0])
		if err != nil {
			return "", err
		}
		name := "lolssd-" + d.LUKSUUID[:8]
		dev := "/dev/mapper/" + name
		if _, err := os.Stat(dev); err != nil {
			fmt.Printf("Пароль от зашифрованного раздела %s (%s):\n", d.DMName, lower)
			cmd := exec.Command("cryptsetup", "open", "--type", "luks", lower, name)
			cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
			if err := cmd.Run(); err != nil {
				return "", fmt.Errorf("cryptsetup open %s: %w", lower, err)
			}
		}
		if a.opened == nil {
			a.opened = map[string]string{}
		}
		a.opened[d.LUKSUUID] = dev
		return dev, nil

	case "lvm":
		for _, l := range d.Lower {
			if _, err := a.activate(l); err != nil {
				return "", err
			}
		}
		if !a.lvmUp {
			if out, err := exec.Command("vgchange", "-ay").CombinedOutput(); err != nil {
				return "", fmt.Errorf("vgchange -ay: %w: %s", err, out)
			}
			a.lvmUp = true
		}
		dev := "/dev/mapper/" + d.DMName
		if _, err := os.Stat(dev); err != nil {
			return "", fmt.Errorf("том LVM %s не появился после vgchange", d.DMName)
		}
		return dev, nil
	}
	return "", fmt.Errorf("%s: тип устройства %q не поддерживается", d.Name, d.Kind)
}

// checkSwap читает первую страницу swap и ищет образ гибернации. path — для сообщения.
func checkSwap(file, path string) string {
	f, err := os.Open(file)
	if err != nil {
		return fmt.Sprintf("swap %s: не удалось прочитать (%v)", path, err)
	}
	defer f.Close()
	page := make([]byte, 4096)
	if _, err := f.ReadAt(page, 0); err != nil {
		return fmt.Sprintf("swap %s: не удалось прочитать (%v)", path, err)
	}
	if swapHibernated(page) {
		return fmt.Sprintf("в swap %s лежит образ гибернации", path)
	}
	return ""
}

// swapHibernated: в конце первой страницы swap лежит SWAPSPACE2, а у спящей системы —
// подпись образа гибернации.
func swapHibernated(page []byte) bool {
	sig := string(page[4086:4096])
	for _, s := range []string{"S1SUSPEND", "S2SUSPEND", "ULSUSPEND", "LINHIB0001"} {
		if strings.HasPrefix(sig, s) {
			return true
		}
	}
	return false
}

// checkFS проверяет то, что можно понять по суперблоку. Пока это ext3/ext4: незавершённый
// журнал (needs_recovery) значит, что ФС не размонтировали — система упала или спит.
func checkFS(dev string, m hostprofile.Mount) string {
	if m.FSType != "ext4" && m.FSType != "ext3" {
		return ""
	}
	f, err := os.Open(dev)
	if err != nil {
		return fmt.Sprintf("%s: не удалось прочитать суперблок (%v)", m.Target, err)
	}
	defer f.Close()
	sb := make([]byte, 1024)
	if _, err := f.ReadAt(sb, 1024); err != nil {
		return fmt.Sprintf("%s: не удалось прочитать суперблок (%v)", m.Target, err)
	}
	return ext4Problem(sb, m.Target)
}

func ext4Problem(sb []byte, target string) string {
	le := binary.LittleEndian
	if le.Uint16(sb[0x38:]) != 0xef53 {
		return fmt.Sprintf("%s: на устройстве не ext4", target)
	}
	const validFS, errorFS, needsRecovery = 0x1, 0x2, 0x4
	state, incompat := le.Uint16(sb[0x3a:]), le.Uint32(sb[0x60:])
	switch {
	case incompat&needsRecovery != 0:
		return fmt.Sprintf("%s: журнал ext4 не завершён (ФС не была размонтирована)", target)
	case state&errorFS != 0 || state&validFS == 0:
		return fmt.Sprintf("%s: ext4 помечена как содержащая ошибки, нужна проверка fsck на хосте", target)
	}
	return ""
}

func sortedMounts(ms []hostprofile.Mount) []hostprofile.Mount {
	s := append([]hostprofile.Mount(nil), ms...)
	sort.Slice(s, func(i, j int) bool { return len(s[i].Target) < len(s[j].Target) })
	return s
}

// mountTree монтирует ФС профиля под dir (сначала родители). readonly — только чтение
// без восстановления журналов: ничего не пишет на диск даже у «грязной» ФС.
func mountTree(ms []hostprofile.Mount, devs map[string]string, dir string, readonly bool) error {
	for _, m := range sortedMounts(ms) {
		target := filepath.Join(dir, m.Target)
		if err := os.MkdirAll(target, 0o755); err != nil && !readonly {
			return err
		}
		var flags uintptr
		var opts []string
		if m.Subvol != "" {
			opts = append(opts, "subvol="+m.Subvol)
		}
		if readonly {
			flags |= syscall.MS_RDONLY
			switch m.FSType {
			case "ext4", "ext3":
				opts = append(opts, "noload")
			case "btrfs":
				opts = append(opts, "rescue=nologreplay")
			case "xfs", "f2fs":
				opts = append(opts, "norecovery")
			}
		}
		if err := syscall.Mount(devs[m.Target], target, m.FSType, flags, strings.Join(opts, ",")); err != nil {
			unmountTree(dir)
			return fmt.Errorf("mount %s → %s (%s): %w", devs[m.Target], target, m.FSType, err)
		}
	}
	return nil
}

// unmountTree отмонтирует всё под dir, начиная с самых глубоких точек.
func unmountTree(dir string) {
	b, _ := os.ReadFile("/proc/self/mounts")
	var targets []string
	for _, line := range strings.Split(string(b), "\n") {
		if f := strings.Fields(line); len(f) > 1 && (f[1] == dir || strings.HasPrefix(f[1], dir+"/")) {
			targets = append(targets, f[1])
		}
	}
	sort.Slice(targets, func(i, j int) bool { return len(targets[i]) > len(targets[j]) })
	for _, t := range targets {
		syscall.Unmount(t, 0)
	}
}

func mounted(dir string) bool {
	b, _ := os.ReadFile("/proc/self/mounts")
	for _, line := range strings.Split(string(b), "\n") {
		if f := strings.Fields(line); len(f) > 1 && f[1] == dir {
			return true
		}
	}
	return false
}
