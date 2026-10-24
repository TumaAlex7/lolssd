//go:build windows

package main

import (
	"debug/pe"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"lolssd/hostprofile"
	"lolssd/keymap"
	"lolssd/winctl"
)

var (
	procSetDpiAwareness = user32.NewProc("SetProcessDpiAwarenessContext")
	procRegisterHotKey  = user32.NewProc("RegisterHotKey")
	procSetTimer        = user32.NewProc("SetTimer")
	procGetMessage      = user32.NewProc("GetMessageW")
	procGetCursorPos    = user32.NewProc("GetCursorPos")
)

const (
	hotkeyToggle = 1
	wmHotkey     = 0x0312
	wmTimer      = 0x0113
	modAlt       = 0x1
	modControl   = 0x2
	modNoRepeat  = 0x4000
	vkF12        = 0x7b
	qmpPort      = 4444
)

type msg struct {
	Hwnd           uintptr
	Message        uint32
	WParam, LParam uintptr
	Time           uint32
	X, Y           int32
	Private        uint32
}

var _ [48]byte = [unsafe.Sizeof(msg{})]byte{} // MSG на amd64

var dataDir = filepath.Join(os.Getenv("ProgramData"), "lolssd")

// desktop — ярлык «Linux desktop (host)» (docs/ARCHITECTURE.md §6): система хоста в ВМ QEMU с
// WHPX, своим ядром со своих дисков; на каждый монитор Windows — полноэкранный TigerVNC на
// выход virtio-gpu этой ВМ.
//
// Диски хоста целиком открывает только администратор, поэтому из ярлыка программа
// перезапускает себя с запросом UAC. Монитор отдают Windows, закрыв его просмотрщик
// (Ctrl+Alt+M → Exit viewer), и возвращают Linux сочетанием Ctrl+Alt+F12 на нём. Всё
// заканчивается, когда система в ВМ выключается: тогда закрываются и просмотрщики.
func desktop() int {
	if !windows.GetCurrentProcessToken().IsElevated() {
		exe, _ := os.Executable()
		if err := windows.ShellExecute(0, windows.StringToUTF16Ptr("runas"), windows.StringToUTF16Ptr(exe),
			windows.StringToUTF16Ptr("desktop"), nil, windows.SW_HIDE); err != nil {
			log.Println("запуск с правами администратора:", err)
			return 1
		}
		return 0
	}
	runtime.LockOSThread() // горячая клавиша и таймер привязаны к потоку
	// Координаты CCD API физические: процесс должен понимать DPI каждого монитора.
	const perMonitorAwareV2 = ^uintptr(3) // DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2 = -4
	procSetDpiAwareness.Call(perMonitorAwareV2)

	prog := filepath.Join(os.Getenv("ProgramFiles"), "lolssd")
	viewer := filepath.Join(prog, "vncviewer.exe")
	qemu := filepath.Join(prog, "qemu", "qemu-system-x86_64w.exe")
	for _, f := range []string{viewer, qemu} {
		if _, err := os.Stat(f); err != nil {
			return fail("Нет %s — образ собран без него.", f)
		}
	}
	if !hypervisorPlatform() {
		return 1
	}
	vol, err := lolssdVolume()
	if err != nil {
		return fail("%v", err)
	}
	p, disks, err := findProfile(vol)
	if err != nil {
		return fail("%v.\nСнимите профиль на хосте: lolssd profile с раздела LOLSSD.", err)
	}
	if p.VM == nil {
		return fail("В профиле нет образа загрузки для ВМ: снимите профиль заново (lolssd profile).")
	}
	mons, err := activeMonitors()
	if err != nil || len(mons) == 0 {
		return fail("Мониторы Windows: %v", err)
	}
	mons = distinctMonitors(mons)

	// Журналы прошлого запуска — в prev-*.
	for _, f := range []string{"serial.log", "qemu.log", "vncviewer-1.log", "vncviewer-2.log", "vncviewer-3.log", "vncviewer-4.log"} {
		os.Rename(filepath.Join(dataDir, f), filepath.Join(dataDir, "prev-"+f))
	}
	vm := winctl.VM{
		MemMiB:    max(totalMemoryMiB()/2, 4096),
		CPUs:      max(runtime.NumCPU()-2, 2),
		Monitors:  mons,
		AudioPCI:  p.AudioPCI,
		Keyd:      keymap.VMConfig(p.Keyboard.Keyd),
		SerialLog: filepath.Join(dataDir, "serial.log"),
		QMPPort:   qmpPort,
	}
	vm.Kernel, vm.Initrd, vm.Cmdline, err = unpackUKI(filepath.Join(vol, p.VM.Path), filepath.Join(dataDir, "vm"))
	if err != nil {
		return fail("Образ загрузки %s: %v", p.VM.Path, err)
	}
	// MAC гостя постоянный (из machine-id): ICS выдаёт ему по DHCP один и тот же адрес.
	vm.MAC = "52:54:00:" + p.MachineID[0:2] + ":" + p.MachineID[2:4] + ":" + p.MachineID[4:6]
	if vm.Tap, err = vmNetwork(); err != nil {
		log.Println("сеть ВМ через TAP недоступна, встроенная сеть QEMU (zapret в ней не работает):", err)
	}
	for _, d := range disks {
		vm.Disks = append(vm.Disks, winctl.VMDisk{Path: fmt.Sprintf(`\\.\PhysicalDrive%d`, d.N), Model: d.Info.Model, Serial: d.Info.Serial})
	}
	log.Printf("ВМ: профиль %s, ядро %s, дисков %d, мониторов %d, память %d МиБ, процессоров %d",
		p.MachineID, p.VM.Kernel, len(vm.Disks), len(mons), vm.MemMiB, vm.CPUs)

	// WHPX у части систем не переносит прерывания в гипервизоре: QEMU сразу падает. Тогда —
	// второй раз, с контроллером прерываний в QEMU.
	proc, err := startQEMU(qemu, vm)
	if err == nil && exitsWithin(proc, 15*time.Second) {
		log.Println("QEMU завершился сразу, запуск с kernel-irqchip=off")
		os.Rename(filepath.Join(dataDir, "qemu.log"), filepath.Join(dataDir, "qemu-irqchip.log"))
		vm.IrqchipOff = true
		proc, err = startQEMU(qemu, vm)
		if err == nil && exitsWithin(proc, 15*time.Second) {
			err = fmt.Errorf("QEMU завершился сразу")
		}
	}
	if err != nil {
		b, _ := os.ReadFile(filepath.Join(dataDir, "qemu.log"))
		return fail("ВМ не запустилась: %v\n\n%s\n\nЖурналы: %s", err, tail(string(b), 1500), dataDir)
	}
	vmDone := make(chan struct{})
	go func() { proc.Wait(); close(vmDone) }()

	if r, _, err := procRegisterHotKey.Call(0, hotkeyToggle, modControl|modAlt|modNoRepeat, vkF12); r == 0 {
		log.Println("RegisterHotKey Ctrl+Alt+F12:", err) // не мешает: остаётся Ctrl+Alt+M в TigerVNC
	}
	procSetTimer.Call(0, 0, 500, 0)

	viewers := make([]*exec.Cmd, len(mons))
	started := make([]time.Time, len(mons))
	given := make([]bool, len(mons)) // отдан Windows
	sized := make([]bool, len(mons)) // выход включён запросом размера (первый включён сразу)
	sized[0] = true
	exited := make(chan int, len(mons))
	open := func(i int) {
		c := exec.Command(viewer, winctl.ViewerArgs(mons[i], i)...)
		// Журнал просмотрщика: выбранное кодирование и запросы размера экрана. У оконной
		// программы stderr иначе никуда не идёт.
		if f, err := os.OpenFile(filepath.Join(dataDir, fmt.Sprintf("vncviewer-%d.log", i+1)),
			os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err == nil {
			c.Stdout, c.Stderr = f, f
			defer f.Close()
		}
		if err := c.Start(); err != nil {
			log.Println("vncviewer:", err)
			return
		}
		viewers[i], started[i] = c, time.Now()
		go func() { c.Wait(); exited <- i }()
	}
	closeAll := func() {
		for _, c := range viewers {
			if c != nil {
				c.Process.Kill()
			}
		}
	}

	var m msg
	for {
		r, _, _ := procGetMessage.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) <= 0 {
			closeAll()
			return 0
		}
		switch m.Message {
		case wmTimer:
			select {
			case <-vmDone:
				closeAll()
				log.Println("ВМ выключена")
				return 0
			default:
			}
		drain:
			for {
				select {
				case i := <-exited:
					// Закрыли сами — монитор отдан Windows. Вылетел сразу после запуска — VNC
					// ещё не принимал, откроем снова.
					if time.Since(started[i]) > 3*time.Second && listening(winctl.VNCPort(i)) {
						given[i] = true
						log.Printf("монитор %d отдан Windows", i+1)
					}
					viewers[i] = nil
				default:
					break drain
				}
			}
			for i := range mons {
				if viewers[i] == nil && !given[i] && listening(winctl.VNCPort(i)) {
					if !sized[i] {
						addr := fmt.Sprintf("127.0.0.1:%d", winctl.VNCPort(i))
						if err := winctl.SetDesktopSize(addr, mons[i].Width, mons[i].Height); err != nil {
							log.Printf("монитор %d: размер %dx%d: %v", i+1, mons[i].Width, mons[i].Height, err)
							continue
						}
						sized[i] = true
					}
					open(i)
				}
			}

		case wmHotkey:
			var pt struct{ X, Y int32 }
			procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
			k := winctl.MonitorAt(mons, int(pt.X), int(pt.Y))
			if k < 0 {
				continue
			}
			if viewers[k] != nil {
				viewers[k].Process.Kill() // дальше как при закрытии: given[k] выставит таймер
				started[k] = time.Time{}
			} else {
				given[k] = false
				log.Printf("монитор %d возвращён Linux", k+1)
			}
		}
	}
}

