//go:build linux

package audio

import (
	"context"
	"os/exec"
	"strings"
)

func (p *Poller) refreshSources(ctx context.Context) {
	out, err := exec.CommandContext(ctx, "pactl", "list", "sources", "short").Output()
	if err != nil {
		return
	}
	m := map[string]string{}
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 {
			m[f[0]] = f[1]
		}
	}
	p.sourceNames = m
}

// list parses `pactl list source-outputs`. The format is stable enough:
// "Source Output #N" headers, indented "Key: value" lines, and a Properties
// block of `key = "value"` lines.
func list(ctx context.Context) ([]Stream, error) {
	out, err := exec.CommandContext(ctx, "pactl", "list", "source-outputs").Output()
	if err != nil {
		return nil, err
	}
	return parse(string(out)), nil
}
