//go:build linux

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"lolssd/hostprofile"
)

// Типы профиля общие с Windows-частью, см. пакет hostprofile.
type (
	profile  = hostprofile.Profile
	mount    = hostprofile.Mount
	blockDev = hostprofile.BlockDev
	disk     = hostprofile.Disk
	swap     = hostprofile.Swap
)

var hostFS = map[string]bool{"ext4": true, "ext3": true, "btrfs": true, "xfs": true, "f2fs": true}

func runProfile() int {
	if os.Geteuid() == 0 {
		fmt.Fprintln(os.Stderr, "запускайте без sudo: профиль описывает вашего пользователя и ваш сеанс")
		return 1
	}
	var p profile
	p.Version = 1
	p.Created = time.Now().UTC().Truncate(time.Second)
	b, _ := os.ReadFile("/etc/machine-id")
	p.MachineID = strings.TrimSpace(string(b))
	if p.MachineID == "" {
		fmt.Fprintln(os.Stderr, "нет /etc/machine-id")
		return 1
	}

	osRelease, _ := os.ReadFile("/etc/os-release")
	for _, line := range strings.Split(string(osRelease), "\n") {
		k, v, _ := strings.Cut(line, "=")
		v = strings.Trim(v, `"`)
		switch k {
		case "ID":
			p.Distro.ID = v
		case "ID_LIKE":
			p.Distro.IDLike = v
		}
	}

	u, err := user.Current()
	if err != nil {
		fmt.Fprintln(os.Stderr, "не удалось определить пользователя:", err)
		return 1
	}
	p.User.Name, p.User.Home = u.Username, u.HomeDir
	p.User.UID, _ = strconv.Atoi(u.Uid)
	p.User.GID, _ = strconv.Atoi(u.Gid)
	p.User.Shell = os.Getenv("SHELL")

	ssdDisk := ""
	if dev, err := exeBlockDev(); err == nil {
		ssdDisk = filepath.Base(filepath.Dir(dev))
	}
	p.Mounts, err = hostMounts(ssdDisk)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	p.Swaps = hostSwaps()
	p.Keyboard = hostKeyboard()
	p.AudioPCI = hostAudioPCI()

	dir, dirErr := lolssdDir()
	if dirErr == nil {
		if p.VM, err = buildVMImage(dir); err != nil {
			fmt.Fprintln(os.Stderr, "внимание: образ для ВМ не собран:", err)
		}
	}

	out, _ := json.MarshalIndent(p, "", "  ")
	out = append(out, '\n')

	if dirErr != nil {
		fmt.Fprintf(os.Stderr, "lolssd запущен не с раздела %s (%v), профиль выведен в stdout\n", partLabel, dirErr)
		os.Stdout.Write(out)
		return 0
	}
	path := filepath.Join(dir, "profiles", p.MachineID+".json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err == nil {
		err = os.WriteFile(path, out, 0o644)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "не удалось записать профиль:", err)
		return 1
	}
	fmt.Println("Профиль записан:", path)
	for _, m := range p.Mounts {
		fmt.Printf("  %-12s %-6s %s\n", m.Target, m.FSType, describe(m.Device))
	}
	if p.VM != nil {
		fmt.Printf("  образ для ВМ: %s (ядро %s)\n", filepath.Join(dir, p.VM.Path), p.VM.Kernel)
	}
	return 0
}

// lolssdDir возвращает точку монтирования раздела LOLSSD, если бинарник запущен с него.
func lolssdDir() (string, error) {
	dev, err := exeBlockDev()
	if err != nil {
		return "", err
	}
	label, err := filepath.EvalSymlinks("/dev/disk/by-partlabel/" + partLabel)
	if err != nil || filepath.Base(label) != filepath.Base(dev) {
		return "", fmt.Errorf("раздел %s не найден или это не он", partLabel)
	}
	b, _ := os.ReadFile(filepath.Join(dev, "dev"))
	majmin := strings.TrimSpace(string(b))
	for _, mi := range mountinfo() {
		if mi.majmin == majmin && mi.root == "/" {
			return mi.target, nil
		}
	}
	return "", fmt.Errorf("раздел %s не смонтирован", partLabel)
}

