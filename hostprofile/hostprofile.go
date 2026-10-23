// Package hostprofile — профиль хоста для режима «Linux внутри Windows» (docs/ARCHITECTURE.md §6).
// Пишет его lolssd profile в Linux, читают lolssd-win и пусковой дистрибутив.
package hostprofile

import "time"

// Profile — только данные: что и как монтировать, кто пользователь, какие мониторы.
// Решения о том, что выполнять, принимает пусковой дистрибутив на SSD.
type Profile struct {
	Version   int       `json:"version"`
	MachineID string    `json:"machine_id"`
	Created   time.Time `json:"created"`
	Distro    struct {
		ID     string `json:"id"`
		IDLike string `json:"id_like,omitempty"`
	} `json:"distro"`
	User struct {
		Name  string `json:"name"`
		UID   int    `json:"uid"`
		GID   int    `json:"gid"`
		Home  string `json:"home"`
		Shell string `json:"shell"`
	} `json:"user"`
	Desktop    string    `json:"desktop"`
	HyprConfig string    `json:"hypr_config,omitempty"`
	MesaD3D12  bool      `json:"mesa_d3d12"`
	MemMiB     int       `json:"mem_mib"`
	Mounts     []Mount   `json:"mounts"`
	Swaps      []Swap    `json:"swaps"`
	Monitors   []Monitor `json:"monitors"`
	Keyboard   Keyboard  `json:"keyboard"`
	VM         *VMImage  `json:"vm,omitempty"`
}

// VMImage — образ загрузки системы хоста в ВМ Hyper-V (UKI на разделе LOLSSD): её ядро, initramfs
// с драйверами Hyper-V и командная строка с корнем по UUID. Ядро в образе должно совпадать с
// модулями системы: после обновления ядра на хосте профиль снимают заново.
type VMImage struct {
	Path    string `json:"path"`    // относительно раздела LOLSSD, например vm/<machine-id>.efi
	Kernel  string `json:"kernel"`  // uname -r
	Cmdline string `json:"cmdline"` // встроенная командная строка
}

// Keyboard — раскладка (RMLVO композитора) и конфиги keyd как есть: разбирает их пакет keymap
// на той стороне, где применяет.
type Keyboard struct {
	Layout  string            `json:"layout,omitempty"`
	Variant string            `json:"variant,omitempty"`
	Options string            `json:"options,omitempty"`
	Model   string            `json:"model,omitempty"`
	Keyd    map[string]string `json:"keyd,omitempty"`
}

type Mount struct {
	Target  string   `json:"target"`
	FSType  string   `json:"fstype"`
	Subvol  string   `json:"subvol,omitempty"`
	Options string   `json:"options"`
	Device  BlockDev `json:"device"`
}

// BlockDev — цепочка устройств от точки монтирования вниз до раздела:
// например lvm → luks → partition.
type BlockDev struct {
	Name      string     `json:"name"`
	Kind      string     `json:"kind"` // partition, luks, lvm, dm
	DMName    string     `json:"dm_name,omitempty"`
	LUKSUUID  string     `json:"luks_uuid,omitempty"`
	Disk      *Disk      `json:"disk,omitempty"`
	Partition int        `json:"partition,omitempty"`
	PartUUID  string     `json:"partuuid,omitempty"`
	Lower     []BlockDev `json:"lower,omitempty"`
}

type Disk struct {
	Name   string `json:"name"`
	Model  string `json:"model"`
	Serial string `json:"serial"`
}

type Swap struct {
	Path   string   `json:"path"`
	Type   string   `json:"type"` // file, partition
	Device BlockDev `json:"device"`
}

type Monitor struct {
	Port    string  `json:"port"`
	EDID    []byte  `json:"edid"`
	Desc    string  `json:"desc,omitempty"`
	X       int     `json:"x"`
	Y       int     `json:"y"`
	Width   int     `json:"width,omitempty"`
	Height  int     `json:"height,omitempty"`
	Scale   float64 `json:"scale,omitempty"`
	Refresh float64 `json:"refresh,omitempty"`
}

// PartUUIDs возвращает PARTUUID всех разделов, на которых лежат файловые системы и swap профиля:
// по ним Windows находит диски хоста.
func (p Profile) PartUUIDs() []string {
	var res []string
	var walk func(d BlockDev)
	walk = func(d BlockDev) {
		if d.PartUUID != "" {
			res = append(res, d.PartUUID)
		}
		for _, l := range d.Lower {
			walk(l)
		}
	}
	for _, m := range p.Mounts {
		walk(m.Device)
	}
	for _, s := range p.Swaps {
		walk(s.Device)
	}
	return res
}

// WinMonitor — активный монитор Windows. Список пишет lolssd-win mount в monitors.json;
// порядок в нём задаёт номер вложенного выхода Hyprland: первый — WAYLAND-1 и т. д.
type WinMonitor struct {
	Name    string  `json:"name"`
	EDID    []byte  `json:"edid"`
	X       int     `json:"x"`
	Y       int     `json:"y"`
	Width   int     `json:"width"`
	Height  int     `json:"height"`
	Refresh float64 `json:"refresh"`
	Primary bool    `json:"primary"`
}

// SameMonitor сравнивает базовый блок EDID (первые 128 байт): DRM в Linux и реестр Windows
// могут отдавать разный набор блоков расширения.
func SameMonitor(a, b []byte) bool {
	return len(a) >= 128 && len(b) >= 128 && string(a[:128]) == string(b[:128])
}
