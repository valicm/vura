//go:build !linux && !darwin

package audio

import (
	"context"
	"errors"
	"runtime"
)

func (p *Poller) refreshSources(context.Context) {}

func list(context.Context) ([]Stream, error) {
	return nil, errors.New("mic detection not supported on " + runtime.GOOS + "; set audio = false")
}
