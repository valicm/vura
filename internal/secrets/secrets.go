// Package secrets fetches tokens from the environment or the OS keyring:
// the GNOME keyring (secret-tool) on Linux, the login Keychain on macOS.
// Tokens never live in config.toml.
//
// Store one with:
//
//	secret-tool store --label='vura tempo token' service vura key tempo   # Linux
//	security add-generic-password -s vura -a tempo -w                     # macOS (prompts)
package secrets

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// Get returns the secret for name ("tempo", "jira"). Order: VURA_<NAME>_TOKEN
// env var, then the keyring.
func Get(name string) (string, error) {
	env := "VURA_" + strings.ToUpper(name) + "_TOKEN"
	if v := os.Getenv(env); v != "" {
		return v, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := lookup(ctx, name).Output()
	if err != nil {
		return "", fmt.Errorf("no %s token: set %s or run: %s", name, env, StoreHint(name))
	}
	v := strings.TrimSpace(string(out))
	if v == "" {
		return "", fmt.Errorf("no %s token in keyring: %s", name, StoreHint(name))
	}
	return v, nil
}

func lookup(ctx context.Context, name string) *exec.Cmd {
	if runtime.GOOS == "darwin" {
		return exec.CommandContext(ctx, "security", "find-generic-password", "-s", "vura", "-a", name, "-w")
	}
	return exec.CommandContext(ctx, "secret-tool", "lookup", "service", "vura", "key", name)
}

// StoreHint is the command that saves a token for name on this platform.
func StoreHint(name string) string {
	if runtime.GOOS == "darwin" {
		return fmt.Sprintf("security add-generic-password -s vura -a %s -w", name)
	}
	return fmt.Sprintf("secret-tool store --label='vura %s' service vura key %s", name, name)
}
