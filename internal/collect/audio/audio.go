// Package audio detects calls: an application holding an open PipeWire
// capture stream (a "source-output" in pactl terms) is recording the mic.
// Headphones plugged in tells you nothing; a capture stream tells you someone
// is talking. Each stream becomes an interval row in the audio table.
package audio

import (
	"bufio"
	"context"
	"log/slog"
	"os/exec"
	"strings"
	"time"

	"github.com/valicm/vura/internal/store"
)

type Poller struct {
	store  *store.Store
	device string
	log    *slog.Logger
	open   map[string]int64 // pactl stream index -> audio.id
	// sourceNames maps source index -> node name so monitor streams are labelled.
	sourceNames map[string]string
}

func New(st *store.Store, device string, log *slog.Logger) *Poller {
	return &Poller{store: st, device: device, log: log}
}

func (p *Poller) Run(ctx context.Context, every time.Duration) {
	// Re-adopt rows left open by a previous daemon instance, then close any
	// that no longer correspond to a live stream on the first poll.
	if open, err := p.store.OpenAudioStreams(ctx); err == nil {
		p.open = open
	} else {
		p.log.Warn("audio: open streams", "err", err)
		p.open = map[string]int64{}
	}
	t := time.NewTicker(every)
	defer t.Stop()
	p.poll(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			p.poll(ctx)
		}
	}
}

// Stream is one capture stream as reported by `pactl list source-outputs`.
type Stream struct {
	Index, App, Binary, Media, Source string
}

func (p *Poller) poll(ctx context.Context) {
	now := time.Now()
	streams, err := list(ctx)
	if err != nil {
		p.log.Warn("audio: pactl", "err", err)
		return
	}
	p.refreshSources(ctx)
	_ = p.store.SetState(ctx, "collector.ok.audio", now.Format(time.RFC3339))
	seen := map[string]bool{}
	for _, st := range streams {
		seen[st.Index] = true
		if id, ok := p.open[st.Index]; ok {
			if err := p.store.TouchAudio(ctx, id, now); err != nil {
				p.log.Error("audio: touch", "err", err)
			}
			continue
		}
		src := st.Source
		if n, ok := p.sourceNames[st.Source]; ok {
			src = n
		}
		id, err := p.store.StartAudio(ctx, now, store.AudioStream{
			Stream: st.Index, App: st.App, Binary: st.Binary, Media: st.Media, Source: src, Device: p.device,
		})
		if err != nil {
			p.log.Error("audio: start", "err", err)
			continue
		}
		p.open[st.Index] = id
		p.log.Info("audio: capture started", "app", st.App, "binary", st.Binary, "source", src)
	}
	for idx, id := range p.open {
		if seen[idx] {
			continue
		}
		if err := p.store.EndAudio(ctx, id); err != nil {
			p.log.Error("audio: end", "err", err)
			continue
		}
		delete(p.open, idx)
		p.log.Info("audio: capture ended", "id", id)
	}
}

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

func parse(text string) []Stream {
	var streams []Stream
	var cur *Stream
	sc := bufio.NewScanner(strings.NewReader(text))
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "Source Output #") {
			streams = append(streams, Stream{Index: strings.TrimPrefix(line, "Source Output #")})
			cur = &streams[len(streams)-1]
			continue
		}
		if cur == nil {
			continue
		}
		t := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(t, "Source: "):
			cur.Source = strings.TrimPrefix(t, "Source: ")
		case strings.HasPrefix(t, "application.name = "):
			cur.App = unquote(strings.TrimPrefix(t, "application.name = "))
		case strings.HasPrefix(t, "application.process.binary = "):
			cur.Binary = unquote(strings.TrimPrefix(t, "application.process.binary = "))
		case strings.HasPrefix(t, "media.name = "):
			cur.Media = unquote(strings.TrimPrefix(t, "media.name = "))
		}
	}
	return streams
}

func unquote(s string) string {
	return strings.Trim(s, `"`)
}
