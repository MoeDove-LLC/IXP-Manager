package sflow

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestFetchBindingUsesServerIntervalRatherThanLocalWallClock(t *testing.T) {
	e := configFixture().Exporters[0]
	for _, skew := range []time.Duration{-48 * time.Hour, 48 * time.Hour} {
		issued := time.Now().UTC().Add(skew)
		binding := Binding{ExporterID: e.ID, SwitchID: e.SwitchID, MappingRevision: strings.Repeat("a", 22), IssuedAt: issued, ExpiresAt: issued.Add(time.Minute)}
		client := &http.Client{Transport: fakeTransport(func(r *http.Request) (*http.Response, error) {
			raw, _ := json.Marshal(binding)
			return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(string(raw))), Request: r}, nil
		})}
		if _, err := FetchBinding(context.Background(), client, "https://manager.example/api/sflow", "fixture-token", e); err != nil {
			t.Fatal("valid server lease rejected because local clock differed", err)
		}
		binding.IssuedAt = time.Time{}
		if _, err := FetchBinding(context.Background(), client, "https://manager.example/api/sflow", "fixture-token", e); err == nil {
			t.Fatal("server issue time missing")
		}
	}
	if NewHTTPClient().Timeout != 15*time.Second {
		t.Fatal("control-plane timeout changed from15 seconds")
	}
}
