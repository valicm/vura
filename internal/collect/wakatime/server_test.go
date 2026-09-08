package wakatime

import (
	"encoding/base64"
	"encoding/json"
	"net/http/httptest"
	"testing"
)

func TestAuthorized(t *testing.T) {
	s := &Server{key: "k-1"}
	cases := []struct {
		url, header string
		want        bool
	}{
		{"/x", "", false},
		{"/x?api_key=k-1", "", true},
		{"/x?api_key=nope", "", false},
		{"/x", "Basic " + base64.StdEncoding.EncodeToString([]byte("k-1")), true},
		{"/x", "Basic " + base64.StdEncoding.EncodeToString([]byte("k-1:")), true},
		{"/x", "Basic " + base64.StdEncoding.EncodeToString([]byte("k-2")), false},
		{"/x", "Bearer k-1", false},
	}
	for _, c := range cases {
		r := httptest.NewRequest("POST", c.url, nil)
		if c.header != "" {
			r.Header.Set("Authorization", c.header)
		}
		if got := s.authorized(r); got != c.want {
			t.Errorf("%s %q: got %v", c.url, c.header, got)
		}
	}
	if !(&Server{}).authorized(httptest.NewRequest("POST", "/x", nil)) {
		t.Error("no key configured must accept")
	}
}

func TestBulkAcceptsStringTime(t *testing.T) {
	var hb heartbeat
	if err := json.Unmarshal([]byte(`{"entity":"github.com","type":"domain","time":"1788858000.25","plugin":"firefox-wakatime/4.1.0"}`), &hb); err != nil {
		t.Fatal(err)
	}
	if float64(hb.Time) != 1788858000.25 || hb.Plugin != "firefox-wakatime/4.1.0" {
		t.Errorf("%+v", hb)
	}
	if err := json.Unmarshal([]byte(`{"entity":"/a.go","time":1788858000.5}`), &hb); err != nil || float64(hb.Time) != 1788858000.5 {
		t.Errorf("numeric time: %v %+v", err, hb)
	}
}
