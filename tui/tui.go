//go:build linux

// Package tui — меню и списки с галочками в терминале, без внешних библиотек: сырой режим
// терминала (termios) и escape-последовательности ANSI. Для lolssd (меню на диске) и мастера
// сборки.
package tui

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

// Item — пункт списка с галочкой. Sub — группа: галочка группы ставит и снимает все вложенные,
// стрелка вправо раскрывает её, влево сворачивает.
type Item struct {
	Label string
	Hint  string // серым после названия
	On    bool
	Sub   []*Item
	open  bool
}

// Checked возвращает, отмечен ли пункт; у группы — отмечен ли хоть один вложенный.
func (it *Item) Checked() bool {
	if it.Sub == nil {
		return it.On
	}
	for _, s := range it.Sub {
		if s.On {
			return true
		}
	}
	return false
}

func (it *Item) toggle() {
	if it.Sub == nil {
		it.On = !it.On
		return
	}
	on := !it.Checked()
	for _, s := range it.Sub {
		s.On = on
	}
}

// IsTerminal — стандартный ввод и вывод — терминал (иначе меню не показать).
func IsTerminal() bool {
	_, err1 := unix.IoctlGetTermios(0, unix.TCGETS)
	_, err2 := unix.IoctlGetTermios(1, unix.TCGETS)
	return err1 == nil && err2 == nil
}

// raw переводит терминал в посимвольный ввод без эха; возвращает функцию восстановления.
func raw() func() {
	old, err := unix.IoctlGetTermios(0, unix.TCGETS)
	if err != nil {
		return func() {}
	}
	t := *old
	t.Lflag &^= unix.ICANON | unix.ECHO
	t.Iflag &^= unix.ICRNL
	t.Cc[unix.VMIN], t.Cc[unix.VTIME] = 1, 0
	unix.IoctlSetTermios(0, unix.TCSETS, &t)
	fmt.Print("\x1b[?25l") // спрятать курсор
	return func() {
		fmt.Print("\x1b[?25h")
		unix.IoctlSetTermios(0, unix.TCSETS, old)
	}
}

type key int

const (
	keyOther key = iota
	keyUp
	keyDown
	keyLeft
	keyRight
	keyEnter
	keySpace
	keyQuit
)

func readKey() key {
	var b [8]byte
	n, err := os.Stdin.Read(b[:])
	if err != nil || n == 0 {
		return keyQuit
	}
	s := string(b[:n])
	switch s {
	case "\x1b[A", "k":
		return keyUp
	case "\x1b[B", "j":
		return keyDown
	case "\x1b[D", "h":
		return keyLeft
	case "\x1b[C", "l":
		return keyRight
	case "\r", "\n":
		return keyEnter
	case " ":
		return keySpace
	case "q", "\x1b", "\x03":
		return keyQuit
	}
	return keyOther
}

func clear() { fmt.Print("\x1b[H\x1b[2J") }

const (
	bold  = "\x1b[1m"
	dim   = "\x1b[2m"
	inv   = "\x1b[7m"
	reset = "\x1b[0m"
	Green = "\x1b[32m"
	Red   = "\x1b[31m"
	Amber = "\x1b[33m"
)

// Menu показывает заголовок, строки над меню (header) и пункты; возвращает номер выбранного
// пункта или -1 (q, Esc, Ctrl+C).
func Menu(title string, header []string, items []string) int {
	restore := raw()
	defer restore()
	cur := 0
	for {
		clear()
		fmt.Printf("%s%s%s\r\n\r\n", bold, title, reset)
		for _, h := range header {
			fmt.Printf("%s\r\n", h)
		}
		if len(header) > 0 {
			fmt.Print("\r\n")
		}
		for i, it := range items {
			if i == cur {
				fmt.Printf(" %s> %s %s\r\n", inv, it, reset)
			} else {
				fmt.Printf("   %s\r\n", it)
			}
		}
		fmt.Printf("\r\n%s↑↓ — выбор, Enter — выполнить, q — выход%s\r\n", dim, reset)
		switch readKey() {
		case keyUp:
			cur = (cur + len(items) - 1) % len(items)
		case keyDown:
			cur = (cur + 1) % len(items)
		case keyEnter:
			clear()
			return cur
		case keyQuit:
			clear()
			return -1
		}
	}
}

// Checklist — список с галочками; Enter подтверждает (true), q отменяет (false).
func Checklist(title string, header []string, items []*Item) bool {
	restore := raw()
	defer restore()
	cur := 0
	for {
		type row struct {
			it    *Item
			depth int
		}
		var rows []row
		for _, it := range items {
			rows = append(rows, row{it, 0})
			if it.open {
				for _, s := range it.Sub {
					rows = append(rows, row{s, 1})
				}
			}
		}
		if cur >= len(rows) {
			cur = len(rows) - 1
		}
		clear()
		fmt.Printf("%s%s%s\r\n\r\n", bold, title, reset)
		for _, h := range header {
			fmt.Printf("%s\r\n", h)
		}
		if len(header) > 0 {
			fmt.Print("\r\n")
		}
		for i, r := range rows {
			box := "[ ]"
			switch {
			case r.it.Sub != nil && r.it.Checked() && !allOn(r.it):
				box = "[-]"
			case r.it.Checked():
				box = "[x]"
			}
			arrow := "  "
			if r.it.Sub != nil {
				arrow = "▸ "
				if r.it.open {
					arrow = "▾ "
				}
			}
			line := strings.Repeat("    ", r.depth) + arrow + box + " " + r.it.Label
			if i == cur {
				line = inv + line + reset
			}
			if r.it.Hint != "" {
				line += "  " + dim + r.it.Hint + reset
			}
			fmt.Printf(" %s\r\n", line)
		}
		fmt.Printf("\r\n%s↑↓ — выбор, пробел — галочка, → ← — раскрыть группу, Enter — дальше, q — отмена%s\r\n", dim, reset)
		r := rows[cur]
		switch readKey() {
		case keyUp:
			cur = (cur + len(rows) - 1) % len(rows)
		case keyDown:
			cur = (cur + 1) % len(rows)
		case keySpace:
			r.it.toggle()
		case keyRight:
			if r.it.Sub != nil {
				r.it.open = true
			}
		case keyLeft:
			if r.it.Sub != nil {
				r.it.open = false
			} else if r.depth == 1 {
				for i := cur; i >= 0; i-- {
					if rows[i].depth == 0 {
						rows[i].it.open = false
						cur = i
						break
					}
				}
			}
		case keyEnter:
			clear()
			return true
		case keyQuit:
			clear()
			return false
		}
	}
}

func allOn(it *Item) bool {
	for _, s := range it.Sub {
		if !s.On {
			return false
		}
	}
	return true
}

// Input спрашивает строку в обычном режиме терминала (работают стирание и вставка, в том числе
// перетаскивание файла в окно терминала). Пустой ответ — def.
func Input(prompt, def string) string {
	if def != "" {
		fmt.Printf("%s [%s]: ", prompt, def)
	} else {
		fmt.Printf("%s: ", prompt)
	}
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	line = strings.TrimSpace(line)
	if line == "" {
		return def
	}
	return line
}

// Pause ждёт Enter.
func Pause() {
	fmt.Printf("\n%sEnter — назад в меню%s", dim, reset)
	bufio.NewReader(os.Stdin).ReadString('\n')
}
