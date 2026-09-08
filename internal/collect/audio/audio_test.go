package audio

import "testing"

const sample = `Source Output #57
	Driver: PipeWire
	Owner Module: n/a
	Client: 108
	Source: 45
	Sample Specification: float32le 1ch 48000Hz
	Properties:
		application.name = "Google Chrome"
		application.process.binary = "chrome"
		media.name = "Chromium input"
		media.class = "Stream/Input/Audio"

Source Output #58
	Driver: PipeWire
	Source: 46
	Properties:
		application.name = "pavucontrol"
		media.name = "Peak detect"
`

func TestParse(t *testing.T) {
	got := parse(sample)
	if len(got) != 2 {
		t.Fatalf("got %d streams", len(got))
	}
	if got[0].Index != "57" || got[0].App != "Google Chrome" || got[0].Binary != "chrome" ||
		got[0].Media != "Chromium input" || got[0].Source != "45" {
		t.Errorf("stream 0 = %+v", got[0])
	}
	if got[1].Index != "58" || got[1].App != "pavucontrol" || got[1].Binary != "" {
		t.Errorf("stream 1 = %+v", got[1])
	}
	if len(parse("")) != 0 {
		t.Error("empty input should yield no streams")
	}
}
