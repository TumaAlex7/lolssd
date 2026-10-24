// Package keymap — клавиши для режима «Linux внутри Windows» (docs/ARCHITECTURE.md §7): в ВМ
// клавиши переназначает keyd самой системы хоста, как на железе. Windows и VNC сливают все
// клавиатуры в одну, поэтому keyd в ВМ получает конфиг, сведённый из всех её файлов (VMConfig).
// Windows сама ничего не переназначает, кроме перевозки клавиш, которых нет у PS/2-клавиатуры
// QEMU (Scancode Map, lolssd-win).
package keymap

import (
	"encoding/binary"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"
)

// ScancodeMap — двоичное значение HKLM\SYSTEM\CurrentControlSet\Control\Keyboard Layout\Scancode Map:
// заголовок (8 нулевых байт), число записей вместе с завершающей, пары «во что, что», ноль.
func ScancodeMap(m map[uint16]uint16) []byte {
	le := binary.LittleEndian
	b := make([]byte, 8)
	b = le.AppendUint32(b, uint32(len(m)+1))
	for _, from := range slices.Sorted(maps.Keys(m)) {
		b = le.AppendUint16(b, m[from])
		b = le.AppendUint16(b, from)
	}
	return le.AppendUint32(b, 0)
}

// VMConfig сводит конфиги keyd в один для ВМ: Windows и VNC сливают все клавиатуры в одну
// (PS/2 QEMU), поэтому она получает правила всех файлов. Секции, кроме [ids], объединяются
// построчно; одна и та же клавиша или сочетание из нескольких файлов — по общему конфигу (с «*»
// в [ids]): он описывает обычные внешние клавиатуры, а частный — особенности одной. Например,
// Menu: «compose = rightmeta» из default.conf, а не «compose = menu» из laptop.conf, и при этом
// аккорд Copilot из laptop.conf остаётся. Пустая строка — конфигов нет.
func VMConfig(files map[string]string) string {
	type file struct {
		name string
		all  bool
	}
	var order []file
	for name, text := range files {
		all := false
		section := ""
		for _, line := range strings.Split(text, "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "[") {
				section = strings.Trim(line, "[]")
			} else if section == "ids" && line == "*" {
				all = true
			}
		}
		order = append(order, file{name, all})
	}
	// Частные файлы раньше общих: у одинаковой клавиши побеждает последний.
	sort.Slice(order, func(i, j int) bool {
		if order[i].all != order[j].all {
			return !order[i].all
		}
		return order[i].name < order[j].name
	})
	var sections []string
	lines := map[string][]string{} // секция → строки по порядку появления клавиш
	index := map[string]int{}      // секция + клавиша → номер строки в lines
	for _, f := range order {
		section := ""
		for _, line := range strings.Split(files[f.name], "\n") {
			line = strings.TrimSpace(line)
			if i := strings.Index(line, "#"); i >= 0 {
				line = strings.TrimSpace(line[:i])
			}
			if line == "" {
				continue
			}
			if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
				section = strings.Trim(line, "[]")
				if _, ok := lines[section]; !ok && section != "ids" {
					sections = append(sections, section)
					lines[section] = nil
				}
				continue
			}
			if section == "ids" || section == "" {
				continue
			}
			key, _, _ := strings.Cut(line, "=")
			id := section + "\x00" + strings.ReplaceAll(strings.TrimSpace(key), " ", "")
			if i, ok := index[id]; ok {
				lines[section][i] = line
				continue
			}
			index[id] = len(lines[section])
			lines[section] = append(lines[section], line)
		}
	}
	if len(sections) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("# lolssd: конфиги keyd хоста, сведённые для ВМ (все клавиатуры в ней — одна)\n[ids]\n*\n")
	for _, s := range sections {
		fmt.Fprintf(&b, "\n[%s]\n", s)
		for _, l := range lines[s] {
			b.WriteString(l + "\n")
		}
	}
	return b.String()
}
