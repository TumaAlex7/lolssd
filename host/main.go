// lolssd — утилита для Linux-хоста, лежит на разделе LOLSSD внешнего SSD.
package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	switch os.Args[1] {
	case "check":
		os.Exit(check())
	case "boot":
		os.Exit(boot(os.Args[2:]))
	case "profile":
		os.Exit(runProfile())
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "использование: lolssd check | profile | boot [--no-reboot] | boot --cleanup")
	os.Exit(2)
}
