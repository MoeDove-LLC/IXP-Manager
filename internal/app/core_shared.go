package app

import (
	"errors"
	"math"
	"sort"
	"time"

	"github.com/moedove/ixp-manager/internal/core"
)

func init() {
	Schemas["switches"]["active"] = Field{Type: "boolean"}
	Schemas["switches"]["asn"] = num(1, math.MaxUint32-1, false)
	Schemas["switches"]["loopbackIpv4"] = Field{Type: "ipv4"}
	Schemas["switches"]["loopbackIpv6"] = Field{Type: "ipv6"}
	Schemas["switches"]["loopbackName"] = textField(false)
	Schemas["ports"]["coreReserved"] = Field{Type: "boolean"}
}

func exportCoreBundles(bundles []core.Bundle) []core.Bundle {
	out := append([]core.Bundle{}, bundles...)
	for i := range out {
		// Import is new ownership, not a restoration of old sample authority.
		out[i].BindingRevision = ""
	}
	return out
}

func importCoreBundles(s *State, bundles []core.Bundle) error {
	if len(s.CoreBundles)+len(bundles) > core.MaxBundles {
		return errors.New("core bundle import exceeds supported limit")
	}
	seen := map[string]bool{}
	for _, b := range s.CoreBundles {
		seen[b.ID] = true
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, b := range bundles {
		if !identifier.MatchString(b.ID) || seen[b.ID] {
			return errors.New("invalid or duplicate core bundle ID")
		}
		seen[b.ID] = true
		b.BindingRevision = token(16)
		b.CreatedAt, b.UpdatedAt = now, now
		for _, id := range corePorts(b) {
			if p := find(s, "ports", id); p != nil {
				p["coreReserved"] = true
			}
		}
		s.CoreBundles = append(s.CoreBundles, b)
		stampCorePorts(s, nil, &b, now)
	}
	return nil
}

// Touch every old and new endpoint even when it leaves core use. The ordinary
// port lineage must not revert to a pre-core value after a bundle is deleted.
func stampCorePorts(s *State, old, next *core.Bundle, now string) {
	ids := map[string]bool{}
	for _, b := range []*core.Bundle{old, next} {
		if b == nil {
			continue
		}
		for _, link := range b.Links {
			ids[link.SideA.PortID], ids[link.SideB.PortID] = true, true
		}
	}
	for id := range ids {
		if port := find(s, "ports", id); port != nil {
			port["ownershipSince"] = now
		}
	}
}

func stampCoreFlatPorts(s *State, old, next Record, now string) {
	for _, row := range []Record{old, next} {
		for _, key := range []string{"portAId", "portBId"} {
			if port := find(s, "ports", str(row, key)); port != nil {
				port["ownershipSince"] = now
				port["coreReserved"] = true
			}
		}
	}
}

func normalizeLegacyCorePorts(s *State) bool {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	changed := false
	for _, link := range s.Resources["core-links"] {
		for _, key := range []string{"portAId", "portBId"} {
			if p := find(s, "ports", str(link, key)); p != nil && p["coreReserved"] != true {
				p["coreReserved"] = true
				p["ownershipSince"] = now
				changed = true
			}
		}
	}
	return changed
}

func guardCoreResourceMutation(s *State, kind, method string, old, body Record) error {
	if kind == "switches" && method != "DELETE" {
		if value, exists := body["active"]; exists {
			if _, ok := value.(bool); !ok {
				return errors.New("switch active must be an explicit boolean")
			}
		} else if previous, exists := old["active"]; exists {
			body["active"] = previous
		}
		return nil
	}
	if kind != "ports" || method == "DELETE" {
		return nil
	}
	value, provided := body["coreReserved"]
	if provided {
		if _, ok := value.(bool); !ok {
			return errors.New("coreReserved must be an explicit boolean")
		}
	}
	if !provided {
		if previous, exists := old["coreReserved"]; exists {
			body["coreReserved"] = previous
		}
		return nil
	}
	if value != false {
		return nil
	}
	id := str(old, "id")
	for _, b := range s.CoreBundles {
		if contains(corePorts(b), id) {
			return errors.New("remove the core endpoint before releasing its reserved port")
		}
	}
	for _, link := range s.Resources["core-links"] {
		if str(link, "portAId") == id || str(link, "portBId") == id {
			return errors.New("remove or adopt the unbundled core link before releasing its port")
		}
	}
	return nil
}

func validateCoreInventoryFlags(s *State) error {
	for _, entry := range []struct{ kind, key string }{{"switches", "active"}, {"ports", "coreReserved"}} {
		for _, r := range s.Resources[entry.kind] {
			if value, exists := r[entry.key]; exists {
				if _, ok := value.(bool); !ok {
					return errors.New(entry.kind + " " + entry.key + " must be boolean")
				}
			}
		}
	}
	return nil
}

func corePortBinding(s *State, portID string) string {
	bindings := []string{}
	for _, b := range s.CoreBundles {
		for _, link := range b.Links {
			for _, endpoint := range []core.Endpoint{link.SideA, link.SideB} {
				if endpoint.PortID == portID {
					bindings = append(bindings, "bundle:"+b.ID+":"+b.BindingRevision+":"+link.ID+":"+endpoint.ID)
				}
			}
		}
	}
	for _, link := range s.Resources["core-links"] {
		if str(link, "portAId") == portID || str(link, "portBId") == portID {
			bindings = append(bindings, "flat:"+str(link, "id")+":"+str(link, "updatedAt"))
		}
	}
	if find(s, "ports", portID)["coreReserved"] == true {
		bindings = append(bindings, "reserved")
	}
	if len(bindings) == 0 {
		return ""
	}
	sort.Strings(bindings)
	return patchHash(bindings)
}
