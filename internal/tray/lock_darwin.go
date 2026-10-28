//go:build darwin && cgo

package tray

import "runtime"

// AppKit only runs on the process's main thread. The main goroutine starts
// there; locking it in init keeps it there until Run takes over.
func init() { runtime.LockOSThread() }
