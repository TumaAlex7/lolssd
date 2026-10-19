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
