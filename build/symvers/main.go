// symvers — Module.symvers настоящего ядра WSL: экспорты vmlinux и всех его модулей с их
// контрольными суммами (MODVERSIONS), прочитанные из самих файлов, а не вычисленные заново.
//
//	go run ./build/symvers <vmlinux> <каталог модулей> > Module.symvers
//
// Вычислять суммы самим нельзя: они зависят от компилятора (Microsoft собирает GCC 13, атрибуты
// новых версий вроде __counted_by меняют объявления), и с суммами из своей сборки ядро WSL не
// принимает модули («disagrees about version of symbol»). Раскладка структур от этого не зависит.
package main

import (
	"bytes"
	"debug/elf"
	"encoding/binary"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type export struct{ crc, name, module, kind, ns string }

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: symvers <vmlinux> <modules dir>")
		os.Exit(2)
	}
	var all []export
	ex, err := exports(os.Args[1], "vmlinux")
	if err != nil {
		fmt.Fprintln(os.Stderr, os.Args[1]+":", err)
		os.Exit(1)
	}
	all = append(all, ex...)
	root := os.Args[2]
	filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".ko") {
			return nil
		}
		// Имя модуля в Module.symvers — путь в дереве ядра без .ko (kernel/lib/crypto/libchacha → lib/crypto/libchacha).
		rel, _ := filepath.Rel(root, strings.TrimSuffix(path, ".ko"))
		rel = strings.TrimPrefix(rel, "kernel/")
		ex, err := exports(path, rel)
		if err != nil {
			fmt.Fprintln(os.Stderr, path+":", err)
			return nil
		}
		all = append(all, ex...)
		return nil
	})
	sort.Slice(all, func(i, j int) bool { return all[i].name < all[j].name })
	for _, e := range all {
		fmt.Printf("%s\t%s\t%s\t%s\t%s\n", e.crc, e.name, e.module, e.kind, e.ns)
	}
}

// exports читает __ksymtab{,_gpl} (struct kernel_symbol с PREL32: смещения значения, имени и
// пространства имён) и параллельные __kcrctab{,_gpl}. В модулях смещения — в перемещениях
// R_X86_64_PC32, в vmlinux уже вычислены.
func exports(path, module string) ([]export, error) {
	f, err := elf.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	strs := f.Section("__ksymtab_strings")
	if strs == nil {
		return nil, nil
	}
	strData, err := strs.Data()
	if err != nil {
		return nil, err
	}
	var syms []elf.Symbol
	if f.Type == elf.ET_REL {
		syms, _ = f.Symbols()
	}
	var out []export
	for _, kind := range []string{"", "_gpl"} {
		tab, crcs := f.Section("__ksymtab"+kind), f.Section("__kcrctab"+kind)
		if tab == nil || crcs == nil {
			continue
		}
		tabData, _ := tab.Data()
		crcData, _ := crcs.Data()
		relas := map[uint64]uint64{} // смещение поля в __ksymtab → смещение строки в __ksymtab_strings
		if f.Type == elf.ET_REL {
			for _, s := range f.Sections {
				if s.Type == elf.SHT_RELA && s.Info < uint32(len(f.Sections)) && f.Sections[s.Info] == tab {
					d, _ := s.Data()
					for i := 0; i+24 <= len(d); i += 24 {
						off := binary.LittleEndian.Uint64(d[i:])
						info := binary.LittleEndian.Uint64(d[i+8:])
						addend := int64(binary.LittleEndian.Uint64(d[i+16:]))
						si := int(info >> 32)
						if si == 0 || si > len(syms) || f.Sections[syms[si-1].Section] != strs {
							continue
						}
						// PC32: поле = S + A − P, указатель = P + поле = S + A
						relas[off] = uint64(int64(syms[si-1].Value) + addend)
					}
				}
			}
		}
		str := func(fieldOff uint64) string {
			var at uint64
			if f.Type == elf.ET_REL {
				v, ok := relas[fieldOff]
				if !ok {
					return ""
				}
				at = v
			} else {
				rel := int64(int32(binary.LittleEndian.Uint32(tabData[fieldOff:])))
				at = uint64(int64(tab.Addr+fieldOff)+rel) - strs.Addr
			}
			if at >= uint64(len(strData)) {
				return ""
			}
			end := bytes.IndexByte(strData[at:], 0)
			return string(strData[at : at+uint64(end)])
		}
		n := len(tabData) / 12
		for i := 0; i < n && 4*i+4 <= len(crcData); i++ {
			name := str(uint64(12*i + 4))
			if name == "" {
				continue
			}
			out = append(out, export{
				crc:    fmt.Sprintf("0x%08x", binary.LittleEndian.Uint32(crcData[4*i:])),
				name:   name,
				module: module,
				kind:   "EXPORT_SYMBOL" + strings.ToUpper(kind),
				ns:     str(uint64(12*i + 8)),
			})
		}
	}
	return out, nil
}
