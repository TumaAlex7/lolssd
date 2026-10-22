//go:build windows

package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"log"
	"os/exec"

	"lolssd/efi"
)

const (
	bootLabel = "lolssd" // та же запись, что создаёт `lolssd boot` на Linux
	bootFile  = `\EFI\BOOT\BOOTX64.EFI`
)

// restartSelf — ярлык «Restart into Windows (SSD)»: ставит BootNext на ESP этого SSD и
// перезагружает машину. Обычная перезагрузка уводит в систему хоста: nvram-guard убирает SSD
// из BootOrder (docs/ARCHITECTURE.md §2). BootNext действует один раз, порядок не меняется.
// Запускается заданием \lolssd\restart с повышенными правами — без запроса UAC.
func restartSelf() int {
	if err := enablePrivilege("SeSystemEnvironmentPrivilege"); err != nil {
		log.Println("нет привилегии на переменные UEFI:", err)
		return 1
	}
	esp, err := systemESP()
	if err != nil {
		log.Println("ESP системного диска:", err)
		return 1
	}
	num, err := entryFor(esp)
	if err != nil {
		log.Println("запись загрузки:", err)
		return 1
	}
	if err := setVar("BootNext", binary.LittleEndian.AppendUint16(nil, num)); err != nil {
		log.Println("запись BootNext:", err)
		return 1
	}
	log.Printf("BootNext = Boot%04X, перезагрузка", num)
	if out, err := exec.Command("shutdown.exe", "/r", "/t", "0").CombinedOutput(); err != nil {
		log.Printf("shutdown /r: %v %s", err, out)
		return 1
	}
	return 0
}

// entryFor возвращает номер своей записи lolssd на этот ESP или создаёт её. Перебирать все
// 65536 переменных через Windows долго, поэтому смотрятся номера 0000–00FF: прошивки и
// `lolssd boot` берут первые свободные. Записи прошивки не используются: у USB-записей часто
// нет узла HD() с GUID раздела, и проверить, что они ведут именно на этот SSD, нельзя.
func entryFor(esp efi.Part) (uint16, error) {
	free := -1
	for n := 0; n <= 0xff; n++ {
		opt, err := getVar(fmt.Sprintf("Boot%04X", n))
		if err != nil {
			if free < 0 {
				free = n
			}
			continue
		}
		if desc, guid := efi.ParseLoadOption(opt); desc == bootLabel && guid == esp.GUID {
			return uint16(n), nil
		}
	}
	if free < 0 {
		return 0, errors.New("нет свободного номера Boot0000–Boot00FF")
	}
	return uint16(free), setVar(fmt.Sprintf("Boot%04X", free), efi.BuildLoadOption(bootLabel, bootFile, esp))
}
