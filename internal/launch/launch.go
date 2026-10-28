// Package launch opens the dashboard as an app window.
package launch

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// Open starts a chromeless browser window on url. The window runs in its own
// browser profile under dataDir, so it never touches the user's browsing
// profiles and it is a fresh instance, which lets the window class "vura"
// take effect: GNOME then matches the window to vura.desktop for the dock
// icon. Falls back to the default browser.
func Open(url, dataDir string) error {
	profile := filepath.Join(dataDir, "app-profile")
	_ = os.MkdirAll(profile, 0o700)
	for _, path := range browsers() {
		cmd := exec.Command(path,
			"--user-data-dir="+profile, "--no-first-run", "--no-default-browser-check",
			"--class=vura", "--window-size=1280,900", "--app="+url)
		if err := cmd.Start(); err != nil {
			return err
		}
		return cmd.Process.Release()
	}
	opener := "xdg-open"
	if runtime.GOOS == "darwin" {
		opener = "open"
	}
	if path, err := exec.LookPath(opener); err == nil {
		cmd := exec.Command(path, url)
		if err := cmd.Start(); err != nil {
			return err
		}
		return cmd.Process.Release()
	}
	return errors.New("no browser found: " + url)
}

// browsers lists Chromium-family executables that exist, in preference
// order: on PATH for Linux, inside the .app bundles on macOS.
func browsers() []string {
	var out []string
	if runtime.GOOS == "darwin" {
		home, _ := os.UserHomeDir()
		for _, app := range []string{"Google Chrome", "Chromium", "Brave Browser", "Microsoft Edge"} {
			for _, dir := range []string{"/Applications", filepath.Join(home, "Applications")} {
				p := filepath.Join(dir, app+".app", "Contents", "MacOS", app)
				if _, err := os.Stat(p); err == nil {
					out = append(out, p)
				}
			}
		}
		return out
	}
	for _, b := range []string{"google-chrome", "google-chrome-stable", "chromium", "chromium-browser", "brave-browser", "microsoft-edge"} {
		if p, err := exec.LookPath(b); err == nil {
			out = append(out, p)
		}
	}
	return out
}
