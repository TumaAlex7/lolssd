package winctl

import (
	"encoding/binary"
	"io"
)

// FSState — как система хоста оставила раздел: Windows проверяет это перед запуском ВМ.
type FSState int

const (
	FSUnknown    FSState = iota // не ext2/3/4 и не swap, или зашифрован: проверить нельзя
	FSClean                     // выключена чисто
	FSUnclean                   // журнал ext4 не закрыт: сбой, зависание или гибернация в файл
	FSHibernated                // раздел подкачки с образом гибернации
)

func (s FSState) String() string {
	return [...]string{"не проверить", "чисто", "журнал не закрыт", "гибернация"}[s]
}

// PartitionState читает начало раздела (off — его смещение на диске в байтах, кратное сектору)
// и определяет состояние по самой ФС, без драйверов Linux:
//   - ext2/3/4: флаг INCOMPAT_RECOVER в суперблоке (смещение 1024) — журнал надо проигрывать,
//     то есть ФС смонтирована или не размонтирована: система в гибернации, упала или работает;
//   - swap: подпись в конце первой страницы (4 КиБ на x86) — SWAPSPACE2 у обычного раздела,
//     *SUSPEND или LINHIB0001, когда в нём образ гибернации.
//
// Читает 8 КиБ целиком: Windows читает сырой диск только целыми секторами.
func PartitionState(r io.ReaderAt, off int64) FSState {
	b := make([]byte, 8192)
	if _, err := r.ReadAt(b, off); err != nil && err != io.EOF {
		return FSUnknown
	}
	le := binary.LittleEndian
	if le.Uint16(b[1024+0x38:]) == 0xef53 {
		const incompatRecover = 0x4
		if le.Uint32(b[1024+0x60:])&incompatRecover != 0 {
			return FSUnclean
		}
		return FSClean
	}
	switch sig := string(b[4086:4096]); {
	case sig == "SWAPSPACE2":
		return FSClean
	case sig[:9] == "S1SUSPEND" || sig[:9] == "S2SUSPEND" || sig[:9] == "ULSUSPEND" || sig == "LINHIB0001":
		return FSHibernated
	}
	return FSUnknown
}
