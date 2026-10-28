// Package presence samples "is someone at the keyboard": milliseconds since
// the last input, and what is holding the machine awake (Caffeine, a browser
// playing audio, ...). On Linux it asks GNOME over D-Bus; on macOS it reads
// IOKit's HIDIdleTime and pmset's power assertions. It records; it never
// judges.
package presence

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/valicm/vura/internal/store"
)

// Inhibitor flags, gnome-session's values. macOS assertions map onto them.
const (
	FlagLogout  = 1
	FlagSwitch  = 2
	FlagSuspend = 4
	FlagIdle    = 8
)

type Sampler struct {
	src    source // platform: GNOME over D-Bus, or IOKit + pmset
	store  *store.Store
	device string
	log    *slog.Logger
}

// source is the platform half: idle time and the inhibitor flags plus the
// apps holding them.
type source interface {
	idle(ctx context.Context) (int64, error)
	inhibitors(ctx context.Context) (int, []string, error)
}

func New(st *store.Store, device string, log *slog.Logger) (*Sampler, error) {
	src, err := newSource()
	if err != nil {
		return nil, err
	}
	return &Sampler{src: src, store: st, device: device, log: log}, nil
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
	idle, err := s.src.idle(ctx)
	if err != nil {
		s.log.Warn("presence: idle", "err", err)
		return
	}
	flags, apps, err := s.src.inhibitors(ctx)
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
