//go:build linux

package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"unsafe"

	"lolssd/efi"
)

const (
	efivars   = "/sys/firmware/efi/efivars/"
	globalVar = "-" + efi.GlobalGUID
	bootLabel = "lolssd"
	bootFile  = `\EFI\BOOT\BOOTX64.EFI`
	partLabel = "LOLSSD"
)

var bootVarRe = regexp.MustCompile(`^Boot([0-9A-F]{4})` + globalVar + `$`)

// boot ставит BootNext на ESP этого SSD и перезагружает машину.
// --cleanup удаляет созданную нами запись, --no-reboot только ставит BootNext.
func boot(args []string) int {
	cleanup, noReboot := false, false
	for _, a := range args {
		switch a {
		case "--cleanup":
			cleanup = true
		case "--no-reboot":
			noReboot = true
		default:
			usage()
		}
	}
	if os.Geteuid() != 0 {
		fmt.Fprintln(os.Stderr, "нужны права root: sudo lolssd boot")
		return 1
	}
	if cleanup {
		return bootCleanup()
	}

	esp, err := findESP()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}

	num, err := entryFor(esp)
	if err != nil {
		fmt.Fprintln(os.Stderr, "не удалось подготовить загрузочную запись:", err)
		return 1
	}
	if err := writeVar("BootNext", binary.LittleEndian.AppendUint16(nil, num)); err != nil {
		fmt.Fprintln(os.Stderr, "не удалось записать BootNext:", err)
		return 1
	}
	fmt.Printf("Следующая загрузка: Boot%04X (SSD). Порядок загрузки хоста не менялся.\n", num)
	if noReboot {
		return 0
	}

	fmt.Println("Перезагрузка...")
	if err := exec.Command("systemctl", "reboot").Run(); err != nil {
		if err := exec.Command("reboot").Run(); err != nil {
			fmt.Fprintln(os.Stderr, "не удалось перезагрузить, перезагрузитесь вручную:", err)
			return 1
		}
	}
	return 0
}

// findESP находит ESP на том же диске, где лежит бинарник. Бинарник обязан лежать на
// разделе LOLSSD: так lolssd, случайно запущенный с диска хоста, не нацелится на ESP хоста.
func findESP() (efi.Part, error) {
	dev, err := exeBlockDev()
	if err != nil {
		return efi.Part{}, fmt.Errorf("не удалось определить диск, с которого запущен lolssd: %w", err)
	}
	b, err := os.ReadFile(filepath.Join(dev, "partition"))
	if err != nil {
		return efi.Part{}, errors.New("lolssd запущен не с раздела диска; запускайте его с раздела " + partLabel + " на SSD")
	}
	self, _ := strconv.Atoi(strings.TrimSpace(string(b)))

	diskPath := "/dev/" + filepath.Base(filepath.Dir(dev))
	f, err := os.Open(diskPath)
	if err != nil {
		return efi.Part{}, err
	}
	defer f.Close()
	parts, err := efi.ReadGPT(f)
	if err != nil {
		return efi.Part{}, fmt.Errorf("%s: %w", diskPath, err)
	}

	var esps []efi.Part
	selfOK := false
	for _, p := range parts {
		if p.Num == self && p.Name == partLabel {
			selfOK = true
		}
		if p.Type == efi.ESPType {
			esps = append(esps, p)
		}
	}
	if !selfOK {
		return efi.Part{}, errors.New("lolssd лежит не на разделе " + partLabel + ": отказ, чтобы не нацелиться на чужой диск")
	}
	if len(esps) != 1 {
		return efi.Part{}, fmt.Errorf("на SSD должен быть ровно один ESP, найдено %d", len(esps))
	}
	return esps[0], nil
}

// entryFor возвращает номер Boot#### для ESP: свою запись lolssd, если она уже есть,
// иначе создаёт новую. Записи прошивки не используются: у USB-записей часто нет
// узла HD() с GUID раздела, и проверить, что они ведут именно на этот SSD, нельзя.
func entryFor(esp efi.Part) (uint16, error) {
	entries, err := bootEntries()
	if err != nil {
		return 0, err
	}
	for num, opt := range entries {
		if desc, guid := efi.ParseLoadOption(opt); desc == bootLabel && guid == esp.GUID {
			return num, nil
		}
	}
	for n := 0; n <= 0xffff; n++ {
		if _, used := entries[uint16(n)]; !used {
			name := fmt.Sprintf("Boot%04X", n)
			return uint16(n), writeVar(name, efi.BuildLoadOption(bootLabel, bootFile, esp))
		}
	}
	return 0, errors.New("нет свободного номера Boot####")
}

func bootCleanup() int {
	entries, err := bootEntries()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	next, _ := os.ReadFile(efivars + "BootNext" + globalVar)
	removed := 0
	for num, opt := range entries {
		if desc, _ := efi.ParseLoadOption(opt); desc != bootLabel {
			continue
		}
		if err := removeVar(fmt.Sprintf("Boot%04X", num)); err != nil {
			fmt.Fprintln(os.Stderr, "не удалось удалить запись:", err)
			return 1
		}
		if len(next) == 6 && binary.LittleEndian.Uint16(next[4:]) == num {
			removeVar("BootNext")
		}
		fmt.Printf("Удалена запись Boot%04X (%s)\n", num, bootLabel)
		removed++
	}
	if removed == 0 {
		fmt.Println("Записей " + bootLabel + " нет, удалять нечего")
	}
	return 0
}

// bootEntries читает все Boot#### без 4 байт атрибутов efivarfs.
func bootEntries() (map[uint16][]byte, error) {
	dir, err := os.ReadDir(efivars)
	if err != nil {
		return nil, fmt.Errorf("efivarfs недоступен: %w", err)
	}
	entries := map[uint16][]byte{}
	for _, e := range dir {
		m := bootVarRe.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		b, err := os.ReadFile(efivars + e.Name())
		if err != nil || len(b) < 4 {
			continue
		}
		n, _ := strconv.ParseUint(m[1], 16, 16)
		entries[uint16(n)] = b[4:]
	}
	return entries, nil
}

// writeVar записывает глобальную переменную UEFI с атрибутами NV|BS|RT.
// efivarfs требует, чтобы всё значение пришло одной записью.
func writeVar(name string, data []byte) error {
	path := efivars + name + globalVar
	clearImmutable(path)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE, 0o644)
	if err != nil {
		return err
	}
	_, err = f.Write(append([]byte{7, 0, 0, 0}, data...))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

func removeVar(name string) error {
	path := efivars + name + globalVar
	clearImmutable(path)
	return os.Remove(path)
}

// clearImmutable снимает флаг immutable, который ядро ставит на переменные efivarfs
// (защита от случайного rm). Ошибка не важна: если флага нет, запись и так пройдёт.
func clearImmutable(path string) {
	const getFlags, setFlags, immutable = 0x80086601, 0x40086602, 0x10
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	var flags uint64
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), getFlags, uintptr(unsafe.Pointer(&flags))); e != 0 {
		return
	}
	if flags&immutable != 0 {
		flags &^= immutable
		syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), setFlags, uintptr(unsafe.Pointer(&flags)))
	}
}
