//go:build darwin && !cgo

package audio

import (
	"context"
	"errors"
)

func (p *Poller) refreshSources(context.Context) {}

// Mic detection on macOS goes through CoreAudio, which needs cgo. Build with
// CGO_ENABLED=1 (Xcode command line tools) or set audio = false.
func list(context.Context) ([]Stream, error) {
	return nil, errors.New("built without cgo: mic detection needs CoreAudio; set audio = false or rebuild with CGO_ENABLED=1")
}