// startQEMU запускает QEMU без окна, вывод — в qemu.log.
func startQEMU(exe string, vm winctl.VM) (*os.Process, error) {
	f, err := os.Create(filepath.Join(dataDir, "qemu.log"))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	args := append([]string{"-L", filepath.Join(filepath.Dir(exe), "share")}, winctl.QEMUArgs(vm)...)
	fmt.Fprintf(f, "%s %s\n\n", exe, strings.Join(args, " "))
	c := exec.Command(exe, args...)
	c.Stdout, c.Stderr = f, f
	const createNoWindow = 0x08000000
	c.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
	if err := c.Start(); err != nil {
		return nil, err
	}
	return c.Process, nil
}

func exitsWithin(p *os.Process, d time.Duration) bool {
	h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(p.Pid))
	if err != nil {
		return true
	}
	defer windows.CloseHandle(h)
	r, _ := windows.WaitForSingleObject(h, uint32(d/time.Millisecond))
	return r == windows.WAIT_OBJECT_0
}

// unpackUKI достаёт из UKI ядро, initramfs и командную строку (секции PE .linux, .initrd,
// .cmdline) для прямого запуска ядра в QEMU.
func unpackUKI(uki, dir string) (kernel, initrd, cmdline string, err error) {
	f, err := pe.Open(uki)
	if err != nil {
		return "", "", "", err
	}
	defer f.Close()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", "", "", err
	}
	section := func(name string) ([]byte, error) {
		s := f.Section(name)
		if s == nil {
			return nil, fmt.Errorf("нет секции %s", name)
		}
		b, err := s.Data()
		if err != nil {
			return nil, err
		}
		// Размер в памяти может быть меньше выровненного размера в файле.
		if s.VirtualSize != 0 && int(s.VirtualSize) < len(b) {
			b = b[:s.VirtualSize]
		}
		return b, nil
	}
	for _, x := range []struct {
		sec  string
		path *string
	}{{".linux", &kernel}, {".initrd", &initrd}} {
		b, err := section(x.sec)
		if err != nil {
			return "", "", "", err
		}
		*x.path = filepath.Join(dir, strings.TrimPrefix(x.sec, "."))
		if err := os.WriteFile(*x.path, b, 0o644); err != nil {
			return "", "", "", err
		}
	}
	b, err := section(".cmdline")
	if err != nil {
		return "", "", "", err
	}
	return kernel, initrd, strings.TrimSpace(strings.TrimRight(string(b), "\x00")), nil
}

