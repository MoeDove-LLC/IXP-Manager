package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/moedove/ixp-manager/internal/core"
	"github.com/moedove/ixp-manager/internal/provision"
)

type coreFetchTransport func(*http.Request) (*http.Response, error)

func (f coreFetchTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestCoreFetchBindsPhysicalSwitchAndChecksTypedJSON(t *testing.T) {
	group := uint32(1)
	b := core.Bundle{ID: "bundle", Name: "Core plan", GraphTitle: "Core plan", Type: core.L2LAG, Enabled: true, SideA: core.Side{SwitchID: "switch-a", InterfaceName: "PortChannel1", ChannelGroup: &group, MTU: 9000, LAGFraming: true}, SideB: core.Side{SwitchID: "switch-b", InterfaceName: "PortChannel1", ChannelGroup: &group, MTU: 9000, LAGFraming: true}, Links: []core.Link{{ID: "link", Enabled: true, SideA: core.Endpoint{ID: "endpoint-a", PortID: "port-a", Duplex: "full"}, SideB: core.Endpoint{ID: "endpoint-b", PortID: "port-b", Duplex: "full"}}}}
	plan, err := core.BuildSwitchPlan("switch-a", []core.Bundle{b}, core.PlanInventory{Switches: []core.SwitchInventory{{ID: "switch-a", Name: "A", Active: true}, {ID: "switch-b", Name: "B", Active: true}}, Ports: []core.PortInventory{{ID: "port-a", SwitchID: "switch-a", Name: "Ethernet1"}, {ID: "port-b", SwitchID: "switch-b", Name: "Ethernet2"}}})
	if err != nil {
		t.Fatal(err)
	}
	a, err := provision.Render(provision.Input{Options: provision.Options{Kind: "switch-core", SwitchID: "switch-a"}, CoreConfig: &plan})
	if err != nil {
		t.Fatal(err)
	}
	if err := provision.ValidateArtifact(a); err != nil {
		t.Fatal(err)
	}
	headerID, content := "switch-a", a.Content
	transport := coreFetchTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Scheme != "https" || r.URL.Path != "/api/provisioning/revision/config" || r.Header.Get("Authorization") != "Bearer "+testToken {
			t.Fatal("invalid trusted fetch request")
		}
		h := http.Header{}
		h.Set("X-Provisioning-Revision", "revision")
		h.Set("X-Provisioning-Profile-ID", "profile")
		h.Set("X-Provisioning-Kind", "switch-core")
		h.Set("X-Provisioning-Switch-ID", headerID)
		h.Set("X-Config-SHA256", checksum(content))
		return &http.Response{StatusCode: 200, Header: h, Body: io.NopCloser(strings.NewReader(content)), Request: r}, nil
	})
	options := fetchOptions{Server: "https://control.example.test", Profile: "profile", Revision: "revision", Kind: "switch-core", SwitchID: "switch-a", Output: filepath.Join(t.TempDir(), "core.json")}
	if _, err := fetch(context.Background(), transport, options, testToken); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(options.Output)
	if err != nil || st.Mode().Perm() != 0600 {
		t.Fatal("core plan output not private")
	}
	options.Output = filepath.Join(t.TempDir(), "wrong.json")
	options.SwitchID = "switch-b"
	if _, err := fetch(context.Background(), transport, options, testToken); err == nil {
		t.Fatal("plan for another switch accepted")
	}
	if _, err := os.Stat(options.Output); !os.IsNotExist(err) {
		t.Fatal("mismatched target wrote output")
	}
	options.SwitchID = "switch-a"
	headerID = "switch-b"
	if _, err := fetch(context.Background(), transport, options, testToken); err == nil {
		t.Fatal("mismatched switch header accepted")
	}
	headerID = "switch-a"
	plan.NativeValidated = true
	raw, _ := json.Marshal(plan)
	content = string(raw)
	if _, err := fetch(context.Background(), transport, options, testToken); err == nil {
		t.Fatal("forged native-validation claim accepted")
	}
}
