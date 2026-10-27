//go:build windows

package main

import (
	"fmt"
	"log"
	"runtime"
	"sort"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"

	"lolssd/hostprofile"
	"lolssd/winctl"
)

var (
	user32            = windows.NewLazySystemDLL("user32.dll")
	procGetBufSizes   = user32.NewProc("GetDisplayConfigBufferSizes")
	procQueryConfig   = user32.NewProc("QueryDisplayConfig")
	procGetDevInfo    = user32.NewProc("DisplayConfigGetDeviceInfo")
	procGlobalMemory  = kernel32.NewProc("GlobalMemoryStatusEx")
	procSetDisplayCfg = user32.NewProc("SetDisplayConfig")
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

// extendDisplays переключает Windows в режим «Расширить», если какие-то мониторы показывают одну
// и ту же область (режим «Дублировать» — его Windows включает по умолчанию при подключении
// второго монитора). Порядок и положение мониторов Windows берёт из своей базы для этого набора
// мониторов; возвращает true, если раскладка менялась.
func extendDisplays() bool {
	mons, err := activeMonitors()
	if err != nil || len(distinctMonitors(mons)) == len(mons) {
		return false
	}
	const sdcTopologyExtend, sdcApply = 0x4, 0x80
	if r, _, _ := procSetDisplayCfg.Call(0, 0, 0, 0, sdcTopologyExtend|sdcApply); r != 0 {
		log.Printf("SetDisplayConfig(extend): %d", r)
		return false
	}
	log.Println("мониторы: «Дублировать» → «Расширить»")
	return true
}

// watchDisplays — задание при входе (lolssd-win displays), живёт весь сеанс: при входе и при
// каждой смене набора мониторов (подключили ещё один) ставит раскладку как в Linux — по профилю
// этого компьютера — или, без профиля, «Расширить» вместо «Дублировать». Только при смене набора:
// раскладка, выбранная потом самим пользователем (Win+P), не перебивается.
func watchDisplays() int {
	runtime.LockOSThread()
	var host []hostprofile.HostMonitor
	if vol, err := lolssdVolume(); err == nil {
		if p, _, err := findProfile(vol); err == nil {
			host = p.Monitors
		}
	}
	log.Printf("мониторов в профиле Linux: %d", len(host))
	last := ""
	for {
		mons, err := activeMonitors()
		if err == nil {
			names := make([]string, len(mons))
			for i, m := range mons {
				names[i] = m.Name
			}
			sort.Strings(names)
			if key := strings.Join(names, "|"); key != last {
				if last != "" {
					log.Printf("мониторы: %s", strings.Join(names, ", "))
				}
				arrangeDisplays(host)
				last = key
			}
		}
		time.Sleep(3 * time.Second)
	}
}

const (
	qdcAllPaths, qdcOnlyActivePaths = 1, 2
	pathActive                      = 0x1
	modeIdxInvalid                  = 0xffffffff
	sdcTopologySupplied             = 0x10
	sdcUseSuppliedConfig            = 0x20
	sdcApply                        = 0x80
	sdcSaveToDatabase               = 0x200
	sdcAllowChanges                 = 0x400
	sdcAllowPathOrderChanges        = 0x2000
)

func queryConfig(flags uintptr) ([]dcPath, []dcMode, error) {
	var nPaths, nModes uint32
	if r, _, _ := procGetBufSizes.Call(flags, uintptr(unsafe.Pointer(&nPaths)), uintptr(unsafe.Pointer(&nModes))); r != 0 {
		return nil, nil, fmt.Errorf("GetDisplayConfigBufferSizes: %d", r)
	}
	paths := make([]dcPath, nPaths+1)
	modes := make([]dcMode, nModes+1)
	if r, _, _ := procQueryConfig.Call(flags, uintptr(unsafe.Pointer(&nPaths)), uintptr(unsafe.Pointer(&paths[0])),
		uintptr(unsafe.Pointer(&nModes)), uintptr(unsafe.Pointer(&modes[0])), 0); r != 0 {
		return nil, nil, fmt.Errorf("QueryDisplayConfig: %d", r)
	}
	return paths[:nPaths], modes[:nModes], nil
}

type targetKey struct {
	adapter windows.LUID
	id      uint32
}

func targetEDID(k targetKey) []byte {
	const getTargetName = 2
	tn := dcTargetName{Type: getTargetName, Adapter: k.adapter, ID: k.id}
	tn.Size = uint32(unsafe.Sizeof(tn))
	if r, _, _ := procGetDevInfo.Call(uintptr(unsafe.Pointer(&tn))); r != 0 {
		return nil
	}
	return edidFor(windows.UTF16ToString(tn.DevicePath[:]))
}

// arrangeDisplays — раскладка мониторов: как в Linux (профиль с мониторами), иначе просто
// «Расширить» вместо «Дублировать». Возвращает true, если раскладка менялась.
func arrangeDisplays(host []hostprofile.HostMonitor) bool {
	if len(host) > 0 {
		changed, err := applyHostLayout(host)
		if err == nil {
			return changed
		}
		log.Println("мониторы как в Linux:", err)
	}
	return extendDisplays()
}

// applyHostLayout ставит мониторы как в Linux (winctl.PlanDisplays, PlaceDisplays) через CCD API в
// две фазы: сначала топология — какие мониторы включены и какие показывают один источник
// (зеркало), без режимов — Windows подбирает их сама; потом — позиции по тем размерам, что
// Windows выбрала, с сохранением в её базу раскладок. Если уже так, ничего не трогает.
func applyHostLayout(host []hostprofile.HostMonitor) (bool, error) {
	all, _, err := queryConfig(qdcAllPaths)
	if err != nil {
		return false, err
	}
	ids := map[targetKey]int{}
	keys := []targetKey{}
	edids := map[int][]byte{}
	for _, p := range all {
		k := targetKey{p.TgtAdapter, p.TgtID}
		if _, ok := ids[k]; ok || p.Available == 0 {
			continue
		}
		ids[k] = len(keys)
		keys = append(keys, k)
		edids[ids[k]] = targetEDID(k)
	}
	groups, off := winctl.PlanDisplays(host, edids)
	if len(groups) == 0 {
		return false, fmt.Errorf("ни один монитор Windows не найден")
	}
	if layoutMatches(groups, ids) {
		return false, nil
	}
	log.Printf("мониторы как в Linux: групп %d, выключено %d", len(groups), len(off))

	// Фаза 1: путь (источник → монитор) для каждого монитора группы; у зеркала источник общий.
	type srcKey struct {
		adapter windows.LUID
		id      uint32
	}
	used := map[srcKey]bool{}
	var sel []dcPath
	pick := func(t int, want *srcKey) *srcKey {
		for _, p := range all {
			s := srcKey{p.SrcAdapter, p.SrcID}
			if (targetKey{p.TgtAdapter, p.TgtID}) != keys[t] || want == nil && used[s] || want != nil && s != *want {
				continue
			}
			p.Flags = pathActive
			p.SrcModeIdx, p.TgtModeIdx = modeIdxInvalid, modeIdxInvalid
			sel = append(sel, p)
			used[s] = true
			return &s
		}
		return nil
	}
	for _, g := range groups {
		src := pick(g.Targets[0], nil)
		if src == nil {
			log.Printf("монитор %d: нет свободного источника", g.Targets[0])
			continue
		}
		for _, t := range g.Targets[1:] {
			// Зеркало между разными видеокартами Windows не умеет — тогда расширение.
			if pick(t, src) == nil && pick(t, nil) == nil {
				log.Printf("монитор %d: ни зеркала, ни своего источника", t)
			}
		}
	}
	if len(sel) == 0 {
		return false, fmt.Errorf("нет путей для мониторов")
	}
	if r, _, _ := procSetDisplayCfg.Call(uintptr(len(sel)), uintptr(unsafe.Pointer(&sel[0])), 0, 0,
		sdcTopologySupplied|sdcApply|sdcAllowPathOrderChanges); r != 0 {
		return false, fmt.Errorf("SetDisplayConfig (мониторы): %d", r)
	}

	// Фаза 2: позиции по размерам источников, которые выбрала Windows.
	paths, modes, err := queryConfig(qdcOnlyActivePaths)
	if err != nil {
		return true, err
	}
	size := map[int][2]int{}
	for _, p := range paths {
		if t, ok := ids[targetKey{p.TgtAdapter, p.TgtID}]; ok && int(p.SrcModeIdx) < len(modes) {
			m := modes[p.SrcModeIdx]
			size[t] = [2]int{int(m.Width), int(m.Height)}
		}
	}
	pos := winctl.PlaceDisplays(groups, size)
	for _, p := range paths {
		t, ok := ids[targetKey{p.TgtAdapter, p.TgtID}]
		if xy, has := pos[t]; ok && has && int(p.SrcModeIdx) < len(modes) {
			modes[p.SrcModeIdx].X, modes[p.SrcModeIdx].Y = int32(xy[0]), int32(xy[1])
		}
	}
	if r, _, _ := procSetDisplayCfg.Call(uintptr(len(paths)), uintptr(unsafe.Pointer(&paths[0])), uintptr(len(modes)),
		uintptr(unsafe.Pointer(&modes[0])), sdcUseSuppliedConfig|sdcApply|sdcSaveToDatabase|sdcAllowChanges); r != 0 {
		return true, fmt.Errorf("SetDisplayConfig (позиции): %d", r)
	}
	log.Println("мониторы расставлены как в Linux")
	return true, nil
}

// layoutMatches: включены ровно мониторы групп, у зеркал общий источник, и стоят они там, где
// поставил бы PlaceDisplays.
func layoutMatches(groups []winctl.DisplayGroup, ids map[targetKey]int) bool {
	paths, modes, err := queryConfig(qdcOnlyActivePaths)
	if err != nil {
		return false
	}
	type src struct {
		adapter windows.LUID
		id      uint32
	}
	srcOf := map[int]src{}
	size := map[int][2]int{}
	at := map[int][2]int{}
	for _, p := range paths {
		t, ok := ids[targetKey{p.TgtAdapter, p.TgtID}]
		if !ok || int(p.SrcModeIdx) >= len(modes) {
			return false
		}
		srcOf[t] = src{p.SrcAdapter, p.SrcID}
		m := modes[p.SrcModeIdx]
		size[t], at[t] = [2]int{int(m.Width), int(m.Height)}, [2]int{int(m.X), int(m.Y)}
	}
	n := 0
	for _, g := range groups {
		for _, t := range g.Targets {
			if _, ok := srcOf[t]; !ok || srcOf[t] != srcOf[g.Targets[0]] {
				return false
			}
			n++
		}
	}
	if n != len(srcOf) {
		return false
	}
	for t, xy := range winctl.PlaceDisplays(groups, size) {
		if at[t] != xy {
			return false
		}
	}
	return true
}
