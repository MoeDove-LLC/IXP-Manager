package app

import (
	"fmt"
	"math/big"
	"net/http"
	"net/netip"
	"sort"
	"strings"
	"time"
)

func ipamOccupancy(s *State, vlanID string) map[string]Record {
	out := map[string]Record{}
	ensure := func(ip string) Record {
		if out[ip] == nil {
			out[ip] = Record{"address": ip, "state": "free", "memberId": "", "connectionIds": []string{}, "routerIds": []string{}}
		}
		return out[ip]
	}
	for _, pool := range s.Resources["ip-addresses"] {
		if str(pool, "vlanId") != vlanID {
			continue
		}
		item := ensure(str(pool, "address"))
		item["id"] = pool["id"]
		item["name"] = pool["name"]
		if name := str(pool, "dnsName"); name != "" {
			item["dnsName"] = name
		}
		item["memberId"] = str(pool, "memberId")
		if str(pool, "memberId") != "" {
			item["state"] = "reserved"
		}
	}
	for _, connection := range s.Resources["connections"] {
		if str(connection, "vlanId") != vlanID {
			continue
		}
		for _, field := range []string{"ipv4", "ipv6"} {
			if ip := str(connection, field); ip != "" {
				item := ensure(ip)
				item["state"] = "assigned"
				item["memberId"] = connection["memberId"]
				item["connectionIds"] = append(item["connectionIds"].([]string), str(connection, "id"))
			}
		}
	}
	for _, router := range s.Resources["routers"] {
		if str(router, "vlanId") != vlanID {
			continue
		}
		for _, field := range []string{"localIpv4", "localIpv6"} {
			if ip := str(router, field); ip != "" {
				item := ensure(ip)
				item["state"] = "router"
				item["routerIds"] = append(item["routerIds"].([]string), str(router, "id"))
			}
		}
	}
	return out
}
func ipamPrefix(vlan Record, family int) (netip.Prefix, error) {
	return ipamPrefixChoice(vlan, family, "")
}
func ipamPrefixChoice(vlan Record, family int, selected string) (netip.Prefix, error) {
	if family != 4 && family != 6 {
		return netip.Prefix{}, fmt.Errorf("family must be 4 or6")
	}
	choices, err := vlanFamilyPrefixes(vlan, family)
	if err != nil {
		return netip.Prefix{}, err
	}
	if len(choices) == 0 {
		return netip.Prefix{}, fmt.Errorf("VLAN has no prefix for selected family")
	}
	if selected != "" {
		wanted, err := netip.ParsePrefix(selected)
		if err != nil {
			return netip.Prefix{}, fmt.Errorf("invalid selected prefix")
		}
		for _, p := range choices {
			if p == wanted {
				return p, nil
			}
		}
		return netip.Prefix{}, fmt.Errorf("selected prefix is not assigned to VLAN")
	}
	if len(choices) > 1 {
		return netip.Prefix{}, fmt.Errorf("choose an explicit prefix for this multi-prefix VLAN")
	}
	return choices[0], nil
}
func ipamUsableCount(prefix netip.Prefix) *big.Int {
	total := new(big.Int).Lsh(big.NewInt(1), uint(prefix.Addr().BitLen()-prefix.Bits()))
	if prefix.Addr().Is4() && prefix.Bits() < 31 {
		total.Sub(total, big.NewInt(2))
	}
	if prefix.Addr().Is6() && prefix.Bits() < 127 {
		total.Sub(total, big.NewInt(1))
	}
	return total
}
func (a *App) ipam(w http.ResponseWriter, r *http.Request, user Record) {
	if str(user, "role") == "member" {
		problem(w, 403, "staff access required")
		return
	}
	write := str(user, "role") == "admin" || str(user, "role") == "operator"
	if r.URL.Path == "/api/ipam" && r.Method == "GET" {
		s := a.store.Snapshot()
		id := r.URL.Query().Get("vlanId")
		vlan := find(&s, "vlans", id)
		if vlan == nil {
			problem(w, 404, "VLAN not found")
			return
		}
		occupancy := ipamOccupancy(&s, id)
		items := []Record{}
		for ip, item := range occupancy {
			addr, err := netip.ParseAddr(ip)
			if err != nil {
				continue
			}
			family := 4
			if addr.Is6() {
				family = 6
			}
			choices, _ := vlanFamilyPrefixes(vlan, family)
			allocatable := false
			for _, p := range choices {
				if usablePoolAddress(addr, p) {
					allocatable = true
				}
			}
			item["family"] = family
			item["allocatable"] = allocatable
			items = append(items, item)
		}
		sort.Slice(items, func(i, j int) bool {
			left, _ := netip.ParseAddr(str(items[i], "address"))
			right, _ := netip.ParseAddr(str(items[j], "address"))
			return left.Less(right)
		})
		prefixes := []Record{}
		for _, family := range []int{4, 6} {
			choices, err := vlanFamilyPrefixes(vlan, family)
			if err != nil {
				continue
			}
			for _, prefix := range choices {
				used := 0
				for raw, item := range occupancy {
					address, err := netip.ParseAddr(raw)
					if err == nil && usablePoolAddress(address, prefix) && str(item, "state") != "free" {
						used++
					}
				}
				total := ipamUsableCount(prefix)
				available := new(big.Int).Sub(total, big.NewInt(int64(used)))
				prefixes = append(prefixes, Record{"family": family, "prefix": prefix.String(), "usable": total.String(), "occupied": used, "available": available.String()})
			}
		}
		respond(w, 200, Record{"vlanId": id, "prefixes": prefixes, "items": items})
		return
	}
	if r.Method != "POST" || !write {
		problem(w, 403, "address allocation requires staff write access")
		return
	}
	switch r.URL.Path {
	case "/api/ipam/allocate":
		var body struct {
			VLANID   string `json:"vlanId"`
			MemberID string `json:"memberId"`
			Family   int    `json:"family"`
			Count    int    `json:"count"`
			Address  string `json:"address"`
			Prefix   string `json:"prefix"`
		}
		if !decode(w, r, &body) {
			return
		}
		if body.Count == 0 {
			body.Count = 1
		}
		if body.Count < 1 || body.Count > 64 || (body.Address != "" && body.Count != 1) {
			problem(w, 400, "allocate 1..64 addresses; explicit address requires count 1")
			return
		}
		result := []Record{}
		err := a.store.Update(func(s *State) error {
			vlan := find(s, "vlans", body.VLANID)
			member := find(s, "members", body.MemberID)
			if vlan == nil || member == nil {
				return fmt.Errorf("VLAN or member not found")
			}
			if str(member, "status") == "closed" || str(member, "status") == "suspended" {
				return fmt.Errorf("cannot allocate addresses to a closed or suspended member")
			}
			prefix, err := ipamPrefixChoice(vlan, body.Family, body.Prefix)
			if err != nil {
				return err
			}
			occupied := ipamOccupancy(s, body.VLANID)
			addresses := []netip.Addr{}
			if body.Address != "" {
				address, err := netip.ParseAddr(body.Address)
				if err != nil || address.Zone() != "" || !usablePoolAddress(address, prefix) {
					return fmt.Errorf("address is not a usable host in the VLAN prefix")
				}
				if item := occupied[address.String()]; item != nil && str(item, "state") != "free" {
					return fmt.Errorf("address is already reserved or assigned")
				}
				addresses = append(addresses, address)
			} else {
				candidate := prefix.Masked().Addr()
				limit := len(occupied) + body.Count + 3
				for scanned := 0; scanned < limit && candidate.IsValid() && prefix.Contains(candidate) && len(addresses) < body.Count; scanned++ {
					if usablePoolAddress(candidate, prefix) {
						item := occupied[candidate.String()]
						if item == nil || str(item, "state") == "free" {
							addresses = append(addresses, candidate)
						}
					}
					candidate = candidate.Next()
				}
				if len(addresses) != body.Count {
					return fmt.Errorf("not enough available host addresses")
				}
			}
			now := time.Now().UTC().Format(time.RFC3339Nano)
			for _, address := range addresses {
				var record Record
				for _, pool := range s.Resources["ip-addresses"] {
					if str(pool, "vlanId") == body.VLANID && str(pool, "address") == address.String() {
						record = pool
						break
					}
				}
				if record == nil {
					record = Record{"id": token(16), "name": address.String(), "address": address.String(), "vlanId": body.VLANID, "createdAt": now}
					s.Resources["ip-addresses"] = append(s.Resources["ip-addresses"], record)
				}
				record["memberId"] = body.MemberID
				// A free slot must not carry a previous owner's PTR name.
				delete(record, "dnsName")
				record["updatedAt"] = now
				result = append(result, clean(record))
			}
			if err := validateAll(s); err != nil {
				return err
			}
			audit(s, str(user, "id"), "allocate-addresses", "vlans", body.VLANID)
			return nil
		})
		if err != nil {
			problem(w, 409, err.Error())
			return
		}
		respond(w, 201, Record{"items": result})
	case "/api/ipam/release":
		var body struct {
			ID               string `json:"id"`
			ExpectedMemberID string `json:"expectedMemberId"`
		}
		if !decode(w, r, &body) {
			return
		}
		var result Record
		err := a.store.Update(func(s *State) error {
			pool := find(s, "ip-addresses", body.ID)
			if pool == nil {
				return fmt.Errorf("address-pool entry not found")
			}
			if body.ExpectedMemberID == "" || str(pool, "memberId") != body.ExpectedMemberID {
				return fmt.Errorf("address ownership changed; reload before releasing")
			}
			item := ipamOccupancy(s, str(pool, "vlanId"))[str(pool, "address")]
			if str(item, "state") == "assigned" || str(item, "state") == "router" {
				return fmt.Errorf("remove all connection/router bindings before releasing this address")
			}
			delete(pool, "memberId")
			delete(pool, "dnsName")
			pool["updatedAt"] = time.Now().UTC().Format(time.RFC3339Nano)
			audit(s, str(user, "id"), "release-address", "ip-addresses", body.ID)
			result = clean(pool)
			return nil
		})
		if err != nil {
			problem(w, 409, err.Error())
			return
		}
		respond(w, 200, result)
	default:
		problem(w, 404, "IPAM endpoint not found")
	}
}
func ipamScope(path string) bool { return path == "/api/ipam" || strings.HasPrefix(path, "/api/ipam/") }
