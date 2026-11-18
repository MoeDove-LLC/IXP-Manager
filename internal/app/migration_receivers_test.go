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

func TestLegacyReceiverConverterImportRequiresNewPhysicalReview(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, filepath.Join(runtime.GOROOT(), "bin", "go"), "run", "./cmd/ixp-migrate", "-input", "tools/migrate/example-legacy-sflow-receivers.json")
	cmd.Dir = "../.."
	raw, err := cmd.Output()
	if err != nil {
		if e, ok := err.(*exec.ExitError); ok {
			t.Fatalf("converter: %v %s", err, e.Stderr)
		}
		t.Fatal(err)
	}
	var payload Record
	if err = json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	c, _ := setupClient(t)
	c.ok("POST", "/api/import", payload)
	s := c.a.store.Snapshot()
	if len(s.SFlowReceivers) != 2 || len(s.Resources["mac-addresses"]) != 3 {
		t.Fatal("receiver or configured MAC records lost")
	}
	for _, receiver := range s.SFlowReceivers {
		if receiver.MemberRevision != c.a.trafficMemberRevision(&s, "legacy-members-1") || receiver.ApprovedBy != "" || !receiver.ApprovedAt.IsZero() || receiver.ApprovedChecksum != "" || len(receiver.Spec.ConnectionIDs) != 2 || len(receiver.Spec.VLANIDs) != 2 {
			t.Fatal("legacy conversion transferred authority or lost scope", receiver)
		}
		path := "/api/sflow/receivers/" + receiver.Spec.ID
		if receiver.Spec.Enabled {
			if receiver.Status != "draft" {
				t.Fatal("imported enabled receiver bypassed approval")
			}
			if w := c.request("GET", path+"/plan", nil); w.Code != 409 {
				t.Fatal("unreviewed physical mappings emitted execution plan")
			}
		} else {
			if receiver.Status != "disabled" {
				t.Fatal("disabled desired state lost")
			}
			plan := c.ok("GET", path+"/plan", nil)
			if plan["forwardingEnabled"] != false || len(plan["bindings"].([]any)) != 0 {
				t.Fatal("disabled imported plan grants delivery")
			}
		}
	}
	for _, mac := range s.Resources["mac-addresses"] {
		if str(mac, "address") == "02:00:00:00:00:ff" {
			t.Fatal("legacy discovered MAC became authoritative registration")
		}
	}
	if w := c.request("POST", "/api/import", payload); w.Code != 400 {
		t.Fatal("repeat receiver import silently duplicated scope")
	}
	if len(c.a.store.Snapshot().SFlowReceivers) != 2 {
		t.Fatal("failed repeat import partially changed receiver state")
	}
}
