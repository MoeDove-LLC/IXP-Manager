package app

import (
	"errors"
	"fmt"
	"math"
	"net/netip"
	"sort"
	"time"

	"github.com/moedove/ixp-manager/internal/sflow"
)

const sflowExporterKind = "sflow-exporters"

func stringList(v any) []string {
	switch items := v.(type) {
	case []string:
		return append([]string(nil), items...)
	case []any:
		out := []string{}
		for _, item := range items {
			value, ok := item.(string)
			if !ok {
				return nil
			}
			out = append(out, value)
		}
		return out
	}
	return nil
}

func init() {
	Schemas[sflowExporterKind] = Schema{"name": textField(true), "switchId": ref("switches", true), "sourceAddress": {Type: "ip", Required: true}, "agentAddress": {Type: "ip", Required: true}, "subAgentId": num(0, math.MaxUint32, false), "vlanIds": {Type: "references", Ref: "vlans", Required: true}, "enabled": {Type: "boolean"}}
}
func flowIP(value string) string {
	ip, err := netip.ParseAddr(value)
	if err != nil || ip.Zone() != "" || ip.Is4In6() || ip.IsUnspecified() || ip.IsMulticast() {
		return ""
	}
	return ip.String()
}
func validateSFlowInventory(s *State) error {
	if len(s.Resources[sflowExporterKind]) > 128 {
		return errors.New("sFlow exporter limit reached")
	}
	seen := map[string]bool{}
	for _, e := range s.Resources[sflowExporterKind] {
		sw := find(s, "switches", str(e, "switchId"))
		vlans := stringList(e["vlanIds"])
		if sw == nil || flowIP(str(e, "sourceAddress")) == "" || flowIP(str(e, "agentAddress")) == "" || len(vlans) < 1 || len(vlans) > 64 {
			return errors.New("sFlow exporter requires a switch, literal source/agent addresses and1..64 VLANs")
		}
		if value, exists := e["enabled"]; exists {
			if _, ok := value.(bool); !ok {
				return errors.New("sFlow enabled must be boolean")
			}
		}
		for _, id := range vlans {
			v := find(s, "vlans", id)
			if v == nil || fabricID(v) != fabricID(sw) {
				return errors.New("sFlow VLAN belongs to a different infrastructure")
			}
		}
		key := fmt.Sprintf("%s/%s/%d", flowIP(str(e, "sourceAddress")), flowIP(str(e, "agentAddress")), uint32(number(e, "subAgentId")))
		if seen[key] {
			return errors.New("duplicate sFlow transport/agent/sub-agent identity")
		}
		seen[key] = true
	}
	return nil
}

type flowConnection struct {
	ID, MemberID, MemberRevision, VLANID string
	Ports                                []string
	MACs                                 []string
}
type flowMapping struct {
	Exporter         Record
	Revision         string
	NotBefore        time.Time
	InfrastructureID string
	Ports            map[uint32][]Record
	VLANs            map[uint16]string
	Connections      []flowConnection
	ByIngress        map[string][]flowConnection
	ByMAC            map[string][]flowConnection
}

type cachedFlowMapping struct {
	Version uint64
	Mapping flowMapping
}

// Caller holds sflowMu. Store.Update publishes a new state instead of mutating
// the preceding snapshot, so cached read-only projections remain detached.
func (a *App) currentFlowMapping(user Record, session Session, id string) (flowMapping, int, error) {
	a.store.mu.RLock()
	defer a.store.mu.RUnlock()
	s := &a.store.state
	if a.store.closed {
		return flowMapping{}, 503, errors.New("store is closed")
	}
	_, scope, err := a.sflowActor(s, user, session, true)
	if err != nil {
		return flowMapping{}, 403, errors.New("collector access changed")
	}
	e := find(s, sflowExporterKind, id)
	if e == nil || !flowSwitchAllowed(scope, str(e, "switchId")) {
		return flowMapping{}, 404, errors.New("exporter not found in collector scope")
	}
	if cached, ok := a.sflowMappings[id]; ok && cached.Version == a.store.revision {
		return cached.Mapping, 200, nil
	}
	mapping, err := a.flowMapping(s, e)
	if err != nil {
		return mapping, 409, err
	}
	if a.sflowMappings == nil {
		a.sflowMappings = map[string]cachedFlowMapping{}
	}
	if len(a.sflowMappings) > 128 {
		a.sflowMappings = map[string]cachedFlowMapping{}
	}
	a.sflowMappings[id] = cachedFlowMapping{Version: a.store.revision, Mapping: mapping}
	return mapping, 200, nil
}