type mountEntry struct {
	majmin, root, target, fstype, source, options string
}

func mountinfo() []mountEntry {
	b, _ := os.ReadFile("/proc/self/mountinfo")
	var res []mountEntry
	for _, line := range strings.Split(string(b), "\n") {
		pre, post, ok := strings.Cut(line, " - ")
		if !ok {
			continue
		}
		f, g := strings.Fields(pre), strings.Fields(post)
		if len(f) < 6 || len(g) < 3 {
			continue
		}
		res = append(res, mountEntry{
			majmin: f[2], root: unescape(f[3]), target: unescape(f[4]),
			fstype: g[0], source: g[1], options: g[2],
		})
	}
	return res
}

// unescape раскрывает восьмеричные последовательности вроде \040 из mountinfo.
func unescape(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			if n, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				b.WriteByte(byte(n))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// hostMounts отбирает файловые системы хоста, которые нужно поднять внутри Windows:
// Linux-ФС на внутренних дисках, без /boot и ESP, без самого SSD и других USB-дисков.
func hostMounts(ssdDisk string) ([]mount, error) {
	var res []mount
	for _, mi := range mountinfo() {
		if !hostFS[mi.fstype] || mi.target == "/boot" || strings.HasPrefix(mi.target, "/boot/") || mi.target == "/efi" {
			continue
		}
		subvol := ""
		for _, o := range strings.Split(mi.options, ",") {
			if v, ok := strings.CutPrefix(o, "subvol="); ok {
				subvol = v
			}
		}
		// bind-монтирования части ФС пропускаем; у btrfs root — это подтом, его оставляем.
		if mi.root != "/" && mi.fstype != "btrfs" {
			continue
		}
		dev, err := resolveDev(mi.majmin)
		if err != nil {
			return nil, fmt.Errorf("не удалось разобрать устройство %s (%s): %w", mi.target, mi.source, err)
		}
		if onDisk(dev, ssdDisk) || onUSB(dev) {
			continue
		}
		res = append(res, mount{Target: mi.target, FSType: mi.fstype, Subvol: subvol, Options: mi.options, Device: dev})
	}
	if len(res) == 0 {
		return nil, fmt.Errorf("не найдено ни одной подходящей файловой системы хоста")
	}
	return res, nil
}

// resolveDev спускается по sysfs от устройства до разделов, через dm (LUKS, LVM).
func resolveDev(majmin string) (blockDev, error) {
	sys, err := filepath.EvalSymlinks("/sys/dev/block/" + majmin)
	if err != nil {
		return blockDev{}, err
	}
	d := blockDev{Name: filepath.Base(sys)}
	read := func(f string) string {
		b, _ := os.ReadFile(filepath.Join(sys, f))
		return strings.TrimSpace(string(b))
	}

	if uuid := read("dm/uuid"); uuid != "" || strings.HasPrefix(d.Name, "dm-") {
		d.DMName = read("dm/name")
		switch {
		case strings.HasPrefix(uuid, "CRYPT-LUKS"):
			d.Kind = "luks"
			// CRYPT-LUKS2-<32 hex>-<name> → UUID с дефисами.
			if parts := strings.SplitN(uuid, "-", 4); len(parts) >= 3 && len(parts[2]) == 32 {
				h := parts[2]
				d.LUKSUUID = h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
			}
		case strings.HasPrefix(uuid, "LVM-"):
			d.Kind = "lvm"
		default:
			d.Kind = "dm"
		}
		slaves, _ := os.ReadDir(filepath.Join(sys, "slaves"))
		for _, s := range slaves {
			mm := strings.TrimSpace(readFile(filepath.Join(sys, "slaves", s.Name(), "dev")))
			lower, err := resolveDev(mm)
			if err != nil {
				return blockDev{}, err
			}
			d.Lower = append(d.Lower, lower)
		}
		return d, nil
	}

	if n := read("partition"); n != "" {
		d.Kind = "partition"
		d.Partition, _ = strconv.Atoi(n)
		diskDir := filepath.Dir(sys)
		d.Disk = &disk{
			Name:   filepath.Base(diskDir),
			Model:  strings.TrimSpace(readFile(filepath.Join(diskDir, "device", "model"))),
			Serial: strings.TrimSpace(readFile(filepath.Join(diskDir, "device", "serial"))),
		}
		links, _ := os.ReadDir("/dev/disk/by-partuuid")
		for _, l := range links {
			if t, err := filepath.EvalSymlinks("/dev/disk/by-partuuid/" + l.Name()); err == nil && filepath.Base(t) == d.Name {
				d.PartUUID = l.Name()
			}
		}
		return d, nil
	}
	return blockDev{}, fmt.Errorf("%s: не раздел и не dm (ФС на диске целиком не поддерживается)", d.Name)
}

func readFile(path string) string {
	b, _ := os.ReadFile(path)
	return string(b)
}

func onDisk(d blockDev, name string) bool {
	if d.Disk != nil && d.Disk.Name == name {
		return true
	}
	for _, l := range d.Lower {
		if onDisk(l, name) {
			return true
		}
	}
	return false
}

func onUSB(d blockDev) bool {
	if d.Disk != nil {
		p, _ := filepath.EvalSymlinks("/sys/block/" + d.Disk.Name)
		return strings.Contains(p, "/usb")
	}
	for _, l := range d.Lower {
		if onUSB(l) {
			return true
		}
	}
	return false
}

func describe(d blockDev) string {
	s := d.Kind + " " + d.Name
	if d.Disk != nil {
		s += fmt.Sprintf(" (диск %s, %s)", d.Disk.Serial, strings.TrimSpace(d.Disk.Model))
	}
	for _, l := range d.Lower {
		s += " → " + describe(l)
	}
	return s
}

// hostSwaps нужен для проверки гибернации: в swap не должно быть образа спящего хоста.
func hostSwaps() []swap {
	b, _ := os.ReadFile("/proc/swaps")
	var res []swap
	for i, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if i == 0 || len(f) < 2 || strings.HasPrefix(f[0], "/dev/zram") {
			continue
		}
		s := swap{Path: unescape(f[0]), Type: f[1]}
		majmin := ""
		if s.Type == "partition" {
			majmin = strings.TrimSpace(readFile("/sys/class/block/" + filepath.Base(s.Path) + "/dev"))
		} else {
			// swap-файл: устройство той ФС, на которой он лежит.
			best := ""
			for _, mi := range mountinfo() {
				if strings.HasPrefix(s.Path, mi.target) && len(mi.target) > len(best) {
					best, majmin = mi.target, mi.majmin
				}
			}
		}
		if dev, err := resolveDev(majmin); err == nil {
			s.Device = dev
		}
		res = append(res, s)
	}
	return res
}

