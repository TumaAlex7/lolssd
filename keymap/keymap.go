// Package keymap переводит переназначения клавиш хоста (keyd) в то, что работает в режиме
// «Linux внутри Windows» (docs/ARCHITECTURE.md §7). Цепочка там такая: Windows (Scancode Map)
// → полноэкранный TigerVNC (забирает всю клавиатуру, включая Win, и шлёт keysym) → wayvnc →
// Hyprland (обёртка конфига).
//
// Что куда:
//   - Win остаётся Win: полноэкранный TigerVNC сам передаёт её как Super, а в Windows она
//     работает как обычно (раньше, для окон WSLg, Win уводилась в F13/F14 — это ломало и Super
//     в Linux через VNC, и Win-сочетания в Windows);
//   - переназначения 1:1 (`capslock = esc`) → Scancode Map: действуют и в игре, и в Linux;
//   - сочетания (`f23+leftshift+leftmeta = rightmeta`, так прошивки шлют клавишу Copilot) →
//     обёртка Hyprland: пока зажата «главная» клавиша сочетания, считается зажатой цель;
//   - остальное (слои, overload, макросы) не переносится и попадает в Notes.
package keymap

import (
	"encoding/binary"
	"fmt"
	"slices"
	"sort"
	"strings"
)

// Key — клавиша в именах keyd: скан-код набора 1 для Windows (0xE0xx — расширенный),
// имя клавиши XKB и keysym, которым её видит Hyprland в режиме WSL.
type Key struct {
	Scancode uint16
	XKB      string
	Keysym   string
	Mod      string // семейство модификатора для привязок Hyprland, если это модификатор
}

var keys = map[string]Key{
	"leftshift": {0x2a, "LFSH", "Shift_L", "SHIFT"}, "rightshift": {0x36, "RTSH", "Shift_R", "SHIFT"},
	"leftcontrol": {0x1d, "LCTL", "Control_L", "CTRL"}, "rightcontrol": {0xe01d, "RCTL", "Control_R", "CTRL"},
	"leftalt": {0x38, "LALT", "Alt_L", "ALT"}, "rightalt": {0xe038, "RALT", "Alt_R", "ALT"},
	"leftmeta": {0xe05b, "LWIN", "Super_L", "SUPER"}, "rightmeta": {0xe05c, "RWIN", "Super_R", "SUPER"},
	"compose": {0xe05d, "MENU", "Menu", ""},
	"esc":     {0x01, "ESC", "Escape", ""}, "capslock": {0x3a, "CAPS", "Caps_Lock", ""},
	"tab": {0x0f, "TAB", "Tab", ""}, "enter": {0x1c, "RTRN", "Return", ""},
	"space": {0x39, "SPCE", "space", ""}, "backspace": {0x0e, "BKSP", "BackSpace", ""},
	"insert": {0xe052, "INS", "Insert", ""}, "delete": {0xe053, "DELE", "Delete", ""},
	"home": {0xe047, "HOME", "Home", ""}, "end": {0xe04f, "END", "End", ""},
	"pageup": {0xe049, "PGUP", "Prior", ""}, "pagedown": {0xe051, "PGDN", "Next", ""},
	"up": {0xe048, "UP", "Up", ""}, "down": {0xe050, "DOWN", "Down", ""},
	"left": {0xe04b, "LEFT", "Left", ""}, "right": {0xe04d, "RGHT", "Right", ""},
	"grave": {0x29, "TLDE", "grave", ""}, "minus": {0x0c, "AE11", "minus", ""}, "equal": {0x0d, "AE12", "equal", ""},
}

func init() {
	for i, c := range "1234567890" {
		keys[string(c)] = Key{uint16(0x02 + i), fmt.Sprintf("AE%02d", i+1), string(c), ""}
	}
	for i, row := range []string{"qwertyuiop", "asdfghjkl", "zxcvbnm"} {
		base, xkbRow := []uint16{0x10, 0x1e, 0x2c}[i], []string{"AD", "AC", "AB"}[i]
		for j, c := range row {
			keys[string(c)] = Key{base + uint16(j), fmt.Sprintf("%s%02d", xkbRow, j+1), string(c), ""}
		}
	}
	fsc := []uint16{0x3b, 0x3c, 0x3d, 0x3e, 0x3f, 0x40, 0x41, 0x42, 0x43, 0x44, 0x57, 0x58,
		0x64, 0x65, 0x66, 0x67, 0x68, 0x69, 0x6a, 0x6b, 0x6c, 0x6d, 0x6e, 0x76}
	for i, sc := range fsc {
		keys[fmt.Sprintf("f%d", i+1)] = Key{sc, fmt.Sprintf("FK%02d", i+1), fmt.Sprintf("F%d", i+1), ""}
	}
}

