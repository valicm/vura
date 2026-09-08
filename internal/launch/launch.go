// Package launch opens the dashboard as an app window.
package launch

import (
	"errors"
	"os/exec"
)

// Open starts a chromeless browser window on url, falling back to the
// default browser. It returns once the launcher process is detached.
func Open(url string) error {
	for _, b := range []string{"google-chrome", "google-chrome-stable", "chromium", "chromium-browser", "brave-browser", "microsoft-edge"} {
		path, err := exec.LookPath(b)
		if err != nil {
			continue
		}
		cmd := exec.Command(path, "--app="+url, "--window-size=1280,900", "--class=vura")
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
