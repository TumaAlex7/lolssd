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
	Mounts   []Mount  `json:"mounts"`
	Swaps    []Swap   `json:"swaps"`
	Keyboard Keyboard `json:"keyboard"`
	VM       *VMImage `json:"vm,omitempty"`
	// AudioPCI — PCI-адрес встроенной звуковой карты (HDA с аналоговым кодеком), например
	// 0000:03:00.6. Звуковая карта ВМ ставится на тот же адрес: имена устройств PipeWire
	// (alsa_output.pci-0000_03_00.6...) и настройки, завязанные на них, совпадают.
	AudioPCI string `json:"audio_pci,omitempty"`
}

// VMImage — образ загрузки системы хоста в ВМ (UKI на разделе LOLSSD): её ядро, initramfs и
// командная строка с корнем по UUID. Ядро в образе должно совпадать с модулями системы: после
// обновления ядра на хосте профиль снимают заново.
type VMImage struct {
	Path    string `json:"path"`    // относительно раздела LOLSSD, например vm/<machine-id>.efi
	Kernel  string `json:"kernel"`  // uname -r
	Cmdline string `json:"cmdline"` // встроенная командная строка
}

// Keyboard — раскладка (RMLVO композитора) и конфиги keyd как есть: разбирает их пакет keymap
// на той стороне, где применяет.
type Keyboard struct {
	Keyd map[string]string `json:"keyd,omitempty"`
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

// WinMonitor — активный монитор Windows; ВМ получает по выходу virtio-gpu на каждый, слева направо.
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