// hostKeyboard: все конфиги keyd — в ВМ keyd получает их сведёнными (keymap.VMConfig).
func hostKeyboard() hostprofile.Keyboard {
	var kb hostprofile.Keyboard
	files, _ := filepath.Glob("/etc/keyd/*.conf")
	for _, f := range files {
		if b, err := os.ReadFile(f); err == nil {
			if kb.Keyd == nil {
				kb.Keyd = map[string]string{}
			}
			kb.Keyd[filepath.Base(f)] = string(b)
		}
	}
	return kb
}

// hostAudioPCI — PCI-адрес первой звуковой карты HDA с аналоговым кодеком (не HDMI).
func hostAudioPCI() string {
	cards, _ := filepath.Glob("/sys/class/sound/card*")
	for _, c := range cards {
		dev, err := filepath.EvalSymlinks(filepath.Join(c, "device"))
		addr := filepath.Base(dev)
		if err != nil || strings.Count(addr, ":") != 2 {
			continue // не PCI (USB и т. п.)
		}
		codecs, _ := filepath.Glob(filepath.Join("/proc/asound", filepath.Base(c), "codec#*"))
		for _, f := range codecs {
			first, _, _ := strings.Cut(readFile(f), "\n")
			if !strings.Contains(first, "HDMI") {
				return addr
			}
		}
	}
	return ""
}
