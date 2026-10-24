package winctl

import (
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"

	"lolssd/hostprofile"
)

// ConsolePort — второй COM-порт ВМ (ttyS1, getty) на 127.0.0.1: к нему подключается окно
// «Linux terminal». Первый COM-порт — журнал загрузки в файл.
const ConsolePort = 4555

// VM — что нужно для запуска системы хоста в QEMU с WHPX (docs/ARCHITECTURE.md §6).
type VM struct {
	Kernel, Initrd, Cmdline string // из UKI профиля
	Disks                   []VMDisk
	MemMiB, CPUs            int
	Monitors                []hostprofile.WinMonitor // по одному выходу virtio-gpu на монитор, слева направо
	AudioPCI                string                   // адрес звуковой карты хоста, см. hostprofile.Profile
	Keyd                    string                   // конфиг keyd для ВМ (keymap.VMConfig), пусто — без keyd
	Tap, MAC                string                   // адаптер TAP Windows и MAC гостя; без Tap — встроенная сеть QEMU
	SerialLog               string
	QMPPort                 int
	IrqchipOff              bool // запасной режим WHPX: прерывания в QEMU, а не в гипервизоре
}

// VMDisk — диск хоста целиком (\\.\PhysicalDriveN), который Windows держит отключённым.
type VMDisk struct {
	Path, Model, Serial string
}

// QEMUArgs — командная строка qemu-system-x86_64.
//   - Ядро грузится напрямую (-kernel), без прошивки UEFI: быстрее и без загрузочного диска.
//   - -cpu max без вложенной виртуализации: с -cpu host и VMX WHPX падает с «Unexpected VP exit
//     code 4».
//   - Экран — virtio-gpu, по выходу на монитор Windows, каждый отдаёт свой VNC-сервер QEMU
//     (только изменившиеся области) на VNCPort(i); у каждого выхода свой планшет для мыши.
//   - Звуковая карта — на том же PCI-адресе, что у хоста (hdaArgs).
//   - Сеть — через адаптер TAP Windows (пакеты гостя как есть, это нужно zapret), без него —
//     встроенная сеть QEMU.
func QEMUArgs(v VM) []string {
	accel := "whpx"
	if v.IrqchipOff {
		accel += ",kernel-irqchip=off"
	}
	a := []string{
		"-name", "lolssd", "-nodefaults", "-machine", "q35,vmport=off", "-accel", accel,
		"-cpu", "max,vmx=off,svm=off", "-smp", strconv.Itoa(v.CPUs), "-m", strconv.Itoa(v.MemMiB),
		"-kernel", v.Kernel, "-initrd", v.Initrd,
		// Вход в консоли (ярлык «Linux terminal»): getty на втором COM-порту.
		"-append", v.Cmdline + " systemd.wants=serial-getty@ttyS1.service",
	}
	for i, d := range v.Disks {
		a = append(a, "-drive", fmt.Sprintf("if=none,id=disk%d,format=raw,file=%s", i, optEscape(d.Path)),
			"-device", fmt.Sprintf("nvme,drive=disk%d,serial=%s,model=%s", i, optEscape(nvmeID(d.Serial, 20, fmt.Sprintf("lolssd%d", i))),
				optEscape(nvmeID(d.Model, 40, "QEMU NVMe Ctrl"))))
	}
	// Включён только первый выход; остальные включает запрос размера по VNC (SetDesktopSize,
	// lolssd-win до открытия просмотрщиков). Список outputs с размерами включил бы их сразу, но
	// с ним QEMU 11 отдаёт события указателя всех выходов планшету последнего (проверено на
	// стенде: VNC первого монитора двигал курсор по второму).
	a = append(a, "-device", fmt.Sprintf("virtio-gpu-pci,id=video0,max_outputs=%d", max(len(v.Monitors), 1)))
	if len(v.Monitors) > 0 {
		a[len(a)-1] += fmt.Sprintf(",xres=%d,yres=%d", v.Monitors[0].Width, v.Monitors[0].Height)
	}
	a = append(a, "-device", "qemu-xhci,id=xhci")
	for i := range v.Monitors {
		a = append(a,
			"-vnc", fmt.Sprintf("127.0.0.1:%d,id=vnc%d,display=video0,head=%d", VNCPort(i)-5900, i, i),
			"-device", fmt.Sprintf("usb-tablet,bus=xhci.0,serial=lolssd-head%d,display=video0,head=%d", i, i))
	}
	a = append(a, "-smbios", "type=11,value=io.systemd.credential.binary:udev.rules.70-lolssd-vm="+
		base64.StdEncoding.EncodeToString([]byte(VMUdevRules(v.Monitors))))
	a = append(a, keydArgs(v.Keyd)...)
	a = append(a, "-smbios", "type=11,value=io.systemd.credential.binary:systemd.unit-dropin.serial-getty@ttyS1.service="+
		base64.StdEncoding.EncodeToString([]byte("[Service]\nEnvironment=TERM=xterm-256color\n")))
	a = append(a, "-audiodev", "dsound,id=snd0")
	a = append(a, hdaArgs(v.AudioPCI)...)
	if v.Tap != "" {
		a = append(a, "-netdev", "tap,id=net0,ifname="+optEscape(v.Tap),
			"-device", "virtio-net-pci,netdev=net0,mac="+v.MAC)
	} else {
		a = append(a, "-nic", "user,model=virtio-net-pci")
	}
	a = append(a,
		"-display", "none", "-serial", "file:"+v.SerialLog,
		"-serial", fmt.Sprintf("tcp:127.0.0.1:%d,server=on,wait=off,nodelay=on", ConsolePort),
		"-qmp", fmt.Sprintf("tcp:127.0.0.1:%d,server=on,wait=off", v.QMPPort))
	return a
}

