package audio

import (
	"path/filepath"
	"strings"
)

// appName turns a macOS process path into the app's name. Browsers capture
// from a helper process ("Google Chrome Helper (Renderer)"); the call
// belongs to the browser.
//
//	/Applications/Google Chrome.app/Contents/Frameworks/.../Google Chrome Helper.app/Contents/MacOS/Google Chrome Helper
func appName(comm, bundle string) string {
	if i := strings.Index(comm, ".app/"); i >= 0 {
		comm = comm[:i]
	}
	name := filepath.Base(comm)
	if n, _, ok := strings.Cut(name, " Helper"); ok {
		name = n
	}
	if name == "" || name == "." || name == "/" {
		name = bundle
	}
	return name
}

// ignoredCapture: Apple daemons that keep input open without anyone being
// in a call (dictation and "Hey Siri" listening).
func ignoredCapture(bundle string) bool {
	for _, p := range []string{"com.apple.corespeech", "com.apple.siri", "com.apple.SpeechRecognitionCore", "com.apple.assistant"} {
		if strings.HasPrefix(bundle, p) {
			return true
		}
	}
	return false
}
