//go:build windows

package main

import (
	"fmt"
	"log"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"

	"lolssd/efi"
)

var (
	kernel32    = windows.NewLazySystemDLL("kernel32.dll")
	procGetVar  = kernel32.NewProc("GetFirmwareEnvironmentVariableExW")
	procSetVar  = kernel32.NewProc("SetFirmwareEnvironmentVariableExW")
	globalGUIDw = windows.StringToUTF16Ptr("{" + efi.GlobalGUID + "}")
)

// nvramGuard следит, чтобы загрузка с SSD не оставляла следов в NVRAM хоста
// (docs/ARCHITECTURE.md §2): записи, ведущие на ESP этого SSD, убираются из BootOrder,
// а созданные Windows ещё и удаляются. Запись lolssd от утилиты хоста только убирается
// из порядка — её переиспользует и удаляет `lolssd boot`. Остальные записи и их порядок
// не трогаются. Запускается при старте Windows от SYSTEM.
func nvramGuard() int {
	if err := enablePrivilege("SeSystemEnvironmentPrivilege"); err != nil {
		log.Println("нет привилегии на переменные UEFI:", err)
		return 1
	}
	esp, err := systemESP()
	if err != nil {
		log.Println("ESP системного диска:", err)
		return 1
	}

	b, err := getVar("BootOrder")
	if err != nil {
		log.Println("чтение BootOrder:", err)
		return 1
	}
	order := efi.ParseBootOrder(b)
	entries := map[uint16][]byte{}
	for _, n := range order {
		if opt, err := getVar(fmt.Sprintf("Boot%04X", n)); err == nil {
			entries[n] = opt
		}
	}

	newOrder, remove := efi.Strip(order, entries, esp.GUID, "lolssd")
	if len(newOrder) == len(order) {
		log.Println("в BootOrder нет записей на этот SSD, ничего не меняю")
		return 0
	}
	// Пустой BootOrder хуже лишней записи: прошивка может перестать видеть систему хоста.
	if len(newOrder) == 0 {
		log.Println("в BootOrder только записи на этот SSD, оставляю как есть")
		return 0
	}
	if err := setVar("BootOrder", efi.BuildBootOrder(newOrder)); err != nil {
		log.Println("запись BootOrder:", err)
		return 1
	}
	log.Printf("BootOrder: %04X → %04X", order, newOrder)

	failed := 0
	for _, n := range remove {
		desc, _ := efi.ParseLoadOption(entries[n])
		if err := setVar(fmt.Sprintf("Boot%04X", n), nil); err != nil {
			log.Printf("удаление Boot%04X (%s): %v", n, desc, err)
			failed++
			continue
		}
		log.Printf("удалена Boot%04X (%s)", n, desc)
	}
	return failed
}

// systemESP находит ESP на диске, с которого загружена Windows (то есть на этом SSD).
func systemESP() (efi.Part, error) {
	vol, err := windows.CreateFile(windows.StringToUTF16Ptr(`\\.\`+os.Getenv("SystemDrive")),
		0, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		return efi.Part{}, err
	}
	defer windows.CloseHandle(vol)

	// VOLUME_DISK_EXTENTS с одним DISK_EXTENT (выравнивание LARGE_INTEGER — 8 байт).
	var ext struct {
		Count         uint32
		_             uint32
		DiskNumber    uint32
		_             uint32
		Start, Length int64
	}
	var n uint32
	const ioctlVolumeGetVolumeDiskExtents = 0x00560000
	if err := windows.DeviceIoControl(vol, ioctlVolumeGetVolumeDiskExtents, nil, 0,
		(*byte)(unsafe.Pointer(&ext)), uint32(unsafe.Sizeof(ext)), &n, nil); err != nil {
		return efi.Part{}, fmt.Errorf("номер диска системного тома: %w", err)
	}

	disk := fmt.Sprintf(`\\.\PhysicalDrive%d`, ext.DiskNumber)
	f, err := os.Open(disk)
	if err != nil {
		return efi.Part{}, err
	}
	defer f.Close()
	parts, err := efi.ReadGPT(f)
	if err != nil {
		return efi.Part{}, fmt.Errorf("%s: %w", disk, err)
	}
	var esps []efi.Part
	for _, p := range parts {
		if p.Type == efi.ESPType {
			esps = append(esps, p)
		}
	}
	if len(esps) != 1 {
		return efi.Part{}, fmt.Errorf("%s: ожидался один ESP, найдено %d", disk, len(esps))
	}
	return esps[0], nil
}

func getVar(name string) ([]byte, error) {
	buf := make([]byte, 4096)
	var attrs uint32
	n, _, err := procGetVar.Call(uintptr(unsafe.Pointer(windows.StringToUTF16Ptr(name))), uintptr(unsafe.Pointer(globalGUIDw)),
		uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), uintptr(unsafe.Pointer(&attrs)))
	if n == 0 {
		return nil, err
	}
	return buf[:n], nil
}

// setVar записывает глобальную переменную с атрибутами NV|BS|RT; пустое значение удаляет её.
func setVar(name string, data []byte) error {
	var p *byte
	if len(data) > 0 {
		p = &data[0]
	}
	ok, _, err := procSetVar.Call(uintptr(unsafe.Pointer(windows.StringToUTF16Ptr(name))), uintptr(unsafe.Pointer(globalGUIDw)),
		uintptr(unsafe.Pointer(p)), uintptr(len(data)), 7)
	if ok == 0 {
		return err
	}
	return nil
}

func enablePrivilege(name string) error {
	var tok windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_ADJUST_PRIVILEGES|windows.TOKEN_QUERY, &tok); err != nil {
		return err
	}
	defer tok.Close()
	var luid windows.LUID
	if err := windows.LookupPrivilegeValue(nil, windows.StringToUTF16Ptr(name), &luid); err != nil {
		return err
	}
	tp := windows.Tokenprivileges{PrivilegeCount: 1}
	tp.Privileges[0] = windows.LUIDAndAttributes{Luid: luid, Attributes: windows.SE_PRIVILEGE_ENABLED}
	// Если привилегии у процесса нет вовсе, вызов всё равно «успешен»; тогда упадут
	// сами Get/SetFirmwareEnvironmentVariable с ERROR_PRIVILEGE_NOT_HELD, и это попадёт в журнал.
	return windows.AdjustTokenPrivileges(tok, false, &tp, 0, nil, nil)
}
