package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPresenceFallsBackToGnome(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("VURA_DATA", dir)
	for _, c := range []struct {
		toml string
		want bool
	}{
		{"", true},
		{"[sources]\ngnome = false\n", false},
		{"[sources]\npresence = false\n", false},
		{"[sources]\ngnome = false\npresence = true\n", true},
	} {
		p := filepath.Join(dir, "config.toml")
		if err := os.WriteFile(p, []byte(c.toml), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg, err := Load(p)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Sources.Presence != c.want {
			t.Errorf("%q: presence = %v, want %v", c.toml, cfg.Sources.Presence, c.want)
		}
	}
}

func TestPathIsDotConfig(t *testing.T) {
	t.Setenv("VURA_CONFIG", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "/Users/someone")
	if got := Path(); got != "/Users/someone/.config/vura/config.toml" {
		t.Errorf("Path() = %q", got)
	}
}
