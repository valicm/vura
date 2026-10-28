//go:build darwin && !cgo

// Package tray: the macOS menu bar needs cgo (AppKit). A build without it
// has no indicator; Run just waits so the daemon behaves the same.
package tray

import (
	"context"
	"log/slog"
	"time"
)

type Status struct {
	Observed, Logged time.Duration
	Pending          int
}

type Actions struct {
	Restart func()
	Stop    func()
}

func Run(ctx context.Context, url, dataDir string, status func(context.Context) (Status, error), act Actions, log *slog.Logger) {
	log.Warn("tray: built without cgo, no menu bar icon; rebuild with CGO_ENABLED=1 or set tray = false")
	<-ctx.Done()
}
