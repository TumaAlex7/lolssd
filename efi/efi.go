// Package efi — разбор GPT и загрузочных записей UEFI, общий для Linux- и Windows-утилит.
package efi

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"unicode/utf16"
)

// GlobalGUID — пространство имён глобальных переменных UEFI (Boot####, BootOrder, BootNext).
const GlobalGUID = "8be4df61-93ca-11d2-aa0d-00e098032b8c"

// ESPType — GUID типа раздела ESP в том порядке байт, в каком он лежит в GPT.
var ESPType = [16]byte{0x28, 0x73, 0x2a, 0xc1, 0x1f, 0xf8, 0xd2, 0x11, 0xba, 0x4b, 0x00, 0xa0, 0xc9, 0x3e, 0xc9, 0x3b}

type Part struct {
	Num         int
	Type, GUID  [16]byte
	First, Last uint64
	Name        string
}

// ReadGPT читает таблицу разделов GPT. Пробует логический сектор 512 и 4096 байт.
// Читает только целыми секторами: так требует Windows при чтении сырого диска.
func ReadGPT(r io.ReaderAt) ([]Part, error) {
	le := binary.LittleEndian
	for _, sector := range []int{512, 4096} {
		hdr := make([]byte, sector)
		if _, err := r.ReadAt(hdr, int64(sector)); err != nil {
			return nil, fmt.Errorf("чтение заголовка GPT: %w", err)
		}
		if string(hdr[:8]) != "EFI PART" {
			continue
		}
		lba, count, size := le.Uint64(hdr[72:]), int(le.Uint32(hdr[80:])), int(le.Uint32(hdr[84:]))
		if count > 1024 || size < 128 || size > 4096 {
			return nil, errors.New("повреждённый заголовок GPT")
		}
		n := (count*size + sector - 1) / sector * sector
		buf := make([]byte, n)
		if _, err := r.ReadAt(buf, int64(lba)*int64(sector)); err != nil {
			return nil, fmt.Errorf("чтение разделов GPT: %w", err)
		}

		var parts []Part
		for i := range count {
			e := buf[i*size : (i+1)*size]
			var p Part
			copy(p.Type[:], e[0:16])
			if p.Type == [16]byte{} {
				continue
			}
			copy(p.GUID[:], e[16:32])
			p.Num, p.First, p.Last = i+1, le.Uint64(e[32:]), le.Uint64(e[40:])
			p.Name = decodeUCS2(e[56:128])
			parts = append(parts, p)
		}
		return parts, nil
	}
	return nil, errors.New("нет таблицы разделов GPT")
}

// ParseLoadOption разбирает EFI_LOAD_OPTION: описание и GUID раздела из узла HD() первого пути.
func ParseLoadOption(b []byte) (desc string, guid [16]byte) {
	if len(b) < 6 {
		return
	}
	pathLen := int(binary.LittleEndian.Uint16(b[4:6]))
	desc = decodeUCS2(b[6:])
	i := 6 + 2*len(utf16.Encode([]rune(desc))) + 2
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

// BuildLoadOption собирает активную запись «label → HD(раздел)/File(file)».
// Короткая форма пути (с узла HD) по спецификации UEFI раскрывается прошивкой сама.
func BuildLoadOption(label, file string, p Part) []byte {
	le := binary.LittleEndian
	hd := make([]byte, 42)
	hd[0], hd[1] = 4, 1
	le.PutUint16(hd[2:], 42)
	le.PutUint32(hd[4:], uint32(p.Num))
	le.PutUint64(hd[8:], p.First)
	le.PutUint64(hd[16:], p.Last-p.First+1)
	copy(hd[24:40], p.GUID[:])
	hd[40], hd[41] = 2, 2 // MBRType = GPT, SignatureType = GUID

	fileNode := append([]byte{4, 4, 0, 0}, ucs2(file)...)
	le.PutUint16(fileNode[2:], uint16(len(fileNode)))

	path := append(append(hd, fileNode...), 0x7f, 0xff, 4, 0)

	var b bytes.Buffer
	binary.Write(&b, le, uint32(1)) // LOAD_OPTION_ACTIVE
	binary.Write(&b, le, uint16(len(path)))
	b.Write(ucs2(label))
	b.Write(path)
	return b.Bytes()
}

// ParseBootOrder и BuildBootOrder: BootOrder — просто массив uint16.
func ParseBootOrder(b []byte) []uint16 {
	var order []uint16
	for i := 0; i+1 < len(b); i += 2 {
		order = append(order, binary.LittleEndian.Uint16(b[i:]))
	}
	return order
}

func BuildBootOrder(order []uint16) []byte {
	var b []byte
	for _, n := range order {
		b = binary.LittleEndian.AppendUint16(b, n)
	}
	return b
}

func ucs2(s string) []byte {
	var b []byte
	for _, c := range utf16.Encode([]rune(s)) {
		b = binary.LittleEndian.AppendUint16(b, c)
	}
	return append(b, 0, 0)
}

// decodeUCS2 читает строку UTF-16LE до нулевого символа.
func decodeUCS2(b []byte) string {
	var u []uint16
	for i := 0; i+1 < len(b); i += 2 {
		c := binary.LittleEndian.Uint16(b[i:])
		if c == 0 {
			break
		}
		u = append(u, c)
	}
	return string(utf16.Decode(u))
}

// Strip убирает из порядка загрузки записи, ведущие на раздел guid. Записи с описанием
// keep (свои, ими управляет утилита хоста) только убираются из порядка, остальные
// возвращаются в remove на удаление.
func Strip(order []uint16, entries map[uint16][]byte, guid [16]byte, keep string) (newOrder, remove []uint16) {
	for _, n := range order {
		desc, g := ParseLoadOption(entries[n])
		switch {
		case g != guid:
			newOrder = append(newOrder, n)
		case desc != keep:
			remove = append(remove, n)
		}
	}
	return newOrder, remove
}
