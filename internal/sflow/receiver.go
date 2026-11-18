package sflow

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Receiver contains desired member delivery scope, never an execution grant.
// The control plane owns approval and must recheck current allocation for every
// delivery. A cached plan never authorizes the collector's per-packet forwarder.
type Receiver struct {
	ID            string   `json:"id"`
	MemberID      string   `json:"memberId"`
	ConnectionIDs []string `json:"connectionIds"`
	VLANIDs       []string `json:"vlanIds"`
	Address       string   `json:"address"`
	Port          uint16   `json:"port"`
	Enabled       bool     `json:"enabled"`
	Revision      string   `json:"revision"`
}
type FilterBinding struct {
	ExporterID       string `json:"exporterId"`
	SwitchID         string `json:"switchId"`
	InfrastructureID string `json:"infrastructureId,omitempty"`
	VLANID           string `json:"vlanId"`
	VLANTag          uint16 `json:"vlanTag"`
	AgentAddress     string `json:"agentAddress"`
	SubAgentID       uint32 `json:"subAgentId"`
	IfIndex          uint32 `json:"ifIndex"`
	MAC              string `json:"mac"`
	Direction        string `json:"direction"` // source or destination, not ambiguous in/out
	ConnectionID     string `json:"connectionId"`
	MemberID         string `json:"memberId"`
	MappingRevision  string `json:"mappingRevision"`
}
type ReceiverPlan struct {
	Version                       int             `json:"version"`
	Receiver                      Receiver        `json:"receiver"`
	Bindings                      []FilterBinding `json:"bindings"`
	GeneratedAt                   time.Time       `json:"generatedAt"`
	ExpiresAt                     time.Time       `json:"expiresAt"`
	ForwardingEnabled             bool            `json:"forwardingEnabled"`
	RequiresCurrentOwnershipCheck bool            `json:"requiresCurrentOwnershipCheck"`
	NativeValidated               bool            `json:"nativeValidated"`
}

func containsID(ids []string, id string) bool {
	for _, v := range ids {
		if v == id {
			return true
		}
	}
	return false
}
func ValidateReceiver(r Receiver) error {
	if !identifier.MatchString(r.ID) || !identifier.MatchString(r.MemberID) || !revisionPattern.MatchString(r.Revision) || !validIP(r.Address) || r.Port == 0 || len(r.ConnectionIDs) == 0 || len(r.ConnectionIDs) > 64 || len(r.VLANIDs) == 0 || len(r.VLANIDs) > 64 {
		return errors.New("invalid bounded receiver destination or scope")
	}
	for _, ids := range [][]string{r.ConnectionIDs, r.VLANIDs} {
		seen := map[string]bool{}
		for _, id := range ids {
			if !identifier.MatchString(id) || seen[id] {
				return errors.New("receiver scope requires distinct IDs")
			}
			seen[id] = true
		}
	}
	return nil
}
func BuildReceiverPlan(r Receiver, bindings []FilterBinding, now time.Time) (ReceiverPlan, error) {
	empty := ReceiverPlan{}
	if err := ValidateReceiver(r); err != nil {
		return empty, err
	}
	if len(bindings) == 0 || len(bindings) > 4096 || now.IsZero() {
		return empty, errors.New("receiver needs bounded current bindings")
	}
	seen := map[string]string{}
	for _, b := range bindings {
		for _, id := range []string{b.ExporterID, b.SwitchID, b.VLANID, b.ConnectionID, b.MemberID} {
			if !identifier.MatchString(id) {
				return empty, errors.New("invalid receiver binding identity")
			}
		}
		if b.InfrastructureID != "" && !identifier.MatchString(b.InfrastructureID) {
			return empty, errors.New("invalid receiver infrastructure")
		}
		if b.MemberID != r.MemberID || !containsID(r.ConnectionIDs, b.ConnectionID) || !containsID(r.VLANIDs, b.VLANID) || !validIP(b.AgentAddress) || !revisionPattern.MatchString(b.MappingRevision) || !validMAC(b.MAC) || b.VLANTag < 1 || b.VLANTag > 4094 || b.IfIndex == 0 || b.IfIndex >= 0x3fffffff || (b.Direction != "source" && b.Direction != "destination") {
			return empty, errors.New("receiver binding outside reviewed member/VLAN/port scope")
		}
		key := fmt.Sprintf("%s/%d/%d/%d/%s/%s", b.AgentAddress, b.SubAgentID, b.VLANTag, b.IfIndex, b.Direction, b.MAC)
		identity := b.ExporterID + "/" + b.SwitchID + "/" + b.InfrastructureID + "/" + b.VLANID + "/" + b.ConnectionID
		if old := seen[key]; old != "" {
			return empty, errors.New("ambiguous or duplicate exporter/VLAN/MAC receiver match")
		}
		seen[key] = identity
	}
	for _, id := range r.ConnectionIDs {
		found := false
		for _, b := range bindings {
			found = found || b.ConnectionID == id
		}
		if !found {
			return empty, errors.New("receiver connection has no current binding")
		}
	}
	plan := ReceiverPlan{Version: 1, Receiver: r, Bindings: append([]FilterBinding(nil), bindings...), GeneratedAt: now.UTC(), ExpiresAt: now.UTC().Add(time.Minute), RequiresCurrentOwnershipCheck: true}
	sort.Slice(plan.Bindings, func(i, j int) bool { return digest(plan.Bindings[i]) < digest(plan.Bindings[j]) })
	return plan, nil
}

// RenderPretag exports bounded sfacctd match rules. It is NOT a tenant delivery
// ACL: sfacctd VLAN matching uses in/out VLANs and its ip key is the claimed agent
// address, not authenticated UDP transport. Live delivery must use the complete
// typed binding and current control-plane approval; installing this map alone
// cannot grant forwarding authority.
func RenderPretag(p ReceiverPlan) (string, error) {
	if p.Version != 1 || p.ForwardingEnabled || p.NativeValidated || !p.RequiresCurrentOwnershipCheck || p.ExpiresAt.Sub(p.GeneratedAt) != time.Minute {
		return "", errors.New("invalid receiver plan claims")
	}
	checked, err := BuildReceiverPlan(p.Receiver, p.Bindings, p.GeneratedAt)
	if err != nil {
		return "", err
	}
	var out strings.Builder
	out.WriteString("! Generated advisory sfacctd pretag map; not a tenant delivery ACL.\n! Claimed agent IP/sub-agent plus VLAN and physical interface are explicit.\n! Revalidate ownership, expiry and approval in a separate delivery adapter.\n")
	for i, b := range checked.Bindings {
		iface, mac := "in", "src_mac"
		if b.Direction == "destination" {
			iface, mac = "out", "dst_mac"
		}
		fmt.Fprintf(&out, "! exporter=%s switch=%s infrastructure=%s vlan=%s connection=%s mapping=%s\n", b.ExporterID, b.SwitchID, b.InfrastructureID, b.VLANID, b.ConnectionID, b.MappingRevision)
		fmt.Fprintf(&out, "set_tag=%d ip=%s agent_id=%d vlan=%d %s=%d %s=%s\n", i+1, b.AgentAddress, b.SubAgentID, b.VLANTag, iface, b.IfIndex, mac, b.MAC)
	}
	return out.String(), nil
}
