// Package presence samples "is someone at the keyboard" from GNOME:
// mutter's IdleMonitor (ms since last input) and gnome-session's inhibitor
// list (Caffeine, a browser playing audio, ...). It records; it never judges.
package presence

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/valicm/vura/internal/store"
)

const (
	idleDest = "org.gnome.Mutter.IdleMonitor"
	idlePath = "/org/gnome/Mutter/IdleMonitor/Core"
	idleIfc  = "org.gnome.Mutter.IdleMonitor"

	smDest = "org.gnome.SessionManager"
	smPath = "/org/gnome/SessionManager"
	smIfc  = "org.gnome.SessionManager"

	// gnome-session inhibitor flags
	FlagLogout  = 1
	FlagSwitch  = 2
	FlagSuspend = 4
	FlagIdle    = 8
)

type Sampler struct {
	conn   *dbus.Conn
	store  *store.Store
	device string
	log    *slog.Logger
}

func New(st *store.Store, device string, log *slog.Logger) (*Sampler, error) {
	conn, err := dbus.SessionBus()
	if err != nil {
		return nil, fmt.Errorf("session bus: %w", err)
	}
	return &Sampler{conn: conn, store: st, device: device, log: log}, nil
}

// Run samples every `every` until ctx is done. A failed sample is logged
// and skipped; the shell may not be up yet at login.
func (s *Sampler) Run(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	s.sample(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.sample(ctx)
		}
	}
}

func (s *Sampler) sample(ctx context.Context) {
	now := time.Now()
	idle, err := s.idle(ctx)
	if err != nil {
		s.log.Warn("presence: idle", "err", err)
		return
	}
	flags, apps, err := s.inhibitors(ctx)
	if err != nil {
		// Not fatal: idle alone is still worth a row.
		s.log.Warn("presence: inhibitors", "err", err)
	}
	if err := s.store.InsertPresence(ctx, store.Presence{
		TS: now, IdleMS: idle, InhibitFlags: flags, Inhibitors: strings.Join(apps, ","), Device: s.device,
	}); err != nil {
		s.log.Error("presence: insert", "err", err)
		return
	}
	_ = s.store.SetState(ctx, "collector.ok.presence", now.Format(time.RFC3339))
}

func (s *Sampler) idle(ctx context.Context) (int64, error) {
	var ms uint64
	obj := s.conn.Object(idleDest, dbus.ObjectPath(idlePath))
	if err := obj.CallWithContext(ctx, idleIfc+".GetIdletime", 0).Store(&ms); err != nil {
		return 0, err
	}
	return int64(ms), nil
}

// inhibitors returns the OR of all active inhibitor flags and the app ids
// holding them, e.g. 12, ["caffeine-gnome-extension","/usr/bin/google-chrome-stable"].
func (s *Sampler) inhibitors(ctx context.Context) (int, []string, error) {
	var paths []dbus.ObjectPath
	sm := s.conn.Object(smDest, dbus.ObjectPath(smPath))
	if err := sm.CallWithContext(ctx, smIfc+".GetInhibitors", 0).Store(&paths); err != nil {
		return 0, nil, err
	}
	flags := 0
	var apps []string
	for _, p := range paths {
		obj := s.conn.Object(smDest, p)
		var f uint32
		var app string
		if err := obj.CallWithContext(ctx, smIfc+".Inhibitor.GetFlags", 0).Store(&f); err != nil {
			continue // inhibitor vanished between calls
		}
		if err := obj.CallWithContext(ctx, smIfc+".Inhibitor.GetAppId", 0).Store(&app); err != nil {
			app = "?"
		}
		flags |= int(f)
		apps = append(apps, app)
	}
	return flags, apps, nil
}
