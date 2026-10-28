//go:build linux

package presence

import (
	"context"
	"fmt"

	"github.com/godbus/dbus/v5"
)

const (
	idleDest = "org.gnome.Mutter.IdleMonitor"
	idlePath = "/org/gnome/Mutter/IdleMonitor/Core"
	idleIfc  = "org.gnome.Mutter.IdleMonitor"

	smDest = "org.gnome.SessionManager"
	smPath = "/org/gnome/SessionManager"
	smIfc  = "org.gnome.SessionManager"
)

// gnome reads mutter's IdleMonitor and gnome-session's inhibitor list.
type gnome struct{ conn *dbus.Conn }

func newSource() (source, error) {
	conn, err := dbus.SessionBus()
	if err != nil {
		return nil, fmt.Errorf("session bus: %w", err)
	}
	return &gnome{conn: conn}, nil
}

func (s *gnome) idle(ctx context.Context) (int64, error) {
	var ms uint64
	obj := s.conn.Object(idleDest, dbus.ObjectPath(idlePath))
	if err := obj.CallWithContext(ctx, idleIfc+".GetIdletime", 0).Store(&ms); err != nil {
		return 0, err
	}
	return int64(ms), nil
}

// inhibitors returns the OR of all active inhibitor flags and the app ids
// holding them, e.g. 12, ["caffeine-gnome-extension","/usr/bin/google-chrome-stable"].
func (s *gnome) inhibitors(ctx context.Context) (int, []string, error) {
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
