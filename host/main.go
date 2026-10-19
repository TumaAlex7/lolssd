// lolssd — утилита для Linux-хоста, лежит на разделе LOLSSD внешнего SSD.
package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) != 2 {
		usage()
	}
	switch os.Args[1] {
	case "check":
		os.Exit(check())
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "использование: lolssd check")
	os.Exit(2)
}
