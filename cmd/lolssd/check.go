//go:build linux

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

const secureBootVar = "/sys/firmware/efi/efivars/SecureBoot-8be4df61-93ca-11d2-aa0d-00e098032b8c"

// check проверяет требования из docs/ARCHITECTURE.md §3. Только читает систему.
// Возвращает код выхода: 1, если есть хотя бы один FAIL.
func check() int {
	fails := 0
	report := func(status, name, detail string) {
		if status == "FAIL" {
			fails++
		}
		fmt.Printf("%-4s  %-18s %s\n", status, name, detail)
	}

	if _, err := os.Stat("/sys/firmware/efi"); err == nil {
		report("OK", "UEFI", "система загружена в режиме UEFI")
	} else {
		report("FAIL", "UEFI", "загрузка в режиме Legacy/CSM, Vanguard требует UEFI (VAN 9005)")
	}

	// Переменная efivarfs: 4 байта атрибутов, затем 1 байт значения.
	switch sb, err := os.ReadFile(secureBootVar); {
	case err != nil:
		report("WARN", "Secure Boot", "состояние неизвестно: "+err.Error())
	case len(sb) == 5 && sb[4] == 1:
		report("OK", "Secure Boot", "включён")
	default:
		report("WARN", "Secure Boot", "выключен; если Vanguard его потребует, включать в BIOS перед игрой")
	}

	switch v, err := os.ReadFile("/sys/class/tpm/tpm0/tpm_version_major"); {
	case err != nil:
		report("FAIL", "TPM 2.0", "TPM не найден: выключен в BIOS (AMD fTPM / Intel PTT) или отсутствует")
	case strings.TrimSpace(string(v)) == "2":
		report("OK", "TPM 2.0", "есть")
	default:
		report("FAIL", "TPM 2.0", "TPM версии "+strings.TrimSpace(string(v))+", нужен 2.0")
	}

	cpuinfo, _ := os.ReadFile("/proc/cpuinfo")
	var flags []string
	for _, line := range strings.Split(string(cpuinfo), "\n") {
		if strings.HasPrefix(line, "flags") {
			flags = strings.Fields(line)
			break
		}
	}
	hasVirt := false
	for _, f := range flags {
		if f == "svm" || f == "vmx" {
			hasVirt = true
		}
	}
	_, kvmErr := os.Stat("/dev/kvm")
	switch {
	case !hasVirt:
		report("FAIL", "Виртуализация", "SVM / VT-x не видна: выключена в BIOS или не поддерживается (нужна для VBS/HVCI и ВМ с Linux хоста)")
	case kvmErr == nil:
		report("OK", "Виртуализация", "включена")
	default:
		report("WARN", "Виртуализация", "процессор умеет, но /dev/kvm нет: модуль kvm не загружен или выключено в BIOS (SVM Mode / Intel VT-x)")
	}

	if entries, _ := os.ReadDir("/sys/class/iommu"); len(entries) > 0 {
		report("OK", "IOMMU", "включён")
	} else {
		report("WARN", "IOMMU", "не найден (AMD-Vi / VT-d в BIOS); нужен для режима Vanguard Pre-Check на Windows 11")
	}

	meminfo, _ := os.ReadFile("/proc/meminfo")
	var memKB int
	for _, line := range strings.Split(string(meminfo), "\n") {
		if f := strings.Fields(line); len(f) >= 2 && f[0] == "MemTotal:" {
			memKB, _ = strconv.Atoi(f[1])
		}
	}
	memGiB := float64(memKB) / (1 << 20)
	mem := fmt.Sprintf("%.1f ГиБ видно системе", memGiB)
	switch {
	case memGiB < 7:
		report("WARN", "Память", mem+": для Windows с лолкой впритык")
	case memGiB < 13:
		report("WARN", "Память", mem+": на игру хватит, для Linux внутри Windows тесно")
	default:
		report("OK", "Память", mem)
	}

	usbSpeed(report)

	if fails > 0 {
		return 1
	}
	return 0
}

// usbSpeed находит USB-устройство, на котором лежит сам бинарник (раздел LOLSSD),
// и проверяет скорость порта и драйвер (uas = UASP работает).
func usbSpeed(report func(status, name, detail string)) {
	dev, err := exeBlockDev()
	if err != nil {
		report("INFO", "USB", "lolssd запущен не с блочного устройства, скорость не проверялась")
		return
	}

	// Поднимаемся по sysfs: сначала интерфейс USB (в нём драйвер), выше само устройство (в нём speed).
	var driver, speed string
	for d := dev; d != "/sys/devices" && d != "/"; d = filepath.Dir(d) {
		if _, err := os.Stat(filepath.Join(d, "bInterfaceNumber")); err == nil && driver == "" {
			if link, err := os.Readlink(filepath.Join(d, "driver")); err == nil {
				driver = filepath.Base(link)
			}
		}
		if b, err := os.ReadFile(filepath.Join(d, "speed")); err == nil {
			speed = strings.TrimSpace(string(b))
			break
		}
	}
	if speed == "" {
		report("INFO", "USB", "lolssd запущен не с USB-диска, скорость не проверялась")
		return
	}

	mbps, _ := strconv.Atoi(speed)
	switch {
	case mbps >= 10000:
		report("OK", "Порт USB", fmt.Sprintf("%d Гбит/с", mbps/1000))
	case mbps >= 5000:
		report("WARN", "Порт USB", "5 Гбит/с: работать будет, но медленнее; поищите порт на 10 Гбит/с")
	default:
		report("WARN", "Порт USB", "USB 2.0 ("+speed+" Мбит/с): очень медленно, подключите к порту USB 3")
	}

	switch driver {
	case "uas":
		report("OK", "UASP", "работает")
	case "usb-storage":
		report("WARN", "UASP", "не работает (драйвер usb-storage): случайные операции заметно медленнее")
	default:
		report("INFO", "UASP", "драйвер не определён: "+driver)
	}
}

// exeBlockDev возвращает каталог sysfs блочного устройства (обычно раздела), на котором лежит бинарник.
// LOLSSD_ROOT подменяет место бинарника: lolssd.sh запускает копию из /tmp, потому что udisks
// монтирует FAT с showexec и запустить файл прямо с раздела нельзя.
func exeBlockDev() (string, error) {
	exe, err := os.Executable()
	if root := os.Getenv("LOLSSD_ROOT"); root != "" {
		exe, err = root, nil
	}
	if err == nil {
		exe, err = filepath.EvalSymlinks(exe)
	}
	var st syscall.Stat_t
	if err == nil {
		err = syscall.Stat(exe, &st)
	}
	if err != nil {
		return "", err
	}
	major := (st.Dev>>8)&0xfff | (st.Dev>>32)&^0xfff
	minor := st.Dev&0xff | (st.Dev>>12)&^0xff
	return filepath.EvalSymlinks(fmt.Sprintf("/sys/dev/block/%d:%d", major, minor))
}
