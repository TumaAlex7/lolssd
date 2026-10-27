# Сборка lolssd. Итог — один бинарник dist/lolssd-setup (мастер), в который вшит «набор файлов»
# (dist/payload, раскладка — как его использует build/install.sh):
#   autounattend.xml      шаблон файла ответов
#   install.sh            разметка и установка в QEMU
#   options.default       галочки по умолчанию
#   windows/              на конфигурационный диск установки: скрипты и реестр (CRLF),
#                         lolssd-win.exe, значки, список файлов QEMU, запасные обои
#   LOLSSD/               на раздел LOLSSD после установки: утилита для Linux, README
# Для сборки нужен только Go; для установки — QEMU, OVMF и swtpm.

GO      ?= go
GOFLAGS := -trimpath -ldflags=-s\ -w
export CGO_ENABLED := 0

P := dist/payload
.PHONY: payload setup test clean

payload:
	rm -rf $(P)
	mkdir -p $(P)/windows/art $(P)/LOLSSD
	GOOS=linux GOARCH=amd64 $(GO) build $(GOFLAGS) -o $(P)/LOLSSD/lolssd ./cmd/lolssd
	# windowsgui: задания планировщика не открывают окно консоли; журнал пишется в файл.
	GOOS=windows GOARCH=amd64 $(GO) build -trimpath -ldflags="-s -w -H windowsgui" -o $(P)/windows/lolssd-win.exe ./cmd/lolssd-win
	# Файлы для Windows — с CRLF: cmd и reg import на LF спотыкаются.
	for f in build/setup/*.ps1 build/setup/*.reg build/setup/SetupComplete.cmd build/setup/qemu-files.txt; do \
		sed 's/\r*$$/\r/' $$f > $(P)/windows/$$(basename $$f); done
	cp build/art/linux.ico build/art/terminal.ico $(P)/windows/
	cp build/art/img0*.jpg build/art/img100.jpg $(P)/windows/art/
	cp build/setup/autounattend.xml build/install.sh build/options.default $(P)/
	cp README.md $(P)/LOLSSD/README.md

# Мастер с вшитым набором файлов: go:embed берёт его из каталога пакета.
setup: payload
	rm -rf cmd/lolssd-setup/payload
	cp -r $(P) cmd/lolssd-setup/payload
	GOOS=linux GOARCH=amd64 $(GO) build $(GOFLAGS) -o dist/lolssd-setup ./cmd/lolssd-setup

test:
	$(GO) vet ./...
	GOOS=windows $(GO) vet ./cmd/lolssd-win
	$(GO) test ./...

clean:
	rm -rf dist cmd/lolssd-setup/payload
