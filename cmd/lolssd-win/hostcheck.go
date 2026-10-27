//go:build windows

package main

import (
	"fmt"
	"log"
	"os"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"

	"lolssd/efi"
	"lolssd/hostprofile"
	"lolssd/winctl"
)

// hostShutDown проверяет перед запуском ВМ, что система хоста выключена чисто: ВМ монтирует те же
// разделы. Раздел подкачки с образом гибернации — отказ: Linux, проснувшись потом на железе,
// перезапишет ФС поверх того, что изменила ВМ. Незакрытый журнал ext4 (сбой питания, зависание —
// безопасно, Linux проиграет журнал сам; но и гибернация в файл подкачки выглядит так же) —
// вопрос, по умолчанию «нет». Зашифрованные разделы и другие ФС проверить нельзя: только журнал.
func hostShutDown(p hostprofile.Profile, disks []hostDisk) bool {
	where := map[string]struct {
		n    int
		part efi.Part
	}{}
	for _, d := range disks {
		parts, err := diskGPT(d.N)
		if err != nil {
			log.Println(err)
			continue
		}
		for _, pt := range parts {
			where[efi.GUIDString(pt.GUID)] = struct {
				n    int
				part efi.Part
			}{d.N, pt}
		}
	}
	type target struct{ what, partuuid string }
	var targets []target
	var walk func(d hostprofile.BlockDev, what string)
	walk = func(d hostprofile.BlockDev, what string) {
		switch {
		case d.Kind == "luks":
			log.Printf("%s: зашифрован, состояние не проверить", what)
		case d.Kind == "partition" && d.PartUUID != "":
			targets = append(targets, target{what, d.PartUUID})
		default:
			for _, l := range d.Lower {
				walk(l, what)
			}
		}
	}
	for _, m := range p.Mounts {
		walk(m.Device, m.Target)
	}
	for _, s := range p.Swaps {
		walk(s.Device, "swap")
	}

	var hibernated, unclean []string
	for _, t := range targets {
		w, ok := where[strings.ToLower(t.partuuid)]
		if !ok {
			w, ok = where[strings.ToUpper(t.partuuid)]
		}
		if !ok {
			continue
		}
		f, err := os.Open(fmt.Sprintf(`\\.\PhysicalDrive%d`, w.n))
		if err != nil {
			log.Printf("%s: %v", t.what, err)
			continue
		}
		st := winctl.PartitionState(f, int64(w.part.First)*int64(w.part.SectorSize))
		f.Close()
		log.Printf("%s (PARTUUID %s): %v", t.what, t.partuuid, st)
		switch st {
		case winctl.FSHibernated:
			hibernated = append(hibernated, t.what)
		case winctl.FSUnclean:
			unclean = append(unclean, t.what)
		}
	}
	if len(hibernated) > 0 {
		fail("Linux этого компьютера в гибернации (спящий режим на диск: %s).\n\n"+
			"Запуск ВМ испортил бы данные: проснувшись, Linux запишет на диск своё состояние поверх "+
			"изменений из ВМ. Загрузите Linux и выключите его полностью, потом запускайте снова.",
			strings.Join(hibernated, ", "))
		return false
	}
	if len(unclean) > 0 {
		const mbYesNo, mbIconWarning, mbDefButton2, mbSetForeground, idYes = 0x4, 0x30, 0x100, 0x10000, 6
		r, _, _ := procMessageBox.Call(0, uintptr(unsafe.Pointer(windows.StringToUTF16Ptr(fmt.Sprintf(
			"Linux этого компьютера выключен некорректно: журнал %s не закрыт.\n\n"+
				"После сбоя питания или зависания это безопасно — Linux восстановит журнал сам.\n"+
				"Но если Linux в гибернации (в том числе в файл подкачки), запуск ВМ испортит данные: "+
				"тогда нажмите «Нет», загрузите Linux и выключите его.\n\nЗапустить Linux в ВМ?",
			strings.Join(unclean, ", "))))),
			uintptr(unsafe.Pointer(windows.StringToUTF16Ptr("lolssd: Linux"))), mbYesNo|mbIconWarning|mbDefButton2|mbSetForeground)
		if r != idYes {
			log.Println("запуск отменён: журнал не закрыт")
			return false
		}
	}
	return true
}
