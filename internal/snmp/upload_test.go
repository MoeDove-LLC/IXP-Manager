package snmp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestUploadRedirectionDoesNotLeakToken(t *testing.T) {
	received := false
	other := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { received = true; w.WriteHeader(200) }))
	defer other.Close()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL, http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	snapshot, err := CollectWithTransport(context.Background(), configFixture(), mibFixture())
	if err != nil {
		t.Fatal(err)
	}
	if err = Upload(context.Background(), server.Client(), server.URL+"/api/devices", "secret-device-token", "discoveries", snapshot); err == nil || received || strings.Contains(err.Error(), "secret-device-token") {
		t.Fatal("redirect policy leaked token")
	}
}
func TestTypedFailureContainsNoCredentialText(t *testing.T) {
	failure := Failure(configFixture(), context.DeadlineExceeded)
	if err := ValidateSnapshot(failure, time.Now()); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(failure)
	if strings.Contains(string(data), "private-") {
		t.Fatal("private config serialized in failure")
	}
	if len(failure.Interfaces) != 0 {
		t.Fatal("failure fabricated interface samples")
	}
}

func TestBindingsFetchNeverAdoptsRemappedIndex(t *testing.T) {
	index := uint32(7)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/api/devices/bindings" || r.URL.Query().Get("switchId") != "switch-one" {
			t.Error("unexpected bindings request")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"switchId": "switch-one", "mappings": []PortMapping{{PortID: "port-one", IfIndex: index, OwnershipRevision: strings.Repeat("a", 64)}}})
	}))
	defer server.Close()
	mappings, err := FetchBindings(context.Background(), server.Client(), server.URL+"/api/devices", "private-token", "switch-one", []PortMapping{{PortID: "port-one", IfIndex: 7}})
	if err != nil || mappings[0].OwnershipRevision == "" {
		t.Fatal(err)
	}
	index = 8
	if _, err = FetchBindings(context.Background(), server.Client(), server.URL+"/api/devices", "private-token", "switch-one", []PortMapping{{PortID: "port-one", IfIndex: 7}}); err == nil {
		t.Fatal("collector automatically adopted an unreviewed local remap")
	}
}
