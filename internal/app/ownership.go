package app

import (
	"fmt"
	"net/netip"
	"reflect"
	"slices"
	"strconv"
	"time"
)

func ownsObservations(kind string) bool {
	return kind == "ports" || kind == "connections" || kind == "peers"
}
func ownershipTime(r Record) time.Time {
	if value := str(r, "ownershipSince"); value != "" {
		parsed, _ := time.Parse(time.RFC3339Nano, value)
		return parsed
	}
	var latest time.Time
	for _, key := range []string{"createdAt", "updatedAt"} {
		parsed, _ := time.Parse(time.RFC3339Nano, str(r, key))
		if parsed.After(latest) {
			latest = parsed
		}
	}
	return latest
}
func setOwnershipTime(kind string, old, next Record, now string) {
	if !ownsObservations(kind) {
		return
	}
	if kind == "connections" {
		a, _ := connectionPortIDs(old)
		b, _ := connectionPortIDs(next)
		changed := old == nil || str(old, "memberId") != str(next, "memberId") || !reflect.DeepEqual(a, b) || str(old, "vlanId") != str(next, "vlanId") || !sameObservationAddress(str(old, "ipv4"), str(next, "ipv4")) || !sameObservationAddress(str(old, "ipv6"), str(next, "ipv6"))
		if changed {
			next["bindingSince"] = now
		} else {
			at := connectionBindingTime(old)
			if at.IsZero() {
				next["bindingSince"] = now
			} else {
				next["bindingSince"] = at.UTC().Format(time.RFC3339Nano)
			}
		}
	}

	changed := old == nil
	if old != nil {
		switch kind {
		case "ports":
			changed = str(old, "memberId") != str(next, "memberId") || str(old, "switchId") != str(next, "switchId") || number(old, "snmpIfIndex") != number(next, "snmpIfIndex") || (old["coreReserved"] == true) != (next["coreReserved"] == true) || normalizedCounterSource(old) != normalizedCounterSource(next)
		case "connections":
			a, _ := connectionPortIDs(old)
			b, _ := connectionPortIDs(next)
			changed = str(old, "memberId") != str(next, "memberId") || !reflect.DeepEqual(a, b)
		case "peers":
			changed = str(old, "memberId") != str(next, "memberId") || number(old, "asn") != number(next, "asn") || str(old, "routerId") != str(next, "routerId") || !sameObservationAddress(str(old, "address"), str(next, "address"))
		}
	}
	if changed {
		next["ownershipSince"] = now
		return
	}
	at := ownershipTime(old)
	if at.IsZero() {
		next["ownershipSince"] = now
	} else {
		next["ownershipSince"] = at.UTC().Format(time.RFC3339Nano)
	}
}
func observationOwnershipSince(s *State, port Record) time.Time {
	latest := ownershipTime(port)
	id := str(port, "id")
	for _, connection := range s.Resources["connections"] {
		ports, err := connectionPortIDs(connection)
		if err == nil && slices.Contains(ports, id) {
			if at := ownershipTime(connection); at.After(latest) {
				latest = at
			}
		}
	}
	return latest
}
func peerOwnershipSince(s *State, peer Record) time.Time {
	latest := ownershipTime(peer)
	address := str(peer, "address")
	router := find(s, "routers", str(peer, "routerId"))
	for _, connection := range s.Resources["connections"] {
		if str(connection, "memberId") == str(peer, "memberId") && str(connection, "vlanId") == str(router, "vlanId") && (str(connection, "ipv4") == address || str(connection, "ipv6") == address) {
			if at := connectionBindingTime(connection); at.After(latest) {
				latest = at
			}
		}
	}
	return latest
}
func validateOwnershipTime(kind string, r Record) error {
	if !ownsObservations(kind) {
		return nil
	}
	for _, key := range []string{"ownershipSince", "bindingSince"} {
		if value, exists := r[key]; exists {
			raw, ok := value.(string)
			if !ok {
				return fmt.Errorf("invalid ownership marker")
			}
			if _, err := time.Parse(time.RFC3339Nano, raw); err != nil {
				return fmt.Errorf("invalid ownership timestamp")
			}
		}
	}
	return nil
}

func peerObservationBound(s *State, peer Record) bool {
	router := find(s, "routers", str(peer, "routerId"))
	if router == nil {
		return false
	}
	for _, connection := range s.Resources["connections"] {
		if str(connection, "memberId") == str(peer, "memberId") && str(connection, "vlanId") == str(router, "vlanId") && (str(connection, "ipv4") == str(peer, "address") || str(connection, "ipv6") == str(peer, "address")) {
			return true
		}
	}
	return false
}

func monitoringBindingRevision(s *State, kind, id, routerID string) string {
	var owner string
	var at time.Time
	if kind == "port" {
		record := find(s, "ports", id)
		owners := monitoringPortMembers(s, id)
		if len(owners) == 1 {
			owner = owners[0]
		}
		at = observationOwnershipSince(s, record)
	} else {
		record := find(s, "peers", id)
		owner = str(record, "memberId")
		at = peerOwnershipSince(s, record)
	}
	value := kind + "\x00" + id + "\x00" + routerID + "\x00" + owner + "\x00" + at.UTC().Format(time.RFC3339Nano)
	if kind == "peer" {
		if router := find(s, "routers", routerID); routerPlatform(router) == "quagga" {
			identity := monitoringRouterIdentity(router)
			value += "\x00" + identity.Platform + "\x00" + identity.RouterID + "\x00" + strconv.FormatUint(uint64(identity.ASN), 10)
		}
	}
	return digest(value)
}

func sameObservationAddress(a, b string) bool {
	left, e1 := netip.ParseAddr(a)
	right, e2 := netip.ParseAddr(b)
	if e1 == nil && e2 == nil {
		return left == right
	}
	return a == b
}

func connectionBindingTime(r Record) time.Time {
	if raw := str(r, "bindingSince"); raw != "" {
		at, _ := time.Parse(time.RFC3339Nano, raw)
		return at
	}
	return ownershipTime(r)
}
