// Package tray puts vura in the GNOME top bar as a StatusNotifierItem (the
// AppIndicator extension renders it). The menu shows today's totals and the
// queue and opens the dashboard. Pure Go over D-Bus; no cgo.
package tray

import (
	"context"
	_ "embed"
	"fmt"
	"log/slog"
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

// Run blocks until ctx is done. status is polled every minute. It must be
// called from the main goroutine on some platforms; on Linux any goroutine
// works, but main keeps it simple.
func Run(ctx context.Context, url string, status func(context.Context) (Status, error), log *slog.Logger) {
	onReady := func() {
		systray.SetIcon(icon)
		systray.SetTitle("vura")
		systray.SetTooltip("vura")
		today := systray.AddMenuItem("today: –", "observed · logged")
		today.Disable()
		queue := systray.AddMenuItem("queue: –", "days waiting for reconcile")
		queue.Disable()
		systray.AddSeparator()
		open := systray.AddMenuItem("Open dashboard", "app window on the local daemon")
		refresh := systray.AddMenuItem("Refresh", "")
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
					if err := launch.Open(url); err != nil {
						log.Warn("tray: open", "err", err)
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
