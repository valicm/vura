// Package secrets fetches tokens from the environment or the GNOME keyring.
// Tokens never live in config.toml.
//
// Store one with:
//
//	secret-tool store --label='vura tempo token' service vura key tempo
package secrets

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Get returns the secret for name ("tempo", "jira"). Order: VURA_<NAME>_TOKEN
// env var, then `secret-tool lookup service vura key <name>`.
func Get(name string) (string, error) {
	env := "VURA_" + strings.ToUpper(name) + "_TOKEN"
	if v := os.Getenv(env); v != "" {
		return v, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "secret-tool", "lookup", "service", "vura", "key", name).Output()
	if err != nil {
		return "", fmt.Errorf("no %s token: set %s or run: secret-tool store --label='vura %s' service vura key %s", name, env, name, name)
	}
	v := strings.TrimSpace(string(out))
	if v == "" {
		return "", fmt.Errorf("no %s token in keyring: secret-tool store --label='vura %s' service vura key %s", name, name, name)
	}
	return v, nil
}