// Транспорт Meta: что Windows отдаёт вместо Win. Пуст: через полноэкранный TigerVNC Win доходит
// до Linux сама (см. описание пакета). Механизм оставлен на случай другого способа показа.
var transport = map[string]string{}

// Rule — строка [main] одного файла keyd.
type Rule struct {
	File   string
	AllIDs bool     // в [ids] есть «*»: правило для всех клавиатур
	From   []string // одна клавиша или сочетание
	To     string
}

// ParseKeyd разбирает конфиги keyd (имя файла → содержимое). Возвращает правила [main] и
// заметки о том, что не переносится (другие секции, действия-функции).
func ParseKeyd(files map[string]string) ([]Rule, []string) {
	var rules []Rule
	var notes []string
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		section, all := "", false
		var main []Rule
		for _, line := range strings.Split(files[name], "\n") {
			line = strings.TrimSpace(line)
			if i := strings.Index(line, "#"); i >= 0 {
				line = strings.TrimSpace(line[:i])
			}
			if line == "" {
				continue
			}
			if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
				section = strings.Trim(line, "[]")
				if section != "ids" && section != "main" {
					notes = append(notes, fmt.Sprintf("%s: секция [%s] (слой keyd) не переносится", name, section))
				}
				continue
			}
			switch section {
			case "ids":
				if line == "*" {
					all = true
				}
			case "main":
				from, to, ok := strings.Cut(line, "=")
				from, to = strings.TrimSpace(from), strings.TrimSpace(to)
				if !ok || strings.ContainsAny(to, "()") || strings.Contains(to, "-") {
					notes = append(notes, fmt.Sprintf("%s: «%s» — не простое переназначение, не переносится", name, line))
					continue
				}
				main = append(main, Rule{File: name, From: strings.Split(from, "+"), To: to})
			}
		}
		for _, r := range main {
			r.AllIDs = all
			rules = append(rules, r)
		}
	}
	return rules, notes
}

// Chord — сочетание, которое в Hyprland превращается в одну клавишу-модификатор.
type Chord struct {
	Trigger string   // keysym немодификатора сочетания, например F23
	Target  string   // keysym цели, например Super_R
	TMod    string   // семейство модификатора цели, например SUPER
	Mods    []string // семейства модификаторов, которые сочетание зажимает вместе с Trigger
	Hide    []string // keysym этих модификаторов: пока зажат Trigger, их «не видно»
}

// Plan — что применить в Windows и в Hyprland.
type Plan struct {
	Scancodes map[uint16]uint16 // что → во что (Scancode Map)
	XKB       map[string]string // клавиша XKB → keysym (дописывается в раскладку Hyprland)
	Mod4      []string          // клавиши XKB, которые становятся модификатором Super
	Chords    []Chord
	Notes     []string
}

