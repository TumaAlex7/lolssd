//go:build windows

package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// tapName — имя адаптера TAP, через который ВМ выходит в сеть.
const tapName = "lolssd"

// vmNetwork готовит сеть ВМ через TAP (docs/ARCHITECTURE.md §6): встроенная сеть QEMU (slirp)
// принимает TCP гостя внутри себя и открывает из Windows новое соединение, и пакеты, которые
// собирает zapret (фальшивки с малым TTL, нарезка сегментов), до сети не доходят — zapret ломает
// TLS. Через TAP пакеты гостя уходят в Windows как есть, а наружу их выпускает общий доступ к
// интернету (ICS) с подключения, через которое сейчас идёт маршрут по умолчанию; ICS же раздаёт
// гостю адрес по DHCP.
//
// Нет адаптера — ставит драйвер TAP-Windows (из \Program Files\lolssd\tap) и создаёт его.
// Возвращает имя адаптера для QEMU.
func vmNetwork() (string, error) {
	dir := filepath.Join(os.Getenv("ProgramFiles"), "lolssd", "tap")
	if _, err := os.Stat(filepath.Join(dir, "OemVista.inf")); err != nil {
		return "", fmt.Errorf("нет драйвера TAP-Windows в %s", dir)
	}
	script := `$ErrorActionPreference = 'Stop'
$name = '` + tapName + `'
$tap = '` + dir + `'
function Find-Tap { Get-NetAdapter | Where-Object { $_.InterfaceDescription -like 'TAP-Windows Adapter V9*' } | Select-Object -First 1 }
$a = Find-Tap
if (-not $a) {
  & "$tap\tapinstall.exe" install "$tap\OemVista.inf" tap0901 | Out-Host
  if ($LASTEXITCODE -ne 0) { throw "tapinstall: $LASTEXITCODE" }
  for ($i = 0; $i -lt 20 -and -not $a; $i++) { Start-Sleep -Milliseconds 500; $a = Find-Tap }
  if (-not $a) { throw 'адаптер TAP не появился' }
  "installed"
}
if ($a.Name -ne $name) { Rename-NetAdapter -Name $a.Name -NewName $name; "renamed $($a.Name)" }

# Подключение к интернету — с маршрутом по умолчанию и наименьшей метрикой.
$up = Get-NetRoute -DestinationPrefix 0.0.0.0/0 -ErrorAction SilentlyContinue |
  Where-Object { $_.InterfaceAlias -ne $name } |
  Sort-Object { $_.RouteMetric + (Get-NetIPInterface -InterfaceIndex $_.ifIndex -AddressFamily IPv4).InterfaceMetric } |
  Select-Object -First 1
if (-not $up) { throw 'нет подключения к интернету (маршрута по умолчанию)' }

# ICS: общий доступ с него на TAP; общий доступ у остальных подключений снимается (ICS допускает
# только одну пару).
$m = New-Object -ComObject HNetCfg.HNetShare
$pub = $null; $priv = $null
foreach ($c in $m.EnumEveryConnection) {
  $p = $m.NetConnectionProps.Invoke($c)
  $cfg = $m.INetSharingConfigurationForINetConnection.Invoke($c)
  if ($p.Name -eq $up.InterfaceAlias) { $pub = $cfg }
  elseif ($p.Name -eq $name) { $priv = $cfg }
  elseif ($cfg.SharingEnabled) { $cfg.DisableSharing(); "unshared $($p.Name)" }
}
if (-not $pub -or -not $priv) { throw "ICS: не найдены подключения $($up.InterfaceAlias) / $name" }
if (-not ($pub.SharingEnabled -and $pub.SharingConnectionType -eq 0)) { $pub.DisableSharing(); $pub.EnableSharing(0); "public $($up.InterfaceAlias)" }
if (-not ($priv.SharingEnabled -and $priv.SharingConnectionType -eq 1)) { $priv.DisableSharing(); $priv.EnableSharing(1); "private $name" }
`
	out, err := hidden(3*time.Minute, "powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass",
		"-Command", script).CombinedOutput()
	msg := strings.TrimSpace(string(out))
	if err != nil {
		return "", fmt.Errorf("%v: %s", err, msg)
	}
	if msg != "" {
		log.Printf("сеть ВМ: %s", strings.ReplaceAll(msg, "\r\n", "; "))
	}
	return tapName, nil
}
