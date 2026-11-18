package sflow

import (
	"strings"
	"testing"
	"time"
)

func TestReceiverPlanRequiresUnambiguousCurrentScope(t *testing.T) {
	r := Receiver{ID: "receiver", MemberID: "member", ConnectionIDs: []string{"connection"}, VLANIDs: []string{"vlan"}, Address: "192.0.2.10", Port: 6343, Enabled: true, Revision: strings.Repeat("a", 22)}
	binding := FilterBinding{ExporterID: "exporter", SwitchID: "switch", InfrastructureID: "fabric", VLANID: "vlan", VLANTag: 100, AgentAddress: "192.0.2.1", SubAgentID: 9, IfIndex: 7, MAC: "00:01:02:03:04:05", Direction: "source", ConnectionID: "connection", MemberID: "member", MappingRevision: strings.Repeat("b", 22)}
	plan, err := BuildReceiverPlan(r, []FilterBinding{binding}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if plan.ForwardingEnabled || plan.NativeValidated || !plan.RequiresCurrentOwnershipCheck {
		t.Fatal("plan fabricated live permission")
	}
	text, err := RenderPretag(plan)
	if err != nil || !strings.Contains(text, "ip=192.0.2.1 agent_id=9 vlan=100 in=7 src_mac=00:01:02:03:04:05") {
		t.Fatal("pretag omitted precise tuple", text, err)
	}
	other := binding
	other.InfrastructureID = "other-fabric"
	other.ExporterID = "other-exporter"
	if _, err = BuildReceiverPlan(r, []FilterBinding{binding, other}, time.Now()); err == nil {
		t.Fatal("ambiguous same claimed agent tuple fell back to first match")
	}
	other = binding
	other.MemberID = "previous-member"
	if _, err = BuildReceiverPlan(r, []FilterBinding{other}, time.Now()); err == nil {
		t.Fatal("receiver crossed member scope")
	}
	other = binding
	other.MAC = "00:01:02:03:04:05\nset_tag=999"
	if _, err = BuildReceiverPlan(r, []FilterBinding{other}, time.Now()); err == nil {
		t.Fatal("pretag injection accepted")
	}
	plan.ForwardingEnabled = true
	if _, err = RenderPretag(plan); err == nil {
		t.Fatal("render accepted unsupported forwarding grant")
	}
}
