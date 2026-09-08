// Package useragent parses the User-Agent wakatime-cli sends, e.g.
//
//	wakatime/v2.26.0 (linux-7.1.13-100.fc43.x86_64-unknown) go1.26.6 goland/2026.2.2 goland-wakatime/16.1.2
//	wakatime/v2.26.0 (linux-...) go1.26.6 fable/5-1 claude-code/2.1.261      (AI activity: model, then editor)
//	wakatime/v2.26.0 (linux-...) go1.26.6 claude-code/2.1.263 claude-code-wakatime/4.1.0
package useragent

import "strings"

// Parse returns (editor, plugin). Editor is the program the heartbeat came
// from; plugin is the wakatime plugin, or for AI heartbeats the model name.
func Parse(ua string) (editor, plugin string) {
	// A browser user agent: name the browser, leave the plugin to the caller.
	if strings.HasPrefix(ua, "Mozilla/") {
		for _, b := range []string{"Firefox/", "Edg/", "Chrome/", "Safari/"} {
			if i := strings.Index(ua, b); i >= 0 {
				v := ua[i+len(b):]
				if j := strings.IndexAny(v, " )"); j >= 0 {
					v = v[:j]
				}
				return strings.ToLower(strings.TrimSuffix(b, "/")) + "/" + v, ""
			}
		}
		return "browser", ""
	}
	// Drop everything up to and including the "goX.Y" token; what follows is
	// one or two name/version tokens.
	fields := strings.Fields(ua)
	var rest []string
	for i, f := range fields {
		if strings.HasPrefix(f, "go") && !strings.Contains(f, "/") && i > 0 {
			rest = fields[i+1:]
			break
		}
	}
	if rest == nil {
		// No Go token: take trailing name/version tokens.
		for _, f := range fields {
			if strings.Contains(f, "/") && !strings.HasPrefix(f, "wakatime/") {
				rest = append(rest, f)
			}
		}
	}
	switch len(rest) {
	case 0:
		return "", ""
	case 1:
		return rest[0], ""
	}
	a, b := rest[len(rest)-2], rest[len(rest)-1]
	if strings.Contains(b, "-wakatime/") {
		return a, b // editor plugin
	}
	if strings.Contains(a, "-wakatime/") {
		return b, a
	}
	return b, a // model editor  -> editor=claude-code, plugin=model
}
