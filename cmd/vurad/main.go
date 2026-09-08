// vurad is the always-on collector. It samples presence, receives editor
// heartbeats, and watches for open microphone streams. It never computes
// hours; that is vura's job at reconcile time.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/valicm/vura/internal/bucket"
	"github.com/valicm/vura/internal/collect/anchors"
	"github.com/valicm/vura/internal/collect/audio"
	"github.com/valicm/vura/internal/collect/calendar"
	gitc "github.com/valicm/vura/internal/collect/git"
	"github.com/valicm/vura/internal/collect/presence"
	"github.com/valicm/vura/internal/collect/shell"
	"github.com/valicm/vura/internal/collect/wakatime"
	"github.com/valicm/vura/internal/config"
	"github.com/valicm/vura/internal/session"
	"github.com/valicm/vura/internal/store"
	"github.com/valicm/vura/internal/tray"
	"github.com/valicm/vura/internal/web"
)

var version = "dev"

func main() {
	cfgPath := flag.String("config", config.Path(), "config file")
	debug := flag.Bool("debug", false, "debug logging")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Println("vurad", version)
		return
	}
	lvl := slog.LevelInfo
	if *debug {
		lvl = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: lvl}))

	if err := run(log, *cfgPath); err != nil {
		log.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger, cfgPath string) error {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return err
	}
	st, err := store.Open(cfg.DBPath)
	if err != nil {
		return fmt.Errorf("store: %w", err)
	}
	defer st.Close()
	log.Info("vurad starting", "version", version, "db", cfg.DBPath, "device", cfg.Identity.Device)
	_ = st.Audit(context.Background(), "vurad.start", version)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	var wg sync.WaitGroup
	errc := make(chan error, 1)

	if cfg.Sources.Gnome {
		ps, err := presence.New(st, cfg.Identity.Device, log)
		if err != nil {
			return fmt.Errorf("presence: %w", err)
		}
		wg.Add(1)
		go func() { defer wg.Done(); ps.Run(ctx, cfg.Sources.PresenceEvery.Duration) }()
	}

	if cfg.Sources.Audio {
		ap := audio.New(st, cfg.Identity.Device, log)
		wg.Add(1)
		go func() { defer wg.Done(); ap.Run(ctx, cfg.Sources.AudioEvery.Duration) }()
	}

	res := bucket.New(cfg)

	if cfg.Sources.Atuin != "" {
		sh := shell.New(cfg.Sources.Atuin, st, res, cfg.Sources.ShellDenylist, cfg.Identity.Device, log)
		wg.Add(1)
		go func() { defer wg.Done(); sh.Run(ctx, cfg.Sources.ShellEvery.Duration) }()
	}

	if len(cfg.Identity.Emails) > 0 {
		gc := gitc.New(st, res, cfg.Identity.Emails, cfg.Sources.GitSince.Duration, log)
		wg.Add(1)
		go func() { defer wg.Done(); gc.Run(ctx, cfg.Sources.GitEvery.Duration) }()
	} else {
		log.Warn("git: no identity.emails configured; commit collection disabled")
	}

	if cfg.Anchors.GitHub != "" || cfg.Anchors.GitLab != "" || len(cfg.Anchors.Jira) > 0 {
		ar := anchors.New(cfg, st, log)
		wg.Add(1)
		go func() { defer wg.Done(); ar.Run(ctx, cfg.Anchors.Every.Duration) }()
	}

	for _, f := range cfg.Calendar.Feeds {
		cc := calendar.New(f.Name, f.URL, f.Bucket, st, cfg.Location, log)
		wg.Add(1)
		go func() { defer wg.Done(); cc.Run(ctx, cfg.Calendar.Every.Duration) }()
	}

	if cfg.Sources.Listen != "" {
		ws := wakatime.New(st, cfg.Sources.WakatimeKey, cfg.Identity.Device, log)
		ws.Today = func(ctx context.Context) (time.Duration, error) {
			return heartbeatMinutesToday(ctx, st, cfg)
		}
		ws.Web = web.New(cfg, st, version)
		log.Info("dashboard", "url", "http://"+cfg.Sources.Listen+"/")
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := ws.ListenAndServe(ctx, cfg.Sources.Listen); err != nil {
				select {
				case errc <- fmt.Errorf("wakatime: %w", err):
				default:
				}
			}
		}()
	}

	// Housekeeping: prune raw presence rows older than 90 days, once a day.
	wg.Add(1)
	go func() {
		defer wg.Done()
		t := time.NewTicker(24 * time.Hour)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if n, err := st.PrunePresence(ctx, 90*24*time.Hour); err == nil && n > 0 {
					log.Info("pruned presence", "rows", n)
				}
			}
		}
	}()

	if cfg.Sources.Tray && cfg.Sources.Listen != "" {
		// Blocks on the main goroutine until ctx is done; the collectors keep
		// running in theirs. Errors from the listener still stop everything.
		go func() {
			select {
			case <-ctx.Done():
			case err := <-errc:
				log.Error("fatal", "err", err)
				stop()
			}
		}()
		actions := tray.Actions{
			// A clean exit; the unit has Restart=always, so systemd brings a
			// fresh daemon (and a fresh tray icon) back within seconds.
			Restart: func() { _ = st.Audit(context.Background(), "vurad.restart", "tray"); stop() },
			// Ask systemd to stop the unit; it sends SIGTERM and we exit cleanly.
			Stop: func() {
				_ = st.Audit(context.Background(), "vurad.stopped", "tray")
				if err := exec.Command("systemctl", "--user", "stop", "--no-block", "vurad.service").Run(); err != nil {
					log.Warn("tray: systemctl stop", "err", err)
					stop()
				}
			},
		}
		tray.Run(ctx, "http://"+cfg.Sources.Listen+"/", cfg.DataDir, func(ctx context.Context) (tray.Status, error) {
			return trayStatus(ctx, st, cfg, res)
		}, actions, log)
		wg.Wait()
		_ = st.Audit(context.Background(), "vurad.stop", "")
		return nil
	}

	select {
	case <-ctx.Done():
		log.Info("vurad stopping")
	case err := <-errc:
		stop()
		wg.Wait()
		return err
	}
	wg.Wait()
	_ = st.Audit(context.Background(), "vurad.stop", "")
	return nil
}

