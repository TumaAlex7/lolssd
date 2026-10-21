//go:build linux

// lolssd-wsl — пусковой инструмент внутри WSL-дистрибутива на SSD (docs/ARCHITECTURE.md §6).
// Поднимает систему хоста по активному профилю и запускает в ней программы через systemd-nspawn.
// Запускается от root: wsl.exe -d lolssd -u root -- lolssd-wsl <команда>.
package main

import (
	"fmt"
	"os"
)

const (
	profilePath = "/mnt/c/ProgramData/lolssd/active-profile.json"
	checkDir    = "/run/lolssd/check" // временно, только чтение без восстановления журналов
)

var rootDir = "/run/lolssd/root" // система хоста, на запись; переменная — для тестов

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	// cryptsetup, vgchange и прочие лежат в /usr/sbin, а PATH наследуется от того, кто запустил
	// (wsl.exe, ярлык): не полагаемся на него.
	os.Setenv("PATH", "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin:"+os.Getenv("PATH"))
	if os.Geteuid() != 0 {
		fmt.Fprintln(os.Stderr, "нужны права root: wsl.exe -d lolssd -u root -- lolssd-wsl ...")
		os.Exit(1)
	}
	switch os.Args[1] {
	case "up":
		os.Exit(up())
	case "down":
		os.Exit(down())
	case "run":
		os.Exit(run(os.Args[2:]))
	case "session":
		os.Exit(session())
	case "shell":
		os.Exit(shell())
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "использование: lolssd-wsl up | down | shell | session | run [--] команда [аргументы]")
	os.Exit(2)
}
