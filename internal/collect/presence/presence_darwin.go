//go:build darwin

package presence

import (
	"context"
	"fmt"
	"os/exec"
)

// macos reads IOKit's HIDIdleTime through ioreg and power assertions
// through pmset. Both ship with macOS; no cgo, no permissions prompt.
type macos struct{}

func newSource() (source, error) { return macos{}, nil }

func (macos) idle(ctx context.Context) (int64, error) {
	out, err := exec.CommandContext(ctx, "ioreg", "-c", "IOHIDSystem", "-d", "4").Output()
	if err != nil {
		return 0, fmt.Errorf("ioreg: %w", err)
	}
	return parseIOReg(string(out))
}

func (macos) inhibitors(ctx context.Context) (int, []string, error) {
	out, err := exec.CommandContext(ctx, "pmset", "-g", "assertions").Output()
	if err != nil {
		return 0, nil, fmt.Errorf("pmset: %w", err)
	}
	flags, apps := parsePmset(string(out))
	return flags, apps, nil
}
