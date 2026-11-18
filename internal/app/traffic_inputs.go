package app

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"
	"strings"

	"github.com/moedove/ixp-manager/internal/telemetry"
	"github.com/moedove/ixp-manager/internal/traffic"
)

func init() { Schemas["ports"]["counterSource"] = Field{Type: "string"} }

func normalizedCounterSource(p Record) string {
	value := str(p, "counterSource")
	if value == "" {
		return "auto"
	}
	return value
}
func trafficRouterCandidates(s *State, p Record) []string {
	ids := []string{}
	vlans := map[string]bool{}
	for _, c := range s.Resources["connections"] {
		if contains(monitoringConnectionPorts(c), str(p, "id")) {
			vlans[str(c, "vlanId")] = true
		}
	}
	for _, r := range s.Resources["routers"] {
		if vlans[str(r, "vlanId")] {
			ids = append(ids, str(r, "id"))
		}
	}
	sort.Strings(ids)
	return ids
}
func selectedCounterSource(s *State, p Record) (string, string) {
	switch value := normalizedCounterSource(p); value {
	case "none":
		return "", "disabled"
	case "snmp":
		if number(p, "snmpIfIndex") > 0 {
			return deviceCounterSource(str(p, "switchId")), "explicit"
		}
		return "", "mapping-required"
	case "auto":
		if number(p, "snmpIfIndex") > 0 {
			return deviceCounterSource(str(p, "switchId")), "automatic-snmp"
		}
		candidates := trafficRouterCandidates(s, p)
		if len(candidates) == 1 {
			return candidates[0], "automatic-router"
		}
		if len(candidates) > 1 {
			return "", "ambiguous"
		}
		return "", "unavailable"
	default:
		if strings.HasPrefix(value, "router:") {
			id := strings.TrimPrefix(value, "router:")
			if contains(trafficRouterCandidates(s, p), id) {
				return id, "explicit"
			}
		}
		return "", "unavailable"
	}
}
func trafficSourceAllowed(s *State, p Record, source string) bool {
	selected, _ := selectedCounterSource(s, p)
	return selected != "" && selected == source
}
func validateTrafficInventory(s *State) error {
	for _, p := range s.Resources["ports"] {
		if value, exists := p["counterSource"]; exists {
			if _, ok := value.(string); !ok {
				return errors.New("counterSource must be a string")
			}
		}
		value := normalizedCounterSource(p)
		if value == "auto" || value == "none" || value == "snmp" {
			continue
		}
		if !strings.HasPrefix(value, "router:") || find(s, "routers", strings.TrimPrefix(value, "router:")) == nil {
			return errors.New("counterSource must be auto, none, snmp or a configured router")
		}
		// A detached or reassigned connection pauses an incompatible selection;
		// statistics configuration must not prevent the inventory reassignment.
		// selectedCounterSource still requires the current VLAN relationship.
	}
	return nil
}
func guardTrafficResourceMutation(kind, method string, old, body Record) error {
	if kind != "ports" || method == "DELETE" {
		return nil
	}
	if value, exists := body["counterSource"]; exists {
		if _, ok := value.(string); !ok {
			return errors.New("counterSource must be an explicit string")
		}
	} else if value, exists := old["counterSource"]; exists {
		body["counterSource"] = value
	}
	return nil
}

func (a *App) trafficMemberRevision(s *State, id string) string {
	m := find(s, "members", id)
	if m == nil {
		return ""
	}
	mac := hmac.New(sha256.New, a.store.securityKey)
	mac.Write([]byte("traffic-member-v1\x00" + id + "\x00" + str(m, "createdAt")))
	return hex.EncodeToString(mac.Sum(nil))
}
func (a *App) freezeTrafficDimensions(s *State, p Record, member string) (traffic.Dimensions, error) {
	sw := find(s, "switches", str(p, "switchId"))
	rack := find(s, "racks", str(sw, "rackId"))
	d := traffic.Dimensions{MemberRevision: a.trafficMemberRevision(s, member), SwitchID: str(sw, "id"), RackID: str(rack, "id"), LocationID: str(sw, "locationId"), InfrastructureID: fabricID(sw), OwnershipSince: observationOwnershipSince(s, p)}
	if d.LocationID == "" {
		d.LocationID = str(rack, "locationId")
	}
	for _, c := range s.Resources["connections"] {
		if contains(monitoringConnectionPorts(c), str(p, "id")) {
			d.ConnectionIDs = append(d.ConnectionIDs, str(c, "id"))
		}
	}
	if len(d.ConnectionIDs) > 256 {
		return d, errors.New("traffic dimensions exceed256 connection bindings for one physical port")
	}
	sort.Strings(d.ConnectionIDs)
	for _, b := range s.CoreBundles {
		for _, l := range b.Links {
			side := ""
			if l.SideA.PortID == str(p, "id") {
				side = "a"
			}
			if l.SideB.PortID == str(p, "id") {
				side = "b"
			}
			if side != "" {
				d.CoreBundleID, d.CoreLinkID, d.CoreSide = b.ID, l.ID, side
			}
		}
	}
	return d, nil
}
func (a *App) freezeTrafficSample(s *State, port Record, sample *telemetry.PortSample) error {
	d, err := a.freezeTrafficDimensions(s, port, sample.MemberID)
	if err != nil {
		return err
	}
	sample.Dimensions = d
	return nil
}

func (a *App) ensureObservationKey() error {
	want := digest(string(a.store.securityKey))
	a.store.mu.RLock()
	existing := a.store.state.ObservationKeyHash
	closed := a.store.closed
	a.store.mu.RUnlock()
	if closed {
		return errors.New("store is closed")
	}
	if existing != "" {
		if existing != want {
			return errors.New("observation key does not match persisted history")
		}
		return nil
	}
	return a.store.Update(a.bindObservationKey)
}
func (a *App) bindObservationKey(s *State) error {
	want := digest(string(a.store.securityKey))
	if s.ObservationKeyHash != "" && s.ObservationKeyHash != want {
		return errors.New("observation key changed")
	}
	s.ObservationKeyHash = want
	return nil
}
