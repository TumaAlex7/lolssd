package winctl

import (
	"bytes"
	"math"
	"sort"

	"lolssd/hostprofile"
)

// Раскладка мониторов Windows по раскладке Linux (hostprofile.HostMonitor): какие мониторы
// включены, какие зеркалят друг друга и где стоят. Монитор Windows узнаётся по EDID, так что порт
// подключения не важен. Без вызовов Windows: их делает lolssd-win (CCD API).

// DisplayGroup — мониторы Windows, показывающие одно и то же (зеркало); Targets[0] — тот, чья
// позиция задаёт место группы. Host — его монитор Linux, nil — монитор, которого в профиле нет.
type DisplayGroup struct {
	Targets []int
	Host    *hostprofile.HostMonitor
}

// sameMonitor: EDID совпадает целиком, или хотя бы производитель, модель и серийный номер
// (байты 8–15): часть мониторов по-разному отдаёт остаток базового блока разным системам.
func sameMonitor(a, b []byte) bool {
	if len(a) < 128 || len(b) < 128 {
		return false
	}
	return bytes.Equal(a[:128], b[:128]) || bytes.Equal(a[8:16], b[8:16])
}

// PlanDisplays: для мониторов Windows (EDID по номеру) — группы по раскладке Linux, в порядке
// слева направо и сверху вниз, и список выключенных (выключены в Linux). Мониторы, которых в
// профиле нет, — каждый своей группой, справа от остальных.
func PlanDisplays(host []hostprofile.HostMonitor, targets map[int][]byte) (groups []DisplayGroup, off []int) {
	ids := make([]int, 0, len(targets))
	for id := range targets {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	match := map[int]int{} // монитор Windows → индекс в host
	used := map[int]bool{}
	for _, id := range ids {
		for i, h := range host {
			if !used[i] && sameMonitor(targets[id], h.EDID) {
				match[id], used[i] = i, true
				break
			}
		}
	}
	byName := map[string]int{}
	for i, h := range host {
		byName[h.Name] = i
	}
	// Зеркало в Linux может указывать на зеркало: корень цепочки — монитор группы.
	root := func(i int) int {
		for n := 0; n < len(host) && host[i].MirrorOf != ""; n++ {
			j, ok := byName[host[i].MirrorOf]
			if !ok {
				break
			}
			i = j
		}
		return i
	}
	byRoot := map[int]*DisplayGroup{}
	var unknown []DisplayGroup
	for _, id := range ids {
		i, ok := match[id]
		switch {
		case !ok:
			unknown = append(unknown, DisplayGroup{Targets: []int{id}})
		case host[i].Disabled:
			off = append(off, id)
		default:
			r := root(i)
			g := byRoot[r]
			if g == nil {
				g = &DisplayGroup{Host: &host[r]}
				byRoot[r] = g
			}
			if i == r {
				g.Targets = append([]int{id}, g.Targets...)
			} else {
				g.Targets = append(g.Targets, id)
			}
		}
	}
	for _, g := range byRoot {
		groups = append(groups, *g)
	}
	sort.Slice(groups, func(a, b int) bool {
		ha, hb := groups[a].Host, groups[b].Host
		return ha.X < hb.X || ha.X == hb.X && ha.Y < hb.Y
	})
	return append(groups, unknown...), off
}

// PlaceDisplays — позиции групп на рабочем столе Windows (в физических пикселях) по их размерам
// в Windows. В Linux координаты логические (после масштаба), в Windows — физические: переносится
// стыковка, а не числа. Группа ставится вплотную справа от тех, что в Linux левее неё и
// перекрываются с ней по высоте, и вплотную снизу от тех, что выше и перекрываются по ширине;
// иначе — по своим логическим координатам. Первая группа (самая левая в Linux) — в (0, 0): она
// становится основным монитором. Мониторы, которых нет в профиле, — справа, по верхнему краю.
func PlaceDisplays(groups []DisplayGroup, size map[int][2]int) map[int][2]int {
	type rect struct{ lx, ly, lw, lh, px, py, pw, ph int }
	rs := make([]rect, len(groups))
	right := 0
	for i, g := range groups {
		s := size[g.Targets[0]]
		r := rect{pw: s[0], ph: s[1]}
		if h := g.Host; h != nil {
			sc := h.Scale
			if sc <= 0 {
				sc = 1
			}
			r.lx, r.ly = h.X, h.Y
			r.lw, r.lh = int(math.Round(float64(h.Width)/sc)), int(math.Round(float64(h.Height)/sc))
			r.px, r.py = h.X, h.Y
			for j := range i {
				p := rs[j]
				if groups[j].Host == nil {
					continue
				}
				if p.lx+p.lw <= r.lx && p.ly < r.ly+r.lh && r.ly < p.ly+p.lh {
					r.px = max(r.px, p.px+p.pw)
					if p.lx+p.lw == r.lx {
						r.px = p.px + p.pw // вплотную — ровно к правому краю соседа
					}
				}
			}
			for j := range i {
				p := rs[j]
				if groups[j].Host != nil && p.ly+p.lh <= r.ly && p.lx < r.lx+r.lw && r.lx < p.lx+p.lw {
					r.py = p.py + p.ph
				}
			}
		} else {
			r.px, r.py = right, 0
		}
		rs[i] = r
		right = max(right, r.px+r.pw)
	}
	pos := map[int][2]int{}
	if len(rs) == 0 {
		return pos
	}
	ox, oy := rs[0].px, rs[0].py
	for i, g := range groups {
		for _, t := range g.Targets {
			pos[t] = [2]int{rs[i].px - ox, rs[i].py - oy}
		}
	}
	return pos
}
