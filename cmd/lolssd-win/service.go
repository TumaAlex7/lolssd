//go:build windows

package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"time"

	"golang.org/x/sys/windows/svc"
)

// service — служба lolssd-vm (её регистрирует lolssd-setup.ps1 с опцией vm): при выключении или
// перезагрузке Windows сначала корректно выключает систему хоста в ВМ. Windows шлёт службам
// уведомление PRESHUTDOWN до того, как закрывать программы, и ждёт столько, сколько служба
// попросила (PreshutdownTimeout, 120 с): служба нажимает в ВМ кнопку питания (QMP
// system_powerdown — systemd выключается как от кнопки) и ждёт, пока QEMU закроется. Иначе QEMU
// убивался бы вместе с Windows и Linux каждый раз выключался бы грязно.
func service() int {
	if err := svc.Run("lolssd-vm", vmGuard{}); err != nil {
		log.Println(err)
		return 1
	}
	return 0
}

type vmGuard struct{}

func (vmGuard) Execute(_ []string, req <-chan svc.ChangeRequest, st chan<- svc.Status) (bool, uint32) {
	st <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptPreShutdown}
	for c := range req {
		switch c.Cmd {
		case svc.Interrogate:
			st <- c.CurrentStatus
		case svc.PreShutdown, svc.Stop:
			st <- svc.Status{State: svc.StopPending, WaitHint: 10000}
			if c.Cmd == svc.PreShutdown {
				shutdownVM(st)
			}
			return false, 0
		}
	}
	return false, 0
}

// shutdownVM: ВМ работает (QMP слушает) — кнопка питания и ожидание до 100 с, с отметками
// прогресса, чтобы диспетчер служб не счёл службу зависшей.
func shutdownVM(st chan<- svc.Status) {
	if !listening(qmpPort) {
		return
	}
	if err := qmp("system_powerdown"); err != nil {
		log.Println("QMP system_powerdown:", err)
		return
	}
	log.Println("Windows выключается: выключаю Linux в ВМ")
	for i := uint32(1); i <= 100; i++ {
		if !listening(qmpPort) {
			log.Printf("ВМ выключилась за %d с", i)
			return
		}
		st <- svc.Status{State: svc.StopPending, CheckPoint: i, WaitHint: 5000}
		time.Sleep(time.Second)
	}
	log.Println("ВМ не выключилась за 100 с")
}

// qmp выполняет одну команду QMP без аргументов: приветствие, qmp_capabilities, команда.
func qmp(cmd string) error {
	c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", qmpPort), 2*time.Second)
	if err != nil {
		return err
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(10 * time.Second))
	r := bufio.NewReader(c)
	if _, err := r.ReadBytes('\n'); err != nil { // {"QMP": …}
		return err
	}
	for _, x := range []string{"qmp_capabilities", cmd} {
		if err := json.NewEncoder(c).Encode(map[string]string{"execute": x}); err != nil {
			return err
		}
		for {
			line, err := r.ReadBytes('\n')
			if err != nil {
				return err
			}
			var resp struct {
				Return any `json:"return"`
				Error  *struct {
					Desc string `json:"desc"`
				} `json:"error"`
			}
			if json.Unmarshal(line, &resp) != nil {
				continue
			}
			if resp.Error != nil {
				return fmt.Errorf("%s: %s", x, resp.Error.Desc)
			}
			if resp.Return != nil {
				break // события (без return) пропускаются
			}
		}
	}
	return nil
}
