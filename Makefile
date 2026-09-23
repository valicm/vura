VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS  = -s -w -X main.version=$(VERSION)
BIN      = $(HOME)/.local/bin
UNAME   := $(shell uname -s)
# macOS needs cgo for the menu bar (AppKit) and mic detection (CoreAudio):
# install the Xcode command line tools first (xcode-select --install).
ifeq ($(UNAME),Darwin)
CGO     ?= 1
OS       = darwin
else
CGO     ?= 0
OS       = linux
endif
AGENTS   = $(HOME)/Library/LaunchAgents
LABEL    = com.github.valicm
GUI      = gui/$(shell id -u)

.PHONY: build test install uninstall status logs install-linux install-darwin uninstall-linux uninstall-darwin logs-linux logs-darwin

build:
	CGO_ENABLED=$(CGO) go build -trimpath -ldflags '$(LDFLAGS)' -o bin/vura  ./cmd/vura
	CGO_ENABLED=$(CGO) go build -trimpath -ldflags '$(LDFLAGS)' -o bin/vurad ./cmd/vurad

test:
	go vet ./... && go test ./...

install: build install-$(OS)

install-linux:
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

# launchd agents replace the systemd units: vurad at login with KeepAlive,
# nag at 10:00 and 19:00, backup at 03:30. Logs go to ~/Library/Logs/vura.
# bootout finishes asynchronously; a bootstrap right after it can fail with
# "Input/output error", hence the one retry.
install-darwin:
	install -d $(BIN) $(AGENTS) $(HOME)/Library/Logs/vura
	install -d -m700 $(HOME)/.local/share/vura
	install -m755 bin/vura  $(BIN)/vura
	install -m755 bin/vurad $(BIN)/vurad
	@test -f $(HOME)/.config/vura/config.toml || { install -d $(HOME)/.config/vura && install -m600 config.example.toml $(HOME)/.config/vura/config.toml; }
	for a in vurad vura-nag vura-backup; do \
		sed 's|__HOME__|$(HOME)|g' launchd/$(LABEL).$$a.plist > $(AGENTS)/$(LABEL).$$a.plist; \
		launchctl bootout $(GUI)/$(LABEL).$$a 2>/dev/null || true; \
		launchctl bootstrap $(GUI) $(AGENTS)/$(LABEL).$$a.plist 2>/dev/null || \
			{ sleep 2; launchctl bootstrap $(GUI) $(AGENTS)/$(LABEL).$$a.plist; }; \
	done
	@echo; launchctl print $(GUI)/$(LABEL).vurad | grep -E '^\s*(state|pid|last exit code) =' || true

uninstall: uninstall-$(OS)

uninstall-darwin:
	-for a in vurad vura-nag vura-backup; do launchctl bootout $(GUI)/$(LABEL).$$a 2>/dev/null; rm -f $(AGENTS)/$(LABEL).$$a.plist; done
	rm -f $(BIN)/vura $(BIN)/vurad

uninstall-linux:
	-systemctl --user disable --now vurad.service vura-nag.timer vura-nag-pm.timer vura-backup.timer
	rm -f $(BIN)/vura $(BIN)/vurad $(HOME)/.config/systemd/user/vurad.service \
	      $(HOME)/.config/systemd/user/vura-nag.service $(HOME)/.config/systemd/user/vura-nag*.timer \
	      $(HOME)/.config/systemd/user/vura-backup.service $(HOME)/.config/systemd/user/vura-backup.timer \
	      $(HOME)/.local/share/applications/vura.desktop $(HOME)/.local/share/icons/hicolor/128x128/apps/vura.png
	systemctl --user daemon-reload

status:
	$(BIN)/vura status

logs: logs-$(OS)

logs-linux:
	journalctl --user -u vurad -f

logs-darwin:
	tail -f $(HOME)/Library/Logs/vura/vurad.log
