package winctl

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"time"
)

// SetDesktopSize подключается к VNC-серверу QEMU как клиент и просит размер экрана
// (ExtendedDesktopSize, сообщение SetDesktopSize). Так включается выход virtio-gpu, кроме первого,
// и получает размер монитора: QEMU передаёт его системе как предпочтительный режим и помнит после
// отключения клиента. TigerVNC с RemoteResize такой запрос не отправлял.
func SetDesktopSize(addr string, w, h int) error {
	c, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		return err
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, 24)
	if _, err := io.ReadFull(c, buf[:12]); err != nil { // версия сервера
		return err
	}
	if _, err := c.Write([]byte("RFB 003.008\n")); err != nil {
		return err
	}
	if _, err := io.ReadFull(c, buf[:1]); err != nil { // число типов защиты
		return err
	}
	types := make([]byte, buf[0])
	if _, err := io.ReadFull(c, types); err != nil {
		return err
	}
	if len(types) == 0 || types[0] != 1 {
		return fmt.Errorf("VNC без защиты недоступен: %v", types)
	}
	if _, err := c.Write([]byte{1}); err != nil {
		return err
	}
	if _, err := io.ReadFull(c, buf[:4]); err != nil || binary.BigEndian.Uint32(buf) != 0 {
		return fmt.Errorf("VNC: отказ в подключении (%v)", err)
	}
	if _, err := c.Write([]byte{1}); err != nil { // ClientInit, общий доступ
		return err
	}
	if _, err := io.ReadFull(c, buf[:24]); err != nil { // ServerInit без имени
		return err
	}
	name := make([]byte, binary.BigEndian.Uint32(buf[20:]))
	if _, err := io.ReadFull(c, name); err != nil {
		return err
	}
	msg := []byte{2, 0, 0, 1, 0xff, 0xff, 0xfe, 0xcc} // SetEncodings: ExtendedDesktopSize (-308)
	msg = append(msg, 251, 0)                         // SetDesktopSize
	msg = binary.BigEndian.AppendUint16(msg, uint16(w))
	msg = binary.BigEndian.AppendUint16(msg, uint16(h))
	msg = append(msg, 1, 0)                     // один экран
	msg = binary.BigEndian.AppendUint32(msg, 0) // id
	msg = binary.BigEndian.AppendUint32(msg, 0) // x, y
	msg = binary.BigEndian.AppendUint16(msg, uint16(w))
	msg = binary.BigEndian.AppendUint16(msg, uint16(h))
	msg = binary.BigEndian.AppendUint32(msg, 0) // флаги
	if _, err := c.Write(msg); err != nil {
		return err
	}
	// Дать серверу разобрать запрос до закрытия соединения.
	c.SetReadDeadline(time.Now().Add(time.Second))
	io.Copy(io.Discard, c)
	return nil
}
