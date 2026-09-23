//go:build !linux && !darwin

package presence

import (
	"errors"
	"runtime"
)

func newSource() (source, error) {
	return nil, errors.New("presence: not supported on " + runtime.GOOS + "; set presence = false")
}
