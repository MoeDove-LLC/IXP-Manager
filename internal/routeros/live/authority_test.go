package live

import (
	"context"
	"strings"
	"testing"

	"github.com/moedove/ixp-manager/internal/routeros"
	"github.com/moedove/ixp-manager/internal/routeros/api"
	"github.com/moedove/ixp-manager/internal/routeros/control"
)

func recoveryAuthorityFixture(t *testing.T) (*Runtime, *fakeAPI, control.ReadRequest) {
	t.Helper()
	r, a, _, request := observerFixture()
	request.Strict = false
	owner := routeros.OwnerName(r.target.RouterID)
	name, e := routeros.ManagedConnectionName(r.target.RouterID, "peer-a")
	if e != nil {
		t.Fatal(e)
	}
	request.KnownState.Peers = []routeros.KnownPeer{{ID: "peer-a"}}
	row := api.Record{".id": "*7", "name": name}
	for _, property := range observedFields[api.BGPConnection] {
		row[property] = ""
	}
	for _, property := range []string{"disabled", "dynamic", "connect", "listen", "multihop", "output.remove-private-as", "output.as-override"} {
		row[property] = "false"
	}
	row["disabled"] = "true"
	row["connect"] = "true"
	row["listen"] = "true"
	row["instance"] = owner
	row["routing-table"] = owner
	row["local.role"] = "ebgp-rs"
	row["afi"] = "ip"
	row["local.address"] = "198.51.100.1"
	row["remote.address"] = "198.51.100.2"
	row["remote.as"] = "64501"
	row["input.limit-process-routes-ipv4"] = "10"
	row["nexthop-choice"] = "propagate"
	row["output.default-originate"] = "never"
	row["output.default-prepend"] = "0"
	a.inv.Connections = []api.Record{row}
	checksum, e := control.CredentialAttributesChecksum(row)
	if e != nil {
		t.Fatal(e)
	}
	request.CredentialAuthority = []control.CredentialAuthority{{ID: "*7", Name: name, AttributesChecksum: checksum}}
	return r, a, request
}
func TestJournalAuthorityMatchesExactObjectAndIsOneReadOnlyScope(t *testing.T) {
	r, a, request := recoveryAuthorityFixture(t)
	defer r.Close()
	observation, e := r.Observe(context.Background(), request)
	if e != nil {
		t.Fatal(e)
	}
	if a.credentialCalls != 2 || len(observation.Credentials) != 1 || observation.Credentials["*7"].Known || len(r.credentialIDs) != 0 || a.writes != 0 {
		t.Fatal("exact authority was not bounded to actual unknown getter evidence")
	}
	request.CredentialAuthority = nil
	observation, e = r.Observe(context.Background(), request)
	if e != nil {
		t.Fatal(e)
	}
	if a.credentialCalls != 2 || len(observation.Credentials) != 0 {
		t.Fatal("journal authority was cached as name-only authority")
	}
}
func TestJournalAuthorityRejectsForgedOrStaleObjectsBeforeSecretRead(t *testing.T) {
	cases := []string{"checksum", "id", "name", "unlisted peer", "duplicate id", "missing object", "changed address", "changed disabled", "wrong instance", "wrong table", "wrong role", "invalid checksum"}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			r, a, request := recoveryAuthorityFixture(t)
			defer r.Close()
			switch name {
			case "checksum":
				request.CredentialAuthority[0].AttributesChecksum = strings.Repeat("a", 64)
			case "id":
				request.CredentialAuthority[0].ID = "*8"
			case "name":
				request.CredentialAuthority[0].Name = "foreign"
			case "unlisted peer":
				request.KnownState.Peers = nil
			case "duplicate id":
				request.CredentialAuthority = append(request.CredentialAuthority, request.CredentialAuthority[0])
			case "missing object":
				a.inv.Connections = nil
			case "changed address":
				a.inv.Connections[0]["remote.address"] = "198.51.100.99"
			case "changed disabled":
				a.inv.Connections[0]["disabled"] = "false"
			case "wrong instance":
				a.inv.Connections[0]["instance"] = "foreign"
			case "wrong table":
				a.inv.Connections[0]["routing-table"] = "main"
			case "wrong role":
				a.inv.Connections[0]["local.role"] = "ebgp"
			case "invalid checksum":
				request.CredentialAuthority[0].AttributesChecksum = "not-a-hash"
			}
			if name == "wrong instance" || name == "wrong table" || name == "wrong role" {
				sum, e := control.CredentialAttributesChecksum(a.inv.Connections[0])
				if e != nil {
					t.Fatal(e)
				}
				request.CredentialAuthority[0].AttributesChecksum = sum
			}
			if _, e := r.Observe(context.Background(), request); e == nil {
				t.Fatal("nonmatching authority accepted")
			}
			if a.credentialCalls != 0 || a.writes != 0 || len(r.credentialIDs) != 0 {
				t.Fatal("invalid journal authority caused sensitive read/cache or mutation")
			}
		})
	}
}
