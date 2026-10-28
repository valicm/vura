package presence

import (
	"fmt"
	"strconv"
	"strings"
)

// parseIOReg finds HIDIdleTime (nanoseconds since the last keyboard or
// pointer input) in `ioreg -c IOHIDSystem` output and returns milliseconds:
//
//	|   "HIDIdleTime" = 123456789
func parseIOReg(text string) (int64, error) {
	for _, line := range strings.Split(text, "\n") {
		_, v, ok := strings.Cut(line, `"HIDIdleTime" = `)
		if !ok {
			continue
		}
		ns, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		if err != nil {
			return 0, fmt.Errorf("HIDIdleTime %q: %w", v, err)
		}
		return ns / 1e6, nil
	}
	return 0, fmt.Errorf("ioreg: no HIDIdleTime")
}

// parsePmset reads the "Listed by owning process" part of `pmset -g
// assertions` and maps each assertion onto gnome-session's flags:
// PreventUserIdleDisplaySleep (Amphetamine, `caffeinate -d`, a video) is
// the deliberate keep-awake GNOME calls an idle inhibitor;
// PreventUserIdleSystemSleep (audio playing, plain `caffeinate`) only stops
// suspend. macOS's own daemons are left out.
//
//	pid 412(caffeinate): [0x0000...] 00:10:00 PreventUserIdleDisplaySleep named: "caffeinate command-line tool"
func parsePmset(text string) (int, []string) {
	flags := 0
	var apps []string
	inList := false
	for _, line := range strings.Split(text, "\n") {
		t := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(t, "Listed by owning process"):
			inList = true
			continue
		case !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t"):
			inList = false
			continue
		}
		if !inList || !strings.HasPrefix(t, "pid ") {
			continue
		}
		open, close := strings.Index(t, "("), strings.Index(t, "):")
		if open < 0 || close < open {
			continue
		}
		app := t[open+1 : close]
		if systemAssertion[app] {
			continue
		}
		f := 0
		switch {
		case strings.Contains(t, " PreventUserIdleDisplaySleep "):
			f = FlagIdle
		case strings.Contains(t, " PreventUserIdleSystemSleep "), strings.Contains(t, " PreventSystemSleep "):
			f = FlagSuspend
		default:
			continue
		}
		flags |= f
		apps = append(apps, app)
	}
	return flags, apps
}

// systemAssertion: macOS daemons that hold assertions on their own
// schedule (Power Nap, backups, the display wrangler). They say nothing
// about the person at the keyboard.
var systemAssertion = map[string]bool{
	"powerd": true, "WindowServer": true, "backupd": true, "backupd-helper": true,
	"mds": true, "mds_stores": true, "apsd": true, "sharingd": true, "useractivityd": true,
	"bluetoothd": true, "mDNSResponder": true, "softwareupdated": true,
}
