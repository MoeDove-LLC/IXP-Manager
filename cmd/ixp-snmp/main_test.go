package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	devicesnmp "github.com/moedove/ixp-manager/internal/snmp"
)

func cliConfig(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	config := devicesnmp.Config{Endpoint: "https://manager.example/api/devices", SwitchID: "switch-one", Target: "192.0.2.1", Version: "3", Username: "private-user", AuthPassphrase: "private-auth-password", PrivacyPassphrase: "private-encryption-password", Mappings: []devicesnmp.PortMapping{{PortID: "port-one", IfIndex: 7}}}
	data, _ := json.Marshal(config)
	path := filepath.Join(dir, "snmp.json")
	token := filepath.Join(dir, "token")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(token, []byte("private-api-token"), 0600); err != nil {
		t.Fatal(err)
	}
	return path, token
}
func TestCollectorBindsBeforePollingAndHidesCredentials(t *testing.T) {
	previousCollect, previousFetch, previousUpload := collectDevice, fetchBindings, uploadSnapshot
	t.Cleanup(func() {
		collectDevice = previousCollect
		fetchBindings = previousFetch
		uploadSnapshot = previousUpload
	})
	calls := []string{}
	revision := strings.Repeat("a", 64)
	fetchBindings = func(_ context.Context, _ *http.Client, _, _, _ string, mappings []devicesnmp.PortMapping) ([]devicesnmp.PortMapping, error) {
		calls = append(calls, "bindings")
		mappings = append([]devicesnmp.PortMapping(nil), mappings...)
		mappings[0].OwnershipRevision = revision
		return mappings, nil
	}
	collectDevice = func(_ context.Context, c devicesnmp.Config) (devicesnmp.Snapshot, error) {
		calls = append(calls, "poll")
		if c.Mappings[0].OwnershipRevision != revision {
			t.Fatal("polled before binding revision capture")
		}
		return devicesnmp.Snapshot{Version: 1, SwitchID: c.SwitchID, ObservedAt: time.Now(), Interfaces: []devicesnmp.Interface{{IfIndex: 7, PortID: "port-one", OwnershipRevision: revision}}}, nil
	}
	uploadSnapshot = func(_ context.Context, _ *http.Client, _, _, mode string, snapshot devicesnmp.Snapshot) error {
		calls = append(calls, "upload")
		if mode != "observations" || snapshot.Interfaces[0].OwnershipRevision != revision {
			t.Fatal("lineage lost on upload")
		}
		return nil
	}
	config, token := cliConfig(t)
	var out bytes.Buffer
	if err := run([]string{"observe", "--once", "--config", config, "--token-file", token}, bytes.NewReader(nil), &out); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(calls, []string{"bindings", "poll", "upload"}) {
		t.Fatal(calls)
	}
	for _, secret := range []string{"private-user", "private-auth-password", "private-encryption-password", "private-api-token"} {
		if strings.Contains(out.String(), secret) {
			t.Fatal("credential printed")
		}
	}
}
func TestCollectorFailureNeverWritesFakeSamples(t *testing.T) {
	previousCollect, previousFetch, previousUpload := collectDevice, fetchBindings, uploadSnapshot
	t.Cleanup(func() {
		collectDevice = previousCollect
		fetchBindings = previousFetch
		uploadSnapshot = previousUpload
	})
	fetchBindings = func(_ context.Context, _ *http.Client, _, _, _ string, mappings []devicesnmp.PortMapping) ([]devicesnmp.PortMapping, error) {
		return mappings, nil
	}
	collectDevice = func(context.Context, devicesnmp.Config) (devicesnmp.Snapshot, error) {
		return devicesnmp.Snapshot{}, errors.New("sensitive detail private-auth-password")
	}
	uploadSnapshot = func(_ context.Context, _ *http.Client, _, _, _ string, s devicesnmp.Snapshot) error {
		if len(s.Interfaces) != 0 || s.ErrorCode == "" {
			t.Fatal("failure fabricated interfaces")
		}
		data, _ := json.Marshal(s)
		if strings.Contains(string(data), "private-auth-password") {
			t.Fatal("failure upload leaked credential")
		}
		return nil
	}
	config, token := cliConfig(t)
	var out bytes.Buffer
	err := run([]string{"observe", "--once", "--config", config, "--token-file", token}, bytes.NewReader(nil), &out)
	if err == nil || strings.Contains(err.Error(), "private-auth-password") {
		t.Fatal("failure leaked secret or reported success")
	}
	err = run([]string{"observe", "--secret", "literal-secret"}, bytes.NewReader(nil), &out)
	if err == nil || strings.Contains(err.Error(), "literal-secret") || strings.Contains(out.String(), "literal-secret") {
		t.Fatal("invalid CLI flag echoed secret")
	}
}
