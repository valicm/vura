package audio

import "testing"

func TestAppName(t *testing.T) {
	for _, c := range []struct{ comm, bundle, want string }{
		{"/Applications/Google Chrome.app/Contents/Frameworks/Google Chrome Framework.framework/Versions/1/Helpers/Google Chrome Helper.app/Contents/MacOS/Google Chrome Helper", "com.google.Chrome.helper", "Google Chrome"},
		{"/Applications/zoom.us.app/Contents/MacOS/zoom.us", "us.zoom.xos", "zoom.us"},
		{"/usr/local/bin/sox", "", "sox"},
		{"", "com.microsoft.teams2", "com.microsoft.teams2"},
	} {
		if got := appName(c.comm, c.bundle); got != c.want {
			t.Errorf("appName(%q) = %q, want %q", c.comm, got, c.want)
		}
	}
	if !ignoredCapture("com.apple.corespeechd") || ignoredCapture("com.apple.FaceTime") {
		t.Error("ignoredCapture")
	}
}
