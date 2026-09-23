//go:build !darwin || cgo

// Package tray puts vura in the GNOME top bar as a StatusNotifierItem (the
// AppIndicator extension renders it) or in the macOS menu bar. The menu shows
// today's totals and the queue and opens the dashboard. On Linux it is pure
// Go over D-Bus; on macOS it needs cgo (AppKit).
package tray

import (
	"context"
	_ "embed"
	"fmt"
	"log/slog"
	"runtime"
	"time"

	"fyne.io/systray"

	"github.com/valicm/vura/internal/launch"
)

//go:embed icon.png
var icon []byte

// Status is what the menu shows; the daemon supplies it.
type Status struct {
	Observed, Logged time.Duration
	Pending          int
}

// Actions are what the menu can trigger in the daemon.
type Actions struct {
	Restart func() // exit cleanly; the service supervisor starts a fresh daemon
	Stop    func() // stop the service until it is started again
}

// Run blocks until ctx is done. status is polled every minute. It must be
// called from the main goroutine: macOS runs the menu on the main thread
// (see lock_darwin.go); on Linux any goroutine works, but main keeps it simple.
func Run(ctx context.Context, url, dataDir string, status func(context.Context) (Status, error), act Actions, log *slog.Logger) {
	onReady := func() {
		systray.SetIcon(icon)
		if runtime.GOOS != "darwin" {
			systray.SetTitle("vura") // macOS would print it next to the icon
		}
		systray.SetTooltip("vura")
		today := systray.AddMenuItem("today: –", "observed · logged")
		today.Disable()
		queue := systray.AddMenuItem("queue: –", "days waiting for reconcile")
		queue.Disable()
		systray.AddSeparator()
		open := systray.AddMenuItem("Open dashboard", "app window on the local daemon")
		refresh := systray.AddMenuItem("Refresh", "")
		systray.AddSeparator()
		restart := systray.AddMenuItem("Restart vurad", "exit and let the service manager start it again")
		stop := systray.AddMenuItem("Stop vurad", "stop collecting until it is started again or next login")
		update := func() {
			s, err := status(ctx)
			if err != nil {
				today.SetTitle("today: unavailable")
				return
			}
			today.SetTitle(fmt.Sprintf("today: %s observed · %s logged", hm(s.Observed), hm(s.Logged)))
			switch s.Pending {
			case 0:
				queue.SetTitle("queue: empty")
			case 1:
				queue.SetTitle("queue: 1 day pending")
			default:
				queue.SetTitle(fmt.Sprintf("queue: %d days pending", s.Pending))
			}
			tip := fmt.Sprintf("vura · %s today", hm(s.Observed))
			if s.Pending > 0 {
				tip += fmt.Sprintf(" · %d pending", s.Pending)
			}
			systray.SetTooltip(tip)
		}
		update()
		go func() {
			t := time.NewTicker(time.Minute)
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-t.C:
					update()
				case <-refresh.ClickedCh:
					update()
				case <-open.ClickedCh:
					if err := launch.Open(url, dataDir); err != nil {
						log.Warn("tray: open", "err", err)
					}
				case <-restart.ClickedCh:
					if act.Restart != nil {
						log.Info("tray: restart requested")
						act.Restart()
					}
				case <-stop.ClickedCh:
					if act.Stop != nil {
						log.Info("tray: stop requested")
						act.Stop()
					}
				}
			}
		}()
	}
	go func() {
		<-ctx.Done()
		systray.Quit()
	}()
	systray.Run(onReady, func() {})
}

func hm(d time.Duration) string {
	if d <= 0 {
		return "0m"
	}
	h, m := int(d.Hours()), int(d.Minutes())%60
	if h == 0 {
		return fmt.Sprintf("%dm", m)
	}
	return fmt.Sprintf("%dh%02dm", h, m)
}
