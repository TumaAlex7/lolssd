package main

import (
	"bytes"
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
	"unicode/utf16"
	"unsafe"
)

const (
	efivars   = "/sys/firmware/efi/efivars/"
	globalVar = "-8be4df61-93ca-11d2-aa0d-00e098032b8c"
	bootLabel = "lolssd"
	bootFile  = `\EFI\BOOT\BOOTX64.EFI`
	partLabel = "LOLSSD"
)

// GUID типа раздела ESP в том порядке байт, в каком он лежит в GPT.
var espType = [16]byte{0x28, 0x73, 0x2a, 0xc1, 0x1f, 0xf8, 0xd2, 0x11, 0xba, 0x4b, 0x00, 0xa0, 0xc9, 0x3e, 0xc9, 0x3b}

var bootVarRe = regexp.MustCompile(`^Boot([0-9A-F]{4})` + globalVar + `$`)

type gptPart struct {
	num         int
	typ, guid   [16]byte
	first, last uint64
	name        string
}

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
func findESP() (gptPart, error) {
	dev, err := exeBlockDev()
	if err != nil {
		return gptPart{}, fmt.Errorf("не удалось определить диск, с которого запущен lolssd: %w", err)
	}
	b, err := os.ReadFile(filepath.Join(dev, "partition"))
	if err != nil {
		return gptPart{}, errors.New("lolssd запущен не с раздела диска; запускайте его с раздела " + partLabel + " на SSD")
	}
	self, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	diskDir := filepath.Dir(dev)
	b, _ = os.ReadFile(filepath.Join(diskDir, "queue", "logical_block_size"))
	sector, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	if sector == 0 {
		sector = 512
	}

	parts, err := readGPT("/dev/"+filepath.Base(diskDir), sector)
	if err != nil {
		return gptPart{}, err
	}
	var esps []gptPart
	selfOK := false
	for _, p := range parts {
		if p.num == self && p.name == partLabel {
			selfOK = true
		}
		if p.typ == espType {
			esps = append(esps, p)
		}
	}
	if !selfOK {
		return gptPart{}, errors.New("lolssd лежит не на разделе " + partLabel + ": отказ, чтобы не нацелиться на чужой диск")
	}
	if len(esps) != 1 {
		return gptPart{}, fmt.Errorf("на SSD должен быть ровно один ESP, найдено %d", len(esps))
	}
	return esps[0], nil
}

// entryFor возвращает номер Boot#### для ESP: свою запись lolssd, если она уже есть,
// иначе создаёт новую. Записи прошивки не используются: у USB-записей часто нет
// узла HD() с GUID раздела, и проверить, что они ведут именно на этот SSD, нельзя.
func entryFor(esp gptPart) (uint16, error) {
	entries, err := bootEntries()
	if err != nil {
		return 0, err
	}
	for num, opt := range entries {
		if desc, guid := parseLoadOption(opt); desc == bootLabel && guid == esp.guid {
			return num, nil
		}
	}
	for n := 0; n <= 0xffff; n++ {
		if _, used := entries[uint16(n)]; !used {
			name := fmt.Sprintf("Boot%04X", n)
			return uint16(n), writeVar(name, buildLoadOption(esp))
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
		if desc, _ := parseLoadOption(opt); desc != bootLabel {
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

// parseLoadOption разбирает EFI_LOAD_OPTION: описание и GUID раздела из узла HD(), если он есть.
func parseLoadOption(b []byte) (desc string, guid [16]byte) {
	if len(b) < 6 {
		return
	}
	pathLen := int(binary.LittleEndian.Uint16(b[4:6]))
	i := 6
	var u []uint16
	for ; i+1 < len(b); i += 2 {
		c := binary.LittleEndian.Uint16(b[i:])
		if c == 0 {
			break
		}
		u = append(u, c)
	}
	desc = string(utf16.Decode(u))
	i += 2
	if i+pathLen > len(b) {
		return
	}
	path := b[i : i+pathLen]
	for len(path) >= 4 {
		typ, sub, l := path[0], path[1], int(binary.LittleEndian.Uint16(path[2:4]))
		if l < 4 || l > len(path) || typ == 0x7f && sub == 0xff {
			break
		}
		// HD(): тип 4 (Media), подтип 1, длина 42; SignatureType 2 означает GUID раздела.
		if typ == 4 && sub == 1 && l >= 42 && path[41] == 2 {
			copy(guid[:], path[24:40])
		}
		path = path[l:]
	}
	return
}

// buildLoadOption собирает запись «lolssd → HD(ESP)/File(\EFI\BOOT\BOOTX64.EFI)».
// Короткая форма пути (с узла HD) по спецификации UEFI раскрывается прошивкой сама.
func buildLoadOption(esp gptPart) []byte {
	le := binary.LittleEndian
	hd := make([]byte, 42)
	hd[0], hd[1] = 4, 1
	le.PutUint16(hd[2:], 42)
	le.PutUint32(hd[4:], uint32(esp.num))
	le.PutUint64(hd[8:], esp.first)
	le.PutUint64(hd[16:], esp.last-esp.first+1)
	copy(hd[24:40], esp.guid[:])
	hd[40], hd[41] = 2, 2 // MBRType = GPT, SignatureType = GUID

	file := ucs2(bootFile)
	fileNode := append([]byte{4, 4, 0, 0}, file...)
	le.PutUint16(fileNode[2:], uint16(len(fileNode)))

	path := append(append(hd, fileNode...), 0x7f, 0xff, 4, 0)

	var b bytes.Buffer
	binary.Write(&b, le, uint32(1)) // LOAD_OPTION_ACTIVE
	binary.Write(&b, le, uint16(len(path)))
	b.Write(ucs2(bootLabel))
	b.Write(path)
	return b.Bytes()
}

func ucs2(s string) []byte {
	var b []byte
	for _, c := range utf16.Encode([]rune(s)) {
		b = binary.LittleEndian.AppendUint16(b, c)
	}
	return append(b, 0, 0)
}

// readGPT читает таблицу разделов GPT с диска (или образа).
func readGPT(path string, sector int) ([]gptPart, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	le := binary.LittleEndian
	hdr := make([]byte, 92)
	if _, err := f.ReadAt(hdr, int64(sector)); err != nil {
		return nil, fmt.Errorf("чтение GPT на %s: %w", path, err)
	}
	if string(hdr[:8]) != "EFI PART" {
		return nil, errors.New("на " + path + " нет таблицы разделов GPT")
	}
	lba, count, size := le.Uint64(hdr[72:]), int(le.Uint32(hdr[80:])), int(le.Uint32(hdr[84:]))
	if count > 1024 || size < 128 || size > 4096 {
		return nil, errors.New("повреждённый заголовок GPT на " + path)
	}
	buf := make([]byte, count*size)
	if _, err := f.ReadAt(buf, int64(lba)*int64(sector)); err != nil {
		return nil, fmt.Errorf("чтение разделов GPT на %s: %w", path, err)
	}

	var parts []gptPart
	for i := range count {
		e := buf[i*size : (i+1)*size]
		var p gptPart
		copy(p.typ[:], e[0:16])
		if p.typ == [16]byte{} {
			continue
		}
		copy(p.guid[:], e[16:32])
		p.num, p.first, p.last = i+1, le.Uint64(e[32:]), le.Uint64(e[40:])
		var u []uint16
		for j := 56; j+1 < 128; j += 2 {
			c := le.Uint16(e[j:])
			if c == 0 {
				break
			}
			u = append(u, c)
		}
		p.name = string(utf16.Decode(u))
		parts = append(parts, p)
	}
	return parts, nil
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
