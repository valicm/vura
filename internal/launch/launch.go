// Package launch opens the dashboard as an app window.
package launch

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
)

// Open starts a chromeless browser window on url. The window runs in its own
// browser profile under dataDir, so it never touches the user's browsing
// profiles and it is a fresh instance, which lets the window class "vura"
// take effect: GNOME then matches the window to vura.desktop for the dock
// icon. Falls back to the default browser.
func Open(url, dataDir string) error {
	profile := filepath.Join(dataDir, "app-profile")
	_ = os.MkdirAll(profile, 0o700)
	for _, b := range []string{"google-chrome", "google-chrome-stable", "chromium", "chromium-browser", "brave-browser", "microsoft-edge"} {
		path, err := exec.LookPath(b)
		if err != nil {
			continue
		}
		cmd := exec.Command(path,
			"--user-data-dir="+profile, "--no-first-run", "--no-default-browser-check",
			"--class=vura", "--window-size=1280,900", "--app="+url)
		if err := cmd.Start(); err != nil {
			return err
		}
		return cmd.Process.Release()
	}
	if path, err := exec.LookPath("xdg-open"); err == nil {
		cmd := exec.Command(path, url)
		if err := cmd.Start(); err != nil {
			return err
		}
		return cmd.Process.Release()
	}
	return errors.New("no browser found: " + url)
}
