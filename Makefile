VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS  = -s -w -X main.version=$(VERSION)
BIN      = $(HOME)/.local/bin

.PHONY: build test install uninstall status logs

build:
	CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o bin/vura  ./cmd/vura
	CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o bin/vurad ./cmd/vurad

test:
	go vet ./... && go test ./...

install: build
	install -Dm755 bin/vura  $(BIN)/vura
	install -Dm755 bin/vurad $(BIN)/vurad
	install -d -m700 $(HOME)/.local/share/vura
	install -Dm644 systemd/vurad.service $(HOME)/.config/systemd/user/vurad.service
	install -Dm644 systemd/vura-nag.service $(HOME)/.config/systemd/user/vura-nag.service
	install -Dm644 systemd/vura-nag.timer $(HOME)/.config/systemd/user/vura-nag.timer
	install -Dm644 systemd/vura-nag-pm.timer $(HOME)/.config/systemd/user/vura-nag-pm.timer
	install -Dm644 systemd/vura-backup.service $(HOME)/.config/systemd/user/vura-backup.service
	install -Dm644 systemd/vura-backup.timer $(HOME)/.config/systemd/user/vura-backup.timer
	install -Dm644 systemd/vura.desktop $(HOME)/.local/share/applications/vura.desktop
	install -Dm644 assets/logo-128.png $(HOME)/.local/share/icons/hicolor/128x128/apps/vura.png
	install -Dm644 assets/logo.svg $(HOME)/.local/share/icons/hicolor/scalable/apps/vura.svg
	-gtk-update-icon-cache -q -t $(HOME)/.local/share/icons/hicolor 2>/dev/null || true
	-update-desktop-database $(HOME)/.local/share/applications 2>/dev/null
	@test -f $(HOME)/.config/vura/config.toml || install -Dm600 config.example.toml $(HOME)/.config/vura/config.toml
	systemctl --user daemon-reload
	systemctl --user enable --now vurad.service
	systemctl --user enable --now vura-nag.timer vura-nag-pm.timer vura-backup.timer
	systemctl --user restart vurad.service
	@echo; systemctl --user --no-pager status vurad.service | head -5

uninstall:
	-systemctl --user disable --now vurad.service vura-nag.timer vura-nag-pm.timer vura-backup.timer
	rm -f $(BIN)/vura $(BIN)/vurad $(HOME)/.config/systemd/user/vurad.service \
	      $(HOME)/.config/systemd/user/vura-nag.service $(HOME)/.config/systemd/user/vura-nag*.timer \
	      $(HOME)/.config/systemd/user/vura-backup.service $(HOME)/.config/systemd/user/vura-backup.timer \
	      $(HOME)/.local/share/applications/vura.desktop $(HOME)/.local/share/icons/hicolor/128x128/apps/vura.png
	systemctl --user daemon-reload

status:
	$(BIN)/vura status

logs:
	journalctl --user -u vurad -f