// Build строит план. При конфликте (одна клавиша в разных файлах переназначена по-разному)
// выигрывает правило для всех клавиатур: в Windows клавиатуры не различить.
func Build(rules []Rule, notes []string) Plan {
	p := Plan{Scancodes: map[uint16]uint16{}, XKB: map[string]string{}, Notes: notes}
	toKey := func(name string) (Key, bool) {
		if t, ok := transport[name]; ok {
			k := keys[t]
			k.Keysym, k.Mod = keys[name].Keysym, keys[name].Mod
			return k, true
		}
		k, ok := keys[name]
		return k, ok
	}

	for from, to := range transport {
		p.Scancodes[keys[from].Scancode] = keys[to].Scancode
		k := keys[to]
		p.XKB[k.XKB] = keys[from].Keysym
		p.Mod4 = append(p.Mod4, k.XKB)
	}
	sort.Strings(p.Mod4)

	single := map[string]Rule{}
	for _, r := range rules {
		if len(r.From) != 1 {
			continue
		}
		if prev, ok := single[r.From[0]]; ok && prev.To != r.To {
			keep, drop := prev, r
			if r.AllIDs && !prev.AllIDs {
				keep, drop = r, prev
			}
			single[r.From[0]] = keep
			p.Notes = append(p.Notes, fmt.Sprintf("%s: «%s = %s» только для части клавиатур — в Windows их не различить, действует «%s = %s» из %s",
				drop.File, drop.From[0], drop.To, keep.From[0], keep.To, keep.File))
			continue
		}
		single[r.From[0]] = r
	}
	for _, from := range sortedKeys(single) {
		r := single[from]
		src, ok1 := keys[from]
		dst, ok2 := toKey(r.To)
		if !ok1 || !ok2 {
			p.Notes = append(p.Notes, fmt.Sprintf("%s: «%s = %s» — клавиши нет в Windows, не переносится", r.File, from, r.To))
			continue
		}
		p.Scancodes[src.Scancode] = dst.Scancode
	}

	for _, r := range rules {
		if len(r.From) < 2 {
			continue
		}
		target, ok := toKey(r.To)
		if !ok || target.Mod == "" {
			p.Notes = append(p.Notes, fmt.Sprintf("%s: сочетание «%s = %s» — переносятся только сочетания, дающие модификатор", r.File, strings.Join(r.From, "+"), r.To))
			continue
		}
		c := Chord{Target: target.Keysym, TMod: target.Mod}
		trigger, ok := "", true
		for _, k := range r.From {
			key, known := toKey(k)
			if !known || key.Mod == "" && c.Trigger != "" {
				ok = false
				break
			}
			if key.Mod != "" {
				c.Mods = append(c.Mods, key.Mod)
				c.Hide = append(c.Hide, key.Keysym)
				continue
			}
			c.Trigger, trigger = key.Keysym, key.XKB
		}
		if !ok || c.Trigger == "" {
			p.Notes = append(p.Notes, fmt.Sprintf("%s: сочетание «%s» — нужна ровно одна клавиша-немодификатор из известных", r.File, strings.Join(r.From, "+")))
			continue
		}
		p.XKB[trigger] = c.Trigger // у F23 в стандартной раскладке особый тип, делаем простой
		slices.Sort(c.Mods)
		c.Mods = slices.Compact(c.Mods)
		p.Chords = append(p.Chords, c)
	}
	return p
}

// ScancodeMap — двоичное значение HKLM\SYSTEM\CurrentControlSet\Control\Keyboard Layout\Scancode Map:
// заголовок (8 нулевых байт), число записей вместе с завершающей, пары «во что, что», ноль.
func ScancodeMap(m map[uint16]uint16) []byte {
	le := binary.LittleEndian
	b := make([]byte, 8)
	b = le.AppendUint32(b, uint32(len(m)+1))
	for _, from := range sortedKeys(m) {
		b = le.AppendUint16(b, m[from])
		b = le.AppendUint16(b, from)
	}
	return le.AppendUint32(b, 0)
}

// PatchXKB дописывает в секцию xkb_symbols скомпилированной раскладки (xkbcli compile-keymap)
// клавиши плана. Более поздние определения клавиш в XKB перекрывают ранние.
func PatchXKB(keymap string, p Plan) (string, error) {
	i := strings.Index(keymap, "xkb_symbols")
	if i < 0 {
		return "", fmt.Errorf("в раскладке нет xkb_symbols")
	}
	j := strings.Index(keymap[i:], "\n};")
	if j < 0 {
		return "", fmt.Errorf("не найден конец xkb_symbols")
	}
	var add strings.Builder
	add.WriteString("\n\t// lolssd: режим Linux внутри Windows\n")
	for _, k := range sortedKeys(p.XKB) {
		fmt.Fprintf(&add, "\tkey <%s> { type = \"ONE_LEVEL\", [ %s ] };\n", k, p.XKB[k])
	}
	if len(p.Mod4) > 0 {
		fmt.Fprintf(&add, "\tmodifier_map Mod4 { <%s> };\n", strings.Join(p.Mod4, ">, <"))
	}
	return keymap[:i+j] + add.String() + keymap[i+j:], nil
}

func sortedKeys[K uint16 | string, V any](m map[K]V) []K {
	ks := make([]K, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	slices.Sort(ks)
	return ks
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
