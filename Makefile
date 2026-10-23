# Сборка lolssd. Раскладка dist/ повторяет то, куда файлы ложатся на SSD:
#   dist/LOLSSD/          содержимое раздела LOLSSD (утилита для Linux, профили)
#   dist/windows/C/       файлы, которые кладутся в раздел Windows
#   dist/windows/lolssd.reg  применяется к кустам реестра образа офлайн
#   dist/wsl/             пусковой WSL-дистрибутив (make wsl)

GO      ?= go
GOFLAGS := -trimpath -ldflags=-s\ -w
export CGO_ENABLED := 0

WIN := dist/windows/C
.PHONY: dist wsl kmod qemu iso test clean

dist:
	mkdir -p dist/LOLSSD/host dist/LOLSSD/profiles "$(WIN)/Program Files/lolssd" "$(WIN)/Windows/Setup/Scripts" dist/wsl
	GOOS=linux GOARCH=amd64 $(GO) build $(GOFLAGS) -o dist/LOLSSD/host/lolssd ./cmd/lolssd
	# windowsgui: задания планировщика не открывают окно консоли; журнал пишется в файл.
	GOOS=windows GOARCH=amd64 $(GO) build -trimpath -ldflags="-s -w -H windowsgui" -o "$(WIN)/Program Files/lolssd/lolssd-win.exe" ./cmd/lolssd-win
	GOOS=linux GOARCH=amd64 $(GO) build $(GOFLAGS) -o dist/wsl/lolssd-wsl ./cmd/lolssd-wsl
	# Файлы для Windows — с CRLF: cmd и reg import на LF спотыкаются.
	for f in SetupComplete.cmd lolssd-setup.ps1; do sed 's/\r*$$/\r/' build/setup/$$f > "$(WIN)/Windows/Setup/Scripts/$$f"; done
	sed 's/\r*$$/\r/' build/setup/lolssd.reg > dist/windows/lolssd.reg
	for f in lolssd-drivers.ps1 lolssd-riot.ps1; do sed 's/\r*$$/\r/' build/setup/$$f > "$(WIN)/Program Files/lolssd/$$f"; done
	cp README.md dist/LOLSSD/README.md
	cp build/lolssd.sh dist/LOLSSD/lolssd.sh

# Пусковой WSL-дистрибутив (Debian stable) и MSI WSL, без Docker и без root: build/wsl.sh.
# Результат: dist/wsl/lolssd.tar и dist/wsl/wsl.x64.msi; make iso кладёт оба в образ, импорт —
# при первом входе (lolssd-win mount).
wsl: dist
	build/wsl.sh

# Заголовки ядра WSL для DKMS-модулей хоста; make wsl кладёт их в пусковой дистрибутив.
kmod:
	build/kmod.sh

# QEMU для Windows (ВМ с системой хоста, lolssd-win desktop): dist/qemu, build/qemu.sh.
qemu:
	build/qemu.sh

# Установочный ISO со всем внутри: make iso ISO=~/Downloads/Win11.iso [DRIVERS=~/lolssd-drivers] [EDITION="Windows 11 Pro"]
EDITION ?= Windows 11 Home
iso: dist
	@test -n "$(ISO)" || { echo "укажите ISO=путь/к/Windows.iso"; exit 1; }
	build/iso.sh "$(ISO)" "$(DRIVERS)" "$(EDITION)"

test:
	$(GO) vet ./...
	GOOS=windows $(GO) vet ./cmd/lolssd-win
	$(GO) test ./...

clean:
	rm -rf dist
