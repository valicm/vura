package useragent

import "testing"

func TestParse(t *testing.T) {
	cases := []struct{ ua, editor, plugin string }{
		{"wakatime/v2.26.0 (linux-7.1.13-100.fc43.x86_64-unknown) go1.26.6 goland/2026.2.2 goland-wakatime/16.1.2", "goland/2026.2.2", "goland-wakatime/16.1.2"},
		{"wakatime/v2.26.0 (linux-x) go1.26.6 fable/5-1 claude-code/2.1.261", "claude-code/2.1.261", "fable/5-1"},
		{"wakatime/v2.26.0 (linux-x) go1.26.6 claude-code/2.1.263 claude-code-wakatime/4.1.0", "claude-code/2.1.263", "claude-code-wakatime/4.1.0"},
		{"wakatime/v2.26.0 (linux-x) go1.26.6 datagrip/2026.2.5 datagrip-wakatime/16.1.2", "datagrip/2026.2.5", "datagrip-wakatime/16.1.2"},
		{"curl/8.0", "curl/8.0", ""},
		{"Mozilla/5.0 (X11; Linux x86_64; rv:155.0) Gecko/20100101 Firefox/155.0", "firefox/155.0", ""},
		{"Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/152.0.0.0 Safari/537.36", "chrome/152.0.0.0", ""},
		{"", "", ""},
	}
	for _, c := range cases {
		e, p := Parse(c.ua)
		if e != c.editor || p != c.plugin {
			t.Errorf("Parse(%q) = %q,%q want %q,%q", c.ua, e, p, c.editor, c.plugin)
		}
	}
}