// hypervisorPlatform проверяет, что в Windows включена «Платформа низкоуровневой оболочки
// Windows» (WHPX, через неё QEMU пользуется гипервизором). Нет — включает её (DISM) и
// предлагает перезагрузку: компонент начинает работать только после неё.
func hypervisorPlatform() bool {
	if _, err := os.Stat(filepath.Join(os.Getenv("SystemRoot"), "System32", "WinHvPlatform.dll")); err == nil {
		return true
	}
	log.Println("WHPX выключен, включаю HypervisorPlatform")
	out, err := hidden(10*time.Minute, "dism.exe", "/Online", "/Enable-Feature", "/FeatureName:HypervisorPlatform",
		"/All", "/NoRestart").CombinedOutput()
	// 3010 — успешно, нужна перезагрузка.
	if err != nil && !strings.Contains(err.Error(), "3010") {
		fail("Не удалось включить платформу гипервизора Windows: %v\n%s", err, tail(string(out), 800))
		return false
	}
	const mbYesNo, mbIconInfo, mbSetForeground, idYes = 0x4, 0x40, 0x10000, 6
	r, _, _ := procMessageBox.Call(0, uintptr(unsafe.Pointer(windows.StringToUTF16Ptr(
		"Для Linux включена платформа гипервизора Windows. Она заработает после перезагрузки.\n\n"+
			"Перезагрузить сейчас? Компьютер вернётся в эту Windows."))),
		uintptr(unsafe.Pointer(windows.StringToUTF16Ptr("lolssd"))), mbYesNo|mbIconInfo|mbSetForeground)
	if r == idYes {
		restartSelf()
	}
	return false
}

