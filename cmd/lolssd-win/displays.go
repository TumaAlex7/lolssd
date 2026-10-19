//go:build windows

package main

import (
	"fmt"
	"sort"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"

	"lolssd/hostprofile"
)

var (
	user32           = windows.NewLazySystemDLL("user32.dll")
	procGetBufSizes  = user32.NewProc("GetDisplayConfigBufferSizes")
	procQueryConfig  = user32.NewProc("QueryDisplayConfig")
	procGetDevInfo   = user32.NewProc("DisplayConfigGetDeviceInfo")
	procGlobalMemory = kernel32.NewProc("GlobalMemoryStatusEx")
)

// Структуры CCD API (wingdi.h); все поля по 4 байта, поэтому выравнивание совпадает с C.
type dcPath struct {
	SrcAdapter                windows.LUID
	SrcID, SrcModeIdx, SrcFlg uint32
	TgtAdapter                windows.LUID
	TgtID, TgtModeIdx         uint32
	Tech, Rotation, Scaling   uint32
	RefreshNum, RefreshDen    uint32
	ScanLine                  uint32
	Available                 int32
	TgtFlags, Flags           uint32
}

type dcMode struct {
	InfoType, ID uint32
	Adapter      windows.LUID
	// Для режима источника: Width, Height, PixelFormat, X, Y; дальше — остаток union.
	Width, Height, PixelFormat uint32
	X, Y                       int32
	_                          [28]byte
}

type dcTargetName struct {
	Type, Size        uint32
	Adapter           windows.LUID
	ID                uint32
	Flags, Tech       uint32
	MfgID, ProductID  uint16
	ConnectorInstance uint32
	FriendlyName      [64]uint16
	DevicePath        [128]uint16
}

// activeMonitors перечисляет активные мониторы: позиция на рабочем столе, размер,
// частота и EDID из реестра (по пути устройства монитора). Сортировка — слева направо.
func activeMonitors() ([]hostprofile.WinMonitor, error) {
	const qdcOnlyActivePaths, modeSource, getTargetName = 2, 1, 2
	var nPaths, nModes uint32
	if r, _, _ := procGetBufSizes.Call(qdcOnlyActivePaths, uintptr(unsafe.Pointer(&nPaths)), uintptr(unsafe.Pointer(&nModes))); r != 0 {
		return nil, fmt.Errorf("GetDisplayConfigBufferSizes: %d", r)
	}
	paths := make([]dcPath, nPaths)
	modes := make([]dcMode, nModes)
	if len(paths) == 0 || len(modes) == 0 {
		return nil, nil
	}
	if r, _, _ := procQueryConfig.Call(qdcOnlyActivePaths, uintptr(unsafe.Pointer(&nPaths)), uintptr(unsafe.Pointer(&paths[0])),
		uintptr(unsafe.Pointer(&nModes)), uintptr(unsafe.Pointer(&modes[0])), 0); r != 0 {
		return nil, fmt.Errorf("QueryDisplayConfig: %d", r)
	}

	var res []hostprofile.WinMonitor
	for _, p := range paths[:nPaths] {
		var m hostprofile.WinMonitor
		if int(p.SrcModeIdx) < int(nModes) && modes[p.SrcModeIdx].InfoType == modeSource {
			src := modes[p.SrcModeIdx]
			m.X, m.Y, m.Width, m.Height = int(src.X), int(src.Y), int(src.Width), int(src.Height)
			m.Primary = src.X == 0 && src.Y == 0
		}
		if p.RefreshDen != 0 {
			m.Refresh = float64(p.RefreshNum) / float64(p.RefreshDen)
		}

		tn := dcTargetName{Type: getTargetName, Adapter: p.TgtAdapter, ID: p.TgtID}
		tn.Size = uint32(unsafe.Sizeof(tn))
		if r, _, _ := procGetDevInfo.Call(uintptr(unsafe.Pointer(&tn))); r == 0 {
			m.Name = windows.UTF16ToString(tn.FriendlyName[:])
			m.EDID = edidFor(windows.UTF16ToString(tn.DevicePath[:]))
		}
		res = append(res, m)
	}
	sort.Slice(res, func(i, j int) bool { return res[i].X < res[j].X || res[i].X == res[j].X && res[i].Y < res[j].Y })
	return res, nil
}

// edidFor: путь вида \\?\DISPLAY#SDC416E#5&2a1b&0&UID256#{guid} соответствует ключу
// HKLM\SYSTEM\CurrentControlSet\Enum\DISPLAY\SDC416E\5&2a1b&0&UID256\Device Parameters.
func edidFor(devicePath string) []byte {
	f := strings.Split(strings.TrimPrefix(devicePath, `\\?\`), "#")
	if len(f) < 3 {
		return nil
	}
	k, err := registry.OpenKey(registry.LOCAL_MACHINE,
		`SYSTEM\CurrentControlSet\Enum\`+f[0]+`\`+f[1]+`\`+f[2]+`\Device Parameters`, registry.QUERY_VALUE)
	if err != nil {
		return nil
	}
	defer k.Close()
	edid, _, _ := k.GetBinaryValue("EDID")
	return edid
}

// totalMemoryMiB — объём физической памяти Windows (MEMORYSTATUSEX).
func totalMemoryMiB() int {
	var ms struct {
		Length, Load            uint32
		TotalPhys, AvailPhys    uint64
		TotalPage, AvailPage    uint64
		TotalVirtual, AvailVirt uint64
		AvailExtendedVirtual    uint64
	}
	ms.Length = uint32(unsafe.Sizeof(ms))
	if r, _, _ := procGlobalMemory.Call(uintptr(unsafe.Pointer(&ms))); r == 0 {
		return 0
	}
	return int(ms.TotalPhys >> 20)
}

// Размеры структур как в C (wingdi.h): ошибка в раскладке остановит сборку, а не испортит память.
var (
	_ [72]byte  = [unsafe.Sizeof(dcPath{})]byte{}
	_ [64]byte  = [unsafe.Sizeof(dcMode{})]byte{}
	_ [420]byte = [unsafe.Sizeof(dcTargetName{})]byte{}
)
