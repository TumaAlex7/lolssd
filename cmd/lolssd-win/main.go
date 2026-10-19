//go:build windows

// lolssd-win — часть lolssd, которая живёт в Windows на SSD.
package main

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
)

func main() {
	if len(os.Args) != 2 {
		usage()
	}
	// Запускается из планировщика без консоли, поэтому всё пишется ещё и в журнал.
	dir := filepath.Join(os.Getenv("ProgramData"), "lolssd")
	os.MkdirAll(dir, 0o755)
	if f, err := os.OpenFile(filepath.Join(dir, "lolssd-win.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); err == nil {
		log.SetOutput(io.MultiWriter(os.Stderr, f))
	}
	log.SetPrefix(os.Args[1] + ": ")

	switch os.Args[1] {
	case "nvram-guard":
		os.Exit(nvramGuard())
	case "mount":
		os.Exit(mountHost())
	case "controller":
		os.Exit(controller())
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: lolssd-win nvram-guard | mount | controller")
	os.Exit(2)
}