// trayStatus computes today's totals and the queue for the indicator menu.
func trayStatus(ctx context.Context, st *store.Store, cfg *config.Config, res *bucket.Resolver) (tray.Status, error) {
	day := cfg.Boundary.Day(time.Now().In(cfg.Location))
	ss, _, err := session.Day(ctx, st, cfg, res, day)
	if err != nil {
		return tray.Status{}, err
	}
	var s tray.Status
	for _, x := range ss {
		if x.Bucket == session.BucketUnattributed || x.Bucket == session.BucketCall {
			continue
		}
		s.Observed += x.Duration()
		if x.Duration() > 0 {
			s.Logged += session.RoundUp(x.Duration(), cfg.Session.RoundMin.Duration)
		}
	}
	s.Pending, _ = web.PendingCount(ctx, st, cfg)
	return s, nil
}

// heartbeatMinutesToday is the status-bar number: distinct minutes with at
// least one heartbeat in the current working day. Crude, deliberately; the
// real sessioniser lives in vura.
func heartbeatMinutesToday(ctx context.Context, st *store.Store, cfg *config.Config) (time.Duration, error) {
	start := cfg.Boundary.DayStart(time.Now().In(cfg.Location))
	var n int
	err := st.DB().QueryRowContext(ctx,
		`SELECT COUNT(DISTINCT CAST(ts/60 AS INTEGER)) FROM heartbeats WHERE ts >= ?`, start.Unix()).Scan(&n)
	return time.Duration(n) * time.Minute, err
}