// distinctMonitors убирает повторы: в режиме «Дублировать» несколько мониторов показывают одну
// и ту же область рабочего стола.
func distinctMonitors(mons []hostprofile.WinMonitor) []hostprofile.WinMonitor {
	var res []hostprofile.WinMonitor
	for _, m := range mons {
		dup := false
		for _, r := range res {
			dup = dup || r.X == m.X && r.Y == m.Y && r.Width == m.Width && r.Height == m.Height
		}
		if !dup {
			res = append(res, m)
		}
	}
	return res
}

// fail пишет ошибку в журнал и показывает её: ярлык запущен без консоли.
func fail(format string, a ...any) int {
	s := fmt.Sprintf(format, a...)
	log.Println(s)
	const mbOK, mbIconError, mbSetForeground = 0x0, 0x10, 0x10000
	procMessageBox.Call(0, uintptr(unsafe.Pointer(windows.StringToUTF16Ptr(s))),
		uintptr(unsafe.Pointer(windows.StringToUTF16Ptr("lolssd: Linux"))), mbOK|mbIconError|mbSetForeground)
	return 1
}

func tail(s string, n int) string {
	if len(s) > n {
		return "…" + s[len(s)-n:]
	}
	return s
}

func listening(port int) bool {
	c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 200*time.Millisecond)
	if err != nil {
		return false
	}
	c.Close()
	return true
}
