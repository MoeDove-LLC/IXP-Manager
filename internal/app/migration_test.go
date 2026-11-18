package app

import (
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// This crosses the CLI/API boundary so schema drift cannot silently turn a
// successful conversion into an unusable import file.
func TestLegacyConverterOutputAcceptedAtomically(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, filepath.Join(runtime.GOROOT(), "bin", "go"), "run", "./cmd/ixp-migrate", "-input", "tools/migrate/example-legacy.json")
	cmd.Dir = "../.."
	output, err := cmd.Output()
	if err != nil {
		if e, ok := err.(*exec.ExitError); ok {
			t.Fatalf("converter failed: %v: %s", err, e.Stderr)
		}
		t.Fatal(err)
	}
	var payload Record
	if err := json.Unmarshal(output, &payload); err != nil {
		t.Fatal(err)
	}
	c, _ := setupClient(t)
	result := c.ok("POST", "/api/import", payload)
	if result["imported"] != float64(8) {
		t.Fatalf("expected all eight related records, got %v", result)
	}
	state := c.a.store.Snapshot()
	connection := find(&state, "connections", "legacy-connections-1")
	if str(connection, "ipv4") != "192.0.2.10" || str(connection, "ipv6") != "2001:db8::10" {
		t.Fatal("dual-stack mapping lost")
	}
	if w := c.request("POST", "/api/import", payload); w.Code != 400 {
		t.Fatal("repeat conversion import did not detect conflicts")
	}
	if len(c.a.store.Snapshot().Resources["connections"]) != 1 {
		t.Fatal("rejected repeat import changed data")
	}
}

func TestExtendedLegacyImportPreservesPrivateBusinessData(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, filepath.Join(runtime.GOROOT(), "bin", "go"), "run", "./cmd/ixp-migrate", "-strict", "-input", "tools/migrate/example-legacy-business.json")
	cmd.Dir = "../.."
	output, err := cmd.Output()
	if err != nil {
		if e, ok := err.(*exec.ExitError); ok {
			t.Fatalf("%v: %s", err, e.Stderr)
		}
		t.Fatal(err)
	}
	var payload Record
	if err = json.Unmarshal(output, &payload); err != nil {
		t.Fatal(err)
	}
	c, _ := setupClient(t)
	result := c.ok("POST", "/api/import", payload)
	if result["imported"] != float64(20) {
		t.Fatal(result)
	}
	member := c.ok("GET", "/api/resources/members/legacy-members-1", nil)
	if member["registeredName"] != "示例网络有限公司" || member["vatRate"] != 23.5 {
		t.Fatal("company details lost")
	}
	c.create("users", Record{"name": "Member", "email": "member@example.net", "role": "member", "memberId": "legacy-members-1", "password": "member-long-password"})
	viewer := docLoginClient(t, c.a, "member@example.net", "member-long-password")
	if len(viewer.ok("GET", "/api/resources/notes", nil)["items"].([]any)) != 1 {
		t.Fatal("internal note disclosed")
	}
	if len(viewer.ok("GET", "/api/resources/member-tags", nil)["items"].([]any)) != 1 {
		t.Fatal("internal tag assignment disclosed")
	}
	if w := viewer.request("GET", "/api/resources/tags/legacy-tags-1", nil); w.Code != 404 {
		t.Fatal("internal tag disclosed")
	}
}

func TestLegacyMultiFabricImportAndIXF(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, filepath.Join(runtime.GOROOT(), "bin", "go"), "run", "./cmd/ixp-migrate", "-strict", "-input", "tools/migrate/example-legacy-fabrics.json")
	cmd.Dir = "../.."
	output, err := cmd.Output()
	if err != nil {
		if e, ok := err.(*exec.ExitError); ok {
			t.Fatalf("%v: %s", err, e.Stderr)
		}
		t.Fatal(err)
	}
	var payload Record
	if err = json.Unmarshal(output, &payload); err != nil {
		t.Fatal(err)
	}
	c, _ := setupClient(t)
	c.ok("POST", "/api/import", payload)
	s := c.a.store.Snapshot()
	doc, err := exportIXF(&s)
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.IXPList) != 2 || len(doc.MemberList) != 1 || len(doc.MemberList[0].Connections) != 2 {
		t.Fatal("separate VLAN100 fabrics flattened")
	}
	for _, exchange := range doc.IXPList {
		if exchange.IXFID == 101 && exchange.VLANs[0].IPv4 != nil {
			t.Fatal("multiple subnet metadata became fake aggregate")
		}
	}
	c.ok("PUT", "/api/settings", Record{"name": "Multi fabric", "asn": 64500, "publishIxf": true})
}