// VMUdevRules — правила udev, которые ВМ передаёт системе при загрузке (учётные данные systemd
// udev.rules.*, через SMBIOS; на диск хоста ничего не пишется).
//
// Клавиатура: PS/2-клавиатура QEMU не умеет F16–F23, поэтому Windows шлёт F23 как F24
// (Scancode Map в lolssd-win), а здесь скан-коду 0x76 возвращается F23. Встроенная команда
// keyboard — отдельным правилом: IMPORT выполняется до присваиваний своего правила.
//
// Мышь: у планшета каждого выхода — калибровочная матрица libinput. Hyprland растягивает абсолютные координаты устройства на всю раскладку мониторов,
// а планшет QEMU отдаёт их внутри своего выхода; матрица переводит одно в другое. Раскладка —
// как её ставит Hyprland без правил для выходов: в ряд слева направо, по верхнему краю, масштаб 1.
func VMUdevRules(mons []hostprofile.WinMonitor) string {
	var b strings.Builder
	b.WriteString(`ACTION!="remove", SUBSYSTEM=="input", KERNEL=="event*", ATTRS{name}=="AT Translated Set 2 keyboard", ENV{KEYBOARD_KEY_76}="f23"` + "\n")
	b.WriteString(`ACTION!="remove", SUBSYSTEM=="input", KERNEL=="event*", ENV{KEYBOARD_KEY_76}=="f23", IMPORT{builtin}="keyboard"` + "\n")
	total, height := 0, 0
	for _, m := range mons {
		total += m.Width
		height = max(height, m.Height)
	}
	x := 0
	for i, m := range mons {
		fmt.Fprintf(&b, `ACTION!="remove", SUBSYSTEM=="input", KERNEL=="event*", ATTRS{serial}=="lolssd-head%d", ENV{LIBINPUT_CALIBRATION_MATRIX}="%s 0 %s 0 %s 0"`+"\n",
			i, ratio(m.Width, total), ratio(x, total), ratio(m.Height, height))
		x += m.Width
	}
	return b.String()
}

func ratio(a, b int) string {
	return strconv.FormatFloat(float64(a)/float64(b), 'f', 6, 64)
}

// hdaArgs ставит звуковую карту (HDA ICH9 с кодеком на выход и вход) на адрес addr вида
// 0000:BB:00.F: шина BB — за BB-м корневым портом PCIe (порты нумеруются по порядку, их шины —
// 1, 2, …), а если функция не нулевая — в слоте нужна функция 0, иначе Linux слот не опрашивает:
// там пустое тестовое устройство. Адрес другого вида — карта просто на корневой шине.
func hdaArgs(addr string) []string {
	codec := []string{"-device", "hda-duplex,bus=hda.0,audiodev=snd0"}
	var bus, slot, fn int
	if n, _ := fmt.Sscanf(addr, "0000:%x:%x.%x", &bus, &slot, &fn); n != 3 || bus < 1 || bus > 32 || slot != 0 {
		return append([]string{"-device", "ich9-intel-hda,id=hda"}, codec...)
	}
	var a []string
	for k := 1; k <= bus; k++ {
		port := fmt.Sprintf("pcie-root-port,id=rp%d,chassis=%d,bus=pcie.0,addr=%#x.%d", k, k, 0x18+(k-1)/8, (k-1)%8)
		if (k-1)%8 == 0 {
			port += ",multifunction=on"
		}
		a = append(a, "-device", port)
	}
	if fn != 0 {
		a = append(a, "-device", fmt.Sprintf("pci-testdev,bus=rp%d,addr=0.0,multifunction=on", bus))
	}
	a = append(a, "-device", fmt.Sprintf("ich9-intel-hda,id=hda,bus=rp%d,addr=0.%d", bus, fn))
	return append(a, codec...)
}

// nvmeID — серийный номер или модель для NVMe ВМ: как у диска хоста (ссылки /dev/disk/by-id
// совпадают), в пределах длины поля NVMe и без запятых.
func nvmeID(s string, n int, def string) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, ",", " "))
	if s == "" {
		return def
	}
	if len(s) > n {
		s = s[:n]
	}
	return s
}

// optEscape удваивает запятые: в значениях параметров QEMU запятая — разделитель.
func optEscape(s string) string { return strings.ReplaceAll(s, ",", ",,") }

// keydArgs подменяет keyd в ВМ конфиг на сведённый для неё (keymap.VMConfig), не трогая /etc/keyd
// хоста: учётные данные systemd через SMBIOS — строка tmpfiles, которая пишет конфиг в
// /run/lolssd-vm/keyd, и дополнение keyd.service, которое показывает keyd эту папку вместо
// /etc/keyd. Всё это есть только в памяти ВМ.
func keydArgs(conf string) []string {
	if conf == "" {
		return nil
	}
	b64 := base64.StdEncoding.EncodeToString
	tmpfiles := "f~ /run/lolssd-vm/keyd/vm.conf 0644 root root - " + b64([]byte(conf)) + "\n"
	dropin := "[Service]\nBindReadOnlyPaths=/run/lolssd-vm/keyd:/etc/keyd\n"
	return []string{
		"-smbios", "type=11,value=io.systemd.credential.binary:tmpfiles.extra=" + b64([]byte(tmpfiles)),
		"-smbios", "type=11,value=io.systemd.credential.binary:systemd.unit-dropin.keyd.service=" + b64([]byte(dropin)),
	}
}
