# vura browser heartbeats

A 60-line Chrome extension that tells `vurad` which domain the focused tab is on,
once a minute. Domain only; no path, title or content. It never talks to anything
but `127.0.0.1:4242`.

Install (once):

1. `chrome://extensions` → enable **Developer mode** → **Load unpacked** → pick this `browser/` directory.
2. Remove the WakaTime store extension if installed; its options page cannot save a
   custom API URL, so it keeps posting to wakatime.com.

If `wakatime_key` is set in vura's config, open the extension's options
(puzzle icon → vura → Options) and paste the same key that is in `~/.wakatime.cfg`.
The options page also shows the last heartbeat's HTTP status, which is the
quickest way to see whether the daemon is receiving.

Map domains to buckets in `~/.config/vura/config.toml`:

    [buckets.ACME]
    domains = ["acme.atlassian.net", "github.com/acme"]

Unmapped domains are not evidence of client work and are ignored; `vura domains`
lists the busiest unmapped ones so you can decide.
