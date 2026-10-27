//go:build linux

// lolssd — утилита для Linux-хоста, лежит на разделе LOLSSD внешнего SSD.
package main

import (
	"fmt"
	"os"

	"lolssd/tui"
)

func main() {
	if len(os.Args) < 2 {
		if !tui.IsTerminal() {
			usage()
		}
		os.Exit(menu())
	}
	switch os.Args[1] {
	case "check":
		os.Exit(check())
	case "boot":
		os.Exit(boot(os.Args[2:]))
	case "profile":
		os.Exit(runProfile())
	case "drivers":
		os.Exit(addDrivers(os.Args[2:]))
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "использование: lolssd (меню) | check | profile | drivers ПУТЬ... | boot [--no-reboot] | boot --cleanup")
	os.Exit(2)
}