func (a *App) flowMapping(s *State, e Record) (flowMapping, error) {
	m := flowMapping{Exporter: e, InfrastructureID: fabricID(find(s, "switches", str(e, "switchId"))), Ports: map[uint32][]Record{}, VLANs: map[uint16]string{}, Connections: []flowConnection{}, ByIngress: map[string][]flowConnection{}, ByMAC: map[string][]flowConnection{}}
	if e == nil || e["enabled"] != true {
		return m, errors.New("sFlow exporter is disabled")
	}
	dependencies := []any{e}
	allowed := stringList(e["vlanIds"])
	for _, id := range allowed {
		v := find(s, "vlans", id)
		if v == nil || fabricID(v) != m.InfrastructureID {
			return m, errors.New("sFlow VLAN scope changed")
		}
		m.VLANs[uint16(number(v, "tag"))] = id
		dependencies = append(dependencies, v)
	}
	for _, p := range s.Resources["ports"] {
		if str(p, "switchId") != str(e, "switchId") {
			continue
		}
		dependencies = append(dependencies, p)
		if number(p, "snmpIfIndex") > 0 {
			index := uint32(number(p, "snmpIfIndex"))
			m.Ports[index] = append(m.Ports[index], p)
		}
	}
	for _, c := range s.Resources["connections"] {
		if !contains(allowed, str(c, "vlanId")) {
			continue
		}
		member := find(s, "members", str(c, "memberId"))
		dependencies = append(dependencies, c, Record{"id": str(member, "id"), "status": str(member, "status"), "createdAt": str(member, "createdAt")})
		if member == nil || str(member, "status") != "active" || str(c, "status") != "active" {
			continue
		}
		entry := flowConnection{ID: str(c, "id"), MemberID: str(member, "id"), MemberRevision: a.trafficMemberRevision(s, str(member, "id")), VLANID: str(c, "vlanId"), Ports: monitoringConnectionPorts(c), MACs: []string{}}
		for _, mac := range s.Resources["mac-addresses"] {
			if str(mac, "connectionId") == entry.ID {
				entry.MACs = append(entry.MACs, canonicalDeviceMAC(str(mac, "address")))
				dependencies = append(dependencies, mac)
			}
		}
		sort.Strings(entry.Ports)
		sort.Strings(entry.MACs)
		m.Connections = append(m.Connections, entry)
		for _, id := range entry.Ports {
			key := entry.VLANID + "/" + id
			m.ByIngress[key] = append(m.ByIngress[key], entry)
		}
		for _, mac := range entry.MACs {
			key := entry.VLANID + "/" + mac
			m.ByMAC[key] = append(m.ByMAC[key], entry)
		}
	}
	sort.Slice(m.Connections, func(i, j int) bool { return m.Connections[i].ID < m.Connections[j].ID })
	// Digest the full bounded authorization projection, never just a MAC tag.
	m.Revision = a.coreHMAC("sflow-mapping", dependencies)
	for _, value := range dependencies {
		record, ok := value.(Record)
		if !ok {
			continue
		}
		for _, key := range []string{"createdAt", "updatedAt", "ownershipSince", "bindingSince"} {
			if at, err := time.Parse(time.RFC3339Nano, str(record, key)); err == nil && at.After(m.NotBefore) {
				m.NotBefore = at
			}
		}
	}
	return m, nil
}
func uniqueFlowOwner(candidates []flowConnection) (string, string, string, bool) {
	if len(candidates) == 0 {
		return "", "", "", false
	}
	member, revision := candidates[0].MemberID, candidates[0].MemberRevision
	for _, c := range candidates {
		if c.MemberID != member || c.MemberRevision != revision {
			return "", "", "", false
		}
	}
	connection := ""
	if len(candidates) == 1 {
		connection = candidates[0].ID
	}
	return member, revision, connection, true
}
func (m flowMapping) attribute(f sflow.Flow) sflow.AttributedFlow {
	out := sflow.AttributedFlow{Family: f.Family, EstimatedBytes: f.EstimatedBytes, EstimatedPackets: f.EstimatedPackets, SampleCount: uint64(f.SampleCount), SampledTCP179Activity: f.SampledTCP179Activity}
	fail := func(reason string) sflow.AttributedFlow { out.UnattributedReason = reason; return out }
	if f.UnattributedReason != "" {
		return fail(f.UnattributedReason)
	}
	if f.Family != 4 && f.Family != 6 {
		return fail("not-ip")
	}
	// Count only the member-facing ingress sampling point. Observing the same
	// packet again on a core link or egress sampler must not double P2P traffic.
	if f.SourceType != 0 || f.Input.Format != 0 || f.SourceIndex != f.Input.Value || f.Input.Value == 0 {
		return fail("not-member-ingress-sampler")
	}
	ports := m.Ports[f.Input.Value]
	if len(ports) != 1 {
		return fail("ingress-interface-unmapped-or-ambiguous")
	}
	port := ports[0]
	if port["coreReserved"] == true || number(port, "snmpIfType") != 6 {
		return fail("not-member-physical-ingress")
	}
	var tag uint16
	if f.InputVLAN != nil && *f.InputVLAN > 0 && *f.InputVLAN < 4095 {
		tag = uint16(*f.InputVLAN)
	} else if len(f.HeaderVLANs) == 1 {
		tag = f.HeaderVLANs[0]
	}
	if tag == 0 {
		return fail("vlan-evidence-missing")
	}
	if len(f.HeaderVLANs) > 0 {
		found := false
		for _, header := range f.HeaderVLANs {
			found = found || header == tag
		}
		if !found {
			return fail("vlan-evidence-conflict")
		}
	}
	if f.OutputVLAN != nil && *f.OutputVLAN > 0 && *f.OutputVLAN != uint32(tag) {
		return fail("cross-vlan-sample")
	}
	vlan := m.VLANs[tag]
	if vlan == "" {
		return fail("vlan-outside-exporter-scope")
	}
	out.VLANID = vlan
	sources := m.ByIngress[vlan+"/"+str(port, "id")]
	macSources := m.ByMAC[vlan+"/"+f.SourceMAC]
	destinations := m.ByMAC[vlan+"/"+f.DestinationMAC]
	if len(macSources) > 0 {
		matched := []flowConnection{}
		ids := map[string]bool{}
		for _, byMAC := range macSources {
			ids[byMAC.ID] = true
		}
		for _, c := range sources {
			if ids[c.ID] {
				matched = append(matched, c)
			}
		}
		if len(matched) == 0 {
			return fail("source-mac-ingress-conflict")
		}
		sources = matched
	}
	var ok bool
	out.SourceMemberID, out.SourceMemberRevision, out.SourceConnectionID, ok = uniqueFlowOwner(sources)
	if !ok || out.SourceConnectionID == "" {
		return fail("source-owner-ambiguous-or-missing")
	}
	out.DestinationMemberID, out.DestinationMemberRevision, out.DestinationConnectionID, ok = uniqueFlowOwner(destinations)
	if !ok || out.DestinationConnectionID == "" {
		return fail("destination-owner-ambiguous-or-missing")
	}
	if f.Output.Format == 0 && f.Output.Value != 0 {
		if outputs := m.Ports[f.Output.Value]; len(outputs) == 1 && outputs[0]["coreReserved"] != true {
			expected := str(outputs[0], "id")
			matches := false
			for _, c := range destinations {
				matches = matches || contains(c.Ports, expected)
			}
			if !matches {
				return fail("destination-interface-conflict")
			}
		}
	}
	return out
}
