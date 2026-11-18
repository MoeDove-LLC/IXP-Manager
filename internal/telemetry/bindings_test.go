package telemetry

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestBindingFetchAndUploadAcknowledgement(t *testing.T) {
	revision := strings.Repeat("a", 64)
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Authorization") != "Bearer private-token" {
			t.Fatal("credential missing")
		}
		if r.Method != "GET" || r.URL.Path != "/api/monitoring/bindings" || r.URL.Query().Get("routerId") != "router" {
			t.Fatal("unexpected binding request")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(fmt.Sprintf(`{"routerId":"router","ports":[{"portId":"port","ownershipRevision":"%s"}],"sessions":[]}`, revision))), Header: http.Header{}, Request: r}, nil
	})}
	bindings, err := FetchBindings(context.Background(), client, "https://manager.example/api/monitoring/ingest", "private-token", "router")
	if err != nil {
		t.Fatal(err)
	}
	batch := Batch{RouterID: "router", Ports: []PortSample{{PortID: "port"}}}
	if err = StampBindings(&batch, bindings); err != nil || batch.Ports[0].OwnershipRevision != revision {
		t.Fatal("binding not stamped")
	}
	batch.RouterID = "other"
	if err = StampBindings(&batch, bindings); err == nil {
		t.Fatal("router binding mismatch accepted")
	}
	client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"ok":true,"skippedBeforeOwnership":1}`)), Request: r}, nil
	})
	if err = Upload(context.Background(), client, "https://manager.example/api/monitoring/ingest", "private-token", Batch{}); err == nil {
		t.Fatal("skipped observations reported uploaded")
	}
	client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 302, Header: http.Header{"Location": []string{"https://untrusted.example/"}}, Body: io.NopCloser(strings.NewReader("private-token")), Request: r}, nil
	})
	if _, err = FetchBindings(context.Background(), client, "https://manager.example/api/monitoring/ingest", "private-token", "router"); err == nil || strings.Contains(err.Error(), "private-token") {
		t.Fatal("redirect or credential echo allowed")
	}
}

func TestFetchQuaggaBindingsRequiresTheNativeIdentityTuple(t *testing.T) {
	host := "https://manager.example/api/monitoring/ingest"
	for _, test := range []struct {
		extra string
		valid bool
	}{
		{``, false},
		{`,"routerIdentity":{"platform":"bird2","routerId":"192.0.2.1","asn":64500}`, false},
		{`,"routerIdentity":{"platform":"quagga","routerId":"::1","asn":64500}`, false},
		{`,"routerIdentity":{"platform":"quagga","routerId":"192.0.2.1","asn":64500}`, true},
	} {
		client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if r.URL.Query().Get("routerId") != "router-q" || r.URL.Query().Get("routerIdentity") != "quagga" || r.Header.Get("Authorization") != "Bearer private-token" {
				t.Fatal("collector did not explicitly request the scoped native identity")
			}
			return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"routerId":"router-q","ports":[],"sessions":[]` + test.extra + `}`)), Request: r}, nil
		})}
		binding, err := FetchQuaggaBindings(context.Background(), client, host, "private-token", "router-q")
		if (err == nil) != test.valid || test.valid && binding.RouterIdentity.ASN != 64500 {
			t.Fatalf("unexpected result for %s: %v", test.extra, err)
		}
	}
}

func TestQuaggaCollectorRejectsUnboundOrAmbiguousSelectors(t *testing.T) {
	c := CollectorConfig{Endpoint: "https://manager.example/api/monitoring/ingest", RouterID: "router-q", Platform: "quagga",
		QuaggaSocket: "/not-present/bgpd.vty", Peers: map[string]string{"p1": "192.0.2.2"}}
	if _, err := Collect(context.Background(), c); err == nil || !strings.Contains(err.Error(), "requires a current router identity") {
		t.Fatal("collector reached local observation without bound identity", err)
	}
	c.Peers["p2"] = "192.0.2.2"
	if err := ValidateCollector(c); err == nil || !strings.Contains(err.Error(), "distinct neighbor") {
		t.Fatal("ambiguous Quagga mapping accepted", err)
	}
}
