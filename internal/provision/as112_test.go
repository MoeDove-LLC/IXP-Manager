package provision

import (
	"strings"
	"testing"

	"github.com/moedove/ixp-manager/internal/as112"
)

func TestAS112ProvisioningUsesOnlyBoundServiceAndIdentity(t *testing.T) {
	identity := as112.Identity{ServerName: "node.example.test", ResponsibleMailbox: "hostmaster.example.test", Facility: "AS112 fixture", Location: "Isolated test", UniqueIP: "192.0.2.112"}
	service := as112.Service{Mode: as112.DirectDelegation, IPv4: true}
	input := Input{Options: Options{Kind: "as112-dns", RouterID: "router", AS112: &identity}, AS112Service: &service, AS112RouterCreatedAt: "2026-01-01T00:00:00Z"}
	artifact, err := Render(input)
	if err != nil {
		t.Fatal(err)
	}
	if err = ValidateArtifact(artifact); err != nil {
		t.Fatal(err)
	}
	b, err := as112.Decode(artifact.Content)
	if err != nil || len(b.Families) != 1 || b.Families[0] != 4 || b.RouterCreatedAt != input.AS112RouterCreatedAt || artifact.TargetCount != 3 || !artifact.Complete || artifact.ValidationLevel != "native" {
		t.Fatal("AS112 service projection mismatch", err)
	}
	for _, change := range []func(*Input){func(i *Input) { i.Options.Families = []int{6} }, func(i *Input) { i.Options.RouterID = "" }, func(i *Input) { i.AS112Service = nil }, func(i *Input) { i.Options.VLANID = "another-vlan" }, func(i *Input) { i.Targets = []Target{{ID: "unrelated"}} }, func(i *Input) { i.AS112RouterCreatedAt = "invalid" }} {
		bad := input
		change(&bad)
		if _, err = Render(bad); err == nil {
			t.Fatal("ambiguous/unbound AS112 profile input accepted")
		}
	}
	bad := artifact
	bad.Content = strings.Replace(bad.Content, "\"executed\": false", "\"executed\": true", 1)
	if ValidateArtifact(bad) == nil {
		t.Fatal("planned health promoted into claimed execution")
	}
}
